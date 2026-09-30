// Package engine moves cards. One entry point — enterStage — and three ways
// in: a person takes a card into work, a person moves it by hand, or an event
// advances it along an edge. All three end in the same place, so a flow behaves
// the same whether it is walked by hand or by itself.
//
// The engine knows nothing about ACP. What a stage's work *is* reaches it
// through Runner, whose real implementation spawns agents and whose test
// implementation records what it was asked to do.
package engine

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// Job is one piece of work a stage asks for.
type Job struct {
	Card  model.Card
	Flow  model.Flow
	Stage model.Stage
	Agent model.Agent
	// Prompt is what the agent is told: its own prompt, then the stage's, then
	// the card's task. Composed here so the runner has nothing to decide. On a
	// terminal stage it is only the first message (TerminalOpening), and may be
	// empty.
	Prompt string
	// Brief is what a terminal stage's CLI is told outside the conversation,
	// through the stage's MCP server (TerminalBrief). Empty in the background,
	// where Prompt holds it all.
	Brief string
	// Visit is the transition that put the card on this stage. A step that
	// ends after the card was moved elsewhere by hand reports into a visit
	// that is over, and is not allowed to move the card from where it now is.
	Visit int64
}

// Publisher does what a hosting stage says: push and open the MR, or send a
// review's verdict to one (docs/system.md §15). Like Runner it returns at
// once, and the outcome comes back through StepDone.
type Publisher interface {
	Publish(job Job)
}

// Runner does what a stage says. Start must return as soon as the work is under
// way rather than when it finishes — the outcome comes back later through
// Finished.
type Runner interface {
	Start(job Job) error
	// Busy is the agents with a live session, keyed by model.Username.
	Busy() map[string]bool
	// RunningOnStage counts live sessions of one stage, which is what a stage's
	// own limit is checked against.
	RunningOnStage(stageID string) int
	// Cancel stops whatever is running for a card. A cancelled session produces
	// no outcome: somebody intervened, so the flow waits for them.
	Cancel(cardID string, reason msg.Msg)
	// Leave is Cancel for a card going back to the inbox: a conversation in a
	// terminal is first asked to describe the task as it stands (Described),
	// then closed. Returns at once, like Start, and says whether it asked — a
	// runner that did always answers with Described, if only to say nothing
	// came.
	Leave(cardID string, reason msg.Msg) (asked bool)
}

// Emitter pushes events to the UI. Implementations must be safe to call before
// the UI is ready (drop and log).
type Emitter interface {
	Emit(event string, payload any)
}

// UI event names. Two: a card changed, and something is waiting for a person.
// What a session *says* is not among them — that is the card's journal and the session's stream.
const (
	EventCard      = "card"
	EventAttention = "attention"
)

// Engine ties the store, the runner and the UI together.
type Engine struct {
	store     *store.Store
	runner    Runner
	publisher Publisher
	ui        Emitter
	log       *slog.Logger
	// language names the language the person reads, for the brief. Nil in a
	// test that does not care, and then the brief says nothing about it.
	language func() string

	// publishing is the cards a hosting stage is working now. Its own lock:
	// it is read while the view is built, and a step reports under mu.
	pubMu      sync.Mutex
	publishing map[string]bool

	// describing is the cards back in the inbox whose agent is still writing
	// what they are. Its own lock, like publishing: the inbox reads it.
	descMu     sync.Mutex
	describing map[string]bool

	// mu serializes deciding about one card. Every path here is a short
	// sequence of database reads and writes ending in a non-blocking Start, so
	// one lock is enough and two cards never fight over a decision.
	mu sync.Mutex
}

// New builds an engine. runner and ui may be nil, which is what a test that
// only cares about the graph does.
func New(st *store.Store, runner Runner, ui Emitter, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{store: st, runner: runner, ui: ui, log: log}
}

// SetRunner supplies the runner after construction — the ACP side needs the
// engine to report outcomes to, so one of the two has to be wired second.
func (e *Engine) SetRunner(r Runner) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runner = r
}

// SetPublisher supplies what works the hosting stages.
func (e *Engine) SetPublisher(p Publisher) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.publisher = p
}

