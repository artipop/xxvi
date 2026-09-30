package engine

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// fakeRunner stands in for the ACP side. It records what it was asked to run
// and lets a test decide how each job ends, which is the whole of what the
// engine needs from a runner.
type fakeRunner struct {
	mu      sync.Mutex
	jobs    []Job
	running map[string]string // cardID → stageID
	busy    map[string]bool
	fail    error // when set, Start refuses
	engine  *Engine
}

func newRunner() *fakeRunner {
	return &fakeRunner{running: map[string]string{}, busy: map[string]bool{}}
}

func (r *fakeRunner) Start(job Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.jobs = append(r.jobs, job)
	r.running[job.Card.ID] = job.Stage.ID
	r.busy[model.Username(job.Agent.Name)] = true
	return nil
}

func (r *fakeRunner) Busy() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.busy))
	for k, v := range r.busy {
		out[k] = v
	}
	return out
}

func (r *fakeRunner) RunningOnStage(stageID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, s := range r.running {
		if s == stageID {
			n++
		}
	}
	return n
}

func (r *fakeRunner) Cancel(cardID string, reason msg.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, cardID)
}

func (r *fakeRunner) Leave(cardID string, reason msg.Msg) { r.Cancel(cardID, reason) }

// finish ends the card's session the way a real one would: release the place,
// then report the outcome.
func (r *fakeRunner) finish(cardID, outcome, agentText string) {
	r.mu.Lock()
	for _, job := range r.jobs {
		if job.Card.ID == cardID {
			delete(r.busy, model.Username(job.Agent.Name))
		}
	}
	delete(r.running, cardID)
	r.mu.Unlock()
	r.engine.Finished(cardID, outcome, msg.Msg{}, agentText)
}

func (r *fakeRunner) lastJob(t *testing.T) Job {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.jobs) == 0 {
		t.Fatal("агент ни разу не запускался")
	}
	return r.jobs[len(r.jobs)-1]
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

// ---- fixtures ----

type fixture struct {
	store  *store.Store
	engine *Engine
	runner *fakeRunner
	flow   model.Flow
}

// devFlow: an agent stage, a human checkpoint that forks on the answer, and two
// ends. It exercises every trigger this application has.
func devFlow() model.Flow {
	return model.Flow{
		Name: "Разработка", EntryStage: "work",
		Stages: []model.Stage{
			{ID: "work", Name: "В работе", Action: model.ActionAgent, Crew: []string{"Claude"}},
			{ID: "review", Name: "На проверке", Action: model.ActionNone},
			{ID: "done", Name: "Готово", Final: true},
			{ID: "blocked", Name: "Заблокировано", Final: true},
		},
		Edges: []model.Edge{
			{From: "work", To: "review", On: model.TriggerSuccess},
			{From: "work", To: "blocked", On: model.TriggerFailure},
			{From: "review", To: "done", On: model.TriggerCardChanged, If: &model.Cond{Property: "Одобрено", Value: "Да"}},
			{From: "review", To: "work", On: model.TriggerCardChanged, If: &model.Cond{Property: "Одобрено", Value: "Нет"}},
		},
	}
}

func setup(t *testing.T, flow model.Flow, agents ...model.Agent) fixture {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("база: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if len(agents) == 0 {
		agents = []model.Agent{{Name: "Claude", Kind: model.KindClaude}}
	}
	for _, a := range agents {
		if _, err := st.SaveAgent(a); err != nil {
			t.Fatalf("агент %s: %v", a.Name, err)
		}
	}
	saved, err := st.SaveFlow(flow)
	if err != nil {
		t.Fatalf("флоу: %v", err)
	}
	r := newRunner()
	e := New(st, r, nil, nil)
	r.engine = e
	return fixture{store: st, engine: e, runner: r, flow: saved}
}

func (f fixture) card(t *testing.T, title string) model.Card {
	t.Helper()
	c, err := f.store.CreateCard(model.Card{Source: "Демо", Title: title})
	if err != nil {
		t.Fatalf("карточка: %v", err)
	}
	return c
}

func (f fixture) stageOf(t *testing.T, cardID string) string {
	t.Helper()
	st, ok, err := f.store.FlowState(cardID)
	if err != nil {
		t.Fatalf("положение: %v", err)
	}
	if !ok {
		return ""
	}
	return st.StageID
}

