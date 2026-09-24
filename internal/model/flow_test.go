package model

import (
	"errors"
	"testing"
)

// devFlow is the shape most tests want: an agent stage, a human checkpoint and
// two ends.
func devFlow() Flow {
	return Flow{
		ID: "f1", Name: "Разработка", EntryStage: "work",
		Stages: []Stage{
			{ID: "work", Name: "В работе", Action: ActionAgent, Crew: []string{"Claude"}},
			{ID: "review", Name: "На проверке", Action: ActionNone},
			{ID: "done", Name: "Готово", Action: ActionNone, Final: true},
			{ID: "blocked", Name: "Заблокировано", Action: ActionNone, Final: true},
		},
		Edges: []Edge{
			{From: "work", To: "review", On: TriggerSuccess},
			{From: "work", To: "blocked", On: TriggerFailure},
			{From: "review", To: "done", On: TriggerCardChanged, If: &Cond{Property: "Одобрено", Value: "Да"}},
			{From: "review", To: "work", On: TriggerCardChanged, If: &Cond{Property: "Одобрено", Value: "Нет"}},
		},
	}
}

func TestNextFollowsTheUnconditionalEdge(t *testing.T) {
	next, cond, ok := devFlow().Next("work", TriggerSuccess, nil, "")
	if !ok || next.ID != "review" {
		t.Fatalf("ожидалась стадия review, получено %q (ok=%v)", next.ID, ok)
	}
	if cond != nil {
		t.Fatalf("у безусловного перехода не должно быть условия, получено %v", cond)
	}
}

func TestNextPicksTheEdgeWhoseConditionHolds(t *testing.T) {
	f := devFlow()
	for _, tc := range []struct{ answer, want string }{
		{"Да", "done"},
		{"Нет", "work"},
	} {
		next, cond, ok := f.Next("review", TriggerCardChanged, map[string]string{"Одобрено": tc.answer}, "")
		if !ok || next.ID != tc.want {
			t.Fatalf("ответ %q: ожидалась стадия %q, получено %q (ok=%v)", tc.answer, tc.want, next.ID, ok)
		}
		if cond == nil {
			t.Fatalf("ответ %q: ожидалось условие на ребре", tc.answer)
		}
	}
}

// No condition held and there is no fallback: the flow decided nothing, and
// saying so is the point — a card that silently stalls is the bug this avoids.
func TestNextReportsWhenNoConditionHolds(t *testing.T) {
	_, _, ok := devFlow().Next("review", TriggerCardChanged, map[string]string{"Одобрено": "Может быть"}, "")
	if ok {
		t.Fatal("переход не должен был найтись")
	}
}

// An edge with no condition is the fallback however the editor ordered it, so
// the conditional one still wins when it holds.
func TestUnconditionalEdgeIsTheFallbackWhateverTheOrder(t *testing.T) {
	f := Flow{
		ID: "f", Name: "f", EntryStage: "a",
		Stages: []Stage{{ID: "a", Name: "A"}, {ID: "urgent", Name: "U"}, {ID: "normal", Name: "N"}},
		Edges: []Edge{
			{From: "a", To: "normal", On: TriggerSuccess},
			{From: "a", To: "urgent", On: TriggerSuccess, If: &Cond{Property: "Приоритет", Value: "срочно"}},
		},
	}
	next, _, _ := f.Next("a", TriggerSuccess, map[string]string{"Приоритет": "срочно"}, "")
	if next.ID != "urgent" {
		t.Fatalf("условное ребро должно выигрывать у запасного, получено %q", next.ID)
	}
	next, _, _ = f.Next("a", TriggerSuccess, map[string]string{"Приоритет": "обычный"}, "")
	if next.ID != "normal" {
		t.Fatalf("ожидался запасной переход, получено %q", next.ID)
	}
}

func TestCondOnAgentWords(t *testing.T) {
	c := &Cond{CommentContains: "ГОТОВО К ДЕПЛОЮ"}
	if !c.Holds(nil, "Всё сделал. готово к деплою") {
		t.Fatal("условие про слова агента должно игнорировать регистр")
	}
	if c.Holds(nil, "не получилось") {
		t.Fatal("условие не должно выполняться")
	}
}

func TestPropertyLookupIgnoresCase(t *testing.T) {
	c := &Cond{Property: "одобрено", Value: "да"}
	if !c.Holds(map[string]string{"Одобрено": "Да"}, "") {
		t.Fatal("свойства должны сопоставляться без учёта регистра")
	}
}

