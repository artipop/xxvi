package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/artipop/xxvi/internal/acp"
	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/gitdiff"
	"github.com/artipop/xxvi/internal/hosting"
	"github.com/artipop/xxvi/internal/inbox"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// API is what the UI calls. One service rather than several: the screens do not
// divide along the same lines the packages do — the card screen needs the flow,
// the sessions and the open question at once — and a facade that hands back
// whole screens is less to keep in step than four that hand back fragments.
type API struct{ app *App }

// NewAPI wraps an application for the UI.
func NewAPI(a *App) *API { return &API{app: a} }

// ---- inbox ----

// Inbox is everything waiting to be taken into work, grouped by what brought it.
func (s *API) Inbox() ([]model.InboxGroup, error) {
	cards, err := s.app.Store.CardsInState(model.StateInbox)
	if err != nil {
		return nil, err
	}
	groups := model.GroupBySource(cards)
	// What kind of source a group is decides how the screen words it: a review
	// queue is named after its project, and «review» is the screen's word.
	if sources, err := s.app.Store.Sources(); err == nil {
		plugin := map[string]string{}
		for _, src := range sources {
			plugin[src.Name] = src.Plugin
		}
		for i := range groups {
			groups[i].Plugin = plugin[groups[i].Source]
		}
	}
	return groups, nil
}

// InWork is every card currently travelling a flow.
func (s *API) InWork() ([]CardSummary, error) {
	cards, err := s.app.Store.CardsInState(model.StateFlow)
	if err != nil {
		return nil, err
	}
	out := make([]CardSummary, 0, len(cards))
	for _, c := range cards {
		summary := CardSummary{Card: c}
		if view, err := s.app.Engine.CardFlowFor(c.ID); err == nil && view != nil {
			summary.Flow = view
		}
		summary.Asking = s.app.Agents.WaitingFor(c.ID)
		out = append(out, summary)
	}
	return out, nil
}

// Done is the closed cards, newest first.
func (s *API) Done() ([]model.Card, error) {
	return s.app.Store.CardsInState(model.StateDone)
}

// CardSummary is a card in a list: itself, where it is, and whether it wants
// something from a person.
type CardSummary struct {
	Card   model.Card       `json:"card"`
	Flow   *engine.CardFlow `json:"flow,omitempty"`
	Asking bool             `json:"asking,omitempty"`
}

// ---- one card ----

// CardView is the whole card screen in one answer.
type CardView struct {
	Card     model.Card           `json:"card"`
	Flow     *engine.CardFlow     `json:"flow,omitempty"`
	Journal  []model.JournalEntry `json:"journal"`
	Sessions []store.Session      `json:"sessions"`
	Events   []model.FlowEvent    `json:"events"`
	// Question is what the agent is waiting to hear, if it is waiting.
	Question *acp.Question `json:"question,omitempty"`
}

// Card is everything about one card.
func (s *API) Card(cardID string) (CardView, error) {
	card, err := s.app.Store.Card(cardID)
	if err != nil {
		return CardView{}, err
	}
	view := CardView{Card: card}
	if flow, err := s.app.Engine.CardFlowFor(cardID); err == nil {
		view.Flow = flow
	}
	if view.Journal, err = s.app.Store.Journal(cardID); err != nil {
		return CardView{}, err
	}
	if view.Sessions, err = s.app.Store.SessionsForCard(cardID); err != nil {
		return CardView{}, err
	}
	if view.Events, err = s.app.Store.FlowEvents(cardID); err != nil {
		return CardView{}, err
	}
	view.Question = s.app.Agents.QuestionForCard(cardID)
	return view, nil
}

// SetProp sets a property on a card and tells the engine, because a property is
// how a person answers a waiting stage. Which stage cares, and whether this was
// the value it wanted, is the engine's to decide — setting anything else is
// simply not addressed to it.
func (s *API) SetProp(cardID, name, value string) (CardView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CardView{}, msg.Err("prop.noName")
	}
	if _, err := s.app.Store.UpdateCard(cardID, store.CardEdit{Props: map[string]string{name: value}}); err != nil {
		return CardView{}, err
	}
	if strings.TrimSpace(value) != "" {
		s.app.Engine.CardChanged(cardID, name, value)
	}
	s.app.Emit(engine.EventCard, map[string]any{"cardId": cardID})
	return s.Card(cardID)
}