func (f fixture) stateOf(t *testing.T, cardID string) model.CardState {
	t.Helper()
	c, err := f.store.Card(cardID)
	if err != nil {
		t.Fatalf("карточка: %v", err)
	}
	return c.State
}

// ---- the whole point: a card goes through a flow ----

func TestCardTravelsTheWholeFlow(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Починить кран")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	if got := f.stageOf(t, card.ID); got != "work" {
		t.Fatalf("карточка должна встать на входную стадию, получено %q", got)
	}
	if f.stateOf(t, card.ID) != model.StateFlow {
		t.Fatal("карточка в работе — это состояние flow")
	}
	if f.runner.count() != 1 {
		t.Fatal("на стадии с агентом должна запуститься сессия")
	}

	// The agent finishes: the flow carries the card to the human checkpoint.
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")
	if got := f.stageOf(t, card.ID); got != "review" {
		t.Fatalf("после успеха ожидалась «На проверке», получено %q", got)
	}
	// A stage that runs nothing must not start anything.
	if f.runner.count() != 1 {
		t.Fatal("стадия без действия ничего не запускает")
	}

	// A person answers: the card reaches the end and closes.
	if _, err := f.store.UpdateCard(card.ID, store.CardEdit{Props: map[string]string{"Одобрено": "Да"}}); err != nil {
		t.Fatalf("ответить: %v", err)
	}
	f.engine.CardChanged(card.ID, "Одобрено", "Да")

	if got := f.stageOf(t, card.ID); got != "" {
		t.Fatalf("пройденная до конца карточка снимается с флоу, а стоит на %q", got)
	}
	if f.stateOf(t, card.ID) != model.StateDone {
		t.Fatalf("ожидалось состояние done, получено %q", f.stateOf(t, card.ID))
	}
}

func TestFailureTakesTheFailureBranch(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	f.runner.finish(card.ID, model.TriggerFailure, "")
	if f.stateOf(t, card.ID) != model.StateDone {
		t.Fatal("«Заблокировано» — финальная стадия, карточка закрывается")
	}
	events, _ := f.store.FlowEvents(card.ID)
	if len(events) != 2 || events[1].ToStage != "blocked" {
		t.Fatalf("журнал должен показывать переход в blocked: %+v", events)
	}
}

// A loop is the point of the "Нет" branch: the card goes back to the agent and
// a new session starts.
func TestRejectionSendsTheCardBackToTheAgent(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.runner.finish(card.ID, model.TriggerSuccess, "")

	f.store.UpdateCard(card.ID, store.CardEdit{Props: map[string]string{"Одобрено": "Нет"}})
	f.engine.CardChanged(card.ID, "Одобрено", "Нет")

	if got := f.stageOf(t, card.ID); got != "work" {
		t.Fatalf("отказ должен вернуть карточку в работу, получено %q", got)
	}
	if f.runner.count() != 2 {
		t.Fatalf("на возврате должна запуститься вторая сессия, запусков: %d", f.runner.count())
	}
	// Having been through a stage is a set, not a trail: the card is standing
	// on "work" again and must not be marked as done with it.
	view, _ := f.engine.CardFlowFor(card.ID)
	for _, s := range view.Stages {
		if s.ID == "work" && s.Done {
			t.Fatal("текущая стадия не может быть пройденной")
		}
		if s.ID == "review" && !s.Done {
			t.Fatal("пройденная стадия должна быть отмечена")
		}
	}
}

// Setting an unrelated property must not wake a waiting stage — and must not
// leave a "nothing matched" entry either.
func TestUnrelatedPropertyDoesNotWakeTheStage(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.runner.finish(card.ID, model.TriggerSuccess, "")

	before, _ := f.store.Journal(card.ID)
	f.engine.CardChanged(card.ID, "Приоритет", "срочно")

	if got := f.stageOf(t, card.ID); got != "review" {
		t.Fatalf("карточка не должна была двинуться, получено %q", got)
	}
	after, _ := f.store.Journal(card.ID)
	if len(after) != len(before) {
		t.Fatalf("постороннее изменение не адресовано стадии и не должно ничего писать: %q", after[len(after)-1].Text)
	}
}