func TestWatchesPropertyOnlyForItsOwn(t *testing.T) {
	f := devFlow()
	if !f.WatchesProperty("review", "Одобрено") {
		t.Fatal("стадия ждёт «Одобрено»")
	}
	// Setting an unrelated property must not wake the stage — and must not
	// leave a "nothing matched" trace either.
	if f.WatchesProperty("review", "Приоритет") {
		t.Fatal("стадия не должна реагировать на постороннее свойство")
	}
}

// A parked card must answer "what are you waiting for" precisely, conditions
// included — and must not claim to be waiting for its own session.
func TestWaitsSpellOutConditions(t *testing.T) {
	got := devFlow().Waits("review")
	if len(got) != 2 {
		t.Fatalf("ожидалось два ожидания, получено %v", got)
	}
	if got[0].If == nil || got[0].If.Property != "Одобрено" || got[0].If.Value != "Да" {
		t.Fatalf("ожидание должно называть свойство и значение, получено %+v", got[0])
	}
	if len(devFlow().Waits("work")) != 0 {
		t.Fatal("исходы собственного шага — не ожидание")
	}
}

// ---- validation ----

func agents() []Agent {
	return []Agent{{Name: "Claude", Kind: KindClaude}, {Name: "Codex", Kind: KindCodex}}
}

func TestValidateFlowAcceptsAGoodOne(t *testing.T) {
	f, err := ValidateFlow(devFlow(), agents())
	if err != nil {
		t.Fatalf("флоу должен быть принят: %v", err)
	}
	if len(f.Stages) != 4 || len(f.Edges) != 4 {
		t.Fatalf("граф не должен меняться при проверке: %d стадий, %d рёбер", len(f.Stages), len(f.Edges))
	}
}

func TestValidateFlowRefusals(t *testing.T) {
	for name, mutate := range map[string]func(*Flow){
		"без имени":                  func(f *Flow) { f.Name = "  " },
		"без стадий":                 func(f *Flow) { f.Stages = nil },
		"без входной стадии":         func(f *Flow) { f.EntryStage = "" },
		"входная стадия отсутствует": func(f *Flow) { f.EntryStage = "нет-такой" },
		"две стадии с одним именем":  func(f *Flow) { f.Stages[1].Name = "в работе" },
		"неизвестное действие":       func(f *Flow) { f.Stages[0].Action = "деплой" },
		"неизвестный режим работы":   func(f *Flow) { f.Stages[0].Work = "по переписке" },
		"режим работы там, где нечему работать": func(f *Flow) {
			f.Stages[2].Work = WorkTerminal
		},
		"переход в никуда":             func(f *Flow) { f.Edges[0].To = "нет-такой" },
		"неизвестное событие":          func(f *Flow) { f.Edges[0].On = "полнолуние" },
		"агент не в реестре":           func(f *Flow) { f.Stages[0].Crew = []string{"Никто"} },
		"card.changed без условия":     func(f *Flow) { f.Edges[2].If = nil },
		"финальная стадия что-то дела": func(f *Flow) { f.Stages[2].Action = ActionAgent },
		"переход из финальной стадии":  func(f *Flow) { f.Edges = append(f.Edges, Edge{From: "done", To: "work", On: TriggerSuccess}) },
		"два безусловных перехода": func(f *Flow) {
			f.Edges = append(f.Edges, Edge{From: "work", To: "done", On: TriggerSuccess})
		},
		"условие про слова агента на человеческом событии": func(f *Flow) {
			f.Edges[2].If = &Cond{CommentContains: "да"}
		},
		"условие и про свойство, и про слова": func(f *Flow) {
			f.Edges[0].If = &Cond{Property: "П", Value: "З", CommentContains: "да"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := devFlow()
			mutate(&f)
			if _, err := ValidateFlow(f, agents()); err == nil {
				t.Fatal("флоу должен был быть отвергнут")
			}
		})
	}
}

// A stage that runs an agent must be able to find one. With several registered
// and no crew there is nothing to choose from, and finding that out at run time
// costs a card that silently never starts.
// A stage that named no mode is one somebody meant to watch: the default is the
// terminal, because it is the mode that shows more, and a flow written before
// the field existed is a flow whose author expected to see the work.
func TestAgentStageDefaultsToTheTerminal(t *testing.T) {
	f := devFlow()
	f.Stages[0].Work = ""
	got, err := ValidateFlow(f, agents())
	if err != nil {
		t.Fatalf("не принят: %v", err)
	}
	if got.Stages[0].Work != WorkTerminal {
		t.Fatalf("умолчание — терминал, а не %q", got.Stages[0].Work)
	}
	// A stage that says «сессией» keeps it: the default fills a silence, it does
	// not overrule an answer.
	f.Stages[0].Work = WorkSession
	got, err = ValidateFlow(f, agents())
	if err != nil {
		t.Fatalf("не принят: %v", err)
	}
	if got.Stages[0].Work != WorkSession {
		t.Fatalf("выбранный режим должен сохраняться: %q", got.Stages[0].Work)
	}
}