// MarkOutcome is a person answering for a stage that runs nothing: passed or
// failed, put on the card so the flow sees it and moves.
//
// It goes through SetProp rather than straight to the store, and that is the
// point: the engine's own write of the outcome is silent, because the machine
// recording a fact must not set the card's own automation off. This one has to
// — it is a person's edit, and it goes the way a person's edit goes.
//
// Remarks are what is wrong, said with «failed». They go on the card before the
// outcome does, because the outcome moves the card and whoever it lands on
// reads them from there; and into the journal on the segment they were said on.
// A pass clears them: remarks from an earlier round are not about this one.
func (s *API) MarkOutcome(cardID, value, remarks string) (CardView, error) {
	value = strings.TrimSpace(value)
	if value != model.OutcomePassed && value != model.OutcomeFailed {
		return CardView{}, msg.Err("outcome.unknown", "value", value)
	}
	remarks = strings.TrimSpace(remarks)
	if value == model.OutcomePassed {
		remarks = ""
	}
	if _, err := s.app.Store.UpdateCard(cardID, store.CardEdit{Props: map[string]string{model.RemarksProperty: remarks}}); err != nil {
		return CardView{}, err
	}
	if remarks != "" {
		if _, err := s.app.Store.Record(model.JournalEntry{CardID: cardID, Kind: model.EntryReview, Text: remarks}); err != nil {
			return CardView{}, err
		}
	}
	return s.SetProp(cardID, model.OutcomeProperty, value)
}

// SetAssignee says who the card is for. An agent's name means "let this agent
// work it"; anything else means a person took it, and then no agent starts.
func (s *API) SetAssignee(cardID, who string) (CardView, error) {
	who = strings.TrimSpace(who)
	if _, err := s.app.Store.UpdateCard(cardID, store.CardEdit{Assignee: &who}); err != nil {
		return CardView{}, err
	}
	s.app.Emit(engine.EventCard, map[string]any{"cardId": cardID})
	return s.Card(cardID)
}

// EditCard changes a card's own text.
func (s *API) EditCard(cardID, title, body string) (CardView, error) {
	if _, err := s.app.Store.UpdateCard(cardID, store.CardEdit{Title: &title, Body: &body}); err != nil {
		return CardView{}, err
	}
	s.app.Emit(engine.EventCard, map[string]any{"cardId": cardID})
	return s.Card(cardID)
}

// TakeIntoWork puts an inbox card onto a flow. This is the one way a card
// starts moving, and it is a person's decision.
func (s *API) TakeIntoWork(cardID, flowID string) (CardView, error) {
	if err := s.app.Engine.TakeIntoWork(cardID, flowID); err != nil {
		return CardView{}, err
	}
	return s.Card(cardID)
}

// MoveTo puts a card on a stage by hand. A person is always above the graph.
func (s *API) MoveTo(cardID, stageID string) (CardView, error) {
	if err := s.app.Engine.MoveTo(cardID, stageID); err != nil {
		return CardView{}, err
	}
	return s.Card(cardID)
}

// RemoveFromFlow takes a card off its flow and back into the inbox.
func (s *API) RemoveFromFlow(cardID string) (CardView, error) {
	if err := s.app.Engine.RemoveFromFlow(cardID); err != nil {
		return CardView{}, err
	}
	return s.Card(cardID)
}

// DropCard files a card away without doing it, from the inbox or straight off
// its flow. The card is kept — what was dropped and why is the sort of thing
// somebody asks about later.
func (s *API) DropCard(cardID string) error {
	return s.app.Engine.Drop(cardID)
}

// Journal is the card's audit: who did what to it and when, oldest first.
func (s *API) Journal(cardID string) ([]model.JournalEntry, error) {
	return s.app.Store.Journal(cardID)
}

// ---- the ribbon ----

