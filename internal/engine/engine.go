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
	"github.com/artipop/xxvi/internal/store"
)

// Job is one piece of work a stage asks for.
type Job struct {
	Card  model.Card
	Flow  model.Flow
	Stage model.Stage
	Agent model.Agent
	// Prompt is what the agent is told: its own prompt, then the stage's, then
	// the card's task. Composed here so the runner has nothing to decide.
	Prompt string
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
	Cancel(cardID, reason string)
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
	store  *store.Store
	runner Runner
	ui     Emitter
	log    *slog.Logger

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
		return fmt.Errorf("карточка «%s» уже в работе", card.Title)
	}
	flow, err := e.store.Flow(flowID)
	if err != nil {
		return err
	}
	entry, ok := flow.Entry()
	if !ok {
		return fmt.Errorf("у флоу «%s» не найдена входная стадия", flow.Name)
	}
	e.enterStage(card, flow, entry, "", "взята в работу")
	return nil
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
		return fmt.Errorf("стадия %q не найдена во флоу «%s»", stageID, flow.Name)
	}
	e.cancel(cardID, "карточка переведена на другую стадию вручную")
	e.dequeue(cardID)
	e.enterStage(card, flow, stage, "", "переведена вручную")
	return nil
}

// RemoveFromFlow takes a card off its flow and back into the inbox. Nothing is
// undone — the journal and the flow events stay.
func (e *Engine) RemoveFromFlow(cardID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cancel(cardID, "карточка снята с флоу")
	if err := e.store.LeaveFlow(cardID, model.StateInbox); err != nil {
		return err
	}
	e.record(cardID, model.EntryMove, "Карточка снята с флоу и вернулась во входящие.")
	e.emitCard(cardID)
	return nil
}

// Finished is what a runner calls when a session ends: its outcome is the event
// the stage moves on. A cancelled session reports no outcome at all — somebody
// intervened, and the flow waits for them.
func (e *Engine) Finished(cardID, outcome, detail, agentText string) {
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
	e.cancel(cardID, "на карточке выбрано значение, по которому стадия едет дальше")
	e.advanceLocked(cardID, model.TriggerCardChanged,
		fmt.Sprintf("на карточке выбрано «%s» = «%s»", property, value), "")
}

// advanceLocked moves a card along the edge matching an event and returns the
// stage it left, so the caller can refill it. Callers hold mu.
func (e *Engine) advanceLocked(cardID, on, detail, agentText string) (leftStage string) {
	st, ok, err := e.store.FlowState(cardID)
	if err != nil || !ok {
		return ""
	}
	flow, err := e.store.Flow(st.FlowID)
	if err != nil {
		e.log.Info("флоу исчез, карточка осталась на месте", "card", cardID, "flow", st.FlowID)
		return ""
	}
	stage, ok := flow.Stage(st.StageID)
	if !ok {
		e.record(cardID, model.EntryProblem, fmt.Sprintf("Флоу «%s»: стадия исчезла из маршрута — карточка осталась на месте.", flow.Name))
		return ""
	}
	// How the stage ended goes onto the card before anything is decided by it:
	// an edge may branch on it, and the journal entry below is something a person
	// will read. Every stage produces one, so nothing has to declare it.
	e.writeOutcome(cardID, on)

	if !flow.HasEdge(stage.ID, on) {
		// A missing edge for an outcome is worth saying out loud: the flow
		// stops here and somebody has to know why.
		e.record(cardID, model.EntryProblem, fmt.Sprintf(
			"Флоу «%s»: у стадии «%s» нет перехода по событию «%s» — карточка осталась на месте.",
			flow.Name, stage.Name, model.TriggerLabel(on)))
		return stage.ID
	}

	// The card is read again before the edge is chosen: a conditional edge asks
	// about the card as it is now, not as it was when it parked here.
	card, err := e.store.Card(cardID)
	if err != nil {
		e.log.Warn("не удалось прочитать карточку для перехода", "card", cardID, "err", err)
		return stage.ID
	}
	next, cond, ok := flow.Next(stage.ID, on, card.Props, agentText)
	if !ok {
		// Edges exist for this event, but no condition held and there is no
		// fallback. That is a decision the flow made, and worth recording.
		e.record(cardID, model.EntryProblem, fmt.Sprintf(
			"Флоу «%s»: событие «%s» пришло, но ни одно условие стадии «%s» не выполнено — карточка осталась на месте.",
			flow.Name, model.TriggerLabel(on), stage.Name))
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
		e.log.Error("проверка идемпотентности не удалась", "err", err)
		return stage.ID
	} else if !fresh {
		return ""
	}

	if detail == "" {
		detail = model.TriggerLabel(on)
	}
	if desc := cond.Describe(); desc != "" {
		detail += ", " + desc
	}
	e.enterStage(card, flow, next, on, detail)
	return stage.ID
}