// SetLanguage supplies what the brief tells an agent to write in.
func (e *Engine) SetLanguage(f func() string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.language = f
}

// TakeIntoWork puts an inbox card onto a flow's entry stage. This is the one
// way a card starts moving, and it is a person's decision (docs/system.md §3).
func (e *Engine) TakeIntoWork(cardID, flowID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	card, err := e.store.Card(cardID)
	if err != nil {
		return err
	}
	if card.State == model.StateFlow {
		return msg.Err("card.alreadyInWork", "card", card.Title)
	}
	flow, err := e.store.Flow(flowID)
	if err != nil {
		return err
	}
	entry, ok := flow.Entry()
	if !ok {
		return msg.Err("flow.entryNotFound", "flow", flow.Name)
	}
	e.enterStage(card, flow, entry, "", msg.New("move.taken"))
	return nil
}

// continueWords is what a paused agent is told when the person continuing it
// wrote nothing: the conversation it resumes says what it was doing.
const continueWords = "Continue where you left off."

// Continue picks up a stage the application closed on (store.StatusPaused),
// in the conversation it stopped in. It is a person's move and only theirs: a
// stage that went on by itself after a restart would spend tokens nobody saw
// being spent.
//
// The agent is told what the person wrote, not its brief again — the
// conversation it resumes holds the brief already, and a second copy reads as a
// second task.
func (e *Engine) Continue(cardID, text string) error {
	return e.resume(cardID, text, false)
}

// Reopen picks up a paused stage the way Continue does, but tells the agent
// nothing: its CLI comes back in the conversation it stopped in, showing it, and
// waits for the person. What went wrong may need a look before anything goes
// on, and a word sent on the person's behalf would spend tokens on a guess.
func (e *Engine) Reopen(cardID string) error {
	return e.resume(cardID, "", true)
}

func (e *Engine) resume(cardID, text string, quiet bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	st, ok, err := e.store.FlowState(cardID)
	if err != nil {
		return err
	}
	if !ok {
		return msg.Err("stage.notPaused")
	}
	paused, ok := e.pausedOn(cardID, st.StageID)
	if !ok {
		return msg.Err("stage.notPaused")
	}
	// A session has no CLI to come back in and show its conversation: the
	// stream on the ribbon is all it ever showed, and it is there already.
	if quiet && paused.Work == model.WorkSession {
		return msg.Err("stage.reopenSession")
	}
	card, err := e.store.Card(cardID)
	if err != nil {
		return err
	}
	flow, err := e.store.Flow(st.FlowID)
	if err != nil {
		return err
	}
	stage, ok := flow.Stage(st.StageID)
	if !ok {
		return msg.Err("stage.notInFlow", "stage", st.StageID, "flow", flow.Name)
	}
	text = strings.TrimSpace(text)
	switch {
	case quiet:
		e.record(cardID, model.EntryMove, msg.New("journal.reopened"))
	case text == "":
		text = continueWords
		e.record(cardID, model.EntryMove, msg.New("journal.continued"))
	default:
		e.record(cardID, model.EntryMove, msg.New("journal.continuedSaying", "text", text))
	}
	e.runStageSaying(card, flow, stage, text, quiet)
	e.emitCard(cardID)
	return nil
}

// pausedOn reports whether the last run on the card's stage is one the
// application closed on. Only the last: a paused run followed by another is
// history, not a stage waiting.
func (e *Engine) pausedOn(cardID, stageID string) (store.Session, bool) {
	sessions, err := e.store.SessionsForCard(cardID)
	if err != nil {
		return store.Session{}, false
	}
	for _, s := range sessions {
		if s.StageID == stageID {
			return s, s.Status == store.StatusPaused
		}
	}
	return store.Session{}, false
}