// Ribbons is every card in work: one card in work is one ribbon, and there is
// no other kind (docs/system.md §12.1).
func (s *API) Ribbons() ([]engine.RibbonView, error) { return s.app.Engine.Ribbons() }

// Ribbon is one card's strip of screens.
func (s *API) Ribbon(cardID string) (engine.RibbonView, error) {
	return s.app.Engine.Ribbon(cardID)
}

// SessionEvents is one agent run as it happened: its messages, its thoughts and
// its tool calls, oldest first. sinceSeq is what the caller already has, so a
// screen that is following a live session asks only for the rest of it.
//
// It reads the same rows the manager writes as it goes, rather than a second
// stream alongside them: what the card's history is made of and what the
// ribbon shows are one record.
func (s *API) SessionEvents(sessionID string, sinceSeq int64) ([]store.SessionEvent, error) {
	events, err := s.app.Store.SessionEvents(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]store.SessionEvent, 0, len(events))
	for _, e := range events {
		if e.Seq > sinceSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

// ReadDoc opens a notes screen's file. The path is relative to the card's
// working folder — the same folder the agent works in, so a plan it wrote is
// the file a person edits rather than a copy of it.
//
// A file that is not there yet is empty rather than an error: a stage may well
// declare notes the agent has not written yet, and an error there would be the
// ribbon refusing to show a blank page.
func (s *API) ReadDoc(cardID, name string) (string, error) {
	path, err := s.docPath(cardID, name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", msg.Wrap(err, "doc.readFailed", "name", name)
	}
	return string(data), nil
}

// WriteDoc saves a notes screen's file, creating the folders it needs.
func (s *API) WriteDoc(cardID, name, text string) error {
	path, err := s.docPath(cardID, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return msg.Wrap(err, "doc.folderFailed", "name", name)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return msg.Wrap(err, "doc.saveFailed", "name", name)
	}
	return nil
}

// docPath is where a notes screen's file actually is. The flow editor already
// refuses a path that leaves the card's folder; this refuses it again at the
// moment of opening, because the folder is a boundary and a boundary checked
// only where it is declared is a boundary until somebody edits the database.
func (s *API) docPath(cardID, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", msg.Err("doc.noName")
	}
	dir, err := s.app.Agents.WorkDir(cardID)
	if err != nil {
		return "", err
	}
	return acp.Within(dir, name)
}

// ---- diff ----

// diffTimeout is how long git is given. A repository big enough to need longer
// is a repository whose diff nobody was going to read in a pane anyway.
const diffTimeout = 20 * time.Second

// Diff is what changed in the card's working copy — the screen a review stage
// stands on (docs/system.md §12.7).
//
// Read at the moment it is asked for and kept nowhere. The working copy is the
// answer here, and a diff remembered anywhere else would be a second answer to
// the question the person is deciding by.
func (s *API) Diff(cardID, ref string) (gitdiff.Diff, error) {
	card, err := s.app.Store.Card(cardID)
	if err != nil {
		return gitdiff.Diff{}, err
	}
	dir, err := s.app.Agents.WorkDir(cardID)
	if err != nil {
		return gitdiff.Diff{}, err
	}
	// Only a card with a branch has a base worth comparing against: in the
	// folder as it stands nobody told the agent to commit.
	base := ""
	if card.Branch != "" {
		base = card.Base
	}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	diff, err := gitdiff.Read(ctx, dir, ref, base)
	// An MR under review is checked out on no branch; its branch is the MR's.
	if err == nil && diff.Branch == "HEAD" {
		diff.Branch = card.Branch
	}
	return diff, err
}

// TerminalHandle is what a terminal screen needs to connect: which terminal,
// and where its socket is.
type TerminalHandle struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// OpenTerminal starts the shell behind one terminal screen, or hands back the
// one already running there.
//
// Keyed by screen: a screen id is derived from the card's journal and does not
// change, so re-reading the ribbon — which happens on every step the agent
// takes — must not leave a second shell behind each time.
func (s *API) OpenTerminal(cardID, screenID, command string) (TerminalHandle, error) {
	endpoint := s.app.Terminals.Endpoint()
	if endpoint == "" {
		return TerminalHandle{}, msg.Err("terminal.disabled")
	}
	session, err := s.app.Terminals.Open(cardID, screenID, command)
	if err != nil {
		return TerminalHandle{}, err
	}
	return TerminalHandle{ID: session.ID, URL: endpoint + session.ID}, nil
}

