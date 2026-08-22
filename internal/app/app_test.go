package app

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// A task somebody thought of is not an item somebody sent: it belongs to no
// stream and has no rules to meet. Before there was a way to say that, the only
// road into the inbox went through a source's file and its rules — and on a
// noisy source, rules written about other people's mail would drop it without
// a word.
func TestAnOwnTaskGoesStraightIntoTheInbox(t *testing.T) {
	a := open(t)
	api := NewAPI(a)

	card, err := api.AddCard("", "Позвонить в банк", "До четверга")
	if err != nil {
		t.Fatalf("завести свою задачу: %v", err)
	}
	if card.Source != "" {
		t.Fatalf("своя задача не принадлежит источнику: %q", card.Source)
	}
	if card.State != model.StateInbox {
		t.Fatalf("своя задача ложится во входящие: %q", card.State)
	}

	groups, err := api.Inbox()
	if err != nil {
		t.Fatalf("входящие: %v", err)
	}
	var own []model.Card
	for _, g := range groups {
		if g.Source == "" {
			own = g.Cards
		}
	}
	if len(own) != 1 || own[0].ID != card.ID {
		t.Fatalf("своя задача должна быть в группе без источника: %+v", groups)
	}

	// And it travels like any other card: nothing about the road depends on
	// having been brought by somebody.
	flows, _ := a.Store.Flows()
	if _, err := api.TakeIntoWork(card.ID, flows[0].ID); err != nil {
		t.Fatalf("взять свою задачу в работу: %v", err)
	}
	view, err := api.Ribbon(card.ID)
	if err != nil || len(view.Segments) == 0 {
		t.Fatalf("у своей задачи такая же лента: %+v (%v)", view.Segments, err)
	}
}

// A title is the whole of what a card must have, and a card without one is a
// row nobody can find again.
func TestAnOwnTaskNeedsATitle(t *testing.T) {
	api := NewAPI(open(t))
	if _, err := api.AddCard("", "   ", "текст"); err == nil {
		t.Fatal("карточка без заголовка должна быть отвергнута")
	}
}

// A card's project is where its work happens: the agent, the terminal and the
// notes all open in that folder rather than in a scratch directory beside the
// database.
func TestACardWorksInItsProjectFolder(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	folder := t.TempDir()

	project, err := api.SaveProject(model.Project{Name: "Сайт", Path: folder})
	if err != nil {
		t.Fatalf("завести проект: %v", err)
	}
	card, err := api.AddCard("", "Починить форму", "")
	if err != nil {
		t.Fatalf("карточка: %v", err)
	}
	if _, err := api.SetCardProject(card.ID, project.ID); err != nil {
		t.Fatalf("назначить проект: %v", err)
	}

	// The notes screen writes through the same working folder the agent gets,
	// so this is the whole resolution tested at once.
	if err := api.WriteDoc(card.ID, "план.md", "# план"); err != nil {
		t.Fatalf("записать заметки: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "план.md")); err != nil {
		t.Fatalf("заметки должны лечь в папку проекта: %v", err)
	}

	// And without a project the card keeps a folder of its own, which is the
	// right answer for work that starts from a blank page.
	other, _ := api.AddCard("", "С чистого листа", "")
	if err := api.WriteDoc(other.ID, "план.md", "# план"); err != nil {
		t.Fatalf("записать заметки: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "план.md")); err != nil {
		t.Fatal("папка проекта не должна была измениться")
	}
	if _, err := os.Stat(filepath.Join(a.DataDir, "work", other.ID, "план.md")); err != nil {
		t.Fatalf("карточка без проекта работает в своей папке: %v", err)
	}
}

// A project a card names and nobody has is an error, not a reason to quietly
// open somewhere else: working in the wrong place is worse than not working.
func TestAMissingProjectFolderIsAnError(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	folder := filepath.Join(t.TempDir(), "проект")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	project, err := api.SaveProject(model.Project{Name: "Сайт", Path: folder})
	if err != nil {
		t.Fatalf("завести проект: %v", err)
	}
	card, _ := api.AddCard("", "Починить форму", "")
	if _, err := api.SetCardProject(card.ID, project.ID); err != nil {
		t.Fatalf("назначить проект: %v", err)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}

	if err := api.WriteDoc(card.ID, "план.md", "# план"); err == nil {
		t.Fatal("исчезнувшая папка проекта должна быть отказом, а не тихой подменой места")
	}
}

// A registry entry is refused for a path that is not there, where the person is
// still looking at what they typed.
func TestAProjectNeedsAFolderThatExists(t *testing.T) {
	api := NewAPI(open(t))
	if _, err := api.SaveProject(model.Project{Name: "Сайт", Path: "/нет/такой/папки"}); err == nil {
		t.Fatal("несуществующая папка должна быть отвергнута")
	}
	if _, err := api.SaveProject(model.Project{Name: "Сайт", Path: "относительный/путь"}); err == nil {
		t.Fatal("относительный путь должен быть отвергнут")
	}
}

// Deleting a project a card still names is refused: the card would start its
// next step somewhere else entirely.
func TestAProjectInUseIsNotDeleted(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	project, err := api.SaveProject(model.Project{Name: "Сайт", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("завести проект: %v", err)
	}
	card, _ := api.AddCard("", "Починить форму", "")
	if _, err := api.SetCardProject(card.ID, project.ID); err != nil {
		t.Fatalf("назначить проект: %v", err)
	}
	if err := api.DeleteProject(project.ID); err == nil {
		t.Fatal("занятый проект не должен удаляться")
	}

	// Freed, it goes.
	if _, err := api.SetCardProject(card.ID, ""); err != nil {
		t.Fatalf("снять проект: %v", err)
	}
	if err := api.DeleteProject(project.ID); err != nil {
		t.Fatalf("свободный проект должен удаляться: %v", err)
	}
}
