// Package appmcp is this application from the outside: the moves a person makes
// on a card, offered to an agent that nobody here started.
//
// It is the twin of internal/stagemcp and not the same thing. There an agent we
// launched reports the one step it was given, and the grant dies with the run.
// Here the caller is somebody else's session — a CLI in another window, an
// agent on a schedule — and what it may do is what a person may do: read the
// inbox, put a card on a flow, set a value, say a step is over. Nothing new
// happens to a card because the caller was an agent: every tool ends in one of
// the engine's three roads (engine.TakeIntoWork, CardChanged, Finished), and a
// fourth road is exactly what this must not become.
//
// The door is the one stagemcp uses — loopback HTTP and a bearer token — with
// one difference that follows from the caller outliving nothing: the token is
// the install's, kept in the database, rather than minted per run. Where the
// port is, is written beside it (handoff.go), because the port is new on every
// launch and a configuration file is not.
package appmcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// ServerName is how a CLI is told to call these tools. The same short name
// stagemcp uses: a session sees one of the two and never both.
const ServerName = "xxvi"

// tokenSetting is where the install's token lives. In the database rather than
// in a file, because it is as long-lived as the cards are and everything else
// that long-lived is there.
const tokenSetting = "mcp.token"

// Filer files a card somebody typed — the inbox pipeline, which is what makes a
// card added here an ordinary card rather than a row written behind the rules.
type Filer interface {
	AddManual(source, title, body string, props map[string]string) (model.Card, error)
}

// Folders is where a card's work happens: the same folder its agents work in,
// so a page an outside agent writes is the page the ribbon's browser opens.
type Folders interface {
	WorkDir(cardID string) (string, error)
}

// Deps is everything these tools touch. Interfaces rather than the application
// itself: internal/app wires this package, and a handle on the application
// would be a cycle.
type Deps struct {
	Store  *store.Store
	Engine *engine.Engine
	// Filer and Folders may be nil, and the tools that need them then say what
	// is missing instead of panicking — a headless run has no inbox pipeline.
	Filer   Filer
	Folders Folders
	// Emit tells the UI a card changed. Optional: without a window there is
	// nobody to tell, and the state is in the database either way.
	Emit func(event string, payload any)
}

// Server is the loopback listener, the install's token, and the tools on it.
type Server struct {
	deps Deps
	log  *slog.Logger

	mu    sync.Mutex
	addr  string
	token string

	http *http.Server
}

// New builds the server. Listen opens it.
func New(deps Deps, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{deps: deps, log: log}
}

// Listen opens the port and settles the token, reading the one this install
// already has or minting it on a first run.
//
// A failure here is not fatal: it costs outside agents their way in, and
// everything a person does in the window works without it.
func (s *Server) Listen() error {
	token, err := s.deps.Store.Setting(tokenSetting, "")
	if err != nil {
		return fmt.Errorf("read the application tools token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		token = uuid.NewString()
		if err := s.deps.Store.SetSetting(tokenSetting, token); err != nil {
			return fmt.Errorf("write the application tools token: %w", err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("open the application tools port: %w", err)
	}
	srv := &http.Server{Handler: s.handler()}

	s.mu.Lock()
	s.addr, s.token, s.http = ln.Addr().String(), token, srv
	s.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("application tools server stopped", "err", err)
		}
	}()
	return nil
}

// URL is where a caller connects. Empty until the port is open, which is how a
// caller learns there is nothing to connect to.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addr == "" {
		return ""
	}
	return "http://" + s.addr + "/mcp"
}