// AgentTerminal is where the terminal of one step is watched. It starts
// nothing: the CLI was started by the stage and belongs to it, and a screen
// that could start one would be a second way of working a step.
//
// A step long finished still answers: the socket serves the tail kept on disk
// and then says the terminal has ended, which is what a segment scrolled back
// to should show (docs/system.md §12.2).
func (s *API) AgentTerminal(sessionID string) (TerminalHandle, error) {
	endpoint := s.app.Terminals.Endpoint()
	if endpoint == "" {
		return TerminalHandle{}, msg.Err("terminal.disabled")
	}
	if sessionID == "" {
		return TerminalHandle{}, errors.New("no session named")
	}
	return TerminalHandle{ID: sessionID, URL: endpoint + sessionID}, nil
}

// CloseTerminal ends one shell. A person closing a terminal means the process
// in it, not just the window onto it — the ribbon has no windows to close.
func (s *API) CloseTerminal(id string) error {
	if session := s.app.Terminals.Get(id); session != nil {
		session.Close()
	}
	return nil
}

// ---- flows ----

// Flows is every flow, whole.
func (s *API) Flows() ([]model.Flow, error) { return s.app.Store.Flows() }

// SaveFlow validates a flow and stores it. The whole graph is checked before it
// is taken, and a refusal says which part is wrong.
func (s *API) SaveFlow(flow model.Flow) (model.Flow, error) {
	saved, err := s.app.Store.SaveFlow(flow)
	if err != nil {
		return model.Flow{}, err
	}
	s.app.Emit(EventFlows, map[string]any{"flowId": saved.ID})
	return saved, nil
}

// DeleteFlow removes a flow. Cards standing on it stop advancing by themselves
// and say so; nothing else happens to them.
func (s *API) DeleteFlow(flowID string) error {
	if err := s.app.Store.DeleteFlow(flowID); err != nil {
		return err
	}
	s.app.Emit(EventFlows, map[string]any{"flowId": flowID})
	return nil
}

// FlowOverview is one flow and where its cards are along it.
func (s *API) FlowOverview(flowID string) (engine.FlowOverview, error) {
	return s.app.Engine.Overview(flowID)
}

// FlowCards lists the cards travelling a flow, with the stage each stands on.
func (s *API) FlowCards(flowID string) ([]StageCard, error) {
	cards, stageOf, err := s.app.Store.CardsOnFlow(flowID)
	if err != nil {
		return nil, err
	}
	out := make([]StageCard, 0, len(cards))
	for _, c := range cards {
		out = append(out, StageCard{
			Card: c, StageID: stageOf[c.ID],
			Asking: s.app.Agents.WaitingFor(c.ID),
		})
	}
	return out, nil
}

// StageCard is a card as the flow view draws it.
type StageCard struct {
	Card    model.Card `json:"card"`
	StageID string     `json:"stageId"`
	Asking  bool       `json:"asking,omitempty"`
}

// Vocabulary is the closed sets the editor offers. Sent from here so the UI can
// never offer a trigger or an action the engine does not implement. Only the
// members are sent: what each is called is the UI's to say.
type Vocabulary struct {
	Triggers []model.Trigger `json:"triggers"`
	Actions  []string        `json:"actions"`
	// Where an agent stage runs — the terminal somebody sits at, or a session
	// nobody watches (docs/system.md §4.1.1).
	Works []string `json:"works"`
	Kinds []string `json:"kinds"`
	Rules []string `json:"ruleActions"`
	// The card's own field for how a stage ended, and the two values it takes.
	// Sent so the editor can keep it out of what a stage declares — the engine
	// writes it for every stage — while still offering it to a condition, which
	// is the whole point of it being a closed set.
	OutcomeProperty string   `json:"outcomeProperty"`
	OutcomeValues   []string `json:"outcomeValues"`
	// What kinds of screen a stage may declare — the same closed set the
	// ribbon knows how to render, so the editor cannot offer a window nothing
	// can open (docs/system.md §12.2).
	ScreenKinds []string `json:"screenKinds"`
	// What kinds of place work can happen in. One for now — a folder — and the
	// room for the rest is the same room the triggers keep for git.
	ProjectKinds []string `json:"projectKinds"`
	// WorkModes are how a card may work in a repository (model/workmode.go).
	WorkModes []string `json:"workModes"`
	// Providers are the hostings a project's remote can be (docs/system.md §15).
	Providers []string `json:"providers"`
}

