package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func claude(t *testing.T, s *Store) model.Agent {
	t.Helper()
	a, err := s.SaveAgent(model.Agent{Name: "Claude", Kind: model.KindClaude})
	if err != nil {
		t.Fatalf("сохранить агента: %v", err)
	}
	return a
}

func devFlow() model.Flow {
	return model.Flow{
		Name: "Разработка", EntryStage: "work",
		Stages: []model.Stage{
			{ID: "work", Name: "В работе", Action: model.ActionAgent, Crew: []string{"Claude"}},
			{ID: "review", Name: "На проверке", Action: model.ActionNone},
			{ID: "done", Name: "Готово", Final: true},
		},
		Edges: []model.Edge{
			{From: "work", To: "review", On: model.TriggerSuccess},
			{From: "review", To: "done", On: model.TriggerCardChanged, If: &model.Cond{Property: "Одобрено", Value: "Да"}},
		},
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := open(t)
	// Running the same steps again must be a no-op, which is what an installed
	// database does on every start.
	if err := s.migrate(); err != nil {
		t.Fatalf("повторная миграция: %v", err)
	}
	empty, err := s.IsEmpty()
	if err != nil || !empty {
		t.Fatalf("свежая база должна быть пустой (empty=%v, err=%v)", empty, err)
	}
}

// A new database is brought through the baseline and every step after it, and
// says so: the next start must not take it for one stopped halfway.
func TestNewDatabaseStandsAtTheLastVersion(t *testing.T) {
	s := open(t)
	var version int
	if err := s.db.Get(&version, `SELECT MAX(version) FROM schema_migration`); err != nil {
		t.Fatalf("версия схемы: %v", err)
	}
	if want := Baseline + len(s.d.Migrations()) - 1; version != want {
		t.Fatalf("новая база должна стоять на %d, стоит на %d", want, version)
	}
}

// A database at the baseline — every installed one when the steps were
// squashed — takes only the steps after it.
func TestDatabaseAtTheBaselineTakesTheStepsAfterIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "at-baseline.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("открыть: %v", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE card DROP COLUMN typed`,
		`ALTER TABLE stage DROP COLUMN template`,
		`DROP TABLE stage_template`,
		`DROP TABLE project_folder`,
		`ALTER TABLE card DROP COLUMN folder`,
		`ALTER TABLE agent_session DROP COLUMN revivable`,
		`INSERT INTO project (id, name, name_key, kind, path, created_at) VALUES ('p1', 'Сайт', 'сайт', 'folder', '/tmp/сайт', 0)`,
		`DELETE FROM schema_migration WHERE version > ` + strconv.Itoa(Baseline),
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("вернуть базу на %d (%s): %v", Baseline, stmt, err)
		}
	}
	s.Close()
	back, err := Open(path)
	if err != nil {
		t.Fatalf("открыть базу на %d: %v", Baseline, err)
	}
	defer back.Close()
	if _, err := back.CreateCard(model.Card{Title: "т", Typed: true}); err != nil {
		t.Fatalf("задача после шагов за базой: %v", err)
	}
	// A project from before folders were a list keeps its one, under its own
	// id: the tasks that named no folder go on working where they did.
	p, err := back.Project("p1")
	if err != nil {
		t.Fatalf("проект после шагов за базой: %v", err)
	}
	if len(p.Folders) != 1 || p.Folders[0].ID != "p1" || p.Folders[0].Path != "/tmp/сайт" {
		t.Fatalf("папки старого проекта: %+v", p.Folders)
	}
}

