package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/artipop/gandvik/internal/model"
)

func TestFinishedTerminalMovesTheCardOnlyWhenThePersonAsked(t *testing.T) {
	flow := devFlow()
	flow.Stages[0].Writes = []model.PropertyWrite{{Property: "Ветка"}}
	f := setup(t, flow)
	card := f.card(t, "Вернуться в старый терминал")
	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatal(err)
	}
	work := f.runner.lastJob(t)
	reported := time.Now()
	f.runner.finish(card.ID, model.TriggerSuccess, "")
	if f.stageOf(t, card.ID) != "review" {
		t.Fatal("карточка должна уйти на проверку")
	}

	again := Again{Outcome: model.TriggerSuccess, Text: "поправил\nВетка: fix/again", Reported: reported}
	if err := f.engine.Rejoin(work, again); err == nil || !strings.Contains(err.Error(), "has not asked") {
		t.Fatalf("без реплики человека старый терминал не двигает карточку: %v", err)
	}
	visits := len(ribbonOf(t, f, card.ID).Segments)

	again.Asked = time.Now().Add(time.Second)
	if err := f.engine.Rejoin(work, again); err != nil {
		t.Fatal(err)
	}
	if got := ribbonOf(t, f, card.ID).Segments; len(got) != visits+1 || f.stageOf(t, card.ID) != "review" {
		t.Fatalf("повторный отчёт снова ведёт по рёбрам своей стадии: %+v", got)
	}
	if got, _ := f.store.Card(card.ID); got.Prop("Ветка") != "fix/again" {
		t.Fatalf("значения повторного отчёта пишутся на карточку: %+v", got.Props)
	}

	if err := f.engine.Rejoin(work, Again{Target: "Нет такой", Asked: again.Asked}); err == nil || !strings.Contains(err.Error(), "«Готово»") {
		t.Fatalf("неизвестная стадия перечисляет существующие: %v", err)
	}
	if err := f.engine.Rejoin(work, Again{Target: "«Готово»", Asked: again.Asked}); err != nil {
		t.Fatal(err)
	}
	if f.stateOf(t, card.ID) != model.StateDone {
		t.Fatal("move_to ведёт на любую ноду")
	}

	recordConversation(t, f, work, "first", "conversation")
	revisitable := func() bool {
		return ribbonOf(t, f, card.ID).Segments[0].Screens[0].Revisitable
	}
	if !revisitable() {
		t.Fatal("закрытый терминал открывается заново")
	}
	f.runner.mu.Lock()
	f.runner.live["first"] = true
	f.runner.mu.Unlock()
	if revisitable() {
		t.Fatal("в живой терминал возвращаются, просто написав в него")
	}
}

func TestMoveFromTheCurrentStepLeavesOtherWorkRunning(t *testing.T) {
	flow := devFlow()
	flow.Stages[1].Action = model.ActionAgent
	f := setup(t, flow)
	card := f.card(t, "Перевести по просьбе")
	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatal(err)
	}
	work := f.runner.lastJob(t)
	// The step's own run needs no word from the person: it is the card's work.
	if err := f.engine.Rejoin(work, Again{Target: "На проверке"}); err != nil {
		t.Fatal(err)
	}
	review := f.runner.lastJob(t)
	if review.Stage.ID != "review" || f.runner.count() != 2 {
		t.Fatalf("переход запускает следующую ноду: %+v", review)
	}
	// The review is now the card's work; the work step's late report is not.
	if err := f.engine.Rejoin(work, Again{Outcome: model.TriggerSuccess}); err == nil {
		t.Fatal("ушедший шаг без реплики человека не отчитывается")
	}
	if f.runner.RunningOnStage("review") != 1 {
		t.Fatal("работающий шаг не отменяется")
	}
	if err := f.engine.Drop(card.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Rejoin(review, Again{Target: "В работе"}); err == nil {
		t.Fatal("брошенную задачу нельзя двигать из терминала")
	}
}