// Vocabulary returns those sets.
func (s *API) Vocabulary() Vocabulary {
	return Vocabulary{
		Triggers: model.Triggers,
		Actions:  model.Actions,
		Works:    model.Works,
		Kinds:    model.Kinds,
		Rules:    model.RuleActions,

		OutcomeProperty: model.OutcomeProperty,
		OutcomeValues:   model.OutcomeValues,
		ScreenKinds:     model.ScreenKinds,
		ProjectKinds:    model.ProjectKinds,
		Providers:       model.Providers,
		WorkModes:       model.WorkModes,
	}
}

// ---- sources ----

// Sources is the registry with every source's rules.
func (s *API) Sources() ([]model.Source, error) { return s.app.Store.Sources() }

// SaveSource adds or replaces a source.
func (s *API) SaveSource(src model.Source) (model.Source, error) {
	saved, err := s.app.Store.SaveSource(src)
	if err != nil {
		return model.Source{}, err
	}
	s.app.Emit(EventSources, map[string]any{"source": saved.Name})
	return saved, nil
}

// DeleteSource removes a source. The cards it brought stay: they are work, and
// the source is only where they came from.
func (s *API) DeleteSource(name string) error {
	if err := s.app.Store.DeleteSource(name); err != nil {
		return err
	}
	s.app.Emit(EventSources, map[string]any{"source": name})
	return nil
}

// PollSource reads a source now, so nobody has to wait out an interval to see a
// change.
func (s *API) PollSource(name string) ([]model.InboxGroup, error) {
	if src, err := s.app.Store.Source(name); err == nil && src.Plugin == hosting.PluginReview {
		s.app.Hosting.Poll()
		return s.Inbox()
	}
	if err := s.app.Poller.PollByName(name); err != nil {
		return nil, err
	}
	return s.Inbox()
}

// AddItem files an item into a source by hand. It goes through the source's own
// file rather than straight into a card, so a hand-added item is an item like
// any other: it meets the same rules and survives the file being read again.
func (s *API) AddItem(sourceName, title, body string) ([]model.InboxGroup, error) {
	src, err := s.app.Store.Source(sourceName)
	if err != nil {
		return nil, err
	}
	if src.Plugin != inbox.PluginDemo {
		return nil, msg.Err("source.noManualItems", "source", src.Name)
	}
	if strings.TrimSpace(title) == "" {
		return nil, msg.Err("item.noTitle")
	}
	if err := inbox.AppendItem(inbox.DemoPath(src), model.Item{Title: title, Body: body}); err != nil {
		return nil, err
	}
	if err := s.app.Poller.Poll(src); err != nil {
		return nil, err
	}
	return s.Inbox()
}

// AddCard files a card somebody typed straight into the inbox.
func (s *API) AddCard(sourceName, title, body string) (model.Card, error) {
	card, err := s.app.Pipeline.AddManual(sourceName, title, body, nil)
	if err != nil {
		return model.Card{}, err
	}
	return card, nil
}

