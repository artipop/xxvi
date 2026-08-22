package app

import (
	"io"
	"log/slog"
	"testing"

	"github.com/artipop/xxvi/internal/model"
)

func open(t *testing.T) *App {
	t.Helper()
	a, err := Open(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("открыть приложение: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// The examples are the first thing anybody sees, so they have to be valid by
// the same rules a hand-written flow is checked against — SaveFlow validates,
// so a broken example would fail here rather than in front of a person.
func TestFirstRunCreatesUsableExamples(t *testing.T) {
	a := open(t)

	flows, err := a.Store.Flows()
	if err != nil {
		t.Fatalf("флоу: %v", err)
	}
	if len(flows) != len(SeedFlows()) {
		t.Fatalf("ожидалось %d флоу, создано %d", len(SeedFlows()), len(flows))
	}
	for _, f := range flows {
		if _, ok := f.Entry(); !ok {
			t.Fatalf("у флоу «%s» нет входной стадии", f.Name)
		}
	}

	agents, _ := a.Store.Agents()
	if len(agents) != 1 {
		t.Fatalf("ожидался один зарегистрированный агент, получено %d", len(agents))
	}
	sources, _ := a.Store.Sources()
	if len(sources) != 2 {
		t.Fatalf("ожидалось два источника, получено %d", len(sources))
	}
}

// Between them the examples have to use every trigger, or they are not examples
// of this system — they are examples of half of it.
func TestExamplesUseEveryTrigger(t *testing.T) {
	used := map[string]bool{}
	for _, f := range SeedFlows() {
		for _, e := range f.Edges {
			used[e.On] = true
		}
	}
	for _, tr := range model.Triggers {
		if !used[tr.Kind] {
			t.Fatalf("ни один пример не использует событие «%s»", tr.Label)
		}
	}
}

// Seeding what somebody has already edited would be an application overruling
// its user.
func TestSeedingHappensOnceOnly(t *testing.T) {
	dir := t.TempDir()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	first, err := Open(dir, quiet)
	if err != nil {
		t.Fatalf("первый запуск: %v", err)
	}
	if err := first.Store.DeleteFlow(mustFlow(t, first, "Разработка").ID); err != nil {
		t.Fatalf("удалить флоу: %v", err)
	}
	first.Close()

	second, err := Open(dir, quiet)
	if err != nil {
		t.Fatalf("второй запуск: %v", err)
	}
	defer second.Close()

	flows, _ := second.Store.Flows()
	if len(flows) != len(SeedFlows())-1 {
		t.Fatalf("удалённый пример не должен возвращаться: флоу %d", len(flows))
	}
}

// The application opens showing what it is rather than an empty screen: the
// seeded items are read on the first poll and land in the inbox, grouped by
// what brought them.
func TestSeededItemsReachTheInbox(t *testing.T) {
	a := open(t)
	sources, _ := a.Store.Sources()
	for _, src := range sources {
		if err := a.Poller.Poll(src); err != nil {
			t.Fatalf("прочитать источник «%s»: %v", src.Name, err)
		}
	}

	groups, err := NewAPI(a).Inbox()
	if err != nil {
		t.Fatalf("входящие: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("ожидались две группы по источникам, получено %d", len(groups))
	}
	total := 0
	for _, g := range groups {
		total += len(g.Cards)
	}
	// Five items are seeded and one of them matches no rule on the noisy
	// source, which is exactly what «шумный» is there to demonstrate.
	if total != 4 {
		t.Fatalf("ожидалось четыре карточки, получено %d", total)
	}
}

// A rule's suggestion prefills the «В работу» dialog, so it has to name a flow
// that actually exists — a suggestion pointing nowhere is worse than none.
func TestSuggestedFlowsExist(t *testing.T) {
	a := open(t)
	sources, _ := a.Store.Sources()
	for _, src := range sources {
		for _, rule := range src.Rules {
			if rule.SuggestFlow == "" {
				continue
			}
			if _, err := a.Store.FlowByName(rule.SuggestFlow); err != nil {
				t.Fatalf("правило «%s» источника «%s» предлагает несуществующий флоу «%s»",
					rule.Name, src.Name, rule.SuggestFlow)
			}
		}
	}
}

// Reading the same file twice must not double the inbox — the demo source is
// polled on a timer, so this is the ordinary case, not an edge one.
func TestPollingTwiceBringsNothingNew(t *testing.T) {
	a := open(t)
	sources, _ := a.Store.Sources()
	for i := 0; i < 2; i++ {
		for _, src := range sources {
			if err := a.Poller.Poll(src); err != nil {
				t.Fatalf("прочитать: %v", err)
			}
		}
	}
	cards, _ := a.Store.CardsInState(model.StateInbox)
	if len(cards) != 4 {
		t.Fatalf("повторное чтение не должно ничего добавлять, карточек %d", len(cards))
	}
}

func mustFlow(t *testing.T, a *App, name string) model.Flow {
	t.Helper()
	f, err := a.Store.FlowByName(name)
	if err != nil {
		t.Fatalf("флоу «%s»: %v", name, err)
	}
	return f
}

// The ribbon end to end, through the same facade the UI calls: a seeded card
// taken into work opens a strip whose first segment carries the screens the
// example flow declares, and the notes screen writes into the card's own
// working folder — the one the agent is confined to.
func TestRibbonOpensOnTheScreensTheExampleDeclares(t *testing.T) {
	a := open(t)
	api := NewAPI(a)

	flows, _ := a.Store.Flows()
	var dev model.Flow
	for _, f := range flows {
		if f.Name == "Разработка" {
			dev = f
		}
	}
	if dev.ID == "" {
		t.Fatal("пример «Разработка» не создан")
	}

	card, err := api.AddCard("", "Починить форму входа", "")
	if err != nil {
		t.Fatalf("карточка: %v", err)
	}
	if _, err := api.TakeIntoWork(card.ID, dev.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}

	rows, err := api.Ribbons()
	if err != nil || len(rows) != 1 || rows[0].CardID != card.ID {
		t.Fatalf("карточка в работе — это одна лента: %+v (%v)", rows, err)
	}

	view, err := api.Ribbon(card.ID)
	if err != nil {
		t.Fatalf("лента: %v", err)
	}
	if len(view.Segments) != 1 || !view.Segments[0].Current {
		t.Fatalf("лента начинается первым же шагом: %+v", view.Segments)
	}
	// The stage runs an agent, so the strip carries its stream as well as the
	// plan the flow declares — the undeclared screen first, because what the
	// step did reads before what it was told to show.
	screens := view.Segments[0].Screens
	if len(screens) != 2 || screens[0].Kind != "agent" || screens[0].SessionID == "" {
		t.Fatalf("ход агента — первый экран его сегмента: %+v", screens)
	}
	notes := screens[1]
	if notes.Kind != model.ScreenNotes || notes.Ref != "план.md" {
		t.Fatalf("первый шаг показывает план, который ведёт агент: %+v", notes)
	}
	if view.FocusID != screens[0].ID {
		t.Fatalf("фокус — первый экран текущего сегмента: %q", view.FocusID)
	}

	// The notes screen and the agent share one folder, so what is written here
	// is what an agent would read there.
	if err := api.WriteDoc(card.ID, notes.Ref, "# План\n1. Найти форму\n"); err != nil {
		t.Fatalf("записать заметки: %v", err)
	}
	back, err := api.ReadDoc(card.ID, notes.Ref)
	if err != nil || back != "# План\n1. Найти форму\n" {
		t.Fatalf("заметки должны прочитаться обратно: %q (%v)", back, err)
	}

	// A file nobody has written yet is empty rather than a failure: a stage may
	// declare notes the agent has not got to.
	if text, err := api.ReadDoc(card.ID, "нет-такого.md"); err != nil || text != "" {
		t.Fatalf("несуществующие заметки — пустая страница, а не ошибка: %q (%v)", text, err)
	}
	// And the folder is a boundary at the moment of opening, not only in the editor.
	if _, err := api.ReadDoc(card.ID, "../../секрет"); err == nil {
		t.Fatal("путь за пределы папки карточки должен быть отвергнут")
	}
}
