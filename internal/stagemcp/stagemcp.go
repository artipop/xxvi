// Package stagemcp is the one thing a stage working in a terminal can say back
// to this application: that it has finished, how, and with what values.
//
// A session ends by itself and its last words are the outcome (engine.ParseWrites).
// An interactive CLI never ends — it draws its answer and waits for the next
// remark — so a stage worked in one needs a channel of its own, and this is it
// (docs/system.md §4.1.1). Everything else an agent does in a terminal it does
// in the folder, where it needs no permission from us.
//
// Served over HTTP on loopback rather than as a subprocess: the flow the report
// moves lives in *this* process, and a helper in between would be a proxy of
// ours to ourselves with the tool schema written twice. What guards it is what
// guards the terminal sockets — a port the operating system keeps off the
// network, and a token minted for one run, which dies when the run does.
package stagemcp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/artipop/xxvi/internal/model"
)

// ServerName is how the agent's CLI is told to call these tools. Short, because
// vendors prefix every tool name with it.
const ServerName = "xxvi"

// Report is what an agent hands back when its step ends: whether the work is
// done, what it did, and the values the stage declared it would leave on the
// card.
type Report struct {
	OK      bool
	Summary string
	Props   map[string]string
}

// Step is one run these tools are granted to. It is a description of the work,
// not a handle on the application: everything the agent may do is the one
// function it may call.
type Step struct {
	CardTitle string
	StageName string
	// Writes are the properties this stage declared. They are named in the
	// tool's own description, so an agent learns the requirement from the tool
	// it is about to call rather than from the refusal it gets back.
	Writes []model.PropertyWrite
	// Report delivers the answer. Its error is what the agent reads, so it is
	// written to be acted on: a missing required value names itself, the agent
	// adds it and calls again, and the step has not ended.
	Report func(Report) error
}

// Server is the loopback listener and the grants open on it.
type Server struct {
	log *slog.Logger

	mu     sync.Mutex
	addr   string
	grants map[string]Step

	http *http.Server
}

// New builds the server. Listen opens it.
func New(log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{log: log, grants: map[string]Step{}}
}

// Listen opens the port. Called once, when the application starts; a failure
// here is not fatal — it costs terminals their way to report, and the stage
// says so when it tries to start one.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("открыть порт для инструментов агента: %w", err)
	}
	s.mu.Lock()
	s.addr = ln.Addr().String()
	s.mu.Unlock()

	srv := &http.Server{Handler: s.handler()}
	s.mu.Lock()
	s.http = srv
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("сервер инструментов агента остановлен", "err", err)
		}
	}()
	return nil
}

// URL is where an agent's CLI is pointed. Empty until the port is open, and an
// empty address is how a caller learns there are no tools to hand over.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addr == "" {
		return ""
	}
	return "http://" + s.addr + "/mcp"
}

// Grant opens the tools for one run and returns the token that carries them.
// Revoke closes it: a grant outliving its step would be a door left open onto a
// card nobody is working any more.
func (s *Server) Grant(step Step) string {
	if step.Report == nil {
		return ""
	}
	token := uuid.NewString()
	s.mu.Lock()
	s.grants[token] = step
	s.mu.Unlock()
	return token
}

// Revoke closes one grant. Safe to call twice.
func (s *Server) Revoke(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	delete(s.grants, token)
	s.mu.Unlock()
}

// Close stops the listener and every grant on it.
func (s *Server) Close() {
	s.mu.Lock()
	srv := s.http
	s.grants = map[string]Step{}
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

func (s *Server) step(r *http.Request) (Step, bool) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		return Step{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.grants[token]
	return step, ok
}

// handler serves the tools over MCP's HTTP transport.
//
// Stateless on purpose: the report is one request, and a session id that
// outlived the check on the grant would be a second way in carrying no grant.
func (s *Server) handler() http.Handler {
	inner := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		step, ok := s.step(r)
		if !ok {
			return nil
		}
		return newServer(step)
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.step(r); !ok {
			http.Error(w, "этот шаг уже закончен", http.StatusForbidden)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// finishInput is the report. The properties arrive as a map rather than as
// fields of a schema built per stage: the names are the flow's, not the
// protocol's, and a tool whose shape changed with every card would be a tool
// no CLI could cache. What they are is said in the description instead, where
// the agent reads it before calling rather than after being refused.
type finishInput struct {
	Outcome    string            `json:"outcome" jsonschema:"how the step ended: done or failed"`
	Summary    string            `json:"summary" jsonschema:"what was done, in a few sentences — it lands in the card's comments and is what the next stage reads"`
	Properties map[string]string `json:"properties,omitempty" jsonschema:"the values this stage was asked to leave on the card, by property name"`
}

// newServer exposes one step's report as a tool.
func newServer(step Step) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Title: "XXVI", Version: "1"},
		&mcp.ServerOptions{Instructions: instructions(step)},
	)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "finish_step",
		Description: describe(step),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in finishInput) (*mcp.CallToolResult, any, error) {
		outcome := strings.ToLower(strings.TrimSpace(in.Outcome))
		switch outcome {
		case "done", "failed":
		default:
			return errorResult("«%s» — не исход шага. Ожидается done или failed.", in.Outcome), nil, nil
		}
		if err := step.Report(Report{
			OK:      outcome == "done",
			Summary: strings.TrimSpace(in.Summary),
			Props:   in.Properties,
		}); err != nil {
			return errorResult("%v", err), nil, nil
		}
		return textResult("Шаг записан. Карточка поехала дальше — новых указаний по ней не будет."), nil, nil
	})
	return srv
}

func instructions(step Step) string {
	var b strings.Builder
	b.WriteString("Это шаг флоу XXVI")
	if step.StageName != "" {
		fmt.Fprintf(&b, " — стадия «%s»", step.StageName)
	}
	if step.CardTitle != "" {
		fmt.Fprintf(&b, " по карточке «%s»", step.CardTitle)
	}
	b.WriteString(".\n\nЗакончив работу, вызови finish_step: пока он не вызван, карточка стоит здесь и никуда не едет. ")
	b.WriteString("Выход из терминала концом шага не считается.")
	return b.String()
}

// describe is the tool as this stage's agent sees it: what it does, and which
// values it must bring. Named here rather than only checked on arrival — a
// requirement an agent meets on the second call is a requirement it was told
// about too late.
func describe(step Step) string {
	var b strings.Builder
	b.WriteString("Сообщить, что шаг закончен, и чем. Вызывай, когда работа сделана или когда стало ясно, что сделать её нельзя: до этого вызова карточка стоит на месте.")
	if len(step.Writes) == 0 {
		return b.String()
	}
	b.WriteString("\n\nВ properties нужно передать:")
	for _, w := range step.Writes {
		fmt.Fprintf(&b, "\n- «%s»", w.Property)
		if w.Required {
			b.WriteString(" — обязательно, без него шаг не закончится")
		}
	}
	return b.String()
}

func textResult(text string) *mcp.CallToolResult {
	if strings.TrimSpace(text) == "" {
		text = "(пусто)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(format string, args ...any) *mcp.CallToolResult {
	res := textResult(fmt.Sprintf(format, args...))
	res.IsError = true
	return res
}