// StartTask is a task typed straight into the ribbon: it becomes a card and is
// taken into work in one call, with nothing in between. Made here rather than
// as three calls from the screen, so that a refusal — no such project, no
// such flow — comes before anything exists, and a bad choice does not leave a
// stray card in the inbox for somebody to find later.
//
// The first line is the title, as it is in a commit: what fits in a list. The
// whole text is the body, because that is what the agent is handed.
func (s *API) StartTask(text, projectID, workMode, agent, flowID string) (CardView, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return CardView{}, msg.Err("task.empty")
	}
	if err := s.checkWorkMode(projectID, workMode); err != nil {
		return CardView{}, err
	}
	if _, err := s.app.Store.Flow(flowID); err != nil {
		return CardView{}, err
	}
	title, body := taskTitle(text), ""
	if title != text {
		body = text
	}
	card, err := s.app.Store.CreateCard(model.Card{
		Title: title, Body: body, State: model.StateInbox,
		Assignee: strings.TrimSpace(agent), Project: projectID, WorkMode: workMode,
	})
	if err != nil {
		return CardView{}, err
	}
	if err := s.app.Engine.TakeIntoWork(card.ID, flowID); err != nil {
		return CardView{}, err
	}
	return s.Card(card.ID)
}

func taskTitle(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > 80 {
		line = strings.TrimSpace(string(r[:80])) + "…"
	}
	return line
}

// ---- projects ----

// Projects is the registry of places work happens.
func (s *API) Projects() ([]model.Project, error) {
	list, err := s.app.Store.Projects()
	if err != nil {
		return nil, err
	}
	for i := range list {
		s.describe(&list[i])
	}
	return list, nil
}

func (s *API) describe(p *model.Project) {
	p.Repo = acp.IsRepo(p.Path)
	s.app.Hosting.Describe(p)
	if src, ok := s.app.Hosting.ReviewSource(p.ID); ok {
		p.ReviewInbox = src.Enabled
	}
}

// SetReviewInbox says whether the MRs waiting on this account's review in a
// project come into the inbox (docs/system.md §15.4).
func (s *API) SetReviewInbox(projectID string, on bool) (model.Project, error) {
	p, err := s.app.Store.Project(projectID)
	if err != nil {
		return model.Project{}, err
	}
	if err := s.app.Hosting.SetReviewInbox(p, on); err != nil {
		return model.Project{}, err
	}
	s.describe(&p)
	s.app.Emit(EventProjects, map[string]any{"project": p.ID})
	s.app.Emit(EventSources, map[string]any{})
	return p, nil
}

// SaveProject adds or edits one entry.
//
// Whether the folder is actually there is asked here rather than in the domain,
// which touches no disk — and here is also where a person is looking, so a path
// with a typo in it is refused while they can still see what they typed.
func (s *API) SaveProject(p model.Project) (model.Project, error) {
	checked, err := model.ValidateProject(p)
	if err != nil {
		return model.Project{}, err
	}
	info, err := os.Stat(checked.Path)
	if err != nil || !info.IsDir() {
		return model.Project{}, msg.Err("project.folderNotFound", "path", checked.Path)
	}
	// Where it pushes is git's answer, asked when nobody gave one; which
	// hosting that is, a guess from the server's name that a person may
	// correct — and a correction is kept, not guessed over.
	if checked.Remote == "" {
		checked.Remote = hosting.DetectRemote(checked.Path)
	}
	if checked.Provider == "" {
		if r, ok := hosting.ParseRemote(checked.Remote); ok {
			checked.Provider = hosting.GuessProvider(r)
		}
	}
	saved, err := s.app.Store.SaveProject(checked)
	if err != nil {
		return model.Project{}, err
	}
	s.describe(&saved)
	s.app.Emit(EventProjects, map[string]any{"project": saved.ID})
	return saved, nil
}

// ConnectHosting gives the application a token for the server a project
// pushes to. The server is asked who the token belongs to before anything is
// kept, so a wrong one is refused where it was typed.
func (s *API) ConnectHosting(projectID, token string) (model.Project, error) {
	p, err := s.app.Store.Project(projectID)
	if err != nil {
		return model.Project{}, err
	}
	if _, err := s.app.Hosting.Connect(p, token); err != nil {
		return model.Project{}, err
	}
	s.app.ensureHostingFlows()
	s.describe(&p)
	s.app.Emit(EventProjects, map[string]any{"project": p.ID})
	return p, nil
}

// DisconnectHosting forgets the token of a project's server.
func (s *API) DisconnectHosting(projectID string) (model.Project, error) {
	p, err := s.app.Store.Project(projectID)
	if err != nil {
		return model.Project{}, err
	}
	if err := s.app.Hosting.Disconnect(p); err != nil {
		return model.Project{}, err
	}
	s.describe(&p)
	s.app.Emit(EventProjects, map[string]any{"project": p.ID})
	return p, nil
}