// Token is the install's bearer token.
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// Close stops the listener.
func (s *Server) Close() {
	s.mu.Lock()
	srv := s.http
	s.http, s.addr = nil, ""
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

func (s *Server) allowed(r *http.Request) bool {
	want := s.Token()
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// handler serves the tools over MCP's HTTP transport, stateless for the same
// reason stagemcp is: one request carries its own right to be here, and a
// session id that outlived the check would be a second way in.
func (s *Server) handler() http.Handler {
	inner := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if !s.allowed(r) {
			return nil
		}
		return s.mcp()
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowed(r) {
			http.Error(w, "wrong token", http.StatusForbidden)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// instructions is what a session reads before it calls anything: the shape of
// the application in the smallest number of sentences that still makes the
// tools predictable.
const instructions = `This is XXVI — an inbox and flows. A task sits in the inbox,
a person or you put it on a flow (take_into_work), and from there it travels through stages.

A stage is the only place a task stands. A step on it ends with a report
(finish_step): the step's outcome plus the values the stage promised to leave on
the task. Where the task goes after the report is the flow's decision, not the
caller's — so "go to the next step" here is exactly finish_step with the outcome done.

A stage where nothing runs waits for an answer: there finish_step sets
"` + model.OutcomeProperty + `", and any other value it waits for is set with set_property.`

// mcp is the tool set. Built per request rather than once: the server is
// stateless, and a tool set holding nothing per-call costs nothing to build.
func (s *Server) mcp() *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Title: "XXVI", Version: "1"},
		&mcp.ServerOptions{Instructions: instructions},
	)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "flows",
		Description: "The flows there are: their stages, what happens on them, which values they leave on the task and along which edges the task moves on.",
	}, s.flows)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "tasks",
		Description: "Tasks: in the inbox, in work or closed. For those in work, the stage they stand on and what they wait for.",
	}, s.cards)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "task",
		Description: "One task in full: text, properties, stage, what it waits for, working folder and the latest entries of its history.",
	}, s.card)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_task",
		Description: "Add a task to the inbox. It goes nowhere until it is put on a flow.",
	}, s.addCard)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "take_into_work",
		Description: "Put a task from the inbox on the entry stage of a flow. This is the only way to start it moving.",
	}, s.takeIntoWork)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "finish_step",
		Description: "Say that the task's current step is finished, and how — and so move on to the next one. " +
			"On an agent stage this is the work report: outcome done or failed plus the declared values. " +
			"On a stage where nothing runs it answers for it: done sets «" + model.OutcomeProperty + "» = «" + model.OutcomePassed +
			"», failed sets «" + model.OutcomeFailed + "». " +
			"Where the task goes is the flow's decision.",
	}, s.finishStep)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_property",
		Description: "Set a property of a task. If the stage waits for exactly this value, the task moves on; " +
			"any other property is simply put on the card.",
	}, s.setProperty)
	return srv
}

// ---- tools ----

type noInput struct{}

func (s *Server) flows(_ context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	flows, err := s.deps.Store.Flows()
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	if len(flows) == 0 {
		return text("There are no flows yet."), nil, nil
	}
	var b strings.Builder
	for i, f := range flows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(describeFlow(f))
	}
	return text(b.String()), nil, nil
}

type cardsInput struct {
	State string `json:"state,omitempty" jsonschema:"which tasks: work — in work on a flow (the default), inbox — in the inbox, done — closed"`
}

func (s *Server) cards(_ context.Context, _ *mcp.CallToolRequest, in cardsInput) (*mcp.CallToolResult, any, error) {
	state := model.StateFlow
	switch strings.ToLower(strings.TrimSpace(in.State)) {
	case "", "work", "flow":
	case "inbox":
		state = model.StateInbox
	case "done":
		state = model.StateDone
	case "dropped":
		state = model.StateDropped
	default:
		return errorf("«%s» is not a task state. There are work, inbox, done and dropped.", in.State), nil, nil
	}
	cards, err := s.deps.Store.CardsInState(state)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	if len(cards) == 0 {
		return text("There are no such tasks."), nil, nil
	}
	var b strings.Builder
	for _, c := range cards {
		fmt.Fprintf(&b, "- «%s» [%s]", c.Title, c.ID)
		if place, err := s.deps.Engine.CardFlowFor(c.ID); err == nil && place != nil {
			fmt.Fprintf(&b, " — flow «%s», stage «%s»", place.FlowName, stageName(*place))
			switch {
			case place.Running:
				b.WriteString(", an application agent is working the step")
			case place.Queued:
				b.WriteString(", waiting for room on the stage")
			case len(place.WaitingFor) > 0:
				fmt.Fprintf(&b, ", waiting for: %s", describeWaits(place.WaitingFor))
			}
		}
		b.WriteString("\n")
	}
	return text(b.String()), nil, nil
}