// enterStage is what "the card is now on this stage" means: record the position,
// say why, and run the stage. Callers hold mu.
func (e *Engine) enterStage(card model.Card, flow model.Flow, stage model.Stage, on, detail string) {
	previous, hadPlace, _ := e.store.FlowState(card.ID)

	if err := e.store.EnterStage(card.ID, flow.ID, stage.ID); err != nil {
		e.log.Error("не удалось записать положение карточки", "card", card.ID, "err", err)
		e.record(card.ID, model.EntryProblem, fmt.Sprintf("Флоу «%s»: не удалось перевести карточку в «%s»: %v", flow.Name, stage.Name, err))
		return
	}
	from := ""
	if hadPlace {
		from = previous.StageID
	}
	if err := e.store.AppendFlowEvent(model.FlowEvent{
		CardID: card.ID, FlowID: flow.ID, FromStage: from, ToStage: stage.ID, On: on, Detail: detail,
	}); err != nil {
		e.log.Error("не удалось записать событие флоу", "card", card.ID, "err", err)
	}
	e.record(card.ID, model.EntryMove, fmt.Sprintf("Флоу «%s»: карточка переведена в «%s» — %s.", flow.Name, stage.Name, detail))
	e.log.Info("карточка встала на стадию", "card", card.ID, "flow", flow.Name, "stage", stage.Name, "on", on)
	e.emitCard(card.ID)

	e.runStage(card, flow, stage)
}