// PickFolder opens the system's own folder dialog and returns what was chosen,
// or empty if the person closed it without choosing.
//
// A path is typed only when there is no other way. Somebody who knows where
// their project is knows it as a place they can point at, not as a string they
// can spell — and a typo in a path is a project that refuses to save with a
// sentence about a folder that is not there.
//
// The dialog's title is the UI's, like every other word on the screen.
func (s *API) PickFolder(title, from string) (string, error) {
	s.app.uiMu.RLock()
	chooser := s.app.chooser
	s.app.uiMu.RUnlock()
	if chooser == nil {
		return "", msg.Err("folder.noWindow")
	}
	return chooser.Folder(title, from)
}

// DeleteProject removes one, refusing while a card still names it.
func (s *API) DeleteProject(id string) error {
	if err := s.app.Store.DeleteProject(id); err != nil {
		return err
	}
	s.app.Emit(EventProjects, map[string]any{"project": id})
	return nil
}

// SetCardProject says where a card's work happens. Beside the assignee on
// purpose: both are about by whom and where, and both are a person's answer
// rather than the graph's.
func (s *API) SetCardProject(cardID, projectID string) (CardView, error) {
	card, err := s.app.Store.Card(cardID)
	if err != nil {
		return CardView{}, err
	}
	if card.Branch != "" && projectID != card.Project {
		return CardView{}, msg.Err("card.projectLocked", "branch", card.Branch)
	}
	if projectID != "" {
		if _, err := s.app.Store.Project(projectID); err != nil {
			return CardView{}, err
		}
	}
	if err := s.app.Store.SetCardProject(cardID, projectID); err != nil {
		return CardView{}, err
	}
	// A branch of its own is a question about a repository; a card moved to
	// a folder that is not one, or to no project at all, works as it stands.
	if card.WorkMode != model.WorkModeFolder && s.checkWorkMode(projectID, card.WorkMode) != nil {
		if err := s.app.Store.SetCardWorkMode(cardID, model.WorkModeFolder); err != nil {
			return CardView{}, err
		}
	}
	s.app.Emit(engine.EventCard, map[string]any{"cardId": cardID})
	return s.Card(cardID)
}

// SetCardWorkMode says how a card works in its project's repository: in the
// folder as it stands, in a separate working tree, or on a branch in the folder
// itself (model/workmode.go). Answered before the work starts — once the card
// has a branch, the work is on it, and changing the answer would leave it
// there with nothing pointing at it.
func (s *API) SetCardWorkMode(cardID, mode string) (CardView, error) {
	card, err := s.app.Store.Card(cardID)
	if err != nil {
		return CardView{}, err
	}
	if mode == card.WorkMode {
		return s.Card(cardID)
	}
	if card.Branch != "" {
		return CardView{}, msg.Err("card.workModeLocked", "branch", card.Branch)
	}
	if err := s.checkWorkMode(card.Project, mode); err != nil {
		return CardView{}, err
	}
	if err := s.app.Store.SetCardWorkMode(cardID, mode); err != nil {
		return CardView{}, err
	}
	s.app.Emit(engine.EventCard, map[string]any{"cardId": cardID})
	return s.Card(cardID)
}

// RemoveCardWorktree answers «remove» to a closed card's working tree: the tree
// goes, the branch stays. Discard is the person having been told the tree holds
// uncommitted changes and removing it anyway.
func (s *API) RemoveCardWorktree(cardID string, discard bool) (CardView, error) {
	if err := s.app.Agents.RemoveWorktree(cardID, discard); err != nil {
		return CardView{}, err
	}
	s.app.Emit(engine.EventCard, map[string]any{"cardId": cardID})
	return s.Card(cardID)
}

// KeepCardWorktree answers «keep»: the tree stays, and is not asked about
// again.
func (s *API) KeepCardWorktree(cardID string) (CardView, error) {
	if err := s.app.Agents.KeepWorktree(cardID); err != nil {
		return CardView{}, err
	}
	return s.Card(cardID)
}