type cardInput struct {
	Card string `json:"task" jsonschema:"the task: its id or title"`
}

func (s *Server) card(_ context.Context, _ *mcp.CallToolRequest, in cardInput) (*mcp.CallToolResult, any, error) {
	card, err := s.resolveCard(in.Card)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	return text(s.describeCard(card)), nil, nil
}

type addCardInput struct {
	Title string `json:"title" jsonschema:"the title — what has to be done"`
	Body  string `json:"body,omitempty" jsonschema:"details: what is known about the task"`
}

func (s *Server) addCard(_ context.Context, _ *mcp.CallToolRequest, in addCardInput) (*mcp.CallToolResult, any, error) {
	if s.deps.Filer == nil {
		return errorf("tasks cannot be added right now — the inbox is not up"), nil, nil
	}
	card, err := s.deps.Filer.AddManual("", in.Title, in.Body, nil)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	return text(fmt.Sprintf("The task «%s» [%s] is in the inbox. To get it moving, put it on a flow (take_into_work).",
		card.Title, card.ID)), nil, nil
}

type takeInput struct {
	Card   string `json:"task" jsonschema:"the task: its id or title"`
	Flow   string `json:"flow" jsonschema:"the flow: its name or id"`
	Worker string `json:"worker,omitempty" jsonschema:"who works the steps: the name of an agent in the application registry — then the application starts it; any other name — then the application starts nobody, and the steps are worked by whoever calls these tools"`
}

func (s *Server) takeIntoWork(_ context.Context, _ *mcp.CallToolRequest, in takeInput) (*mcp.CallToolResult, any, error) {
	card, err := s.resolveCard(in.Card)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	flow, err := s.resolveFlow(in.Flow)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	// The worker is set before the card moves: the entry stage starts the
	// moment it arrives, and an assignee arriving after that would be a
	// decision taken too late.
	if who := strings.TrimSpace(in.Worker); who != "" {
		if _, err := s.deps.Store.UpdateCard(card.ID, store.CardEdit{Assignee: &who}); err != nil {
			return errorf("%v", err), nil, nil
		}
	}
	if err := s.deps.Engine.TakeIntoWork(card.ID, flow.ID); err != nil {
		return errorf("%v", err), nil, nil
	}
	s.emit(card.ID)
	return text(s.describeCard(s.reread(card))), nil, nil
}

type finishInput struct {
	Card       string            `json:"task" jsonschema:"the task: its id or title"`
	Outcome    string            `json:"outcome" jsonschema:"how the step ended: done or failed"`
	Summary    string            `json:"summary" jsonschema:"what was done, in a few sentences — it goes into the task's history and is what the next stage reads"`
	Properties map[string]string `json:"properties,omitempty" jsonschema:"the values the stage promised to leave on the task, by property name"`
}