// A database stopped partway through the steps the baseline replaced cannot be
// finished: those steps are gone. Refused by name rather than half-migrated.
func TestDatabaseOlderThanTheBaselineIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("открыть: %v", err)
	}
	for _, stmt := range []string{
		`DELETE FROM schema_migration`,
		`INSERT INTO schema_migration (version, applied_at) VALUES (` + strconv.Itoa(Baseline-1) + `, 0)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("состарить базу (%s): %v", stmt, err)
		}
	}
	s.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "store.olderSchema") {
		t.Fatalf("старая база должна быть отвергнута как старая, получено %v", err)
	}
}

func TestSaveFlowRoundTrip(t *testing.T) {
	s := open(t)
	claude(t, s)

	saved, err := s.SaveFlow(devFlow())
	if err != nil {
		t.Fatalf("сохранить флоу: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("флоу должен получить идентификатор")
	}

	got, err := s.FlowByName("разработка")
	if err != nil {
		t.Fatalf("прочитать флоу: %v", err)
	}
	if got.EntryStage != "work" || len(got.Stages) != 3 || len(got.Edges) != 2 {
		t.Fatalf("граф не сошёлся: вход %q, %d стадий, %d рёбер", got.EntryStage, len(got.Stages), len(got.Edges))
	}
	// Order is a person's decision and must survive the round trip.
	if got.Stages[0].ID != "work" || got.Stages[2].ID != "done" {
		t.Fatalf("порядок стадий не сохранился: %v", stageIDs(got))
	}
	if len(got.Stages[0].Crew) != 1 || got.Stages[0].Crew[0] != "Claude" {
		t.Fatalf("состав стадии не сохранился: %v", got.Stages[0].Crew)
	}
	if got.Edges[1].If == nil || got.Edges[1].If.Property != "Одобрено" {
		t.Fatalf("условие ребра не сохранилось: %+v", got.Edges[1].If)
	}
	// An unconditional edge must come back without a condition, not with an
	// empty one — the engine tells them apart.
	if got.Edges[0].If != nil {
		t.Fatalf("у безусловного ребра появилось условие: %+v", got.Edges[0].If)
	}
}

func TestSaveFlowReplacesTheGraphWhole(t *testing.T) {
	s := open(t)
	claude(t, s)
	saved, _ := s.SaveFlow(devFlow())

	saved.Stages = saved.Stages[:2]
	saved.Stages[1].Final = true
	saved.Edges = saved.Edges[:1]
	if _, err := s.SaveFlow(saved); err != nil {
		t.Fatalf("пересохранить флоу: %v", err)
	}
	got, _ := s.Flow(saved.ID)
	if len(got.Stages) != 2 || len(got.Edges) != 1 {
		t.Fatalf("старые стадии и рёбра должны исчезнуть: %d стадий, %d рёбер", len(got.Stages), len(got.Edges))
	}
}

func TestSaveFlowRefusesADuplicateName(t *testing.T) {
	s := open(t)
	claude(t, s)
	if _, err := s.SaveFlow(devFlow()); err != nil {
		t.Fatalf("сохранить: %v", err)
	}
	// A different flow, same name: two flows a card could name identically.
	if _, err := s.SaveFlow(devFlow()); err == nil {
		t.Fatal("флоу с занятым именем должен быть отвергнут")
	}
}

// Names are matched without case, and most names here are Russian. The
// database's own lower() is ASCII-only, so this is the test that keeps the
// folding in Go where it works.
func TestNamesMatchWithoutCaseInAnyAlphabet(t *testing.T) {
	s := open(t)
	claude(t, s)
	if _, err := s.SaveFlow(devFlow()); err != nil {
		t.Fatalf("сохранить: %v", err)
	}
	for _, spelling := range []string{"Разработка", "разработка", "РАЗРАБОТКА", "  Разработка  "} {
		if _, err := s.FlowByName(spelling); err != nil {
			t.Fatalf("флоу должен находиться по написанию %q: %v", spelling, err)
		}
	}
	// And the same folding must make a differently-cased name a duplicate.
	clash := devFlow()
	clash.Name = "РАЗРАБОТКА"
	if _, err := s.SaveFlow(clash); err == nil {
		t.Fatal("имя, отличающееся только регистром, — то же самое имя")
	}

	src := model.Source{Name: "Телефон", Enabled: true}
	if _, err := s.SaveSource(src); err != nil {
		t.Fatalf("сохранить источник: %v", err)
	}
	if _, err := s.Source("телефон"); err != nil {
		t.Fatalf("источник должен находиться без учёта регистра: %v", err)
	}
}

// A card records where it stands by stage id, so "which flow is this stage in"
// has to have one answer. A hand-written flow reusing an id is refused by name
// rather than by a constraint violation.
func TestStageIDsAreUniqueAcrossFlows(t *testing.T) {
	s := open(t)
	claude(t, s)
	if _, err := s.SaveFlow(devFlow()); err != nil {
		t.Fatalf("сохранить: %v", err)
	}
	other := devFlow()
	other.Name = "Другой"
	_, err := s.SaveFlow(other)
	if err == nil {
		t.Fatal("чужой идентификатор стадии должен быть отвергнут")
	}
	if !strings.Contains(err.Error(), "Разработка") {
		t.Fatalf("ошибка должна называть флоу, который занял идентификатор: %v", err)
	}
	// Re-saving the same flow keeps its own ids, which is the ordinary case.
	if _, err := s.SaveFlow(devFlow()); err == nil {
		t.Log("своё имя занято своим же флоу — это проверяется отдельно")
	}
}

func TestFlowForStage(t *testing.T) {
	s := open(t)
	claude(t, s)
	saved, _ := s.SaveFlow(devFlow())
	got, err := s.FlowForStage("review")
	if err != nil || got.ID != saved.ID {
		t.Fatalf("флоу по стадии не найден: %v (%q)", err, got.ID)
	}
}

func TestCardRoundTripWithProps(t *testing.T) {
	s := open(t)
	card, err := s.CreateCard(model.Card{
		Source: "Демо", ExternalID: "1", Title: "Починить кран",
		Props: map[string]string{"Приоритет": "срочно"},
	})
	if err != nil {
		t.Fatalf("создать карточку: %v", err)
	}
	if card.State != model.StateInbox {
		t.Fatalf("новая карточка должна быть во входящих, получено %q", card.State)
	}
	got, err := s.Card(card.ID)
	if err != nil {
		t.Fatalf("прочитать карточку: %v", err)
	}
	if got.Prop("приоритет") != "срочно" {
		t.Fatalf("свойство не сохранилось: %v", got.Props)
	}
}

// A property set to nothing is a property nobody set: it goes away rather than
// sitting there as an empty value a condition could match.
func TestUpdateCardRemovesEmptyProps(t *testing.T) {
	s := open(t)
	card, _ := s.CreateCard(model.Card{Title: "Т", Props: map[string]string{"Одобрено": "Да"}})
	got, err := s.UpdateCard(card.ID, CardEdit{Props: map[string]string{"Одобрено": ""}})
	if err != nil {
		t.Fatalf("обновить: %v", err)
	}
	if _, ok := got.Props["Одобрено"]; ok {
		t.Fatalf("пустое свойство должно исчезнуть: %v", got.Props)
	}
}

func TestOneItemIsOneCard(t *testing.T) {
	s := open(t)
	if _, err := s.CreateCard(model.Card{Source: "Демо", ExternalID: "42", Title: "Первая"}); err != nil {
		t.Fatalf("первая карточка: %v", err)
	}
	if _, err := s.CreateCard(model.Card{Source: "Демо", ExternalID: "42", Title: "Вторая"}); err == nil {
		t.Fatal("тот же элемент источника не должен заводить вторую карточку")
	}
	// Cards a person typed carry no external id, so they are not all the same
	// item — the index is partial for exactly this reason.
	for i := 0; i < 2; i++ {
		if _, err := s.CreateCard(model.Card{Title: "Своя"}); err != nil {
			t.Fatalf("карточка без источника должна создаваться: %v", err)
		}
	}
}

func TestEnterStageTracksWhereTheCardHasBeen(t *testing.T) {
	s := open(t)
	claude(t, s)
	flow, _ := s.SaveFlow(devFlow())
	card, _ := s.CreateCard(model.Card{Title: "Т"})

	if err := s.EnterStage(card.ID, flow.ID, "work"); err != nil {
		t.Fatalf("встать на стадию: %v", err)
	}
	got, _ := s.Card(card.ID)
	if got.State != model.StateFlow {
		t.Fatalf("карточка на флоу должна быть в состоянии flow, получено %q", got.State)
	}
	if err := s.EnterStage(card.ID, flow.ID, "review"); err != nil {
		t.Fatalf("перейти на стадию: %v", err)
	}
	st, ok, err := s.FlowState(card.ID)
	if err != nil || !ok {
		t.Fatalf("положение не прочиталось: %v", err)
	}
	if st.StageID != "review" {
		t.Fatalf("ожидалась стадия review, получено %q", st.StageID)
	}
	if len(st.Visited) != 1 || st.Visited[0] != "work" {
		t.Fatalf("пройденные стадии не записались: %v", st.Visited)
	}
}

// Re-entering the same stage — a loop bringing the card back — must not record
// it as "already been through" while the card is standing on it.
func TestEnterSameStageTwiceDoesNotMarkItVisited(t *testing.T) {
	s := open(t)
	claude(t, s)
	flow, _ := s.SaveFlow(devFlow())
	card, _ := s.CreateCard(model.Card{Title: "Т"})
	s.EnterStage(card.ID, flow.ID, "work")
	s.EnterStage(card.ID, flow.ID, "work")
	st, _, _ := s.FlowState(card.ID)
	if len(st.Visited) != 0 {
		t.Fatalf("текущая стадия не пройдена: %v", st.Visited)
	}
}

func TestLeaveFlowClearsThePlaceAndTheQueue(t *testing.T) {
	s := open(t)
	claude(t, s)
	flow, _ := s.SaveFlow(devFlow())
	card, _ := s.CreateCard(model.Card{Title: "Т"})
	s.EnterStage(card.ID, flow.ID, "work")
	if _, err := s.Enqueue(QueuedCard{CardID: card.ID, FlowID: flow.ID, StageID: "work"}); err != nil {
		t.Fatalf("в очередь: %v", err)
	}

	if err := s.LeaveFlow(card.ID, model.StateDone); err != nil {
		t.Fatalf("снять с флоу: %v", err)
	}
	if _, ok, _ := s.FlowState(card.ID); ok {
		t.Fatal("положение должно быть очищено")
	}
	if queued, _ := s.IsQueued(card.ID); queued {
		t.Fatal("карточка не должна остаться в очереди")
	}
	got, _ := s.Card(card.ID)
	if got.State != model.StateDone {
		t.Fatalf("ожидалось состояние done, получено %q", got.State)
	}
}

// An entry is placed by the transition it was written in, not by its time: a
// step's report is written in the same millisecond as the transition after it.
func TestJournalEntryKnowsItsTransition(t *testing.T) {
	s := open(t)
	card, _ := s.CreateCard(model.Card{Title: "Т"})

	before, _ := s.Record(model.JournalEntry{CardID: card.ID, Kind: model.EntryProblem, Text: "до флоу"})
	s.AppendFlowEvent(model.FlowEvent{CardID: card.ID, FlowID: "f", ToStage: "a"})
	s.Record(model.JournalEntry{CardID: card.ID, Kind: model.EntryReport, SessionID: "s1", Author: "claude", Text: "итог"})
	s.AppendFlowEvent(model.FlowEvent{CardID: card.ID, FlowID: "f", FromStage: "a", ToStage: "b"})
	if _, err := s.Record(model.JournalEntry{CardID: card.ID, Text: "   "}); err != nil {
		t.Fatalf("пустая запись: %v", err)
	}

	events, _ := s.FlowEvents(card.ID)
	got, _ := s.Journal(card.ID)
	if len(got) != 2 {
		t.Fatalf("пустая запись не пишется, ожидалось 2, получено %+v", got)
	}
	if got[0].ID != before.ID || got[0].EventID != 0 {
		t.Fatalf("запись до первого перехода ни к чему не привязана: %+v", got[0])
	}
	if r := got[1]; r.EventID != events[0].ID || r.Kind != model.EntryReport || r.SessionID != "s1" || r.Author != "claude" {
		t.Fatalf("отчёт принадлежит первому переходу и своей сессии: %+v", r)
	}
}

func TestQueueIsFIFOPerStage(t *testing.T) {
	s := open(t)
	claude(t, s)
	flow, _ := s.SaveFlow(devFlow())
	first, _ := s.CreateCard(model.Card{Title: "Первая"})
	second, _ := s.CreateCard(model.Card{Title: "Вторая"})

	fresh, err := s.Enqueue(QueuedCard{CardID: first.ID, FlowID: flow.ID, StageID: "work"})
	if err != nil || !fresh {
		t.Fatalf("первая постановка должна быть свежей: fresh=%v err=%v", fresh, err)
	}
	// Told once, not on every retry.
	if fresh, _ := s.Enqueue(QueuedCard{CardID: first.ID, FlowID: flow.ID, StageID: "work"}); fresh {
		t.Fatal("повторная постановка на ту же стадию не свежая")
	}
	time.Sleep(2 * time.Millisecond)
	s.Enqueue(QueuedCard{CardID: second.ID, FlowID: flow.ID, StageID: "work"})

	next, ok, err := s.NextQueued("work")
	if err != nil || !ok || next.CardID != first.ID {
		t.Fatalf("первой должна выйти карточка, ждавшая дольше: %+v (%v)", next, err)
	}
	s.Dequeue(first.ID)
	next, _, _ = s.NextQueued("work")
	if next.CardID != second.ID {
		t.Fatalf("следующей должна быть вторая, получено %q", next.CardID)
	}
}

func TestClaimIsOnce(t *testing.T) {
	s := open(t)
	fresh, err := s.Claim("flow|c1|work|success")
	if err != nil || !fresh {
		t.Fatalf("первый захват должен пройти: %v", err)
	}
	fresh, err = s.Claim("flow|c1|work|success")
	if err != nil || fresh {
		t.Fatal("одно событие двигает карточку один раз")
	}
}

func TestAgentRegistryRefusesNonsense(t *testing.T) {
	s := open(t)
	if _, err := s.SaveAgent(model.Agent{Name: "  ", Kind: model.KindClaude}); err == nil {
		t.Fatal("агент без имени должен быть отвергнут")
	}
	if _, err := s.SaveAgent(model.Agent{Name: "X", Kind: "тамагочи"}); err == nil {
		t.Fatal("неизвестный тип должен быть отвергнут")
	}
	// The generic kind carries its own agent, so an empty command is an agent
	// that cannot start.
	if _, err := s.SaveAgent(model.Agent{Name: "X", Kind: model.KindACP}); err == nil {
		t.Fatal("acp-агент без команды должен быть отвергнут")
	}
	if _, err := s.SaveAgent(model.Agent{Name: "X", Kind: model.KindACP, Command: []string{"my-agent", " "}}); err != nil {
		t.Fatalf("acp-агент с командой должен сохраниться: %v", err)
	}
	got, _ := s.Agent("x")
	if len(got.Command) != 1 {
		t.Fatalf("пустые элементы команды должны отсеиваться: %v", got.Command)
	}
}

func TestStagesUsingAgentAnswersBeforeADelete(t *testing.T) {
	s := open(t)
	claude(t, s)
	s.SaveFlow(devFlow())
	used, err := s.StagesUsingAgent("claude")
	if err != nil {
		t.Fatalf("прочитать: %v", err)
	}
	if len(used) != 1 || !strings.Contains(used[0], "В работе") {
		t.Fatalf("должна найтись стадия, называющая агента: %v", used)
	}
}

func TestSourceRulesKeepTheirOrder(t *testing.T) {
	s := open(t)
	src := model.Source{
		Name: "Демо", Enabled: true,
		Rules: []model.Rule{
			{Name: "срочные", When: model.Match{Title: "срочно"}, Then: model.ActionCard, SuggestFlow: "Разработка"},
			{Name: "остальное", Then: model.ActionDrop},
		},
	}
	if _, err := s.SaveSource(src); err != nil {
		t.Fatalf("сохранить источник: %v", err)
	}
	got, err := s.Source("демо")
	if err != nil {
		t.Fatalf("прочитать источник: %v", err)
	}
	if len(got.Rules) != 2 || got.Rules[0].Name != "срочные" {
		t.Fatalf("порядок правил — это то, что человек имел в виду: %+v", got.Rules)
	}
	if got.Rules[0].When.Title != "срочно" || got.Rules[0].SuggestFlow != "Разработка" {
		t.Fatalf("правило не сошлось: %+v", got.Rules[0])
	}
}

func TestSourceRefusesABrokenRegexp(t *testing.T) {
	s := open(t)
	_, err := s.SaveSource(model.Source{Name: "Демо", Rules: []model.Rule{{When: model.Match{Title: "("}}}})
	if err == nil {
		t.Fatal("сломанное условие должно быть отвергнуто там, где его набрали")
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := open(t)
	card, _ := s.CreateCard(model.Card{Title: "Т"})
	sess := Session{ID: "s1", CardID: card.ID, AgentName: "Claude", AgentKind: model.KindClaude,
		Status: StatusQueued, StartedAt: time.Now()}
	if err := s.InsertSession(sess); err != nil {
		t.Fatalf("записать сессию: %v", err)
	}
	done, finished := StatusDone, time.Now()
	if err := s.UpdateSession("s1", SessionUpdate{Status: &done, FinishedAt: &finished}); err != nil {
		t.Fatalf("обновить: %v", err)
	}
	got, err := s.SessionsForCard(card.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("сессия карточки не найдена: %v", err)
	}
	if got[0].Status != StatusDone || got[0].FinishedAt.IsZero() {
		t.Fatalf("статус не сохранился: %+v", got[0])
	}
}

// A process that is gone is not still working: a row saying otherwise would
// make a card look busy forever.
func TestAbandonRunningSessionsOnStart(t *testing.T) {
	s := open(t)
	card, _ := s.CreateCard(model.Card{Title: "Т"})
	s.InsertSession(Session{ID: "s1", CardID: card.ID, Status: StatusRunning, StartedAt: time.Now()})
	s.InsertSession(Session{ID: "s2", CardID: card.ID, Status: StatusDone, StartedAt: time.Now()})

	n, err := s.AbandonRunningSessions()
	if err != nil || n != 1 {
		t.Fatalf("должна быть отменена одна сессия: n=%d err=%v", n, err)
	}
	got, _ := s.SessionsForCard(card.ID)
	for _, sess := range got {
		if sess.Status == StatusRunning {
			t.Fatal("работающих сессий после перезапуска не остаётся")
		}
	}
}

// A run whose conversation is known and can be opened again is paused rather
// than cancelled, whether the application closed properly or not: the agent
// keeps the conversation on disk either way. A terminal run's CLI always can; a
// session's agent only if it said so. A run with nothing to resume is
// cancelled.
func TestAbandonPausesATerminalRunWithAConversation(t *testing.T) {
	s := open(t)
	card, _ := s.CreateCard(model.Card{Title: "Т"})
	s.InsertSession(Session{ID: "term", CardID: card.ID, Work: model.WorkTerminal, Status: StatusRunning, StartedAt: time.Now()})
	id := "разговор-1"
	s.UpdateSession("term", SessionUpdate{ACPSessionID: &id})
	s.InsertSession(Session{ID: "term-new", CardID: card.ID, Work: model.WorkTerminal, Status: StatusRunning, StartedAt: time.Now()})
	s.InsertSession(Session{ID: "acp", CardID: card.ID, Work: model.WorkSession, Status: StatusRunning, StartedAt: time.Now()})
	s.UpdateSession("acp", SessionUpdate{ACPSessionID: &id})
	s.InsertSession(Session{ID: "acp-revivable", CardID: card.ID, Work: model.WorkSession, Status: StatusRunning, StartedAt: time.Now()})
	yes := true
	s.UpdateSession("acp-revivable", SessionUpdate{ACPSessionID: &id, Revivable: &yes})

	if _, err := s.AbandonRunningSessions(); err != nil {
		t.Fatalf("уборка: %v", err)
	}
	got, _ := s.SessionsForCard(card.ID)
	want := map[string]SessionStatus{
		"term": StatusPaused, "term-new": StatusCancelled,
		"acp": StatusCancelled, "acp-revivable": StatusPaused,
	}
	for _, sess := range got {
		if sess.Status != want[sess.ID] {
			t.Errorf("%s: %s, ждали %s", sess.ID, sess.Status, want[sess.ID])
		}
	}
}

func TestNotFoundIsRecognisable(t *testing.T) {
	s := open(t)
	if _, err := s.Card("нет-такой"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ожидалось ErrNotFound, получено %v", err)
	}
	if _, err := s.FlowByName("нет-такого"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ожидалось ErrNotFound, получено %v", err)
	}
}

func stageIDs(f model.Flow) []string {
	out := make([]string, 0, len(f.Stages))
	for _, s := range f.Stages {
		out = append(out, s.ID)
	}
	return out
}

// What a stage declares it writes and reads is part of the stage, so it survives
// the same way the rest of it does: saved whole, read back whole.
func TestStageDeclarationsSurviveARoundTrip(t *testing.T) {
	st := open(t)
	if _, err := st.SaveAgent(model.Agent{Name: "Claude", Kind: model.KindClaude}); err != nil {
		t.Fatalf("агент: %v", err)
	}
	saved, err := st.SaveFlow(model.Flow{
		Name: "С проверкой", EntryStage: "qa",
		Stages: []model.Stage{{
			ID: "qa", Name: "Проверка", Action: model.ActionAgent, Crew: []string{"Claude"},
			Writes: []model.PropertyWrite{{Property: "Вердикт", Required: true}, {Property: "Превью"}},
			Reads:  []string{"Ветка"},
		}},
	})
	if err != nil {
		t.Fatalf("сохранить флоу: %v", err)
	}

	back, err := st.Flow(saved.ID)
	if err != nil {
		t.Fatalf("прочитать флоу: %v", err)
	}
	stage := back.Stages[0]
	if len(stage.Writes) != 2 || stage.Writes[0].Property != "Вердикт" || !stage.Writes[0].Required {
		t.Fatalf("выходы стадии не пережили сохранение: %+v", stage.Writes)
	}
	if stage.Writes[1].Required {
		t.Fatalf("необязательный выход не должен стать обязательным: %+v", stage.Writes[1])
	}
	if len(stage.Reads) != 1 || stage.Reads[0] != "Ветка" {
		t.Fatalf("входы стадии не пережили сохранение: %+v", stage.Reads)
	}
}

// A stage's screens are stored with it and come back as declared — including
// the one that has no reference at all, because a terminal without a command is
// a shell in the card's folder rather than an unfinished screen.
func TestStageScreensSurviveSaving(t *testing.T) {
	s := open(t)
	claude(t, s)

	saved, err := s.SaveFlow(model.Flow{
		Name: "С экранами", EntryStage: "work",
		Stages: []model.Stage{{
			ID: "work", Name: "Работа", Action: model.ActionAgent, Crew: []string{"Claude"},
			Screens: []model.Screen{
				{Kind: model.ScreenNotes, Title: "План", Ref: "план.md"},
				{Kind: model.ScreenTerminal},
				{Kind: model.ScreenBrowser, Ref: "{Превью}"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("сохранить флоу: %v", err)
	}

	back, err := s.Flow(saved.ID)
	if err != nil {
		t.Fatalf("прочитать флоу: %v", err)
	}
	screens := back.Stages[0].Screens
	if len(screens) != 3 {
		t.Fatalf("экраны не пережили сохранение: %+v", screens)
	}
	if screens[0].Kind != model.ScreenNotes || screens[0].Title != "План" || screens[0].Ref != "план.md" {
		t.Fatalf("первый экран разошёлся с объявленным: %+v", screens[0])
	}
	if screens[1].Kind != model.ScreenTerminal || screens[1].Ref != "" {
		t.Fatalf("терминал без команды должен пережить сохранение: %+v", screens[1])
	}
}

// A project in two places: a task works in the folder it named, the other one
// is not held by its branch, and a folder a task still works in cannot be
// dropped from under it.
func TestProjectFolders(t *testing.T) {
	s := open(t)
	p, err := s.SaveProject(model.Project{Name: "Магазин", Folders: []model.Folder{
		{Name: "фронт", Path: "/tmp/shop/front"},
		{Name: "бэк", Path: "/tmp/shop/back"},
	}})
	if err != nil {
		t.Fatalf("сохранить проект: %v", err)
	}
	if len(p.Folders) != 2 || p.Folders[1].ID == "" || p.Path != "/tmp/shop/front" {
		t.Fatalf("папки проекта: %+v", p)
	}
	back := p.Folders[1].ID

	c, err := s.CreateCard(model.Card{Title: "API", State: model.StateFlow, Project: p.ID, Folder: back,
		WorkMode: model.WorkModeBranch, Branch: "api"})
	if err != nil {
		t.Fatalf("задача: %v", err)
	}
	if got, err := s.CardProject(c); err != nil || got.Path != "/tmp/shop/back" {
		t.Fatalf("задача работает в %q (%v), а не в бэке", got.Path, err)
	}

	whole, _ := s.Project(p.ID)
	if _, held, _ := s.FolderHolder(whole, back, ""); !held {
		t.Fatal("ветка задачи должна держать папку бэка")
	}
	if _, held, _ := s.FolderHolder(whole, "", ""); held {
		t.Fatal("ветка в бэке не держит фронт")
	}

	whole.Folders = whole.Folders[:1]
	if _, err := s.SaveProject(whole); err == nil {
		t.Fatal("папку, в которой работает задача, убрать нельзя")
	}

	// The front made second: a task that named no folder stays in the front.
	plain, _ := s.CreateCard(model.Card{Title: "Стили", Project: p.ID})
	whole, _ = s.Project(p.ID)
	whole.Folders[0], whole.Folders[1] = whole.Folders[1], whole.Folders[0]
	if _, err := s.SaveProject(whole); err != nil {
		t.Fatalf("переставить папки: %v", err)
	}
	plain, _ = s.Card(plain.ID)
	if got, _ := s.CardProject(plain); got.Path != "/tmp/shop/front" {
		t.Fatalf("задача без папки уехала в %q", got.Path)
	}
}
