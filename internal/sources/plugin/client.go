package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/artipop/gandvik/internal/msg"
	"github.com/artipop/gandvik/internal/procgroup"
)

// Spec is everything needed to start one plugin: the command, where it runs and
// what it is being started for.
type Spec struct {
	Command []string
	Dir     string
	Env     []string // "KEY=value", applied over the app's own environment
	DropEnv []string // names the plugin must not inherit

	Source      SourceInfo
	Credentials Credentials
	Host        HostInfo
}

// Handler receives what a plugin says without being asked. Every method may be
// called from the reader goroutine, so an implementation must not block for
// long.
type Handler interface {
	Items(items []json.RawMessage, cursor string)
	Log(level, message string)
	NeedsReauth(reason string)
}

// Client is one running plugin.
type Client struct {
	proc *procgroup.Process
	caps Capabilities

	mu      sync.Mutex
	enc     *json.Encoder
	nextID  int64
	pending map[int64]chan rpcResponse
	closed  bool

	handler Handler
	done    chan struct{}
}

// rpcRequest and rpcResponse are the wire shapes. They are written by hand
// rather than taken from a library because the whole protocol is four methods
// and three notifications, and a dependency would be larger than the code.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error is what a plugin refused with. Kind is from the closed set in
// protocol.go and is how the caller decides whether to come back.
type Error struct {
	Code    int
	Message string
	Kind    string
	Field   string
}

func (e *Error) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s (field %q)", e.Message, e.Field)
	}
	return e.Message
}

// Retryable reports whether coming back later could work.
func (e *Error) Retryable() bool { return e.Kind == KindRetryable }

// NeedsReauth reports whether a person has to log in again.
func (e *Error) NeedsReauth() bool { return e.Kind == KindNeedsReauth }

// maxLine bounds one message. A plugin that sends more than this is not
// speaking the protocol, and reading it would be the app's own memory.
const maxLine = 8 << 20 // 8 MiB

// Dial starts the plugin and completes the handshake. On any failure the
// process is killed rather than left behind.
func Dial(ctx context.Context, spec Spec, handler Handler) (*Client, error) {
	if len(spec.Command) == 0 {
		return nil, msg.Err("plugin.noCommand")
	}
	proc, err := procgroup.Spawn(ctx, spec.Command, spec.Dir, spec.Env, spec.DropEnv...)
	if err != nil {
		return nil, msg.Wrap(err, "plugin.notStarted", "command", spec.Command[0])
	}
	c := &Client{
		proc:    proc,
		enc:     json.NewEncoder(proc.Stdin),
		pending: map[int64]chan rpcResponse{},
		handler: handler,
		done:    make(chan struct{}),
	}
	go c.read(proc.Stdout)

	var result InitializeResult
	err = c.call(ctx, MethodInitialize, InitializeParams{
		ProtocolVersion: Version,
		Source:          spec.Source,
		Credentials:     spec.Credentials,
		Host:            spec.Host,
	}, &result)
	if err != nil {
		c.Close()
		return nil, err
	}
	// A plugin from a newer build is refused here rather than misunderstood
	// three messages later.
	if result.ProtocolVersion > Version {
		c.Close()
		return nil, msg.Err("plugin.newerProtocol",
			"theirs", strconv.Itoa(result.ProtocolVersion), "ours", strconv.Itoa(Version))
	}
	c.caps = result.Capabilities
	return c, nil
}

// Capabilities is what the plugin said it can do.
func (c *Client) Capabilities() Capabilities { return c.caps }

// Poll asks for what is new.
func (c *Client) Poll(ctx context.Context, cursor string) (PollResult, error) {
	var out PollResult
	err := c.call(ctx, MethodPoll, PollParams{Cursor: cursor}, &out)
	return out, err
}

// UpdateCredentials hands over a refreshed token without restarting anything.
func (c *Client) UpdateCredentials(ctx context.Context, cred Credentials) error {
	return c.call(ctx, MethodCredentialsUpdate, cred, nil)
}

// Close asks the plugin to stop and then makes sure it did. Shutdown is given a
// short moment because a plugin may have a file to flush; after that the
// process group goes, which is what a plugin that has spawned children of its
// own makes necessary.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = c.call(ctx, MethodShutdown, nil, nil)
	cancel()

	c.proc.KillGroup(2 * time.Second)
	_ = c.proc.Wait()
	<-c.done
}

// call sends a request and waits for its answer, the context bounding the wait.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.closed && method != MethodShutdown {
		c.mu.Unlock()
		return msg.Err("plugin.stopped")
	}
	c.nextID++
	id := c.nextID
	reply := make(chan rpcResponse, 1)
	c.pending[id] = reply
	err := c.enc.Encode(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	c.mu.Unlock()
	if err != nil {
		c.forget(id)
		return fmt.Errorf("%s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	case <-c.done:
		c.forget(id)
		return msg.Err("plugin.exited", "method", method, "stderr", c.proc.StderrTail())
	case resp := <-reply:
		if resp.Error != nil {
			return toError(resp.Error)
		}
		if out == nil || len(resp.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return msg.Wrap(err, "plugin.badAnswer", "method", method)
		}
		return nil
	}
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// read is the one goroutine that touches the plugin's output: responses go to
// whoever is waiting, notifications to the handler.
func (c *Client) read(r io.Reader) {
	defer close(c.done)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg rpcResponse
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// A line that is not a message is not fatal: a plugin that prints
			// to stdout by accident should cost that line and not the source.
			c.notifyLog("warn", "not a message: "+truncate(line))
			continue
		}
		if msg.ID != nil && msg.Method == "" {
			c.deliver(*msg.ID, msg)
			continue
		}
		c.handle(msg)
	}
}

func (c *Client) deliver(id int64, msg rpcResponse) {
	c.mu.Lock()
	reply, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		reply <- msg
	}
}

func (c *Client) handle(msg rpcResponse) {
	if c.handler == nil {
		return
	}
	switch msg.Method {
	case NotifyItems:
		var params ItemsNotification
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			c.handler.Items(params.Items, params.Cursor)
		}
	case NotifyLog:
		var params LogNotification
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			c.handler.Log(params.Level, params.Message)
		}
	case NotifyNeedsReauth:
		var params ReauthNotification
		if err := json.Unmarshal(msg.Params, &params); err == nil {
			c.handler.NeedsReauth(params.Reason)
		}
	}
}

func (c *Client) notifyLog(level, message string) {
	if c.handler != nil {
		c.handler.Log(level, message)
	}
}

func toError(e *rpcError) error {
	out := &Error{Code: e.Code, Message: e.Message}
	if len(e.Data) > 0 {
		var data struct {
			Kind  string `json:"kind"`
			Field string `json:"field"`
		}
		if err := json.Unmarshal(e.Data, &data); err == nil {
			out.Kind, out.Field = data.Kind, data.Field
		}
	}
	return out
}

func truncate(s string) string {
	const limit = 200
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
