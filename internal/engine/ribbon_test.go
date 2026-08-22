package engine

import (
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/store"
)

// screenFlow is dataFlow with screens on it: the agent stage keeps a plan, the
// check declares the preview it writes and opens it, and the waiting stage that
// nothing runs opens the same preview — which is the whole reason a stage with
// no action may declare screens (docs/system.md §12.4).
func screenFlow() model.Flow {
	f := dataFlow()
	f.Stages[0].Screens = []model.Screen{{Kind: model.ScreenNotes, Title: "План", Ref: "план.md"}}
	f.Stages[1].Screens = []model.Screen{{Kind: model.ScreenBrowser, Ref: "{Превью}"}}
	return f
}

func ribbonOf(t *testing.T, f fixture, cardID string) RibbonView {
	t.Helper()
	view, err := f.engine.Ribbon(cardID)
	if err != nil {
		t.Fatalf("лента: %v", err)
	}
	return view
}

// The ribbon starts the moment the card does: taking it into work is a
// transition like any other, and the journal records it, so there is no first
// step missing from the strip.
func TestTakingACardIntoWorkOpensTheRibbon(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Починить форму")

	if view := ribbonOf(t, f, card.ID); len(view.Segments) != 0 {
		t.Fatalf("у карточки во входящих ленты ещё нет: %+v", view.Segments)
	}

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}

	view := ribbonOf(t, f, card.ID)
	if len(view.Segments) != 1 {
		t.Fatalf("лента должна начаться первым же шагом: %+v", view.Segments)
	}
	seg := view.Segments[0]
	if seg.StageID != "work" || !seg.Current {
		t.Fatalf("первый сегмент — та стадия, на которой карточка стоит: %+v", seg)
	}
	if len(seg.Screens) != 1 || seg.Screens[0].Kind != model.ScreenNotes {
		t.Fatalf("сегмент несёт экраны своей стадии: %+v", seg.Screens)
	}
	if view.FocusID != seg.Screens[0].ID {
		t.Fatalf("фокус — первый экран текущего сегмента: %q", view.FocusID)
	}
	if view.FlowName != "С проверкой" {
		t.Fatalf("лента должна знать, по какому флоу едет: %q", view.FlowName)
	}
}

// Every finished step writes a segment on the right. That is the whole of how a
// screen gets added: nothing assembles the strip by hand, so it cannot disagree
// with where the card actually is.
func TestEachFinishedStepAddsASegment(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Починить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "")

	view := ribbonOf(t, f, card.ID)
	if len(view.Segments) != 2 {
		t.Fatalf("шаг закончился — сегмент дописался: %+v", view.Segments)
	}
	if view.Segments[0].Current {
		t.Fatal("текущий сегмент только один, и он последний")
	}
	last := view.Segments[1]
	if last.StageID != "qa" || !last.Current {
		t.Fatalf("карточка приехала на проверку: %+v", last)
	}
	if last.On != model.TriggerSuccess {
		t.Fatalf("сегмент помнит, по какому событию въехали: %q", last.On)
	}
}

// A stage entered twice gets two segments rather than one reused: the check
// that sent the card back and the check after the fix are two steps with two
// results, and one screen for both would erase the first.
func TestASecondVisitIsASecondSegment(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Починить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "")              // work → qa
	f.runner.finish(card.ID, model.TriggerSuccess, "Вердикт: fail") // qa → work, по петле
	f.runner.finish(card.ID, model.TriggerSuccess, "")              // work → qa снова

	view := ribbonOf(t, f, card.ID)
	if len(view.Segments) != 4 {
		t.Fatalf("четыре въезда — четыре сегмента: %+v", view.Segments)
	}
	first, second := view.Segments[1], view.Segments[3]
	if first.StageID != "qa" || second.StageID != "qa" {
		t.Fatalf("оба сегмента — проверка: %+v / %+v", first, second)
	}
	if first.EventID == second.EventID {
		t.Fatal("два визита на одну стадию — два разных сегмента")
	}
	if len(first.Screens) == 0 || len(second.Screens) == 0 {
		t.Fatalf("у каждого визита свои экраны: %+v / %+v", first.Screens, second.Screens)
	}
	if first.Screens[0].ID == second.Screens[0].ID {
		t.Fatalf("экраны второго визита — не те же самые: %q", first.Screens[0].ID)
	}
	if !second.Current || first.Current {
		t.Fatal("текущий — второй визит")
	}
}

// The ids are derived from the journal, not generated, so two reads of an
// unchanged ribbon give the same ones. The UI keys its panes by them: a pane
// that loses its identity is an iframe that reloads and a cursor that jumps out
// of the notes.
func TestScreenIDsAreStableAcrossReads(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Починить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "")

	first, second := ribbonOf(t, f, card.ID), ribbonOf(t, f, card.ID)
	if len(first.Segments) != len(second.Segments) {
		t.Fatalf("два чтения одной ленты разошлись: %d и %d", len(first.Segments), len(second.Segments))
	}
	for i := range first.Segments {
		a, b := first.Segments[i].Screens, second.Segments[i].Screens
		if len(a) != len(b) {
			t.Fatalf("сегмент %d: разное число экранов", i)
		}
		for j := range a {
			if a[j].ID != b[j].ID {
				t.Fatalf("сегмент %d, экран %d: id поехал (%q → %q)", i, j, a[j].ID, b[j].ID)
			}
		}
	}
}