// An answer that matches no condition is a decision the flow made, and it is
// worth recording: a card that silently stalls is the bug this avoids.
func TestUnmatchedAnswerIsExplained(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.runner.finish(card.ID, model.TriggerSuccess, "")

	f.store.UpdateCard(card.ID, store.CardEdit{Props: map[string]string{"Одобрено": "Может быть"}})
	f.engine.CardChanged(card.ID, "Одобрено", "Может быть")

	if got := f.stageOf(t, card.ID); got != "review" {
		t.Fatalf("карточка должна остаться на месте, получено %q", got)
	}
	if !lastEntryIs(t, f, model.EntryProblem, "journal.noCondition") {
		t.Fatal("карточка должна сказать, почему она не поехала")
	}
}

// A missing edge is the other way a flow stops, and it is equally worth saying.
func TestMissingEdgeIsExplained(t *testing.T) {
	flow := devFlow()
	flow.Edges = flow.Edges[:1] // success only: nothing catches a failure
	f := setup(t, flow)
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	f.runner.finish(card.ID, model.TriggerFailure, "")
	if got := f.stageOf(t, card.ID); got != "work" {
		t.Fatalf("без ребра карточка остаётся на месте, получено %q", got)
	}
	if !lastEntryIs(t, f, model.EntryProblem, "journal.noEdge") {
		t.Fatal("карточка должна сказать, что перехода нет")
	}
}

func TestOneEventMovesACardOnce(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	f.engine.Finished(card.ID, model.TriggerSuccess, msg.Msg{}, "")
	f.engine.Finished(card.ID, model.TriggerSuccess, msg.Msg{}, "")

	events, _ := f.store.FlowEvents(card.ID)
	// Taking into work, then one transition. A second would mean the repeat
	// moved the card again.
	if len(events) != 2 {
		t.Fatalf("одно событие двигает карточку один раз, переходов: %d", len(events))
	}
}

// ---- conditions on the agent's own words ----

func routingFlow() model.Flow {
	return model.Flow{
		Name: "Маршрутизация", EntryStage: "work",
		Stages: []model.Stage{
			{ID: "work", Name: "В работе", Action: model.ActionAgent, Crew: []string{"Claude"}},
			{ID: "deploy", Name: "Выкатка", Final: true},
			{ID: "review", Name: "На проверке", Final: true},
		},
		Edges: []model.Edge{
			{From: "work", To: "deploy", On: model.TriggerSuccess, If: &model.Cond{CommentContains: "ГОТОВО К ДЕПЛОЮ"}},
			{From: "work", To: "review", On: model.TriggerSuccess},
		},
	}
}

func TestAgentRoutesTheCardByItsClosingWords(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"всё сделал, готово к деплою", "deploy"},
		{"сделал, но надо посмотреть", "review"},
	} {
		f := setup(t, routingFlow())
		card := f.card(t, "Т")
		f.engine.TakeIntoWork(card.ID, f.flow.ID)
		f.runner.finish(card.ID, model.TriggerSuccess, tc.text)

		events, _ := f.store.FlowEvents(card.ID)
		if got := events[len(events)-1].ToStage; got != tc.want {
			t.Fatalf("ответ %q: ожидалась стадия %q, получена %q", tc.text, tc.want, got)
		}
	}
}