// MoveTo puts a card on a stage by hand. A person is always above the graph:
// whatever was running is cancelled, and the stage starts as if an edge had
// brought the card there.
func (e *Engine) MoveTo(cardID, stageID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	card, err := e.store.Card(cardID)
	if err != nil {
		return err
	}
	flow, err := e.store.FlowForStage(stageID)
	if err != nil {
		return err
	}
	stage, ok := flow.Stage(stageID)
	if !ok {
		return msg.Err("stage.notInFlow", "stage", stageID, "flow", flow.Name)
	}
	e.cancel(cardID, msg.New("cancel.movedByHand"))
	e.dequeue(cardID)
	e.enterStage(card, flow, stage, "", msg.New("move.byHand"))
	return nil
}

// RemoveFromFlow takes a card off its flow and back into the inbox. Nothing is
// undone — the journal and the flow events stay.
func (e *Engine) RemoveFromFlow(cardID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.store.LeaveFlow(cardID, model.StateInbox); err != nil {
		return err
	}
	if e.runner != nil && e.runner.Leave(cardID, msg.New("cancel.leftFlow")) {
		e.setDescribing(cardID, true)
	}
	e.record(cardID, model.EntryMove, msg.New("journal.leftFlow"))
	e.emitCard(cardID)
	return nil
}

// Described is what a runner calls when the agent of a card leaving its flow
// has said what the task now is (Runner.Leave). The title and the text replace
// the card's: the conversation may have drifted far from what the card was
// opened for, and the card waits in the inbox for somebody who was not in it.
// Both empty is the runner saying nothing came, and the card stops waiting.
func (e *Engine) Described(cardID, title, text string) {
	title, text = strings.TrimSpace(title), strings.TrimSpace(text)
	defer e.setDescribing(cardID, false)
	if title == "" && text == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	edit := store.CardEdit{Body: &text}
	if title != "" {
		edit.Title = &title
	}
	if _, err := e.store.UpdateCard(cardID, edit); err != nil {
		e.log.Warn("could not write the card's description", "card", cardID, "err", err)
		return
	}
	e.record(cardID, model.EntryMove, msg.New("journal.described"))
}

// Describing reports whether the card's agent is still writing what it is.
func (e *Engine) Describing(cardID string) bool {
	e.descMu.Lock()
	defer e.descMu.Unlock()
	return e.describing[cardID]
}

func (e *Engine) setDescribing(cardID string, on bool) {
	e.descMu.Lock()
	if on {
		if e.describing == nil {
			e.describing = map[string]bool{}
		}
		e.describing[cardID] = true
	} else {
		delete(e.describing, cardID)
	}
	e.descMu.Unlock()
	e.emitCard(cardID)
}

// Return puts a card taken off its flow back on the stage it left, and the
// stage's conversation goes on where it stopped: a terminal stage resumes the
// last one it had (acp's opening). TakeIntoWork would start the flow over from
// its entry, a stage that has nothing of what was said.
func (e *Engine) Return(cardID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	card, err := e.store.Card(cardID)
	if err != nil {
		return err
	}
	if card.State != model.StateInbox {
		return msg.Err("card.notInInbox", "card", card.Title)
	}
	last, ok, err := e.store.LastFlowEvent(cardID)
	if err != nil {
		return err
	}
	if !ok {
		return msg.Err("card.neverInWork", "card", card.Title)
	}
	flow, err := e.store.Flow(last.FlowID)
	if err != nil {
		return err
	}
	stage, ok := flow.Stage(last.ToStage)
	if !ok {
		return msg.Err("stage.notInFlow", "stage", last.ToStage, "flow", flow.Name)
	}
	e.enterStage(card, flow, stage, "", msg.New("move.returned"))
	return nil
}

// Drop files a card away from wherever it is — the inbox or a flow — without
// putting it back in the inbox first: a job abandoned halfway is a decision of
// its own, not two. A finished card is not dropped: it was done, and saying
// otherwise afterwards would rewrite what happened.
func (e *Engine) Drop(cardID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	card, err := e.store.Card(cardID)
	if err != nil {
		return err
	}
	switch card.State {
	case model.StateDone:
		return msg.Err("card.doneCannotDrop", "card", card.Title)
	case model.StateDropped:
		return nil
	}
	e.cancel(cardID, msg.New("cancel.dropped"))
	if err := e.store.LeaveFlow(cardID, model.StateDropped); err != nil {
		return err
	}
	e.record(cardID, model.EntryMove, msg.New("journal.dropped"))
	e.emitCard(cardID)
	return nil
}

