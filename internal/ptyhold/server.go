package ptyhold

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// Timing of the holder's own life. An application that starts one connects
// within moments; one that never does left a holder with nothing to hold.
const (
	firstClientWait = 30 * time.Second
	writeTimeout    = 5 * time.Second
)

// Server is the holder: it owns the sessions and serves one application at a
// time. A second connection replaces the first — the application that made the
// first one is gone or on its way out.
type Server struct {
	socket string
	log    *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
	client   *peer
	ln       net.Listener

	done     chan struct{}
	stopOnce sync.Once
}

// session is one process the holder keeps.
type session struct {
	label Label
	proc  *Proc

	mu         sync.Mutex
	screen     *Screen
	cols, rows int
	running    bool
	// to is the application watching this session's output, nil while none
	// is: output is kept in the tail either way.
	to *peer
	// input is written on a goroutine of its own: a process that does not read
	// its terminal must not stall every other session's keystrokes with it.
	input  chan []byte
	closed bool
}

// peer is one connected application.
type peer struct {
	conn net.Conn
	wmu  sync.Mutex
}

func (p *peer) send(f frame) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	// Bounded, because output is sent from the reader of a pty: an application
	// that stopped reading would otherwise freeze the process it is watching.
	_ = p.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return writeFrame(p.conn, f)
}

// NewServer prepares a holder on socket.
func NewServer(socket string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{socket: socket, log: log, sessions: map[string]*session{}, done: make(chan struct{})}
}

// Run serves until the holder has nothing left to hold or is told to stop.
func (s *Server) Run() error {
	if conn, err := net.DialTimeout("unix", s.socket, time.Second); err == nil {
		conn.Close()
		return errors.New("a holder is already running on " + s.socket)
	}
	_ = os.Remove(s.socket)
	ln, err := net.Listen("unix", s.socket)
	if err != nil {
		return fmt.Errorf("holder listen: %w", err)
	}
	_ = os.Chmod(s.socket, 0o600)
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	defer os.Remove(s.socket)

	go func() {
		<-s.done
		ln.Close()
	}()
	time.AfterFunc(firstClientWait, s.quitIfIdle)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
				return err
			}
		}
		go s.serve(&peer{conn: conn})
	}
}

func (s *Server) stop() { s.stopOnce.Do(func() { close(s.done) }) }

// quitIfIdle ends a holder nobody is using and that holds nothing. Finished
// sessions count as something: their tails are what the next application shows
// of how they ended.
func (s *Server) quitIfIdle() {
	s.mu.Lock()
	idle := s.client == nil && len(s.sessions) == 0
	s.mu.Unlock()
	if idle {
		s.log.Info("nothing to hold, leaving")
		s.stop()
	}
}

func (s *Server) serve(p *peer) {
	defer func() {
		p.conn.Close()
		s.mu.Lock()
		was := s.client == p
		if was {
			s.client = nil
		}
		all := s.all()
		s.mu.Unlock()
		for _, t := range all {
			t.mu.Lock()
			if t.to == p {
				t.detachLocked()
			}
			t.mu.Unlock()
		}
		if was {
			s.quitIfIdle()
		}
	}()

	for {
		f, err := readFrame(p.conn)
		if err != nil {
			return
		}
		switch f.typ {
		case tIn:
			if t := s.session(f.id); t != nil {
				t.write(f.payload)
			}
		case tCtrl:
			var req request
			if json.Unmarshal(f.payload, &req) != nil {
				continue
			}
			resp := s.handle(p, req)
			resp.Req = req.Req
			if p.send(frame{typ: tResp, payload: marshal(resp)}) != nil {
				return
			}
			if req.Op == "shutdown" {
				s.shutdown()
				return
			}
		}
	}
}

func (s *Server) handle(p *peer, req request) response {
	if req.Op == "hello" {
		if req.Protocol != Protocol {
			return response{Err: fmt.Sprintf("protocol mismatch: holder %d, application %d", Protocol, req.Protocol)}
		}
		s.welcome(p)
		return response{Protocol: Protocol}
	}
	switch req.Op {
	case "start":
		if err := s.start(p, req.Label, req.Spec); err != nil {
			return response{Err: err.Error()}
		}
		return response{Running: true}
	case "list":
		s.mu.Lock()
		all := s.all()
		s.mu.Unlock()
		list := make([]Info, 0, len(all))
		for _, t := range all {
			t.mu.Lock()
			list = append(list, Info{Label: t.label, Running: t.running, Cols: t.cols, Rows: t.rows})
			t.mu.Unlock()
		}
		return response{Sessions: list}
	case "shutdown":
		return response{}
	}

	t := s.session(req.ID)
	if t == nil {
		return response{Err: "no session " + req.ID}
	}
	switch req.Op {
	case "attach":
		return response{Running: t.attach(p)}
	case "resize":
		if err := t.resize(req.Cols, req.Rows); err != nil {
			return response{Err: err.Error()}
		}
		return response{}
	case "hangup":
		if err := t.proc.Hangup(); err != nil {
			return response{Err: err.Error()}
		}
		return response{}
	case "kill":
		t.proc.Kill()
		return response{}
	case "forget":
		s.forget(t)
		return response{}
	}
	return response{Err: "unknown op " + req.Op}
}

