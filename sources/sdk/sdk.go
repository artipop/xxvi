// Package sdk is what a source plugin written in Go is built on. It is the
// other end of sources/protocol: the app spawns the plugin and asks, this reads
// the asking and calls the functions given to Serve.
//
// The whole of it is the plumbing an author should not have to write — framing,
// ids, error shapes — and nothing else. A plugin does one thing: it returns
// items. It never sees a board, a card or a rule, and there is deliberately no
// way from here to reach one.
//
// The smallest plugin there is:
//
//	func main() {
//	    sdk.Serve(sdk.Source{
//	        Capabilities: protocol.Capabilities{Poll: true},
//	        Poll: func(ctx context.Context, r sdk.PollRequest) (sdk.PollResult, error) {
//	            return sdk.PollResult{Items: []sdk.Item{{ExternalID: "1", Title: "Hello"}}}, nil
//	        },
//	    })
//	}
package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/artipop/xxvi/sources/protocol"
)

// Item is one thing a plugin found. The fields that matter are the first two:
// ExternalID says which thing this is, Version says which state of it, and
// together they are what keeps the app from making a second card for something
// it already has.
type Item struct {
	ExternalID string            `json:"id"`
	Version    string            `json:"version,omitempty"`
	Title      string            `json:"title"`
	Body       string            `json:"body,omitempty"`
	URL        string            `json:"url,omitempty"`
	At         time.Time         `json:"at,omitempty"`
	Props      map[string]string `json:"props,omitempty"`
	Labels     []string          `json:"labels,omitempty"`
}

// PollRequest is what the app is asking for.
type PollRequest struct {
	// Config is what the person filled into the form the manifest declared.
	Config map[string]string
	// Credentials is the access token of this source, and nothing else: the
	// app performs the authorization and keeps the refresh token.
	Credentials protocol.Credentials
	// Cursor is whatever this plugin returned last time. It is opaque to the
	// app, which only stores it, so it can be anything that fits in a string.
	Cursor string
}

// PollResult is the answer.
type PollResult struct {
	Items  []Item
	Cursor string
	// RetryAfter asks the app to wait at least this long before asking again —
	// what to answer when a service says it has had enough.
	RetryAfter time.Duration
}