// Abandoned is what a runner calls when a person closed a step's terminal
// before it reported: the card is dropped (acp.Reporter).
func (e *Engine) Abandoned(cardID string) {
	if err := e.Drop(cardID); err != nil {
		e.log.Warn("could not drop an abandoned card", "card", cardID, "err", err)
	}
}

// Finished is what a runner calls when a session ends: its outcome is the event
// the stage moves on. A cancelled session reports no outcome at all — somebody
// intervened, and the flow waits for them.
//
// Detail is why, as the card's history will word it: the session ended, the
// step reported over MCP.
func (e *Engine) Finished(cardID, outcome string, detail msg.Msg, agentText string) {
	if outcome == "" {
		return
	}
	e.mu.Lock()
	// What the stage owed the card is taken off the agent's closing words and
	// put on the card before the flow reads anything: an edge may branch on one
	// of these values, and a missing required one turns a finished step into a
	// failed one.
	outcome, detail = e.harvestWritesLocked(cardID, outcome, detail, agentText)
	stage := e.advanceLocked(cardID, outcome, detail, agentText)
	e.mu.Unlock()

	// Draining is deferred until the advance is done and the lock is free, so
	// the place a session frees is filled by the next card rather than racing
	// the card that just left.
	if stage != "" {
		e.DrainStage(stage)
	}
}

// CardChanged advances a parked card when a property set on it is what its
// stage waits for. Setting anything else does nothing and leaves no trace: the
// change was simply not addressed to this stage.
func (e *Engine) CardChanged(cardID, property, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	st, ok, err := e.store.FlowState(cardID)
	if err != nil || !ok {
		return
	}
	flow, err := e.store.Flow(st.FlowID)
	if err != nil || !flow.WatchesProperty(st.StageID, property) {
		return
	}
	// A person answering a working card is a person intervening: the flow is
	// about to move it, so whatever ran here is over. A cancelled session
	// produces no outcome, so the two paths cannot double-move the card.
	e.cancel(cardID, msg.New("cancel.cardChanged"))
	e.advanceLocked(cardID, model.TriggerCardChanged,
		msg.New("move.cardChanged", "property", property, "value", value), "")
}

// HostingEvent moves a card along its stage's edge for something that
// happened to its MR. A stage that does not wait for it hears nothing and
// leaves no trace, as with a property nobody asked about: the MR moving on is
// news only where somebody is waiting for it.
func (e *Engine) HostingEvent(cardID, trigger string, detail msg.Msg) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok, err := e.store.FlowState(cardID)
	if err != nil || !ok {
		return false
	}
	flow, err := e.store.Flow(st.FlowID)
	if err != nil || !flow.HasEdge(st.StageID, trigger) {
		return false
	}
	if e.Publishing(cardID) {
		// The stage is talking to the hosting right now; its own outcome
		// moves the card, and a second mover would race it.
		return false
	}
	e.cancel(cardID, msg.New("cancel.hosting"))
	left := e.advanceLocked(cardID, trigger, detail, "")
	return left != ""
}