// welcome makes p the application the holder serves. Only a peer that said
// hello becomes one: a connection that merely checks the socket is alive — and
// goes — must not be mistaken for the application leaving.
func (s *Server) welcome(p *peer) {
	s.mu.Lock()
	old := s.client
	s.client = p
	s.mu.Unlock()
	if old != nil && old != p {
		old.conn.Close()
	}
}

func (s *Server) start(p *peer, label Label, spec Spec) error {
	if label.ID == "" {
		return errors.New("a session needs an id")
	}
	s.mu.Lock()
	_, taken := s.sessions[label.ID]
	s.mu.Unlock()
	if taken {
		return errors.New("session " + label.ID + " already exists")
	}
	spec.Cols, spec.Rows = sized(spec.Cols, spec.Rows)
	proc, err := Start(spec)
	if err != nil {
		return err
	}
	t := &session{
		label: label, proc: proc, screen: NewScreen(spec.Cols, spec.Rows),
		cols: spec.Cols, rows: spec.Rows,
		running: true, to: p, input: make(chan []byte, 1024),
	}
	s.mu.Lock()
	s.sessions[label.ID] = t
	s.mu.Unlock()

	go func() {
		for data := range t.input {
			_ = proc.Write(data)
		}
	}()
	go func() {
		proc.Pump(t.output)
		t.mu.Lock()
		t.running = false
		if t.to != nil {
			_ = t.to.send(frame{typ: tEvt, payload: marshal(event{Event: "exited", ID: label.ID})})
		}
		t.mu.Unlock()
	}()
	return nil
}

// output keeps a chunk and passes it on. The screen is fed whether or not
// anyone is watching: an application started again has no history of its own,
// and the screen must show what was there before it closed.
func (t *session) output(chunk []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.screen.Write(chunk)
	if t.to != nil && t.to.send(frame{typ: tOut, id: t.label.ID, payload: chunk}) != nil {
		t.to.conn.Close()
		t.detachLocked()
	}
}

// attach hands p the screen as it stands and sends it everything printed
// after. Under the session's lock, so nothing printed in between is lost or
// sent twice.
func (t *session) attach(p *peer) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	if p.send(frame{typ: tOut, id: t.label.ID, payload: t.screen.Bytes()}) != nil {
		return t.running
	}
	t.to = p
	t.screen.Answer(nil)
	return t.running
}

// detachLocked leaves the session with nobody watching. A program that asks its
// terminal something — where the cursor is, what it can do — would otherwise
// wait for an answer until it gave up; the holder's screen answers instead.
func (t *session) detachLocked() {
	t.to = nil
	t.screen.Answer(func(reply []byte) {
		// Called from inside the screen's Write, which output holds mu for.
		if t.closed || !t.running {
			return
		}
		select {
		case t.input <- reply:
		default:
		}
	})
}

func (t *session) resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	if err := t.proc.Resize(cols, rows); err != nil {
		return err
	}
	t.screen.Resize(cols, rows)
	if cols > 1 && rows > 1 {
		t.cols, t.rows = cols, rows
	}
	return nil
}

func (t *session) write(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || !t.running {
		return
	}
	select {
	case t.input <- data:
	default:
		// A process this far behind on reading its input is not reading it.
	}
}

// forget drops a session the application has taken everything it needs from.
func (s *Server) forget(t *session) {
	t.proc.Kill()
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.input)
		t.screen.Close()
	}
	t.mu.Unlock()
	s.mu.Lock()
	if s.sessions[t.label.ID] == t {
		delete(s.sessions, t.label.ID)
	}
	s.mu.Unlock()
}

// shutdown ends every session and the holder.
func (s *Server) shutdown() {
	s.mu.Lock()
	all := s.all()
	s.mu.Unlock()
	for _, t := range all {
		s.forget(t)
	}
	s.stop()
}

func (s *Server) session(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// all copies the sessions out. Called with mu held.
func (s *Server) all() []*session {
	out := make([]*session, 0, len(s.sessions))
	for _, t := range s.sessions {
		out = append(out, t)
	}
	return out
}

// Serve is the whole of `xxvi pty-hold`: a holder on socket, until it is done.
func Serve(socket string) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("holder started", "socket", socket, "pid", os.Getpid())
	err := NewServer(socket, log).Run()
	log.Info("holder stopped", "err", err)
	return err
}