// runStage starts whatever the stage does. A stage that cannot even start
// counts as a failed one, so the flow can carry the card to its failure branch
// instead of silently stalling.
func (e *Engine) runStage(card model.Card, flow model.Flow, stage model.Stage) {
	// A final stage is where the card stops. Nothing runs, nothing waits.
	if stage.Final {
		if err := e.store.LeaveFlow(card.ID, model.StateDone); err != nil {
			e.log.Error("не удалось закрыть карточку", "card", card.ID, "err", err)
			return
		}
		e.record(card.ID, model.EntryMove, fmt.Sprintf("Флоу «%s» пройден до конца. Карточка закрыта.", flow.Name))
		e.emitCard(card.ID)
		return
	}
	if stage.Action != model.ActionAgent {
		return // the card stands and waits for an event
	}
	if e.runner == nil {
		e.record(card.ID, model.EntryProblem, "Запускать агентов сейчас нечем — стадия ничего не сделала.")
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
		e.record(card.ID, model.EntryProblem, fmt.Sprintf(
			"Агент не запускался: %v. Карточка ждёт, пока её отпустят или назначат агента.", err))
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

	job := Job{Card: card, Flow: flow, Stage: stage, Agent: agent,
		Prompt: ComposePrompt(card, flow, stage, agent, e.arrival(card.ID, flow, stage))}
	if err := e.runner.Start(job); err != nil {
		e.failStage(card, flow, stage, err)
		return
	}
	e.dequeue(card.ID)
	e.emitCard(card.ID)
}

// failStage records that a stage could not start and lets the flow take the
// card to its failure branch. Silently stalling would be worse: the card would
// sit with no explanation and no way on.
func (e *Engine) failStage(card model.Card, flow model.Flow, stage model.Stage, cause error) {
	e.log.Warn("стадия не запустилась", "card", card.ID, "stage", stage.Name, "err", cause)
	e.record(card.ID, model.EntryProblem, fmt.Sprintf("Флоу «%s», стадия «%s»: шаг не запущен: %v", flow.Name, stage.Name, cause))
	e.advanceLocked(card.ID, model.TriggerFailure, "шаг не удалось запустить", "")
}

// harvestWritesLocked puts a finished stage's declared outputs on the card and
// says what happened to them. It returns the outcome the flow should act on:
// unchanged, unless a required value never arrived — a stage that owed one and
// did not deliver it has not finished, whatever it said about itself.
//
// Callers hold mu.
func (e *Engine) harvestWritesLocked(cardID, outcome, detail, agentText string) (string, string) {
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
			e.log.Warn("не удалось записать выходы стадии", "card", cardID, "stage", stage.Name, "err", err)
			e.record(cardID, model.EntryProblem, fmt.Sprintf("Не удалось записать результат стадии «%s» на карточку: %v", stage.Name, err))
		} else {
			e.record(cardID, model.EntryProps, fmt.Sprintf("Стадия «%s» записала на карточку: %s.", stage.Name, describeWrites(stage.Writes, delivered)))
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
	e.record(cardID, model.EntryProblem, fmt.Sprintf(
		"Стадия «%s» обязана записать %s — этого в ответе агента нет, шаг считается неудачным.",
		stage.Name, quoteAll(missing)))
	return model.TriggerFailure, fmt.Sprintf("не записано обязательное: %s", strings.Join(missing, ", "))
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
		e.log.Warn("не удалось записать исход стадии на карточку", "card", cardID, "outcome", value, "err", err)
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
	return ArrivalNote(flow, event, revisit)
}

// quoteAll is a list of property names as a person reads them.
func quoteAll(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fmt.Sprintf("«%s»", n))
	}
	return strings.Join(out, ", ")
}

// ---- the stage queue ----

// enqueue parks a card whose stage is full. It says so on the card once — a
// card re-queued by a later attempt has already been explained.
func (e *Engine) enqueue(card model.Card, flow model.Flow, stage model.Stage) {
	fresh, err := e.store.Enqueue(store.QueuedCard{CardID: card.ID, FlowID: flow.ID, StageID: stage.ID})
	if err != nil {
		e.log.Error("не удалось поставить карточку в очередь", "card", card.ID, "err", err)
		return
	}
	e.log.Info("карточка ждёт места на стадии", "card", card.ID, "stage", stage.Name, "fresh", fresh)
	if fresh {
		e.record(card.ID, model.EntryProblem, fmt.Sprintf(
			"Стадия «%s» занята — шаг начнётся, как только освободится место.", stage.Name))
		e.emitCard(card.ID)
	}
}

func (e *Engine) dequeue(cardID string) {
	if err := e.store.Dequeue(cardID); err != nil {
		e.log.Warn("не удалось убрать карточку из очереди", "card", cardID, "err", err)
	}
}

// DrainStage starts the card that has waited longest for a stage, if the stage
// has room again. Called where a session releases its place.
func (e *Engine) DrainStage(stageID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	q, ok, err := e.store.NextQueued(stageID)
	if err != nil {
		e.log.Error("не удалось прочитать очередь стадии", "err", err)
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
		e.log.Warn("не удалось прочитать ждущую карточку", "card", q.CardID, "err", err)
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

func (e *Engine) cancel(cardID, reason string) {
	if e.runner != nil {
		e.runner.Cancel(cardID, reason)
	}
}

func (e *Engine) record(cardID string, kind model.EntryKind, text string) {
	if _, err := e.store.Record(model.JournalEntry{CardID: cardID, Kind: kind, Text: text}); err != nil {
		e.log.Warn("не удалось записать в журнал карточки", "card", cardID, "err", err)
	}
}

func (e *Engine) emitCard(cardID string) {
	if e.ui == nil {
		return
	}
	e.ui.Emit(EventCard, map[string]any{"cardId": cardID})
}
