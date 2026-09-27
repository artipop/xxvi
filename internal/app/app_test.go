package app

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/artipop/xxvi/internal/gitdiff"
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
			t.Fatalf("ни один пример не использует событие «%s»", tr.Kind)
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
	if err := first.Store.DeleteFlow(mustFlow(t, first, "Development").ID); err != nil {
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

// A first run brings no sources: the only ones there were are demos, and an
// inbox filled by a demo is an inbox of tasks nobody set.
func TestFirstRunHasNoSources(t *testing.T) {
	a := open(t)
	sources, _ := a.Store.Sources()
	if len(sources) != 0 {
		t.Fatalf("источников на первом запуске нет: %+v", sources)
	}
	cards, _ := a.Store.CardsInState(model.StateInbox)
	if len(cards) != 0 {
		t.Fatalf("входящие на первом запуске пусты, а карточек %d", len(cards))
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
		if f.Name == "Development" {
			dev = f
		}
	}
	if dev.ID == "" {
		t.Fatal("пример «Development» не создан")
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
	// The stage runs an agent in a terminal, so the strip carries that terminal
	// as well as the plan the flow declares — the undeclared screen first,
	// because what the step did reads before what it was told to show.
	screens := view.Segments[0].Screens
	if len(screens) != 2 || screens[0].Kind != "agentTerminal" || screens[0].SessionID == "" {
		t.Fatalf("терминал агента — первый экран его сегмента: %+v", screens)
	}
	notes := screens[1]
	if notes.Kind != model.ScreenNotes || notes.Ref != "plan.md" {
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
	if err := api.WriteDoc(card.ID, "plan.md", "# план"); err != nil {
		t.Fatalf("записать заметки: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "plan.md")); err != nil {
		t.Fatalf("заметки должны лечь в папку проекта: %v", err)
	}

	// And without a project the card keeps a folder of its own, which is the
	// right answer for work that starts from a blank page.
	other, _ := api.AddCard("", "С чистого листа", "")
	if err := api.WriteDoc(other.ID, "plan.md", "# план"); err != nil {
		t.Fatalf("записать заметки: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "plan.md")); err != nil {
		t.Fatal("папка проекта не должна была измениться")
	}
	if _, err := os.Stat(filepath.Join(a.DataDir, "work", other.ID, "plan.md")); err != nil {
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

	if err := api.WriteDoc(card.ID, "plan.md", "# план"); err == nil {
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

// Without a window there is no dialog to open, and that is said rather than
// crashed: everything else in the application works headless, and this is the
// one thing that cannot.
func TestPickingAFolderNeedsAWindow(t *testing.T) {
	api := NewAPI(open(t))
	if _, err := api.PickFolder("", ""); err == nil {
		t.Fatal("без окна выбор папки невозможен и должен об этом сказать")
	}
}

// What the dialog hands back is what the registry gets, and a person who closed
// it without choosing keeps what they had.
func TestAPickedFolderBecomesTheProjectPath(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	folder := t.TempDir()
	a.SetChooser(fakeChooser{folder: folder})

	got, err := api.PickFolder("", "")
	if err != nil || got != folder {
		t.Fatalf("выбранная папка должна вернуться как есть: %q (%v)", got, err)
	}
	if _, err := api.SaveProject(model.Project{Name: "Сайт", Path: got}); err != nil {
		t.Fatalf("выбранная папка должна сохраняться без правки: %v", err)
	}

	a.SetChooser(fakeChooser{})
	if got, err := api.PickFolder("", ""); err != nil || got != "" {
		t.Fatalf("закрытый диалог — это пусто, а не ошибка: %q (%v)", got, err)
	}
}

type fakeChooser struct{ folder string }

func (f fakeChooser) Folder(string, string) (string, error) { return f.folder, nil }

// The diff screen reads the card's own working folder, which is its project's
// folder — the same one the agent works in and the terminal opens. That is the
// whole point of it: what a review looks at is what the agent did, in the place
// it did it.
func TestTheDiffScreenReadsTheCardsProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не установлен")
	}
	a := open(t)
	api := NewAPI(a)
	folder := t.TempDir()

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = folder
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "--initial-branch=main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Тест")
	if err := os.WriteFile(filepath.Join(folder, "форма.txt"), []byte("было\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-m", "начало")

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

	// Nothing has been done yet, and that is an answer rather than a failure.
	diff, err := api.Diff(card.ID, "")
	if err != nil {
		t.Fatalf("дифф: %v", err)
	}
	if len(diff.Files) != 0 {
		t.Fatalf("в нетронутой копии менять нечего: %+v", diff.Files)
	}

	// What the agent would have written, written the way the agent writes it:
	// into the card's working folder.
	if err := api.WriteDoc(card.ID, "форма.txt", "стало\n"); err != nil {
		t.Fatalf("правка: %v", err)
	}
	if err := api.WriteDoc(card.ID, "plan.md", "# план\n"); err != nil {
		t.Fatalf("новый файл: %v", err)
	}

	diff, err = api.Diff(card.ID, "")
	if err != nil {
		t.Fatalf("дифф: %v", err)
	}
	if len(diff.Files) != 2 {
		t.Fatalf("изменённый файл и новый — оба в ревью: %+v", diff.Files)
	}
	seen := map[string]string{}
	for _, f := range diff.Files {
		seen[f.Path] = f.Status
	}
	if seen["форма.txt"] != gitdiff.StatusModified || seen["plan.md"] != gitdiff.StatusAdded {
		t.Fatalf("дифф должен назвать, что с каждым файлом: %+v", seen)
	}
}

// A card working where there is no repository gets a sentence rather than a
// broken screen: not every card's project is under git, and that is ordinary.
func TestTheDiffScreenSaysWhenThereIsNoRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не установлен")
	}
	a := open(t)
	api := NewAPI(a)

	card, err := api.AddCard("", "Починить форму", "")
	if err != nil {
		t.Fatalf("карточка: %v", err)
	}
	if _, err := api.Diff(card.ID, ""); !errors.Is(err, gitdiff.ErrNoRepo) {
		t.Fatalf("ошибка должна говорить, чего нет: %v", err)
	}
}

// A task typed into the ribbon goes straight to work: one call, and the card is
// on its flow with the agent and the place it was given.
func TestStartTaskGoesStraightToWork(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	dev := mustFlow(t, a, "Development")
	proj, err := api.SaveProject(model.Project{Name: "Тут", Kind: model.ProjectFolder, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("проект: %v", err)
	}

	view, err := api.StartTask("Починить форму входа\n\nПадает на пустом пароле.", proj.ID, "", "Claude", dev.ID)
	if err != nil {
		t.Fatalf("начать задачу: %v", err)
	}
	c := view.Card
	if c.State != model.StateFlow || c.Title != "Починить форму входа" || c.Project != proj.ID || c.Assignee != "Claude" {
		t.Fatalf("карточка сразу в работе, с проектом и агентом: %+v", c)
	}
	if c.Body != "Починить форму входа\n\nПадает на пустом пароле." {
		t.Fatalf("агенту уходит весь текст: %q", c.Body)
	}

	// A refusal comes before anything exists.
	if _, err := api.StartTask("Ещё одна", "нет-такого", "", "Claude", dev.ID); err == nil {
		t.Fatal("несуществующий проект — отказ")
	}
	if _, err := api.StartTask("   ", "", "", "Claude", dev.ID); err == nil {
		t.Fatal("пустая задача — отказ")
	}
	if cards, _ := a.Store.CardsInState(model.StateInbox); len(cards) != 0 {
		t.Fatalf("отказ не оставляет карточек во входящих: %+v", cards)
	}
}

// A card started from a conversation carries its id, and needs no text: the
// conversation is the task, and it names the card.
func TestContinueSessionStartsACardFromAConversation(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	dev := mustFlow(t, a, "Development")
	proj, err := api.SaveProject(model.Project{Name: "Тут", Kind: model.ProjectFolder, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("проект: %v", err)
	}

	view, err := api.ContinueSession("abc-123", "Форма входа падает", "", proj.ID, "", "Claude", dev.ID)
	if err != nil {
		t.Fatalf("продолжить разговор: %v", err)
	}
	c := view.Card
	if c.State != model.StateFlow || c.Session != "abc-123" || c.Title != "Форма входа падает" || c.Assignee != "Claude" {
		t.Fatalf("карточка в работе, с разговором и его названием: %+v", c)
	}

	if _, err := api.ContinueSession("", "", "", proj.ID, "", "Claude", dev.ID); err == nil {
		t.Fatal("без выбранного разговора — отказ")
	}
	if _, err := api.ContinueSession("abc-123", "", "", proj.ID, "", "нет-такого", dev.ID); err == nil {
		t.Fatal("разговор открывает только агент, который его вёл, — и он должен существовать")
	}
}

// A branch of its own is asked of a repository and answered before the work
// starts: a folder with no git cannot have it, and a card whose branch exists
// keeps it.
func TestWorkModeIsARepositoryQuestionAnsweredOnce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не установлен")
	}
	a := open(t)
	api := NewAPI(a)
	plain, err := api.SaveProject(model.Project{Name: "Заметки", Kind: model.ProjectFolder, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("проект: %v", err)
	}
	repoDir := t.TempDir()
	if out, err := exec.Command("git", "-C", repoDir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	repo, err := api.SaveProject(model.Project{Name: "Код", Kind: model.ProjectFolder, Path: repoDir})
	if err != nil {
		t.Fatalf("проект: %v", err)
	}
	list, _ := api.Projects()
	for _, p := range list {
		if p.Repo != (p.ID == repo.ID) {
			t.Fatalf("репозиторий — только «Код»: %+v", list)
		}
	}

	card, _ := api.AddCard("", "Задача", "")
	if _, err := api.SetCardWorkMode(card.ID, model.WorkModeWorktree); err == nil {
		t.Fatal("без проекта своей ветки не бывает")
	}
	if _, err := api.SetCardProject(card.ID, plain.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SetCardWorkMode(card.ID, model.WorkModeBranch); err == nil {
		t.Fatal("папка без git — отказ")
	}
	if _, err := api.SetCardProject(card.ID, repo.ID); err != nil {
		t.Fatal(err)
	}
	if view, err := api.SetCardWorkMode(card.ID, model.WorkModeWorktree); err != nil || view.Card.WorkMode != model.WorkModeWorktree {
		t.Fatalf("репозиторию можно: %+v, %v", view.Card, err)
	}
	// Moved to a folder with no git, the card works in it as it stands.
	if view, _ := api.SetCardProject(card.ID, plain.ID); view.Card.WorkMode != model.WorkModeFolder {
		t.Fatalf("режим сбрасывается: %+v", view.Card)
	}

	// Once the branch is made, neither the mode nor the project moves.
	if _, err := api.SetCardProject(card.ID, repo.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetCardWorkspace(card.ID, "zadacha-1", "main", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SetCardWorkMode(card.ID, model.WorkModeBranch); err == nil {
		t.Fatal("ветка уже есть — режим не меняется")
	}
	if _, err := api.SetCardProject(card.ID, plain.ID); err == nil {
		t.Fatal("ветка уже есть — проект не меняется")
	}

	if _, err := api.StartTask("Ещё", plain.ID, model.WorkModeWorktree, "Claude", mustFlow(t, a, "Development").ID); err == nil {
		t.Fatal("задача с деревом в папке без git — отказ до создания")
	}
}