// finishStep ends the step a card stands on. Which of the engine's roads that
// is depends on the stage and not on the caller: a stage that runs something
// has an outcome, and a stage that runs nothing has an answer.
func (s *Server) finishStep(_ context.Context, _ *mcp.CallToolRequest, in finishInput) (*mcp.CallToolResult, any, error) {
	card, err := s.resolveCard(in.Card)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	var ok bool
	switch strings.ToLower(strings.TrimSpace(in.Outcome)) {
	case "done", "success":
		ok = true
	case "failed", "failure", "fail":
	default:
		return errorf("«%s» is not a step outcome. Expected done or failed.", in.Outcome), nil, nil
	}

	st, onFlow, err := s.deps.Store.FlowState(card.ID)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	if !onFlow {
		return errorf("the task «%s» is not in work (%s) — it has no step to finish. Put it on a flow: take_into_work.",
			card.Title, card.State), nil, nil
	}
	flow, err := s.deps.Store.Flow(st.FlowID)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	stage, found := flow.Stage(st.StageID)
	if !found {
		return errorf("the task's stage is gone from the flow «%s» — move the task in the application", flow.Name), nil, nil
	}
	if s.running(card.ID) {
		return errorf("an application agent is working this step — the report will come from it. " +
			"To take the step over, cancel it in the application."), nil, nil
	}

	if stage.Action == model.ActionPublish || stage.Action == model.ActionVerdict {
		return errorf("the application works this step itself — it talks to the hosting and moves the task when done"), nil, nil
	}
	if stage.Action == model.ActionAgent {
		if missing := missingRequired(stage.Writes, ok, in.Properties); missing != "" {
			return errorf("the step is not finished: %s missing. Add the value and call again.", missing), nil, nil
		}
		on := model.TriggerSuccess
		if !ok {
			on = model.TriggerFailure
		}
		s.deps.Engine.Finished(card.ID, on, msg.New("outcome.reported"), reportText(stage.Writes, in.Summary, in.Properties))
		return text(s.moved(card, flow, stage)), nil, nil
	}

	// A stage that runs nothing has no outcome of its own: what moves it is a
	// person's mark, and this is that mark made by somebody else. Refused where
	// the stage is not waiting for it, because a value nothing reads is a
	// caller left believing the card will move.
	if !flow.WatchesProperty(stage.ID, model.OutcomeProperty) {
		waits := flow.Waits(stage.ID)
		if len(waits) == 0 {
			return errorf("the stage «%s» does not wait for an answer — from here the task is moved in the application.", stage.Name), nil, nil
		}
		return errorf("the stage «%s» waits not for a step outcome but for %s. Set that value: set_property.",
			stage.Name, describeWaits(waits)), nil, nil
	}
	if err := s.putProps(card.ID, in.Properties); err != nil {
		return errorf("%v", err), nil, nil
	}
	if summary := strings.TrimSpace(in.Summary); summary != "" {
		if _, err := s.deps.Store.Record(model.JournalEntry{CardID: card.ID, Kind: model.EntryReport, Text: summary}); err != nil {
			s.log.Warn("could not record the step summary", "card", card.ID, "err", err)
		}
	}
	value := model.OutcomePassed
	if !ok {
		value = model.OutcomeFailed
	}
	if _, err := s.deps.Store.UpdateCard(card.ID, store.CardEdit{
		Props: map[string]string{model.OutcomeProperty: value},
	}); err != nil {
		return errorf("%v", err), nil, nil
	}
	s.deps.Engine.CardChanged(card.ID, model.OutcomeProperty, value)
	s.emit(card.ID)
	return text(s.moved(card, flow, stage)), nil, nil
}

type propInput struct {
	Card     string `json:"task" jsonschema:"the task: its id or title"`
	Property string `json:"property" jsonschema:"the property name, as the flow asks for it"`
	Value    string `json:"value" jsonschema:"the value"`
}

func (s *Server) setProperty(_ context.Context, _ *mcp.CallToolRequest, in propInput) (*mcp.CallToolResult, any, error) {
	card, err := s.resolveCard(in.Card)
	if err != nil {
		return errorf("%v", err), nil, nil
	}
	name := strings.TrimSpace(in.Property)
	if name == "" {
		return errorf("the property has no name"), nil, nil
	}
	if _, err := s.deps.Store.UpdateCard(card.ID, store.CardEdit{Props: map[string]string{name: in.Value}}); err != nil {
		return errorf("%v", err), nil, nil
	}
	// Told to the engine the way a person's edit is told: whether any stage was
	// waiting for exactly this is the engine's to decide.
	if strings.TrimSpace(in.Value) != "" {
		s.deps.Engine.CardChanged(card.ID, name, in.Value)
	}
	s.emit(card.ID)
	return text(s.describeCard(s.reread(card))), nil, nil
}

// ---- what the tools answer with ----