// advanceLocked moves a card along the edge matching an event and returns the
// stage it left, so the caller can refill it. Callers hold mu.
func (e *Engine) advanceLocked(cardID, on string, detail msg.Msg, agentText string) (leftStage string) {
	st, ok, err := e.store.FlowState(cardID)
	if err != nil || !ok {
		return ""
	}
	flow, err := e.store.Flow(st.FlowID)
	if err != nil {
		e.log.Info("flow is gone, card stays where it is", "card", cardID, "flow", st.FlowID)
		return ""
	}
	stage, ok := flow.Stage(st.StageID)
	if !ok {
		e.record(cardID, model.EntryProblem, msg.New("journal.stageGone", "flow", flow.Name))
		return ""
	}
	// How the stage ended goes onto the card before anything is decided by it:
	// an edge may branch on it, and the journal entry below is something a person
	// will read. Every stage produces one, so nothing has to declare it.
	e.writeOutcome(cardID, on)

	if !flow.HasEdge(stage.ID, on) {
		// A missing edge for an outcome is worth saying out loud: the flow
		// stops here and somebody has to know why.
		e.record(cardID, model.EntryProblem,
			msg.New("journal.noEdge", "flow", flow.Name, "stage", stage.Name, "on", on))
		return stage.ID
	}

	// The card is read again before the edge is chosen: a conditional edge asks
	// about the card as it is now, not as it was when it parked here.
	card, err := e.store.Card(cardID)
	if err != nil {
		e.log.Warn("could not read card for a transition", "card", cardID, "err", err)
		return stage.ID
	}
	next, cond, ok := flow.Next(stage.ID, on, card.Props, agentText)
	if !ok {
		// Edges exist for this event, but no condition held and there is no
		// fallback. That is a decision the flow made, and worth recording.
		e.record(cardID, model.EntryProblem,
			msg.New("journal.noCondition", "flow", flow.Name, "on", on, "stage", stage.Name))
		return stage.ID
	}

	// One event moves a card once. The visit is part of the key, not just the
	// stage: a route loops by design (docs/system.md §5.3), and a card that
	// comes back to a stage it has already left is at a new step rather than
	// repeating an old one. Keyed on the stage alone, the second success out of
	// a stage would be swallowed as a duplicate and the card would stand there
	// for good — which is the same guarantee turned into a trap.
	//
	// The visit is named by the journal row that recorded it rather than by the
	// time it happened: two transitions can share a millisecond, and then two
	// different steps would answer to one key.
	visit := int64(0)
	if last, ok, err := e.store.LastFlowEvent(cardID); err == nil && ok {
		visit = last.ID
	}
	key := fmt.Sprintf("flow|%s|%s|%d|%s", cardID, stage.ID, visit, on)
	if fresh, err := e.store.Claim(key); err != nil {
		e.log.Error("idempotency check failed", "err", err)
		return stage.ID
	} else if !fresh {
		return ""
	}

	if detail.IsZero() {
		detail = msg.New("move.on", "on", on)
	}
	e.enterStage(card, flow, next, on, withCond(detail, cond))
	return stage.ID
}

// enterStage is what "the card is now on this stage" means: record the position,
// say why, and run the stage. Callers hold mu.
func (e *Engine) enterStage(card model.Card, flow model.Flow, stage model.Stage, on string, detail msg.Msg) {
	previous, hadPlace, _ := e.store.FlowState(card.ID)

	if err := e.store.EnterStage(card.ID, flow.ID, stage.ID); err != nil {
		e.log.Error("could not record where the card stands", "card", card.ID, "err", err)
		e.record(card.ID, model.EntryProblem,
			msg.New("journal.enterFailed", "flow", flow.Name, "stage", stage.Name).Because(err))
		return
	}
	from := ""
	if hadPlace {
		from = previous.StageID
	}
	if err := e.store.AppendFlowEvent(model.FlowEvent{
		CardID: card.ID, FlowID: flow.ID, FromStage: from, ToStage: stage.ID, On: on, Detail: &detail,
	}); err != nil {
		e.log.Error("could not record a flow event", "card", card.ID, "err", err)
	}
	entered := msg.New("journal.entered", "flow", flow.Name, "stage", stage.Name)
	entered.Cause = &detail
	e.record(card.ID, model.EntryMove, entered)
	e.log.Info("card entered a stage", "card", card.ID, "flow", flow.Name, "stage", stage.Name, "on", on)
	e.emitCard(card.ID)

	e.runStage(card, flow, stage)
}

// runStage starts whatever the stage does. A stage that cannot even start
// counts as a failed one, so the flow can carry the card to its failure branch
// instead of silently stalling.
func (e *Engine) runStage(card model.Card, flow model.Flow, stage model.Stage) {
	e.runStageSaying(card, flow, stage, "", false)
}