// A screen pointing at a property nobody has written yet stands blank and says
// which property it is waiting for; when the stage writes it, the same screen
// opens it.
func TestABrowserScreenWaitsForThePropertyItOpens(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Починить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "") // приехали на проверку

	view := ribbonOf(t, f, card.ID)
	browser := view.Segments[1].Screens[0]
	if len(browser.Waiting) != 1 || browser.Waiting[0] != "Превью" {
		t.Fatalf("экран должен сказать, какого свойства ждёт: %+v", browser)
	}

	// The check delivers the preview and fails the card back to the agent; the
	// address it wrote is on the card, so the screen of that visit shows it.
	f.runner.finish(card.ID, model.TriggerSuccess, "Превью: http://localhost:5173\nВердикт: fail")

	view = ribbonOf(t, f, card.ID)
	browser = view.Segments[1].Screens[0]
	if len(browser.Waiting) != 0 || browser.Ref != "http://localhost:5173" {
		t.Fatalf("свойство записано — экран его открывает: %+v", browser)
	}
}

// A stage removed from the flow does not take its part of the ribbon with it.
// What happened happened; the segment stays and says why it is empty.
func TestASegmentOfARemovedStageStays(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Починить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "") // work → qa

	// The agent stage goes, and with it the edges that touched it.
	trimmed := f.flow
	trimmed.Stages = trimmed.Stages[1:]
	trimmed.EntryStage = "qa"
	trimmed.Edges = []model.Edge{{From: "qa", To: "done", On: model.TriggerSuccess}}
	if _, err := f.store.SaveFlow(trimmed); err != nil {
		t.Fatalf("сохранить урезанный флоу: %v", err)
	}

	view := ribbonOf(t, f, card.ID)
	if len(view.Segments) != 2 {
		t.Fatalf("сегменты не теряются вместе со стадией: %+v", view.Segments)
	}
	if !view.Segments[0].Gone {
		t.Fatalf("сегмент исчезнувшей стадии должен сказать об этом: %+v", view.Segments[0])
	}
	if view.Segments[1].StageID != "qa" || !view.Segments[1].Current {
		t.Fatalf("карточка по-прежнему стоит там, где стояла: %+v", view.Segments[1])
	}
}

// An agent's run is a screen of the visit it started during — not of every
// visit to that stage, which is what matching by stage alone would give on a
// flow that loops.
func TestAnAgentRunBelongsToTheVisitItStartedIn(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Починить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	// A run recorded the way the manager records one, on the visit open now.
	if err := f.store.InsertSession(store.Session{
		ID: "s1", CardID: card.ID, FlowID: f.flow.ID, StageID: "work",
		AgentName: "Claude", AgentKind: model.KindClaude,
		Status: store.StatusRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("сессия: %v", err)
	}

	view := ribbonOf(t, f, card.ID)
	screens := view.Segments[0].Screens
	if len(screens) != 2 || screens[0].Kind != "agent" || screens[0].SessionID != "s1" {
		t.Fatalf("ход агента — первый экран его сегмента: %+v", screens)
	}
	// The agent's screen is not declared and comes before what is: what the
	// step did reads before what it was told to show.
	if screens[1].Kind != model.ScreenNotes {
		t.Fatalf("объявленные экраны идут после хода агента: %+v", screens)
	}

	f.runner.finish(card.ID, model.TriggerSuccess, "")
	view = ribbonOf(t, f, card.ID)
	if len(view.Segments[1].Screens) != 1 || view.Segments[1].Screens[0].Kind == "agent" {
		t.Fatalf("сессия предыдущего визита не должна попасть в следующий: %+v", view.Segments[1].Screens)
	}
}

// One card in work is one ribbon, and there is no other kind.
func TestRibbonsAreTheCardsInWork(t *testing.T) {
	f := setup(t, screenFlow())
	first := f.card(t, "Починить форму")
	second := f.card(t, "Обновить доки")

	if rows, err := f.engine.Ribbons(); err != nil || len(rows) != 0 {
		t.Fatalf("во входящих лент нет: %+v (%v)", rows, err)
	}

	if err := f.engine.TakeIntoWork(first.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	if err := f.engine.TakeIntoWork(second.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}

	rows, err := f.engine.Ribbons()
	if err != nil {
		t.Fatalf("ленты: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("две карточки в работе — две ленты: %+v", rows)
	}
	for _, row := range rows {
		if row.StageName != "В работе" || row.FlowName != "С проверкой" {
			t.Fatalf("лента говорит, где стоит её карточка: %+v", row)
		}
	}
}
