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

// reviewFlow is a route with a review on it: something is made, and then a
// stage where nothing runs shows what was made and waits to be answered.
func reviewFlow() model.Flow {
	return model.Flow{
		Name: "С ревью", EntryStage: "work",
		Stages: []model.Stage{
			{ID: "work", Name: "В работе", Action: model.ActionAgent, Crew: []string{"Claude"}},
			{
				ID: "review", Name: "Ревью", Action: model.ActionNone,
				Screens: []model.Screen{{Kind: model.ScreenDiff, Title: "Что изменилось"}},
			},
			{ID: "done", Name: "Готово", Final: true},
		},
		Edges: []model.Edge{
			{From: "work", To: "review", On: model.TriggerSuccess},
			{
				From: "review", To: "done", On: model.TriggerCardChanged,
				If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomePassed},
			},
			{
				From: "review", To: "work", On: model.TriggerCardChanged,
				If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomeFailed},
			},
		},
	}
}

// The strip is the whole window, so the answer a waiting stage is waiting for
// has to be available on it — beside the diff it is an answer about.
//
// Only the segment the card stands in carries them: a step already over is not
// asking anything, and two segments offering the same two buttons would be two
// ways to answer one question.
func TestOnlyTheSegmentACardStandsInCarriesTheAnswers(t *testing.T) {
	f := setup(t, reviewFlow())
	card := f.card(t, "Починить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")
	if got := f.stageOf(t, card.ID); got != "review" {
		t.Fatalf("карточка должна стоять на ревью, получено %q", got)
	}

	view := ribbonOf(t, f, card.ID)
	if len(view.Segments) != 2 {
		t.Fatalf("два шага — два сегмента: %+v", view.Segments)
	}
	if len(view.Segments[0].Marks) != 0 {
		t.Fatalf("законченный шаг ничего не спрашивает: %+v", view.Segments[0].Marks)
	}

	seg := view.Segments[1]
	if !seg.Current || len(seg.Screens) != 1 || seg.Screens[0].Kind != model.ScreenDiff {
		t.Fatalf("ревью показывает дифф: %+v", seg)
	}
	if len(seg.Marks) != 2 {
		t.Fatalf("у ревью два ответа: %+v", seg.Marks)
	}
	forward, back := seg.Marks[0], seg.Marks[1]
	if forward.Value != model.OutcomePassed || forward.Stage != "Готово" || !forward.Forward {
		t.Fatalf("«прошло» ведёт вперёд, в ту стадию, которую называет: %+v", forward)
	}
	if back.Value != model.OutcomeFailed || back.Stage != "В работе" || back.Forward {
		t.Fatalf("«не прошло» возвращает туда, откуда пришло: %+v", back)
	}

	// The answer is a person's edit of the card, and it moves the card — which
	// is what makes the buttons on the strip the same act as the ones on the
	// card screen rather than a second road to it.
	f.engine.CardChanged(card.ID, model.OutcomeProperty, model.OutcomePassed)
	last := ribbonOf(t, f, card.ID)
	if len(last.Segments) != 3 || last.Segments[2].StageID != "done" {
		t.Fatalf("отмеченное «прошло» должно увезти карточку в «Готово»: %+v", last.Segments)
	}
	if len(last.Segments[1].Marks) != 0 {
		t.Fatalf("сегмент, который уже не текущий, больше ничего не спрашивает: %+v", last.Segments[1].Marks)
	}
}

// A step's report is written the moment before the card moves on — in the same
// millisecond as the next transition, as often as not. It belongs under the
// screen of the run that wrote it, and so does a failure written for a run
// after the card has already left.
func TestAReportStaysUnderItsOwnRun(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Починить форму")
	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	if err := f.store.InsertSession(store.Session{
		ID: "s1", CardID: card.ID, FlowID: f.flow.ID, StageID: "work",
		AgentName: "Claude", AgentKind: model.KindClaude,
		Status: store.StatusDone, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("сессия: %v", err)
	}
	// A run is placed by when it started, and one that started in the very
	// millisecond the card left would belong to neither visit.
	time.Sleep(2 * time.Millisecond)
	f.store.Record(model.JournalEntry{CardID: card.ID, Kind: model.EntryReport, SessionID: "s1", Text: "Форма починена."})
	f.runner.finish(card.ID, model.TriggerSuccess, "")
	f.store.Record(model.JournalEntry{CardID: card.ID, Kind: model.EntryProblem, SessionID: "s1", Text: "Сессия агента отменена."})

	view := ribbonOf(t, f, card.ID)
	work, qa := view.Segments[0], view.Segments[1]
	if got := work.Screens[0].Report; got != "Форма починена." {
		t.Fatalf("итог шага — под экраном его агента: %+v", work.Screens[0])
	}
	if len(work.Problems) != 1 || len(qa.Problems) != 0 {
		t.Fatalf("сбой сессии — в сегменте сессии, а не там, где карточка уже стоит: %+v / %+v", work.Problems, qa.Problems)
	}
}

// A step that could not start fails in the same millisecond as the failure edge
// takes the card on. Why it failed is said on the step that failed.
func TestAStepThatDidNotStartSaysSoOnItsOwnSegment(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.runner.fail = errAgentWontStart
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	view := ribbonOf(t, f, card.ID)
	if len(view.Segments) != 2 {
		t.Fatalf("работа и отказ: %+v", view.Segments)
	}
	if len(view.Segments[0].Problems) != 1 || len(view.Segments[1].Problems) != 0 {
		t.Fatalf("причина — на стадии, которая не запустилась: %+v / %+v",
			view.Segments[0].Problems, view.Segments[1].Problems)
	}
}

// «The stage is full» explained a wait. Once a run has started in the same
// visit the wait is over, and a plaque saying otherwise over a working agent
// would be wrong; what went wrong after the start still shows.
func TestAWaitThatEndedIsNotShown(t *testing.T) {
	f := setup(t, screenFlow())
	card := f.card(t, "Т")
	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.store.Record(model.JournalEntry{CardID: card.ID, Kind: model.EntryProblem, Text: "Стадия занята."})
	if got := ribbonOf(t, f, card.ID).Segments[0].Problems; len(got) != 1 {
		t.Fatalf("пока ждёт — плашка есть: %+v", got)
	}

	time.Sleep(5 * time.Millisecond)
	if err := f.store.InsertSession(store.Session{
		ID: "s1", CardID: card.ID, FlowID: f.flow.ID, StageID: "work",
		AgentName: "Claude", AgentKind: model.KindClaude,
		Status: store.StatusRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("сессия: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	f.store.Record(model.JournalEntry{CardID: card.ID, Kind: model.EntryProblem, Text: "Не записано обязательное."})

	got := ribbonOf(t, f, card.ID).Segments[0].Problems
	if len(got) != 1 || got[0].Text != "Не записано обязательное." {
		t.Fatalf("остаётся только то, что случилось после старта: %+v", got)
	}
}
