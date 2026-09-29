package ptyhold

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// logCap is how big the holder's log may grow before a new holder starts it
// afresh.
const logCap = 1 << 20

// callTimeout bounds one request. The holder answers from memory; one that
// takes this long is not going to answer.
const callTimeout = 10 * time.Second

// ErrClosed is what every call returns once the connection is gone.
var ErrClosed = errors.New("holder connection closed")

// Sink is where one session's output and end go. Out is called on the
// connection's reader, in order, and must not block; Exited comes after the
// last Out, on a goroutine of its own.
type Sink struct {
	Out    func([]byte)
	Exited func()
}

// Client is the application's end of the holder: control requests and every
// session's bytes, over one socket.
type Client struct {
	conn net.Conn
	wmu  sync.Mutex

	mu      sync.Mutex
	nextReq int64
	pending map[int64]chan response
	sinks   map[string]Sink
	closed  bool
	// leaving is a Close we asked for, or the holder saying another
	// application has taken over: the sessions are not over, we just stop
	// watching them.
	leaving bool
	// onLost, when set, is told the connection broke instead of every session
	// being declared over: the holder may well be alive, and the one watching
	// can connect again and find them (term.Manager).
	onLost func()
	// pid is the holder's, as it said in hello.
	pid int
}

// Connect attaches to the holder on socket, starting one with spawn when none
// is there. A holder speaking another protocol is shut down first — it was
// started by an older application — and its sessions go with it.
func Connect(socket string, spawn func() *exec.Cmd) (*Client, error) {
	c, err := dial(socket)
	if err == nil {
		if err = c.hello(); err == nil {
			return c, nil
		}
		if strings.Contains(err.Error(), "protocol mismatch") {
			_ = c.Shutdown()
			time.Sleep(200 * time.Millisecond)
		}
		c.Close()
	}
	if err := Spawn(socket, spawn()); err != nil {
		return nil, err
	}
	return Dial(socket)
}