func TestValidateFlowRefusesAgentStageWithNothingToChooseFrom(t *testing.T) {
	f := devFlow()
	f.Stages[0].Crew = nil
	if _, err := ValidateFlow(f, agents()); err == nil {
		t.Fatal("стадия без состава при нескольких агентах должна быть отвергнута")
	}
	// One registered agent is an unambiguous answer, so the same flow is fine.
	if _, err := ValidateFlow(f, agents()[:1]); err != nil {
		t.Fatalf("с единственным агентом состав не нужен: %v", err)
	}
}

func TestValidateFlowNormalizesCrewToRegistrySpelling(t *testing.T) {
	f := devFlow()
	f.Stages[0].Crew = []string{"claude", "  Claude  "}
	got, err := ValidateFlow(f, agents())
	if err != nil {
		t.Fatalf("не принят: %v", err)
	}
	if len(got.Stages[0].Crew) != 1 || got.Stages[0].Crew[0] != "Claude" {
		t.Fatalf("состав должен быть приведён к написанию реестра и без повторов, получено %v", got.Stages[0].Crew)
	}
}

// ---- picking an agent ----

func TestPickAgentPrefersTheAssigneeInsideTheCrew(t *testing.T) {
	card := Card{Assignee: "codex"}
	got, err := PickAgent(card, []string{"Claude", "Codex"}, agents(), nil)
	if err != nil {
		t.Fatalf("агент должен быть выбран: %v", err)
	}
	if got.Name != "Codex" {
		t.Fatalf("ожидался Codex, получено %q", got.Name)
	}
}

func TestPickAgentSkipsBusyCrewMembers(t *testing.T) {
	busy := map[string]bool{Username("Claude"): true}
	got, err := PickAgent(Card{}, []string{"Claude", "Codex"}, agents(), busy)
	if err != nil {
		t.Fatalf("должен быть выбран свободный: %v", err)
	}
	if got.Name != "Codex" {
		t.Fatalf("ожидался Codex, получено %q", got.Name)
	}
}

func TestPickAgentParksTheCardWhenTheWholeCrewIsBusy(t *testing.T) {
	busy := map[string]bool{Username("Claude"): true, Username("Codex"): true}
	_, err := PickAgent(Card{}, []string{"Claude", "Codex"}, agents(), busy)
	if !errors.Is(err, ErrCrewBusy) {
		t.Fatalf("ожидалось ErrCrewBusy, получено %v", err)
	}
}

func TestPickAgentRefusesACardAPersonTook(t *testing.T) {
	var taken TakenByHumanError
	_, err := PickAgent(Card{Assignee: "Артём"}, []string{"Claude"}, agents(), nil)
	if !errors.As(err, &taken) || taken.Who != "Артём" {
		t.Fatalf("карточку взял человек — агент не должен стартовать, получено %v", err)
	}
}

// An assignee that is a registered agent means the opposite of a person taking
// the card: that agent runs it.
func TestPickAgentTreatsAnAgentAssigneeAsAChoiceNotAsATakenCard(t *testing.T) {
	got, err := PickAgent(Card{Assignee: "Claude"}, nil, agents(), nil)
	if err != nil {
		t.Fatalf("агент-исполнитель должен работать карточку: %v", err)
	}
	if got.Name != "Claude" {
		t.Fatalf("ожидался Claude, получено %q", got.Name)
	}
}

func TestPickAgentFallsBackToTheOnlyRegisteredAgent(t *testing.T) {
	got, err := PickAgent(Card{}, nil, agents()[:1], nil)
	if err != nil {
		t.Fatalf("единственный агент — однозначный ответ: %v", err)
	}
	if got.Name != "Claude" {
		t.Fatalf("получено %q", got.Name)
	}
}

func TestPickAgentRefusesToGuessAmongSeveral(t *testing.T) {
	if _, err := PickAgent(Card{}, nil, agents(), nil); err == nil {
		t.Fatal("без состава и без исполнителя выбирать не из чего")
	}
}