// Error is a refusal the app can act on. Use Retryable for a network failure,
// NeedsReauth for a dead credential and BadConfig for a field that is wrong;
// anything else is treated as a defect of the plugin.
type Error struct {
	Kind    string
	Field   string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Retryable is a failure worth coming back for.
func Retryable(format string, args ...any) *Error {
	return &Error{Kind: protocol.KindRetryable, Message: fmt.Sprintf(format, args...)}
}

// NeedsReauth says the credential is dead and only a person can fix it.
func NeedsReauth(format string, args ...any) *Error {
	return &Error{Kind: protocol.KindNeedsReauth, Message: fmt.Sprintf(format, args...)}
}

// BadConfig says a field is wrong. The app shows the message against that
// field and stops asking until somebody changes it.
func BadConfig(field, format string, args ...any) *Error {
	return &Error{Kind: protocol.KindBadConfig, Field: field, Message: fmt.Sprintf(format, args...)}
}

// Source is the plugin itself: what it can do, and the functions that do it.
type Source struct {
	Capabilities protocol.Capabilities

	// Poll is called on the app's schedule. Required when Capabilities.Poll.
	Poll func(ctx context.Context, req PollRequest) (PollResult, error)
	// Start is called once, after the handshake, for a plugin that watches
	// something rather than being asked. It is given a Session to push through
	// and must return promptly — the watching itself belongs on a goroutine.
	Start func(ctx context.Context, s *Session) error
	// Shutdown is called when the app is closing, before the process is ended.
	Shutdown func()
}

// Session is what a plugin talks to the app through outside of an answer.
type Session struct {
	// Config and Credentials are the same values Poll is given, for a plugin
	// that never polls.
	Config      map[string]string
	Credentials protocol.Credentials

	mu  sync.Mutex
	out *json.Encoder
}

// Items delivers what the plugin found without being asked.
func (s *Session) Items(items []Item, cursor string) {
	s.send(protocol.NotifyItems, protocol.ItemsNotification{Items: rawItems(items), Cursor: cursor})
}

// Log puts a line in the source's own log, which is where somebody looks when
// nothing happened.
func (s *Session) Log(level, message string) {
	s.send(protocol.NotifyLog, protocol.LogNotification{Level: level, Message: message})
}

// NeedsReauth says the credential died between polls.
func (s *Session) NeedsReauth(reason string) {
	s.send(protocol.NotifyNeedsReauth, protocol.ReauthNotification{Reason: reason})
}

func (s *Session) send(method string, params any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.out.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// Serve runs the plugin until the app closes its input or asks it to stop. It
// is the whole of a plugin's main.
func Serve(source Source) {
	serve(source, os.Stdin, os.Stdout)
}

func serve(source Source, in io.Reader, out io.Writer) {
	// stdout is the protocol and nothing else. A plugin that prints to it by
	// accident costs itself that line — the app says so and carries on — but
	// the framing here stays clean.
	encoder := json.NewEncoder(out)
	session := &Session{out: encoder}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}

		switch req.Method {
		case protocol.MethodInitialize:
			var params protocol.InitializeParams
			_ = json.Unmarshal(req.Params, &params)
			session.Config = params.Source.Config
			session.Credentials = params.Credentials
			reply(encoder, req.ID, protocol.InitializeResult{
				ProtocolVersion: protocol.Version,
				Capabilities:    source.Capabilities,
			})
			if source.Start != nil {
				if err := source.Start(ctx, session); err != nil {
					session.Log("error", err.Error())
				}
			}
		case protocol.MethodPoll:
			handlePoll(ctx, source, session, encoder, req.ID, req.Params)
		case protocol.MethodCredentialsUpdate:
			var cred protocol.Credentials
			_ = json.Unmarshal(req.Params, &cred)
			session.mu.Lock()
			session.Credentials = cred
			session.mu.Unlock()
			reply(encoder, req.ID, map[string]any{})
		case protocol.MethodShutdown:
			if source.Shutdown != nil {
				source.Shutdown()
			}
			reply(encoder, req.ID, map[string]any{})
			return
		}
	}
	// The app closed the pipe without a shutdown — it crashed, or was killed.
	// The plugin's own cleanup still runs.
	if source.Shutdown != nil {
		source.Shutdown()
	}
}

func handlePoll(ctx context.Context, source Source, session *Session, out *json.Encoder, id *int64, raw json.RawMessage) {
	if source.Poll == nil {
		fail(out, id, &Error{Message: "плагин не умеет отвечать на poll"})
		return
	}
	var params protocol.PollParams
	_ = json.Unmarshal(raw, &params)

	session.mu.Lock()
	req := PollRequest{Config: session.Config, Credentials: session.Credentials, Cursor: params.Cursor}
	session.mu.Unlock()

	result, err := source.Poll(ctx, req)
	if err != nil {
		fail(out, id, err)
		return
	}
	reply(out, id, protocol.PollResult{
		Items:             rawItems(result.Items),
		Cursor:            result.Cursor,
		RetryAfterSeconds: int(result.RetryAfter.Seconds()),
	})
}

func reply(out *json.Encoder, id *int64, result any) {
	_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func fail(out *json.Encoder, id *int64, err error) {
	message := err.Error()
	data := map[string]string{}
	if e, ok := err.(*Error); ok {
		if e.Kind != "" {
			data["kind"] = e.Kind
		}
		if e.Field != "" {
			data["field"] = e.Field
		}
	}
	_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{
		"code": -32000, "message": message, "data": data,
	}})
}

func rawItems(items []Item) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			continue
		}
		out = append(out, encoded)
	}
	return out
}