// The agent has to know which words the flow listens for, or the condition is
// a trap. Saying it once in the composed prompt beats every stage prompt
// repeating it.
func TestPromptTellsTheAgentWhichWordsRouteTheCard(t *testing.T) {
	f := setup(t, routingFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	prompt := f.runner.lastJob(t).Brief
	if !strings.Contains(prompt, "ГОТОВО К ДЕПЛОЮ") {
		t.Fatalf("в промпте нет слов, по которым едет маршрут:\n%s", prompt)
	}
}

func TestPromptCarriesAgentStageAndCard(t *testing.T) {
	flow := devFlow()
	flow.Stages[0].Prompt = "Работай аккуратно."
	f := setup(t, flow, model.Agent{Name: "Claude", Kind: model.KindClaude, Prompt: "Ты инженер."})

	card, _ := f.store.CreateCard(model.Card{
		Title: "Починить кран", Body: "Капает на кухне", URL: "https://example.org/1",
		Props: map[string]string{"Приоритет": "срочно"},
	})
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	// In a terminal the conversation opens with the card as it was written;
	// the prompts and the values are the route's, and go to the instructions.
	job := f.runner.lastJob(t)
	for _, want := range []string{"Починить кран", "Капает на кухне", "https://example.org/1"} {
		if !strings.Contains(job.Prompt, want) {
			t.Fatalf("в первом сообщении нет %q:\n%s", want, job.Prompt)
		}
	}
	for _, want := range []string{"Ты инженер.", "Работай аккуратно.", "Приоритет: срочно"} {
		if !strings.Contains(job.Brief, want) || strings.Contains(job.Prompt, want) {
			t.Fatalf("%q должно быть в инструкциях, а не в разговоре:\nпервое сообщение: %s\nинструкции: %s", want, job.Prompt, job.Brief)
		}
	}
	if strings.Index(job.Brief, "Ты инженер.") > strings.Index(job.Brief, "Работай аккуратно.") {
		t.Fatal("промпт агента идёт перед промптом стадии")
	}
}

// A task typed into the ribbon is sent as typed, and one typed without a word
// opens the CLI with nothing said — not its title, which only names it in lists.
func TestTypedTaskOpensWithTheWordsTyped(t *testing.T) {
	for _, tc := range []struct{ title, body, want string }{
		{"Починить кран", "Починить кран\n\nКапает на кухне", "Починить кран\n\nКапает на кухне"},
		{"Задача без описания", "", ""},
	} {
		f := setup(t, devFlow())
		card, _ := f.store.CreateCard(model.Card{Title: tc.title, Body: tc.body, Typed: true})
		f.engine.TakeIntoWork(card.ID, f.flow.ID)
		if got := f.runner.lastJob(t).Prompt; got != tc.want {
			t.Fatalf("первое сообщение: %q, ожидалось %q", got, tc.want)
		}
	}
}

// ---- who picks the card up ----

func TestBusyCrewParksTheCardAndTheQueueStartsItLater(t *testing.T) {
	f := setup(t, devFlow())
	first, second := f.card(t, "Первая"), f.card(t, "Вторая")

	f.engine.TakeIntoWork(first.ID, f.flow.ID)
	f.engine.TakeIntoWork(second.ID, f.flow.ID)

	if f.runner.count() != 1 {
		t.Fatalf("единственный агент занят — вторая должна ждать, запусков: %d", f.runner.count())
	}
	if queued, _ := f.store.IsQueued(second.ID); !queued {
		t.Fatal("вторая карточка должна стоять в очереди")
	}
	// A parked card is on its stage, not nowhere: that invariant is what makes
	// "where is this card" answerable at any moment.
	if got := f.stageOf(t, second.ID); got != "work" {
		t.Fatalf("ждущая карточка стоит на своей стадии, получено %q", got)
	}

	// The first finishes and frees the place.
	f.runner.finish(first.ID, model.TriggerSuccess, "")
	if f.runner.count() != 2 {
		t.Fatalf("освободившееся место должна занять ждущая карточка, запусков: %d", f.runner.count())
	}
	if queued, _ := f.store.IsQueued(second.ID); queued {
		t.Fatal("стартовавшая карточка не должна остаться в очереди")
	}
}

func TestStageLimitIsObeyedWhateverTheCrew(t *testing.T) {
	flow := devFlow()
	flow.Stages[0].Crew = []string{"Claude", "Codex"}
	flow.Stages[0].MaxRunning = 1
	f := setup(t, flow,
		model.Agent{Name: "Claude", Kind: model.KindClaude},
		model.Agent{Name: "Codex", Kind: model.KindCodex})

	first, second := f.card(t, "Первая"), f.card(t, "Вторая")
	f.engine.TakeIntoWork(first.ID, f.flow.ID)
	f.engine.TakeIntoWork(second.ID, f.flow.ID)

	if f.runner.count() != 1 {
		t.Fatalf("лимит стадии — 1, а запусков %d", f.runner.count())
	}
	if queued, _ := f.store.IsQueued(second.ID); !queued {
		t.Fatal("вторая карточка должна ждать места")
	}
}

func TestCardTakenByAPersonDoesNotStartAnAgent(t *testing.T) {
	f := setup(t, devFlow())
	card, _ := f.store.CreateCard(model.Card{Title: "Т", Assignee: "Артём"})
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	if f.runner.count() != 0 {
		t.Fatal("на взятой человеком карточке агент не запускается")
	}
	// Not a failed step: the card waits where it stands rather than taking the
	// failure edge.
	if got := f.stageOf(t, card.ID); got != "work" {
		t.Fatalf("карточка должна остаться на своей стадии, получено %q", got)
	}
	if !lastEntryIs(t, f, model.EntryProblem, "Артём") {
		t.Fatal("карточка должна сказать, кто её взял")
	}
}

func TestAnAgentAssigneeIsAChoiceOfAgent(t *testing.T) {
	flow := devFlow()
	flow.Stages[0].Crew = []string{"Claude", "Codex"}
	f := setup(t, flow,
		model.Agent{Name: "Claude", Kind: model.KindClaude},
		model.Agent{Name: "Codex", Kind: model.KindCodex})

	card, _ := f.store.CreateCard(model.Card{Title: "Т", Assignee: "codex"})
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	if got := f.runner.lastJob(t).Agent.Name; got != "Codex" {
		t.Fatalf("исполнитель карточки решает, кто работает; получен %q", got)
	}
}

// A stage that cannot start is a failed stage, so the flow can carry the card
// to its failure branch instead of silently stalling.
func TestAStageThatCannotStartTakesTheFailureBranch(t *testing.T) {
	f := setup(t, devFlow())
	f.runner.fail = errAgentWontStart
	card := f.card(t, "Т")

	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	if f.stateOf(t, card.ID) != model.StateDone {
		t.Fatal("карточка должна была уехать по ветке отказа в финальную стадию")
	}
	if !anyEntryIs(t, f, card.ID, model.EntryProblem, "journal.stepNotStarted") {
		t.Fatal("карточка должна сказать, что шаг не запустился")
	}
}

// ---- a person is always above the graph ----

func TestManualMoveCancelsWhateverWasRunning(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	if err := f.engine.MoveTo(card.ID, "review"); err != nil {
		t.Fatalf("перевести вручную: %v", err)
	}
	if got := f.stageOf(t, card.ID); got != "review" {
		t.Fatalf("ожидалась «На проверке», получено %q", got)
	}
	if f.runner.RunningOnStage("work") != 0 {
		t.Fatal("работавшая сессия должна быть отменена")
	}
}

// Dropping a card in work is one decision, not «take it off, then drop it»:
// the agent stops and the card is gone from the flow in one go.
func TestDropTakesACardStraightOffItsFlow(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	if err := f.engine.Drop(card.ID); err != nil {
		t.Fatalf("отбросить: %v", err)
	}
	if f.stateOf(t, card.ID) != model.StateDropped {
		t.Fatal("карточка отброшена")
	}
	if f.runner.RunningOnStage("work") != 0 {
		t.Fatal("работавшая сессия должна быть отменена")
	}
	if got := f.stageOf(t, card.ID); got != "" {
		t.Fatalf("положение должно быть очищено, получено %q", got)
	}
	if !anyEntryIs(t, f, card.ID, model.EntryMove, "journal.dropped") {
		t.Fatal("журнал говорит, что карточку отбросили")
	}
}

func TestADoneCardIsNotDropped(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.runner.finish(card.ID, model.TriggerFailure, "") // в «Заблокировано», финал

	if err := f.engine.Drop(card.ID); err == nil {
		t.Fatal("сделанную карточку отбросить нельзя")
	}
	if f.stateOf(t, card.ID) != model.StateDone {
		t.Fatal("карточка остаётся сделанной")
	}
}

func TestRemoveFromFlowReturnsTheCardToTheInbox(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	if err := f.engine.RemoveFromFlow(card.ID); err != nil {
		t.Fatalf("снять с флоу: %v", err)
	}
	if f.stateOf(t, card.ID) != model.StateInbox {
		t.Fatal("снятая карточка возвращается во входящие")
	}
	if got := f.stageOf(t, card.ID); got != "" {
		t.Fatalf("положение должно быть очищено, получено %q", got)
	}
}

// Back from the inbox, the card lands on the stage it left, not on the
// flow's entry: that stage's conversation is where the work is.
func TestReturnPutsTheCardBackOnTheStageItLeft(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.runner.finish(card.ID, model.TriggerSuccess, "")
	if got := f.stageOf(t, card.ID); got != "review" {
		t.Fatalf("карточка должна дойти до проверки, стоит на %q", got)
	}
	if err := f.engine.RemoveFromFlow(card.ID); err != nil {
		t.Fatalf("снять с флоу: %v", err)
	}

	if err := f.engine.Return(card.ID); err != nil {
		t.Fatalf("вернуть: %v", err)
	}
	if f.stateOf(t, card.ID) != model.StateFlow || f.stageOf(t, card.ID) != "review" {
		t.Fatalf("карточка возвращается туда, где была: %s на %q", f.stateOf(t, card.ID), f.stageOf(t, card.ID))
	}
	if err := f.engine.Return(card.ID); err == nil {
		t.Fatal("карточку в работе возвращать неоткуда")
	}
}

func TestReturnRefusesACardNeverInWork(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	if err := f.engine.Return(card.ID); err == nil {
		t.Fatal("карточке без флоу возвращаться некуда")
	}
}

// What the agent says the task has become is the card's text from then on.
func TestDescribedReplacesTheCardText(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.Described(card.ID, "  Схема готова, осталось перенести запросы.  ")
	got, err := f.store.Card(card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "Схема готова, осталось перенести запросы." {
		t.Fatalf("описание карточки: %q", got.Body)
	}
	f.engine.Described(card.ID, "   ")
	if got, _ := f.store.Card(card.ID); got.Body == "" {
		t.Fatal("пустое описание не стирает написанное")
	}
}

func TestTakeIntoWorkRefusesACardAlreadyInWork(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err == nil {
		t.Fatal("повторный запуск уже работающей карточки должен быть отвергнут")
	}
}

// ---- what a card says about itself ----

func TestCardFlowSaysWhatItIsWaitingFor(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.runner.finish(card.ID, model.TriggerSuccess, "")

	view, err := f.engine.CardFlowFor(card.ID)
	if err != nil || view == nil {
		t.Fatalf("карточка на флоу должна отвечать о себе: %v", err)
	}
	if view.StageID != "review" || len(view.Stages) != 4 {
		t.Fatalf("вид карточки не сошёлся: %+v", view)
	}
	if len(view.WaitingFor) != 2 {
		t.Fatalf("стадия ждёт двух ответов, получено %v", view.WaitingFor)
	}
	if w := view.WaitingFor[0]; w.If == nil || w.If.Property != "Одобрено" {
		t.Fatalf("ожидание должно называть свойство: %+v", w)
	}
}

func TestCardNotOnAFlowSaysNothing(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	view, err := f.engine.CardFlowFor(card.ID)
	if err != nil || view != nil {
		t.Fatalf("карточка вне флоу ничего о маршруте не говорит: %+v (%v)", view, err)
	}
}

func TestOverviewCountsWhereEverythingIs(t *testing.T) {
	f := setup(t, devFlow())
	first, second := f.card(t, "Первая"), f.card(t, "Вторая")
	f.engine.TakeIntoWork(first.ID, f.flow.ID)
	f.engine.TakeIntoWork(second.ID, f.flow.ID)

	view, err := f.engine.Overview(f.flow.ID)
	if err != nil {
		t.Fatalf("обзор: %v", err)
	}
	if view.Cards != 2 {
		t.Fatalf("на флоу две карточки, посчитано %d", view.Cards)
	}
	work := view.Stages[0]
	if work.Cards != 2 || work.Running != 1 || work.Queued != 1 {
		t.Fatalf("на стадии «В работе» должно быть 2 карточки, 1 работает, 1 ждёт: %+v", work)
	}
}

// ---- helpers ----

var errAgentWontStart = &startError{}

type startError struct{}

func (*startError) Error() string { return "адаптер агента не найден" }

// lastEntryIs checks both what the journal says and what it takes it for: the
// ribbon picks what to show by the kind, so a problem recorded as a move is a
// card that stands with no word on its segment.
func lastEntryIs(t *testing.T, f fixture, kind model.EntryKind, want string) bool {
	t.Helper()
	entries, _ := f.store.Journal(lastCardID(t, f))
	if len(entries) == 0 {
		return false
	}
	last := entries[len(entries)-1]
	return last.Kind == kind && strings.Contains(entryText(last), want)
}

func anyEntryIs(t *testing.T, f fixture, cardID string, kind model.EntryKind, want string) bool {
	t.Helper()
	entries, _ := f.store.Journal(cardID)
	for _, e := range entries {
		if e.Kind == kind && strings.Contains(entryText(e), want) {
			return true
		}
	}
	return false
}

// entryText is an entry as a test reads it: somebody's words, or the
// application's message as its code and values.
func entryText(e model.JournalEntry) string {
	if e.Msg != nil {
		return e.Msg.String()
	}
	return e.Text
}

// lastCardID is the card the fixture most recently touched — the tests above
// each work with one.
func lastCardID(t *testing.T, f fixture) string {
	t.Helper()
	for _, state := range []model.CardState{model.StateFlow, model.StateDone, model.StateInbox} {
		cards, _ := f.store.CardsInState(state)
		if len(cards) > 0 {
			return cards[0].ID
		}
	}
	t.Fatal("карточек нет")
	return ""
}

// A route loops by design, and a card may go round it more than once: the check
// fails twice, the agent fixes it twice. "One event moves a card once" is about
// one event, not about one pair of stage and trigger — keyed on the pair, the
// second success out of a stage is swallowed as a duplicate and the card stands
// on it for good.
func TestACardCanGoRoundALoopTwice(t *testing.T) {
	f := setup(t, dataFlow())
	card := f.card(t, "Починить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	for i := 1; i <= 2; i++ {
		f.runner.finish(card.ID, model.TriggerSuccess, "")
		if got := f.stageOf(t, card.ID); got != "qa" {
			t.Fatalf("круг %d: карточка должна приехать на проверку, а стоит на %q", i, got)
		}
		f.runner.finish(card.ID, model.TriggerSuccess, "Вердикт: fail")
		if got := f.stageOf(t, card.ID); got != "work" {
			t.Fatalf("круг %d: провалившая проверку карточка возвращается агенту, а стоит на %q", i, got)
		}
	}

	// And the way out of the loop still works after it.
	f.runner.finish(card.ID, model.TriggerSuccess, "")
	f.runner.finish(card.ID, model.TriggerSuccess, "Вердикт: ok")
	if got := f.stateOf(t, card.ID); got != model.StateDone {
		t.Fatalf("после двух кругов карточка должна доехать до конца, а она %q", got)
	}
}

// An agent sent into a card's own branch is told so, or a stage that asks for
// «Ветка» invites it to cut another one where nothing looks for the work.
func TestPromptSaysTheCardIsOnItsOwnBranch(t *testing.T) {
	card := model.Card{Title: "Т", WorkMode: model.WorkModeWorktree, Branch: "t-1"}
	got := ComposePrompt(card, model.Flow{}, model.Stage{}, model.Agent{}, "", "")
	if !strings.Contains(got, "do not create another one") || !strings.Contains(got, "Branch: t-1.") {
		t.Fatalf("агенту не сказано про ветку задачи:\n%s", got)
	}
	card.WorkMode, card.Branch = model.WorkModeFolder, ""
	if got := ComposePrompt(card, model.Flow{}, model.Stage{}, model.Agent{}, "", ""); strings.Contains(strings.ToLower(got), "branch") {
		t.Fatalf("в папке как есть про ветку молчат:\n%s", got)
	}
}

// The language is the app's: the brief carries it, so no agent has to be told in
// its own prompt, and it does not reach into the repository's own conventions.
func TestPromptSaysWhichLanguageToWriteIn(t *testing.T) {
	card := model.Card{Title: "Т", WorkMode: model.WorkModeFolder}
	got := ComposePrompt(card, model.Flow{}, model.Stage{}, model.Agent{}, "", "Russian")
	if !strings.Contains(got, "messages to the person in Russian") {
		t.Fatalf("агенту не сказано, на каком языке писать:\n%s", got)
	}
	if !strings.Contains(got, "repository's own conventions") {
		t.Fatalf("язык сообщений не должен перекрывать правила репозитория:\n%s", got)
	}
	if got := ComposePrompt(card, model.Flow{}, model.Stage{}, model.Agent{}, "", ""); strings.Contains(got, "Write your messages") {
		t.Fatalf("без языка бриф о нём молчит:\n%s", got)
	}
}

// A person who sends the work back says what is wrong with it, and the agent
// the card returns to is told — otherwise the second attempt starts knowing
// only that the first one was not accepted.
func TestRemarksReachTheAgentTheCardReturnsTo(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Починить кран")
	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")

	props := map[string]string{model.RemarksProperty: "Кран всё ещё течёт", "Одобрено": "Нет"}
	if _, err := f.store.UpdateCard(card.ID, store.CardEdit{Props: props}); err != nil {
		t.Fatalf("ответить: %v", err)
	}
	f.engine.CardChanged(card.ID, "Одобрено", "Нет")

	if got := f.stageOf(t, card.ID); got != "work" {
		t.Fatalf("«нет» возвращает карточку агенту, а она на %q", got)
	}
	if prompt := f.runner.lastJob(t).Prompt; !strings.Contains(prompt, "What the reviewer says is wrong:\nКран всё ещё течёт") {
		t.Fatalf("замечаний нет в брифе:\n%s", prompt)
	}
}

// pause leaves the card's stage the way the application closing leaves it: its
// run paused on a conversation it can resume.
func (f fixture) pause(t *testing.T, cardID string) {
	t.Helper()
	job := f.runner.lastJob(t)
	f.runner.Cancel(cardID, msg.Msg{})
	f.runner.mu.Lock()
	delete(f.runner.busy, model.Username(job.Agent.Name))
	f.runner.mu.Unlock()
	paused := store.StatusPaused
	n := f.runner.count()
	id := fmt.Sprintf("run-%s-%d", cardID, n)
	if err := f.store.InsertSession(store.Session{
		ID: id, CardID: cardID, FlowID: f.flow.ID, StageID: job.Stage.ID,
		AgentName: job.Agent.Name, Work: model.WorkTerminal, Status: store.StatusRunning,
		StartedAt: time.Now().Add(time.Duration(n) * time.Second),
	}); err != nil {
		t.Fatalf("сессия: %v", err)
	}
	f.store.UpdateSession(id, store.SessionUpdate{Status: &paused})
}

// A paused stage goes on only when a person says so, and the agent is told what
// they said — or to go on — rather than its brief again: the conversation it
// resumes holds the brief already.
func TestAPausedStageContinuesWithWhatThePersonSaid(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Задача")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	brief := f.runner.lastJob(t).Prompt
	f.pause(t, card.ID)

	if err := f.engine.Continue(card.ID, "  а теперь тесты  "); err != nil {
		t.Fatalf("продолжить: %v", err)
	}
	job := f.runner.lastJob(t)
	if job.Prompt != "а теперь тесты" || job.Stage.ID != "work" {
		t.Fatalf("агент должен получить слова человека на той же стадии: %q на %s", job.Prompt, job.Stage.ID)
	}
	if strings.Contains(job.Prompt, strings.TrimSpace(brief)) {
		t.Fatal("бриф второй раз не отправляется")
	}

	f.pause(t, card.ID)
	if err := f.engine.Continue(card.ID, ""); err != nil {
		t.Fatalf("продолжить без слов: %v", err)
	}
	if got := f.runner.lastJob(t).Prompt; got != continueWords {
		t.Fatalf("без слов агенту говорят продолжать: %q", got)
	}
}

// Reopened, the stage comes back in its conversation with nothing said: not
// «go on», and not the opening again.
func TestAReopenedStageSaysNothing(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Задача")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.pause(t, card.ID)

	if err := f.engine.Reopen(card.ID); err != nil {
		t.Fatalf("открыть снова: %v", err)
	}
	job := f.runner.lastJob(t)
	if job.Prompt != "" || job.Stage.ID != "work" {
		t.Fatalf("агенту ничего не говорят, стадия та же: %q на %s", job.Prompt, job.Stage.ID)
	}
}

// Only a paused stage can be continued: one running, finished or never paused
// has nothing to pick up, and continuing it would start a second run.
func TestOnlyAPausedStageContinues(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Задача")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	if err := f.engine.Continue(card.ID, ""); err == nil {
		t.Fatal("идущую стадию не продолжают")
	}
	before := f.runner.count()
	other := f.card(t, "Не в работе")
	if err := f.engine.Continue(other.ID, ""); err == nil {
		t.Fatal("карточку не на флоу не продолжают")
	}
	if f.runner.count() != before {
		t.Fatal("отказ не должен ничего запускать")
	}
}