// Dial attaches to the holder on socket, which has to be there already.
func Dial(socket string) (*Client, error) {
	c, err := dial(socket)
	if err != nil {
		return nil, err
	}
	if err := c.hello(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Spawn starts a holder and waits until it listens. It is detached from the
// application's process group and session, so neither the application quitting
// nor the terminal it was started from closing reaches it.
func Spawn(socket string, cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout = nil, nil
	if cmd.Stderr == nil {
		// What a holder says about itself goes beside its socket: it has no
		// terminal, and its reasons for leaving are worth being able to read.
		// Started over once it is past logCap, or every holder ever started
		// would still be in it.
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if info, err := os.Stat(socket + ".log"); err == nil && info.Size() > logCap {
			flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		}
		if f, err := os.OpenFile(socket+".log", flags, 0o600); err == nil {
			defer f.Close()
			cmd.Stderr = f
		}
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the holder: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	deadline := time.After(5 * time.Second)
	for {
		if conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond); err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-exited:
			return errors.New("the holder exited as it started; see " + socket + ".log")
		case <-deadline:
			return errors.New("the holder did not come up on " + socket)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func dial(socket string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, nextReq: 1, pending: map[int64]chan response{}, sinks: map[string]Sink{}}
	go c.read()
	return c, nil
}

func (c *Client) hello() error {
	resp, err := c.call(request{Op: "hello", Protocol: Protocol})
	c.pid = resp.Pid
	return err
}

// Pid is the holder process's id.
func (c *Client) Pid() int { return c.pid }

func (c *Client) read() {
	for {
		f, err := readFrame(c.conn)
		if err != nil {
			c.lost()
			return
		}
		switch f.typ {
		case tOut:
			c.mu.Lock()
			sink := c.sinks[f.id]
			c.mu.Unlock()
			if sink.Out != nil {
				sink.Out(f.payload)
			}
		case tResp:
			var resp response
			if json.Unmarshal(f.payload, &resp) != nil {
				continue
			}
			c.mu.Lock()
			waiter := c.pending[resp.Req]
			delete(c.pending, resp.Req)
			c.mu.Unlock()
			if waiter != nil {
				waiter <- resp
			}
		case tEvt:
			var evt event
			if json.Unmarshal(f.payload, &evt) != nil {
				continue
			}
			if evt.Event == "replaced" {
				// Another application took the holder: connecting again would
				// take it back, and the two would pass it between them forever.
				c.mu.Lock()
				c.leaving = true
				c.mu.Unlock()
				continue
			}
			if evt.Event != "exited" {
				continue
			}
			c.mu.Lock()
			sink := c.sinks[evt.ID]
			delete(c.sinks, evt.ID)
			c.mu.Unlock()
			if sink.Exited != nil {
				go sink.Exited()
			}
		}
	}
}

// lost ends everything waiting on a connection that is gone. A holder that went
// away took its sessions with it, so each of them has ended — unless the
// application is the one leaving, and then they carry on without it.
func (c *Client) lost() {
	c.mu.Lock()
	c.closed = true
	for _, waiter := range c.pending {
		close(waiter)
	}
	c.pending = map[int64]chan response{}
	sinks := c.sinks
	c.sinks = map[string]Sink{}
	leaving, onLost := c.leaving, c.onLost
	c.mu.Unlock()
	if leaving {
		return
	}
	if onLost != nil {
		go onLost()
		return
	}
	for _, sink := range sinks {
		if sink.Exited != nil {
			go sink.Exited()
		}
	}
}

func (c *Client) call(req request) (response, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return response{}, ErrClosed
	}
	req.Req = c.nextReq
	c.nextReq++
	waiter := make(chan response, 1)
	c.pending[req.Req] = waiter
	c.mu.Unlock()

	if err := c.send(frame{typ: tCtrl, payload: marshal(req)}); err != nil {
		c.mu.Lock()
		delete(c.pending, req.Req)
		c.mu.Unlock()
		return response{}, fmt.Errorf("%w: %v", ErrClosed, err)
	}
	select {
	case resp, ok := <-waiter:
		if !ok {
			return response{}, ErrClosed
		}
		if resp.Err != "" {
			return resp, errors.New(resp.Err)
		}
		return resp, nil
	case <-time.After(callTimeout):
		c.mu.Lock()
		delete(c.pending, req.Req)
		c.mu.Unlock()
		return response{}, errors.New("the holder did not answer " + req.Op)
	}
}

func (c *Client) send(f frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeFrame(c.conn, f)
}

func (c *Client) watch(id string, sink Sink) {
	c.mu.Lock()
	c.sinks[id] = sink
	c.mu.Unlock()
}

func (c *Client) unwatch(id string) {
	c.mu.Lock()
	delete(c.sinks, id)
	c.mu.Unlock()
}

// OnLost replaces «every session is over» with fn when the connection breaks
// without either side meaning it to.
func (c *Client) OnLost(fn func()) {
	c.mu.Lock()
	c.onLost = fn
	c.mu.Unlock()
}

// Drop breaks the connection the way a failure would — the holder giving up on
// an application that stopped reading does exactly this — without leaving.
func (c *Client) Drop() { _ = c.conn.Close() }

// Start runs spec in a new session named by label.ID.
func (c *Client) Start(label Label, spec Spec, sink Sink) error {
	// Watched before it exists: its first output may beat the answer.
	c.watch(label.ID, sink)
	if _, err := c.call(request{Op: "start", Label: label, Spec: spec}); err != nil {
		c.unwatch(label.ID)
		return err
	}
	return nil
}

// Attach watches a session started by an earlier application: its tail comes
// first, through sink.Out, then whatever it prints next. It reports whether the
// session is still running; one that is not will send no Exited.
func (c *Client) Attach(id string, sink Sink) (bool, error) {
	c.watch(id, sink)
	resp, err := c.call(request{Op: "attach", ID: id})
	if err != nil || !resp.Running {
		c.unwatch(id)
	}
	return resp.Running, err
}

// Write is a keystroke on its way to a session.
func (c *Client) Write(id string, data []byte) error {
	if err := c.send(frame{typ: tIn, id: id, payload: data}); err != nil {
		return fmt.Errorf("%w: %v", ErrClosed, err)
	}
	return nil
}

// Resize tells a session how big its window is.
func (c *Client) Resize(id string, cols, rows int) error {
	_, err := c.call(request{Op: "resize", ID: id, Cols: cols, Rows: rows})
	return err
}

// Hangup is a terminal window closing on a session. It fails where there is no
// such thing (Windows), and then only Kill is left.
func (c *Client) Hangup(id string) error {
	_, err := c.call(request{Op: "hangup", ID: id})
	return err
}

// Kill ends a session's process and everything it started.
func (c *Client) Kill(id string) error {
	_, err := c.call(request{Op: "kill", ID: id})
	return err
}

// Forget drops a session from the holder, ending it if it still runs.
func (c *Client) Forget(id string) error {
	c.unwatch(id)
	_, err := c.call(request{Op: "forget", ID: id})
	return err
}

// List is every session the holder keeps, finished ones included.
func (c *Client) List() ([]Info, error) {
	resp, err := c.call(request{Op: "list"})
	return resp.Sessions, err
}

// Shutdown ends every session and the holder.
func (c *Client) Shutdown() error {
	_, err := c.call(request{Op: "shutdown"})
	return err
}

// Close lets go of the holder and leaves its sessions running.
func (c *Client) Close() {
	c.mu.Lock()
	c.leaving = true
	c.mu.Unlock()
	_ = c.conn.Close()
}