// runStageSaying is runStage with what the agent is told given rather than
// composed: a stage continued (Continue) resumes a conversation that already
// holds its brief.
// Quiet is a stage reopened (Reopen): the agent is told nothing at all.
func (e *Engine) runStageSaying(card model.Card, flow model.Flow, stage model.Stage, said string, quiet bool) {
	// A final stage is where the card stops. Nothing runs, nothing waits.
	if stage.Final {
		if err := e.store.LeaveFlow(card.ID, model.StateDone); err != nil {
			e.log.Error("could not close the card", "card", card.ID, "err", err)
			return
		}
		e.record(card.ID, model.EntryMove, msg.New("journal.flowDone", "flow", flow.Name))
		e.emitCard(card.ID)
		return
	}
	switch stage.Action {
	case model.ActionAgent:
	case model.ActionPublish, model.ActionVerdict:
		e.startPublish(card, flow, stage)
		return
	default:
		return // the card stands and waits for an event
	}
	if e.runner == nil {
		e.record(card.ID, model.EntryProblem, msg.New("journal.noRunner"))
		return
	}

	agents, err := e.store.Agents()
	if err != nil {
		e.failStage(card, flow, stage, err)
		return
	}
	// The card is read fresh: the assignee may have been set while it was on
	// its way here, and it decides who runs this.
	current, err := e.store.Card(card.ID)
	if err == nil {
		card = current
	}

	var taken model.TakenByHumanError
	agent, err := model.PickAgent(card, stage.Crew, agents, e.runner.Busy())
	switch {
	case err == nil:
	case errors.Is(err, model.ErrCrewBusy):
		e.enqueue(card, flow, stage)
		return
	case errors.As(err, &taken):
		// Not a failed step — the work is being done, only not by us — so the
		// card waits where it stands rather than taking a failure edge.
		e.record(card.ID, model.EntryProblem, msg.New("journal.takenByHuman", "who", taken.Who))
		return
	default:
		e.failStage(card, flow, stage, err)
		return
	}

	// The stage's own limit: how many cards it works at once. Checked here
	// rather than at each entry point, so every way onto a stage — taking a
	// card into work, an edge, the queue itself — obeys the same number.
	if stage.MaxRunning > 0 && e.runner.RunningOnStage(stage.ID) >= stage.MaxRunning {
		e.enqueue(card, flow, stage)
		return
	}

	lang := ""
	if e.language != nil {
		lang = e.language()
	}
	var prompt, brief string
	if stage.Work == model.WorkSession {
		prompt = ComposePrompt(card, flow, stage, agent, e.arrival(card.ID, flow, stage), lang)
	} else {
		prompt = TerminalOpening(card, e.arrival(card.ID, flow, stage))
		brief = TerminalBrief(card, flow, stage, agent, lang)
	}
	if said != "" || quiet {
		prompt = said
	}
	job := Job{Card: card, Flow: flow, Stage: stage, Agent: agent, Prompt: prompt, Brief: brief}
	if err := e.runner.Start(job); err != nil {
		e.failStage(card, flow, stage, err)
		return
	}
	e.dequeue(card.ID)
	e.emitCard(card.ID)
}

func (e *Engine) startPublish(card model.Card, flow model.Flow, stage model.Stage) {
	if e.publisher == nil {
		e.record(card.ID, model.EntryProblem, msg.New("journal.noPublisher"))
		return
	}
	job := Job{Card: card, Flow: flow, Stage: stage}
	if last, ok, err := e.store.LastFlowEvent(card.ID); err == nil && ok {
		job.Visit = last.ID
	}
	e.pubMu.Lock()
	if e.publishing == nil {
		e.publishing = map[string]bool{}
	}
	e.publishing[card.ID] = true
	e.pubMu.Unlock()
	e.publisher.Publish(job)
	e.emitCard(card.ID)
}

// Publishing reports whether a hosting stage is working the card now.
func (e *Engine) Publishing(cardID string) bool {
	e.pubMu.Lock()
	defer e.pubMu.Unlock()
	return e.publishing[cardID]
}