// moved is the answer to a report: where the card is now, and — when it did not
// move — what the flow said about that, because the engine writes the reason
// into the card's own history rather than returning it.
func (s *Server) moved(card model.Card, flow model.Flow, from model.Stage) string {
	fresh := s.reread(card)
	var b strings.Builder
	place, _ := s.deps.Engine.CardFlowFor(card.ID)
	switch {
	case fresh.State == model.StateDone:
		fmt.Fprintf(&b, "Step recorded. Task «%s»: «%s» → the flow «%s» is complete, the task is closed.", fresh.Title, from.Name, flow.Name)
	case place == nil:
		fmt.Fprintf(&b, "Step recorded. The task «%s» is no longer on a flow (%s).", fresh.Title, fresh.State)
	case place.StageID == from.ID:
		fmt.Fprintf(&b, "Step recorded, but the task «%s» stayed on «%s».", fresh.Title, from.Name)
	default:
		fmt.Fprintf(&b, "Step recorded. Task «%s»: «%s» → «%s».", fresh.Title, from.Name, stageName(*place))
	}
	if outcome := fresh.Prop(model.OutcomeProperty); outcome != "" {
		fmt.Fprintf(&b, " %s: %s.", model.OutcomeProperty, outcome)
	}
	if last := s.lastEntries(card.ID, 2); last != "" {
		b.WriteString("\n\n")
		b.WriteString(last)
	}
	return b.String()
}

// describeCard is one card as a caller needs it: the task, what stands on it,
// where it is, and what the stage it is on expects.
func (s *Server) describeCard(card model.Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "«%s» [%s]\nState: %s", card.Title, card.ID, card.State)
	if card.Assignee != "" {
		fmt.Fprintf(&b, ", worked by: %s", card.Assignee)
	}
	b.WriteString("\n")
	if body := strings.TrimSpace(card.Body); body != "" {
		fmt.Fprintf(&b, "\n%s\n", body)
	}
	if len(card.Props) > 0 {
		b.WriteString("\nProperties:\n")
		for _, name := range sortedKeys(card.Props) {
			fmt.Fprintf(&b, "- «%s» = «%s»\n", name, card.Props[name])
		}
	}
	if s.deps.Folders != nil {
		if dir, err := s.deps.Folders.WorkDir(card.ID); err == nil {
			fmt.Fprintf(&b, "\nWorking folder: %s\n", dir)
		}
	}
	place, err := s.deps.Engine.CardFlowFor(card.ID)
	if err == nil && place != nil {
		fmt.Fprintf(&b, "\nFlow «%s», stage «%s»", place.FlowName, stageName(*place))
		switch {
		case place.Running:
			b.WriteString(" — an application agent is working the step, the report will come from it")
		case place.Queued:
			b.WriteString(" — waiting for room on the stage")
		}
		b.WriteString("\n")
		if flow, err := s.deps.Store.Flow(place.FlowID); err == nil {
			if stage, ok := flow.Stage(place.StageID); ok {
				b.WriteString(describeStage(flow, stage, "  ", card.Props))
			}
		}
		if len(place.WaitingFor) > 0 {
			fmt.Fprintf(&b, "  Waiting for: %s\n", describeWaits(place.WaitingFor))
		}
	}
	if last := s.lastEntries(card.ID, 5); last != "" {
		b.WriteString("\nLatest in the journal:\n")
		b.WriteString(last)
	}
	return b.String()
}

