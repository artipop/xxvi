package engine

import (
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/store"
)

func standingOf(t *testing.T, f fixture, cardID string) (Standing, bool) {
	t.Helper()
	all, err := f.engine.Standing()
	if err != nil {
		t.Fatalf("Standing: %v", err)
	}
	for _, s := range all {
		if s.CardID == cardID {
			return s, true
		}
	}
	return Standing{}, false
}

// running records the live session a real runner would have: the engine asks
// the store, not the runner, whether a card is being worked.
func running(t *testing.T, f fixture, cardID, stageID string) {
	t.Helper()
	err := f.store.InsertSession(store.Session{
		ID: "s-" + cardID, CardID: cardID, StageID: stageID, AgentName: "Claude",
		Status: store.StatusRunning, StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("сессия: %v", err)
	}
}

func TestAWorkingAgentIsNotWaitingForAPerson(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	running(t, f, card.ID, "work")

	if _, ok := standingOf(t, f, card.ID); ok {
		t.Fatal("пока агент работает, ход не за человеком")
	}
}

func TestAReviewWaitsForAnAnswer(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")

	s, ok := standingOf(t, f, card.ID)
	if !ok {
		t.Fatal("карточка на проверке ждёт человека")
	}
	if s.Why != StandAnswer || s.Stage != "На проверке" {
		t.Fatalf("ожидался ответ на «На проверке», получено %+v", s)
	}
}

func TestAStepWithNowhereToGoStopsTheCard(t *testing.T) {
	flow := devFlow()
	flow.Edges = flow.Edges[:1] // success only: nothing catches a failure
	f := setup(t, flow)
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)
	f.runner.finish(card.ID, model.TriggerFailure, "")

	s, ok := standingOf(t, f, card.ID)
	if !ok || s.Why != StandStopped {
		t.Fatalf("карточка без перехода стоит и ждёт человека, получено %+v", s)
	}
	if s.Problem == nil || s.Problem.Code != "journal.noEdge" {
		t.Fatalf("строка должна сказать, что остановило карточку: %+v", s.Problem)
	}
}

func TestACardAPersonTookIsTheirs(t *testing.T) {
	f := setup(t, devFlow())
	card, _ := f.store.CreateCard(model.Card{Title: "Т", Assignee: "Артём"})
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	if s, ok := standingOf(t, f, card.ID); !ok || s.Why != StandStopped {
		t.Fatalf("карточка, взятая человеком, ждёт его: %+v", s)
	}
}

func TestAQueuedCardWaitsForTheStageNotForAPerson(t *testing.T) {
	flow := devFlow()
	flow.Stages[0].MaxRunning = 1
	f := setup(t, flow)
	first, second := f.card(t, "Первая"), f.card(t, "Вторая")
	f.engine.TakeIntoWork(first.ID, f.flow.ID)
	running(t, f, first.ID, "work")
	f.engine.TakeIntoWork(second.ID, f.flow.ID)

	if _, ok := standingOf(t, f, second.ID); ok {
		t.Fatal("карточка в очереди ждёт места на стадии, а не человека")
	}
}

func TestWaitingForTheHostingIsTheWorldsMove(t *testing.T) {
	flow := model.Flow{
		Name: "MR", EntryStage: "wait",
		Stages: []model.Stage{
			{ID: "wait", Name: "Ждём мерджа", Action: model.ActionNone},
			{ID: "merged", Name: "Влит", Final: true},
		},
		Edges: []model.Edge{{From: "wait", To: "merged", On: model.TriggerMRMerged}},
	}
	f := setup(t, flow)
	card := f.card(t, "Т")
	f.engine.TakeIntoWork(card.ID, f.flow.ID)

	if _, ok := standingOf(t, f, card.ID); ok {
		t.Fatal("ожидание мерджа — ход хостинга, а не человека")
	}
}