// StepDone is how a hosting stage reports. The card moves only if it still
// stands in the visit the step was started for: a person who moved it by hand
// meanwhile is above the graph, and the late outcome is recorded by whoever
// sent it and otherwise dropped.
func (e *Engine) StepDone(job Job, outcome string, detail msg.Msg) {
	e.pubMu.Lock()
	delete(e.publishing, job.Card.ID)
	e.pubMu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	last, ok, err := e.store.LastFlowEvent(job.Card.ID)
	if err != nil || !ok || last.ID != job.Visit {
		e.emitCard(job.Card.ID)
		return
	}
	e.advanceLocked(job.Card.ID, outcome, detail, "")
	e.emitCard(job.Card.ID)
}

// failStage records that a stage could not start and lets the flow take the
// card to its failure branch. Silently stalling would be worse: the card would
// sit with no explanation and no way on.
func (e *Engine) failStage(card model.Card, flow model.Flow, stage model.Stage, cause error) {
	e.log.Warn("stage did not start", "card", card.ID, "stage", stage.Name, "err", cause)
	e.record(card.ID, model.EntryProblem,
		msg.New("journal.stepNotStarted", "flow", flow.Name, "stage", stage.Name).Because(cause))
	e.advanceLocked(card.ID, model.TriggerFailure, msg.New("outcome.notStarted"), "")
}

// harvestWritesLocked puts a finished stage's declared outputs on the card and
// says what happened to them. It returns the outcome the flow should act on:
// unchanged, unless a required value never arrived — a stage that owed one and
// did not deliver it has not finished, whatever it said about itself.
//
// Callers hold mu.
func (e *Engine) harvestWritesLocked(cardID, outcome string, detail msg.Msg, agentText string) (string, msg.Msg) {
	st, ok, err := e.store.FlowState(cardID)
	if err != nil || !ok {
		return outcome, detail
	}
	flow, err := e.store.Flow(st.FlowID)
	if err != nil {
		return outcome, detail
	}
	stage, ok := flow.Stage(st.StageID)
	if !ok || len(stage.Writes) == 0 {
		return outcome, detail
	}

	delivered := ParseWrites(agentText, stage.Writes)
	if len(delivered) > 0 {
		if _, err := e.store.UpdateCard(cardID, store.CardEdit{Props: delivered}); err != nil {
			e.log.Warn("could not write stage outputs", "card", cardID, "stage", stage.Name, "err", err)
			e.record(cardID, model.EntryProblem, msg.New("journal.writesFailed", "stage", stage.Name).Because(err))
		} else {
			e.record(cardID, model.EntryProps,
				msg.New("journal.wrote", "stage", stage.Name, "values", describeWrites(stage.Writes, delivered)))
		}
	}

	// Only a step that reported success can be held to its contract. One that
	// already failed is on its way to the failure branch, and refusing it a
	// second time would say nothing new.
	if outcome != model.TriggerSuccess {
		return outcome, detail
	}
	missing := MissingRequired(stage.Writes, delivered)
	if len(missing) == 0 {
		return outcome, detail
	}
	e.record(cardID, model.EntryProblem,
		msg.New("journal.requiredMissing", "stage", stage.Name, "properties", strings.Join(missing, ", ")))
	return model.TriggerFailure, msg.New("outcome.requiredMissing", "properties", strings.Join(missing, ", "))
}

// writeOutcome puts how a stage ended into the card's own outcome field. Silent
// about anything that is not a stage's own outcome: a person's answer on the
// card is news about the work rather than a verdict the machine reached, and
// writing it back would answer the person with their own words.
func (e *Engine) writeOutcome(cardID, on string) {
	value := model.OutcomeValue(on)
	if value == "" {
		return
	}
	if _, err := e.store.UpdateCard(cardID, store.CardEdit{Props: map[string]string{model.OutcomeProperty: value}}); err != nil {
		e.log.Warn("could not write the stage outcome on the card", "card", cardID, "outcome", value, "err", err)
	}
}

