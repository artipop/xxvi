// Package mcp speaks the Model Context Protocol to a server started as a
// subprocess, so a source can be an MCP server somebody else wrote.
//
// There are a lot of MCP servers, and many of them already reach exactly where
// a source would: a tracker, a mailbox, a calendar. What they do not have is a
// notion of "the list of things that are new" — MCP describes tools for a
// model, and nothing in it says which tool is a feed. That gap is filled on
// this side, by the manifest (sources.MCPSpec): it names the tool to call, the
// arguments to call it with and how to read one row of the answer as an item.
// The server stays untouched, which is the whole point — an adapter that
// required a patch would be a plugin with extra steps.
//
// Only the client half is here, and only the three messages a feed needs:
// initialize, tools/list (for diagnosing a manifest somebody typed by hand)
// and tools/call. The transport is the one everything else here uses —
// JSON-RPC 2.0, one message per line, over stdio — so procgroup spawns it and
// cleans it up like any other plugin.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/artipop/gandvik/internal/msg"
	"github.com/artipop/gandvik/internal/procgroup"
)

// ProtocolVersion is what this client asks for. A server that answers with a
// different one is not refused: MCP servers are expected to negotiate down, and
// refusing a version mismatch would break a working server over a number.
const ProtocolVersion = "2025-06-18"

// maxLine bounds one message, as the plugin client does: a server sending more
// than this is not speaking the protocol, and reading it would be our memory.
const maxLine = 8 << 20 // 8 MiB

// Spec is everything needed to start one MCP server.
type Spec struct {
	Command []string
	Dir     string
	Env     []string // "KEY=value", applied over the app's own environment
	DropEnv []string // names the server must not inherit

	// ClientName is what this app calls itself in the handshake. A server may
	// log it, and a server that refuses unknown clients has something to refuse.
	ClientName string
}

// Client is one running MCP server.
type Client struct {
	proc   *procgroup.Process
	server ServerInfo

	mu      sync.Mutex
	enc     *json.Encoder
	nextID  int64
	pending map[int64]chan rpcResponse
	closed  bool

	done chan struct{}
}

// ServerInfo is the server introducing itself, kept for error messages:
// "tracker 0.1.0 said no" is a sentence somebody can act on.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Tool is one tool a server offers, as tools/list reports it. Only what a
// person checking a manifest needs to see.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

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
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Dial starts the server and completes the handshake. On any failure the
// process is killed rather than left behind.
func Dial(ctx context.Context, spec Spec) (*Client, error) {
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
		done:    make(chan struct{}),
	}
	// The server's own logging goes to this process's stderr, which is where
	// every other subprocess here puts it: an MCP server explains a refused
	// token in a line printed there, and it belongs in the app's log beside the
	// source that failed.
	go c.read(proc.Stdout)

	name := spec.ClientName
	if name == "" {
		name = "Gandvik"
	}
	var result struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ServerInfo      ServerInfo `json:"serverInfo"`
	}
	err = c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		// Nothing is claimed: this client asks a server for a list and does
		// nothing else. A capability we do not implement is one a server may
		// call back into, which is a way to hang.
		"capabilities": map[string]any{},
		"clientInfo":   map[string]any{"name": name, "version": "1"},
	}, &result)
	if err != nil {
		c.Close()
		return nil, err
	}
	c.server = result.ServerInfo
	// The handshake is only complete once the server has been told so; a server
	// that follows the spec refuses tools/call before this.
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Server is what the server called itself.
func (c *Client) Server() ServerInfo { return c.server }

// Tools lists what the server offers. It is what a manifest naming a tool that
// does not exist is checked against — the error then says what there is.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// CallTool runs one tool and returns what it printed, parsed as JSON.
//
// MCP hands back a list of content blocks meant for a model to read, which for
// every server that returns data means one text block holding JSON. That is the
// convention this leans on, and it is checked rather than assumed: a block that
// is not JSON is reported as such, with the beginning of it, because the answer
// to that is always to look at what the tool actually printed.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// A tool that failed says so in the result rather than as a JSON-RPC
		// error: in MCP an error is for the protocol, and a tool refusing is a
		// normal answer the model is supposed to read.
		IsError bool `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result); err != nil {
		return nil, err
	}

	text := ""
	for _, block := range result.Content {
		if block.Type == "text" {
			text = strings.TrimSpace(block.Text)
			break
		}
	}
	if result.IsError {
		return nil, msg.Err("mcp.toolFailed", "tool", name, "text", text)
	}
	if text == "" {
		return nil, msg.Err("mcp.emptyAnswer", "tool", name)
	}
	if !json.Valid([]byte(text)) {
		return nil, msg.Err("mcp.notJSON", "tool", name, "text", truncate(text))
	}
	return json.RawMessage(text), nil
}

// Close stops the server. There is no shutdown message in MCP over stdio —
// closing the input is how a server is told — so the pipe goes first and the
// process group after it, which is what a server that spawned children of its
// own makes necessary.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.proc.Stdin.Close()
	c.proc.KillGroup(2 * time.Second)
	_ = c.proc.Wait()
	<-c.done
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.closed {
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
			return fmt.Errorf("%s: %s", method, resp.Error.Message)
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

func (c *Client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// read is the one goroutine that touches the server's output. Anything that is
// not an answer to a request is dropped: this client asks for lists and takes
// no callbacks, so a notification from the server has nobody to go to.
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
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.ID == nil {
			continue
		}
		c.mu.Lock()
		reply, ok := c.pending[*msg.ID]
		delete(c.pending, *msg.ID)
		c.mu.Unlock()
		if ok {
			reply <- msg
		}
	}
}

func truncate(s string) string {
	const limit = 400
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
