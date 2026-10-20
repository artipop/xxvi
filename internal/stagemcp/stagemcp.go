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
// network, and a token minted for one run, which dies when its terminal closes.
package stagemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/artipop/gandvik/internal/model"
)

// ServerName is how the agent's CLI is told to call these tools. Short, because
// vendors prefix every tool name with it.
const ServerName = "gandvik"

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
	// Brief is the route's part of the agent's instructions — the stage's
	// prompt, the card's branch and values — kept out of the conversation,
	// whose first message is the person's own.
	Brief string
	// Next names the stages a finished step leads to, so the agent can ask the
	// person about the move in the words the board shows.
	Next []string
	// Writes are the properties this stage declared. They are named in the
	// tool's own description, so an agent learns the requirement from the tool
	// it is about to call rather than from the refusal it gets back.
	Writes []model.PropertyWrite
	// Report delivers the answer. Its error is what the agent reads, so it is
	// written to be acted on: a missing required value names itself, the agent
	// adds it and calls again, and the step has not ended.
	Report func(Report) error
	// Hook takes what the CLI says about itself (hook.go). Optional: a CLI
	// without hooks is watched by its silence instead.
	Hook func(HookEvent)
	// Describe takes the card's title and description, written when the card
	// leaves its flow for the inbox. Optional. The tool is there from the
	// start rather than added when it is needed: a CLI reads the list of tools
	// once.
	Describe func(Description) error
	// Stages is the card's flow as the agent may name it in move_to. Read at
	// every request rather than at the grant: the flow can be edited and the
	// card moved while the CLI runs.
	Stages func() []StageInfo
	// Move sends the card to a stage by name. Optional: without it, or without
	// stages, there is no move_to.
	Move func(Move) error
}

// StageInfo is one stage of the flow, as move_to describes it.
type StageInfo struct {
	Name string
	// What the stage does with the card when it arrives, in a few words.
	What string
	// Here marks the stage the card stands on now.
	Here bool
}

// Move is a move_to call: where, and what this step leaves behind.
type Move struct {
	Stage   string
	Summary string
	Props   map[string]string
}

// Description is the task as its conversation left it.
type Description struct {
	Title string
	Text  string
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
		return fmt.Errorf("open the agent tools port: %w", err)
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
			s.log.Error("agent tools server stopped", "err", err)
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
		if r.URL.Path == "/hook" {
			s.serveHook(w, r)
			return
		}
		if _, ok := s.step(r); !ok {
			http.Error(w, "this step is already finished", http.StatusForbidden)
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
	Summary    string            `json:"summary" jsonschema:"what was done, in a few sentences — it lands in the task's comments and is what the next stage reads"`
	Properties map[string]string `json:"properties,omitempty" jsonschema:"the values this stage was asked to leave on the task, by property name"`
}

// newServer exposes one step's report as a tool.
func newServer(step Step) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Title: "Gandvík", Version: "1"},
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
			return errorResult("«%s» is not a step outcome. Expected done or failed.", in.Outcome), nil, nil
		}
		if err := step.Report(Report{
			OK:      outcome == "done",
			Summary: strings.TrimSpace(in.Summary),
			Props:   in.Properties,
		}); err != nil {
			return errorResult("%v", err), nil, nil
		}
		return textResult(stepRecorded), nil, nil
	})
	if step.Move != nil && step.Stages != nil {
		if stages := step.Stages(); len(stages) > 0 {
			srv.AddTool(&mcp.Tool{
				Name:        "move_to",
				Description: describeMove(step, stages),
				InputSchema: moveSchema(stages),
			}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var in moveInput
				if raw := req.Params.Arguments; len(raw) > 0 {
					if err := json.Unmarshal(raw, &in); err != nil {
						return errorResult("Could not read the arguments: %v", err), nil
					}
				}
				if strings.TrimSpace(in.Stage) == "" {
					return errorResult("Name the stage to move the task to."), nil
				}
				if err := step.Move(Move{Stage: in.Stage, Summary: strings.TrimSpace(in.Summary), Props: in.Properties}); err != nil {
					return errorResult("%v", err), nil
				}
				return textResult(stepRecorded), nil
			})
		}
	}
	if step.Describe != nil {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "describe_task",
			Description: describeCard,
		}, func(_ context.Context, _ *mcp.CallToolRequest, in describeInput) (*mcp.CallToolResult, any, error) {
			d := Description{Title: strings.TrimSpace(in.Title), Text: strings.TrimSpace(in.Description)}
			if d.Title == "" || d.Text == "" {
				return errorResult("Both the title and the description are needed."), nil, nil
			}
			if err := step.Describe(d); err != nil {
				return errorResult("%v", err), nil, nil
			}
			return textResult("Description saved. The session is being closed."), nil, nil
		})
	}
	return srv
}

type describeInput struct {
	Title       string `json:"title" jsonschema:"a short title for the task as it stands now — one line, no Markdown"`
	Description string `json:"description" jsonschema:"the task as it stands now, in Markdown, in the language of the conversation"`
}

// describeCard keeps the tool out of the agent's own initiative: it is the
// answer to one request the application types into the terminal, and a
// description written mid-task would overwrite the person's own words.
const describeCard = "Only when the application asks for it in this conversation: the task is going back to the inbox, " +
	"and this writes its description — what the task is, what has been done, what is left and what was decided, " +
	"so that somebody who was not in this conversation can pick it up. It replaces the task's title and text. " +
	"Never call it on your own, and never instead of finish_step."