// describeFlow is a flow as something to choose by and to walk: the stages with
// what they do and owe, then the arrows with their conditions.
func describeFlow(f model.Flow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Flow «%s» [%s]\n", f.Name, f.ID)
	if d := strings.TrimSpace(f.Description); d != "" {
		fmt.Fprintf(&b, "%s\n", d)
	}
	for _, st := range f.Stages {
		fmt.Fprintf(&b, "- «%s» [%s]", st.Name, st.ID)
		switch {
		case st.Final:
			b.WriteString(" — final: a task arriving here is closed")
		case st.Action == model.ActionAgent:
			fmt.Fprintf(&b, " — an agent works it (%s)", workName(st.Work))
			if st.ID == f.EntryStage {
				b.WriteString(", entry")
			}
		case st.Action == model.ActionPublish:
			b.WriteString(" — the application pushes the branch and opens or updates the MR")
		case st.Action == model.ActionVerdict:
			b.WriteString(" — the application sends the review's verdict to the MR")
		default:
			b.WriteString(" — nothing runs, the stage waits for an answer")
			if st.ID == f.EntryStage {
				b.WriteString(", entry")
			}
		}
		b.WriteString("\n")
		b.WriteString(describeStage(f, st, "  ", nil))
	}
	for _, e := range f.Edges {
		from, _ := f.Stage(e.From)
		to, _ := f.Stage(e.To)
		fmt.Fprintf(&b, "→ «%s» —%s→ «%s»", from.Name, triggerName(e.On), to.Name)
		if desc := describeCond(e.If); desc != "" {
			fmt.Fprintf(&b, " when %s", desc)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// describeStage is what a caller working this stage has to know: the values it
// owes the card, and the screens a person will be looking at while it works.
//
// With a card's properties in hand the screens are named as that card will see
// them — «{Page}» is the address the browser actually opens, and a caller
// that has to produce it is better off reading it resolved than guessing what
// the placeholder becomes.
func describeStage(f model.Flow, st model.Stage, indent string, props map[string]string) string {
	var b strings.Builder
	if p := strings.TrimSpace(st.Prompt); p != "" {
		fmt.Fprintf(&b, "%sStage task: %s\n", indent, p)
	}
	for _, w := range st.Writes {
		fmt.Fprintf(&b, "%sLeaves on the task: «%s»", indent, w.Property)
		if w.Required {
			b.WriteString(" — required, the step does not finish without it")
		}
		b.WriteString("\n")
	}
	for _, sc := range st.Screens {
		fmt.Fprintf(&b, "%sScreen: %s", indent, sc.Kind)
		if sc.Title != "" {
			fmt.Fprintf(&b, " «%s»", sc.Title)
		}
		if sc.Ref != "" {
			ref, waiting := model.ResolveRef(sc.Ref, props)
			fmt.Fprintf(&b, " → %s", ref)
			// Only where there is a card to be waiting: without one the
			// placeholder is the answer, not a value that is missing.
			if props != nil && len(waiting) > 0 {
				fmt.Fprintf(&b, " (waiting for values: %s)", strings.Join(waiting, ", "))
			}
		}
		b.WriteString("\n")
	}
	for _, m := range f.MarksFrom(st.ID) {
		fmt.Fprintf(&b, "%sThe answer «%s» leads to «%s»\n", indent, m.Value, m.Stage)
	}
	return b.String()
}

// ---- helpers ----

// resolveCard takes an id or a title, because a caller that has just read a
// list has the title in hand and an id is what a list is for.
func (s *Server) resolveCard(ref string) (model.Card, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.Card{}, errors.New("no task named")
	}
	if card, err := s.deps.Store.Card(ref); err == nil {
		return card, nil
	}
	var found []model.Card
	for _, state := range []model.CardState{model.StateFlow, model.StateInbox, model.StateDone, model.StateDropped} {
		cards, err := s.deps.Store.CardsInState(state)
		if err != nil {
			return model.Card{}, err
		}
		for _, c := range cards {
			if strings.EqualFold(strings.TrimSpace(c.Title), ref) {
				found = append(found, c)
			}
		}
	}
	switch len(found) {
	case 0:
		return model.Card{}, fmt.Errorf("the task «%s» was not found — see the list: tasks", ref)
	case 1:
		return found[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "there are several tasks titled «%s», name the id:", ref)
	for _, c := range found {
		fmt.Fprintf(&b, " %s (%s)", c.ID, c.State)
	}
	return model.Card{}, fmt.Errorf("%s", b.String())
}

func (s *Server) resolveFlow(ref string) (model.Flow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return model.Flow{}, errors.New("no flow named")
	}
	if flow, err := s.deps.Store.Flow(ref); err == nil {
		return flow, nil
	}
	if flow, err := s.deps.Store.FlowByName(ref); err == nil {
		return flow, nil
	}
	return model.Flow{}, fmt.Errorf("the flow «%s» was not found — see the list: flows", ref)
}

// reread is the card as it is now. Every tool answers with the state after its
// own move, and the card in hand is the state before it.
func (s *Server) reread(card model.Card) model.Card {
	if fresh, err := s.deps.Store.Card(card.ID); err == nil {
		return fresh
	}
	return card
}

// running reports a live session on the card — the same answer the card screen
// gives, read from the same rows: a session is live while its status is not
// terminal.
func (s *Server) running(cardID string) bool {
	sessions, err := s.deps.Store.SessionsForCard(cardID)
	if err != nil {
		return false
	}
	for _, sess := range sessions {
		if !sess.Status.Terminal() {
			return true
		}
	}
	return false
}

func (s *Server) putProps(cardID string, props map[string]string) error {
	if len(props) == 0 {
		return nil
	}
	clean := map[string]string{}
	for name, value := range props {
		if name = strings.TrimSpace(name); name != "" {
			clean[name] = value
		}
	}
	if len(clean) == 0 {
		return nil
	}
	_, err := s.deps.Store.UpdateCard(cardID, store.CardEdit{Props: clean})
	return err
}

func (s *Server) lastEntries(cardID string, n int) string {
	entries, err := s.deps.Store.Journal(cardID)
	if err != nil || len(entries) == 0 {
		return ""
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	var b strings.Builder
	for _, e := range entries {
		line := strings.TrimSpace(e.Text)
		if e.Msg != nil {
			// The application's own entries are codes the UI words for a
			// person; an agent reads the code and its values as they are.
			line = e.Msg.String()
		}
		fmt.Fprintf(&b, "- %s\n", line)
	}
	return b.String()
}

func (s *Server) emit(cardID string) {
	if s.deps.Emit != nil {
		s.deps.Emit(engine.EventCard, map[string]any{"cardId": cardID})
	}
}

// missingRequired names the values a report came without, or nothing. The same
// refusal a stage worked in a terminal gets (internal/stagemcp): a step that
// owes a value has not ended until the value stands, and the caller has to know
// which one to add.
func missingRequired(writes []model.PropertyWrite, ok bool, props map[string]string) string {
	if !ok {
		// A failed step is telling us why the values do not exist.
		return ""
	}
	var missing []string
	for _, w := range writes {
		if w.Required && strings.TrimSpace(props[w.Property]) == "" {
			missing = append(missing, "«"+w.Property+"»")
		}
	}
	return strings.Join(missing, ", ")
}

// reportText is the report in the shape the engine already reads: what was done,
// then the declared values, one line each. One currency for a session's closing
// words, a terminal's report and this one — one thing to explain rather than
// three.
func reportText(writes []model.PropertyWrite, summary string, props map[string]string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(summary))
	declared := map[string]bool{}
	for _, w := range writes {
		declared[w.Property] = true
	}
	for _, name := range sortedKeys(props) {
		if !declared[name] {
			continue
		}
		if value := strings.TrimSpace(props[name]); value != "" {
			fmt.Fprintf(&b, "\n%s: %s", name, value)
		}
	}
	return strings.TrimSpace(b.String())
}

// stageName is the stage's own name, which the card's view knows and does not
// hand over directly.
func stageName(place engine.CardFlow) string {
	for _, st := range place.Stages {
		if st.ID == place.StageID {
			return st.Name
		}
	}
	return place.StageID
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// describeWaits, describeCond, triggerName and workName word the flow for an
// agent. The UI words the same things for a person in the person's language;
// every tool here answers in English.
func describeWaits(waits []model.Wait) string {
	parts := make([]string, 0, len(waits))
	for _, w := range waits {
		part := triggerName(w.On)
		if desc := describeCond(w.If); desc != "" {
			part += " " + desc
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

func describeCond(c *model.Cond) string {
	switch {
	case c.IsZero():
		return ""
	case c.CommentContains != "":
		return fmt.Sprintf("the agent's answer contains «%s»", c.CommentContains)
	default:
		return fmt.Sprintf("«%s» = «%s»", c.Property, c.Value)
	}
}

func triggerName(on string) string {
	switch on {
	case model.TriggerSuccess:
		return "step passed"
	case model.TriggerFailure:
		return "step failed"
	case model.TriggerCardChanged:
		return "set on the task"
	}
	return on
}

func workName(work string) string {
	if work == model.WorkSession {
		return "as a session"
	}
	return "in a terminal"
}

func text(s string) *mcp.CallToolResult {
	if strings.TrimSpace(s) == "" {
		s = "(empty)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errorf(format string, args ...any) *mcp.CallToolResult {
	res := text(fmt.Sprintf(format, args...))
	res.IsError = true
	return res
}