// checkWorkMode refuses a mode the project cannot have: an unknown one, or a
// branch of its own where there is no repository to make it in.
func (s *API) checkWorkMode(projectID, mode string) error {
	if err := model.ValidateWorkMode(mode); err != nil {
		return err
	}
	if projectID == "" {
		if mode != model.WorkModeFolder {
			return msg.Err("workMode.noProject", "mode", mode)
		}
		return nil
	}
	project, err := s.app.Store.Project(projectID)
	if err != nil {
		return err
	}
	if mode != model.WorkModeFolder && !acp.IsRepo(project.Path) {
		return msg.Err("workMode.notRepo", "project", project.Name, "mode", mode)
	}
	return nil
}

// ---- agents ----

// AgentsView is the registry and what this machine can actually run.
type AgentsView struct {
	Agents   []model.Agent       `json:"agents"`
	Adapters []acp.AdapterStatus `json:"adapters"`
}

// Agents is the registry with the adapter check beside it, because "is this
// agent usable here" is the question a person opens that screen with.
func (s *API) Agents() (AgentsView, error) {
	list, err := s.app.Store.Agents()
	if err != nil {
		return AgentsView{}, err
	}
	return AgentsView{Agents: list, Adapters: acp.AdapterStatuses()}, nil
}

// SaveAgent adds or replaces a registry entry.
func (s *API) SaveAgent(a model.Agent) (model.Agent, error) {
	saved, err := s.app.Store.SaveAgent(a)
	if err != nil {
		return model.Agent{}, err
	}
	s.app.Emit(EventAgents, map[string]any{"agent": saved.Name})
	return saved, nil
}

// DeleteAgent removes an entry, refusing while a flow still names it: a stage
// whose crew is nobody is a card that silently never starts, and finding that
// out here is better than finding it out mid-run.
func (s *API) DeleteAgent(name string) error {
	used, err := s.app.Store.StagesUsingAgent(name)
	if err != nil {
		return err
	}
	if len(used) > 0 {
		return msg.Err("agent.inUse", "agent", name, "stages", strings.Join(used, ", "))
	}
	if err := s.app.Store.DeleteAgent(name); err != nil {
		return err
	}
	s.app.Emit(EventAgents, map[string]any{"agent": name})
	return nil
}

// ---- what is waiting for a person ----

// Attention is everything waiting for a person, oldest first: an agent's
// question, a silent terminal, a closed card's working tree.
func (s *API) Attention() []acp.Attention { return s.app.Agents.Attention() }

// SetNotificationWords hands over what a system notification says around an
// agent's question, in the language the UI is showing. Called by the UI when it
// starts and whenever the language changes.
func (s *API) SetNotificationWords(words NotificationWords) {
	s.app.uiMu.RLock()
	n := s.app.notifier
	s.app.uiMu.RUnlock()
	n.SetWords(words)
}

// SetMenuWords hands over the application menu's titles, keyed by the UI's
// own «menu.» keys, in the language the UI is showing. Called when the UI
// starts and whenever the language changes; headless, there is no menu.
func (s *API) SetMenuWords(words map[string]string) {
	s.app.uiMu.RLock()
	m := s.app.menu
	s.app.uiMu.RUnlock()
	if m != nil {
		m.SetWords(words)
	}
}

// Answer delivers a person's answer to an agent's question.
func (s *API) Answer(questionID string, answer acp.Answer) error {
	return s.app.Agents.Answer(questionID, answer)
}

// CancelCard stops whatever is running for a card. A cancelled session produces
// no outcome: the person who stopped it decides what happens next.
func (s *API) CancelCard(cardID string) error {
	s.app.Agents.Cancel(cardID, msg.New("cancel.byHand"))
	return nil
}

// UI event names the frontend subscribes to. The engine and the acp manager
// emit their own (engine.EventCard, acp.EventSession, acp.EventAttention,
// inbox.EventInbox); these are the ones this facade produces.
const (
	EventFlows    = "flows"
	EventSources  = "sources"
	EventAgents   = "agents"
	EventProjects = "projects"
)