// arrival is what this stage's agent is told about how the card got here. Read
// off the card's own history rather than passed down the call chain: a card
// starting from the queue arrives at runStage with no event in hand, and it has
// the same right to know it is the second attempt.
func (e *Engine) arrival(cardID string, flow model.Flow, stage model.Stage) string {
	event, ok, err := e.store.LastFlowEvent(cardID)
	if err != nil || !ok || event.ToStage != stage.ID {
		return ""
	}
	st, onFlow, err := e.store.FlowState(cardID)
	if err != nil || !onFlow {
		return ""
	}
	revisit := false
	for _, id := range st.Visited {
		if id == stage.ID {
			revisit = true
			break
		}
	}
	note := ArrivalNote(flow, event, revisit)
	if note == "" {
		return ""
	}
	if card, err := e.store.Card(cardID); err == nil && event.On == model.TriggerCardChanged {
		if remarks := card.Prop(model.RemarksProperty); remarks != "" {
			note += "\n\nWhat the reviewer says is wrong:\n" + remarks
		}
	}
	return note
}

// withCond adds the condition that chose the edge to why the card moved: «the
// step passed» and «the step passed, «Verdict» = «ok»» are different reasons,
// and the second is the one that explains where the card went.
func withCond(detail msg.Msg, cond *model.Cond) msg.Msg {
	if cond.IsZero() {
		return detail
	}
	args := make(map[string]string, len(detail.Args)+3)
	for k, v := range detail.Args {
		args[k] = v
	}
	if cond.CommentContains != "" {
		args["ifComment"] = cond.CommentContains
	} else {
		args["ifProperty"], args["ifValue"] = cond.Property, cond.Value
	}
	detail.Args = args
	return detail
}

// ---- the stage queue ----

// enqueue parks a card whose stage is full. It says so on the card once — a
// card re-queued by a later attempt has already been explained.
func (e *Engine) enqueue(card model.Card, flow model.Flow, stage model.Stage) {
	fresh, err := e.store.Enqueue(store.QueuedCard{CardID: card.ID, FlowID: flow.ID, StageID: stage.ID})
	if err != nil {
		e.log.Error("could not queue the card", "card", card.ID, "err", err)
		return
	}
	e.log.Info("card waits for room on a stage", "card", card.ID, "stage", stage.Name, "fresh", fresh)
	if fresh {
		e.record(card.ID, model.EntryProblem, msg.New("journal.queued", "stage", stage.Name))
		e.emitCard(card.ID)
	}
}

func (e *Engine) dequeue(cardID string) {
	if err := e.store.Dequeue(cardID); err != nil {
		e.log.Warn("could not take the card off the queue", "card", cardID, "err", err)
	}
}

// DrainStage starts the card that has waited longest for a stage, if the stage
// has room again. Called where a session releases its place.
func (e *Engine) DrainStage(stageID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	q, ok, err := e.store.NextQueued(stageID)
	if err != nil {
		e.log.Error("could not read the stage queue", "err", err)
		return
	}
	if !ok {
		return
	}
	flow, err := e.store.Flow(q.FlowID)
	if err != nil {
		e.dequeue(q.CardID) // the flow was deleted while the card waited
		return
	}
	stage, found := flow.Stage(stageID)
	if !found {
		e.dequeue(q.CardID) // the stage was edited out of existence
		return
	}
	card, err := e.store.Card(q.CardID)
	if err != nil {
		e.log.Warn("could not read the waiting card", "card", q.CardID, "err", err)
		return
	}
	// The card may have been moved elsewhere while it waited. Its queue entry
	// is stale then, and starting it here would drag it back.
	st, onFlow, _ := e.store.FlowState(q.CardID)
	if !onFlow || st.StageID != stageID {
		e.dequeue(q.CardID)
		return
	}
	e.runStage(card, flow, stage)
}

// ---- helpers ----

func (e *Engine) cancel(cardID string, reason msg.Msg) {
	if e.runner != nil {
		e.runner.Cancel(cardID, reason)
	}
}

func (e *Engine) record(cardID string, kind model.EntryKind, m msg.Msg) {
	if _, err := e.store.Record(model.JournalEntry{CardID: cardID, Kind: kind, Msg: &m}); err != nil {
		e.log.Warn("could not write the card journal", "card", cardID, "err", err)
	}
}

func (e *Engine) emitCard(cardID string) {
	if e.ui == nil {
		return
	}
	e.ui.Emit(EventCard, map[string]any{"cardId": cardID})
}