// whenToFinish is said in both places for the same reason as lastCall. Left to
// judge «done» by feel, an agent closed the step after its first answer —
// often a question or a plan — before the person had read it, with the
// terminal gone and the card already on the next stage. Asking every time is
// the opposite failure: a person sitting through questions whose answer is
// obvious. So «done» is a list the agent can check, and doubt goes to the
// person.
func whenToFinish(step Step) string {
	next := "the next step"
	if len(step.Next) > 0 {
		quoted := make([]string, len(step.Next))
		for i, name := range step.Next {
			quoted[i] = "«" + name + "»"
		}
		next = strings.Join(quoted, " or ")
	}
	return fmt.Sprintf("A person is working with you in this terminal. Call finish_step with done on your own only when all of these hold: "+
		"everything the task and this stage ask for is actually done, not planned or proposed; "+
		"you have checked the result (built it, ran the tests, or looked at what it produces); "+
		"your changes are committed if you are on a branch; "+
		"you have every value this step has to leave on the task; "+
		"and nothing is waiting on the person — you have not just asked them a question, offered options or shown a plan to approve. "+
		"If any of these is in doubt, do not call it: say what you did and what is left open, ask «I'm done — move on to %s?», and wait. "+
		"Call it once the person agrees or asks you to finish; if they want something else, keep working. "+
		"Failed is the same: say why and ask before reporting it. ", next)
}

// The next stage may start immediately, sharing this working directory. The
// terminal is not closed after it: the person may come back and ask for more.
const lastCall = "Make it your last call, on its own: not alongside other tool calls, and only once the results of every earlier call are in — " +
	"after it, stop working and wait. The flow may start the next step immediately, in the same folder. " +
	"This terminal stays open: if the person comes back and asks for more, do it, and call finish_step again when it is done — " +
	"the task then goes on from this stage once more. Never do that on your own."

const stepRecorded = "Step recorded. Stop here and do not modify files: the flow may start its next step now, in the same folder. " +
	"The terminal stays open; do more only if the person asks."

// moveInput is read by hand rather than through a schema the SDK enforces: a
// CLI keeps the list of stages it was given at the start, and a stage renamed
// since should come back as a refusal that names the stages as they are now,
// not as a schema error.
type moveInput struct {
	Stage      string            `json:"stage"`
	Summary    string            `json:"summary"`
	Properties map[string]string `json:"properties,omitempty"`
}

func moveSchema(stages []StageInfo) map[string]any {
	names := make([]string, 0, len(stages))
	for _, st := range stages {
		names = append(names, st.Name)
	}
	return map[string]any{
		"type":     "object",
		"required": []string{"stage", "summary"},
		"properties": map[string]any{
			"stage": map[string]any{"type": "string", "enum": names,
				"description": "the stage to move the task to, by the name the board shows"},
			"summary": map[string]any{"type": "string",
				"description": "what was done and why the task goes there, in a few sentences — it lands in the task's comments"},
			"properties": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
				"description": "values this stage leaves on the task, by property name, if you have them"},
		},
	}
}

// describeMove names every stage with what it does, so a person's «deploy it»
// or «send it to review» finds its stage without the agent guessing.
func describeMove(step Step, stages []StageInfo) string {
	var b strings.Builder
	b.WriteString("Move the task to a stage of its flow by name. Only when the person asks for it in this conversation — «send it to review», «deploy it» — and never on your own: " +
		"when your work here is done and nobody named a stage, finish_step is the way on. ")
	b.WriteString("Leave the work in a state the next stage can take: committed, if you are on a branch. ")
	b.WriteString(lastCall)
	b.WriteString("\n\nThe stages:")
	for _, st := range stages {
		fmt.Fprintf(&b, "\n- «%s»", st.Name)
		if st.What != "" {
			b.WriteString(" — " + st.What)
		}
		if st.Here {
			b.WriteString(" (the task is here now)")
		}
	}
	return b.String()
}

func instructions(step Step) string {
	var b strings.Builder
	b.WriteString("This is a step of a Gandvík flow")
	if step.StageName != "" {
		fmt.Fprintf(&b, " — the stage «%s»", step.StageName)
	}
	if step.CardTitle != "" {
		fmt.Fprintf(&b, " for the task «%s»", step.CardTitle)
	}
	b.WriteString(".\n\nThe step ends with a call to finish_step: until it is called, the task stands here and goes nowhere. ")
	b.WriteString("Leaving the terminal does not count as finishing the step. ")
	b.WriteString(whenToFinish(step))
	b.WriteString(lastCall)
	if step.Move != nil {
		b.WriteString(" When the person asks to send the task to a particular stage — by its name or by what it does — use move_to instead.")
	}
	if brief := strings.TrimSpace(step.Brief); brief != "" {
		b.WriteString("\n\n")
		b.WriteString(brief)
	}
	return b.String()
}

// describe is the tool as this stage's agent sees it: what it does, and which
// values it must bring. Named here rather than only checked on arrival — a
// requirement an agent meets on the second call is a requirement it was told
// about too late.
func describe(step Step) string {
	var b strings.Builder
	b.WriteString("Report that the step is finished, and how: until this call the task stays where it is. ")
	b.WriteString(whenToFinish(step))
	b.WriteString(lastCall)
	if len(step.Writes) == 0 {
		return b.String()
	}
	b.WriteString("\n\nPass in properties:")
	for _, w := range step.Writes {
		fmt.Fprintf(&b, "\n- «%s»", w.Property)
		if w.Required {
			b.WriteString(" — required, the step does not finish without it")
		}
	}
	return b.String()
}

func textResult(text string) *mcp.CallToolResult {
	if strings.TrimSpace(text) == "" {
		text = "(empty)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(format string, args ...any) *mcp.CallToolResult {
	res := textResult(fmt.Sprintf(format, args...))
	res.IsError = true
	return res
}
