package engine

import (
	"testing"
	"time"

	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
	"github.com/artipop/gandvik/internal/store"
)

func TestManualVisitStartsOnceAndKeepsItsSetting(t *testing.T) {
	flow := devFlow()
	flow.Stages[0].ManualStart = true
	flow.Stages[0].KeepFocus = true
	f := setup(t, flow)
	card := f.card(t, "Задача")
	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatal(err)
	}
	view := ribbonOf(t, f, card.ID)
	seg := view.Segments[0]
	if waiting, ok := standingOf(t, f, card.ID); !ok || waiting.Why != StandReady {
		t.Fatalf("ручной шаг ждёт запуска: %+v", waiting)
	}
	if f.runner.count() != 0 || !seg.Startable || !view.KeepFocus || view.FocusID != seg.ID+"|empty" {
		t.Fatalf("вход открывает ноду и ждёт запуска: %+v", view)
	}
	// A new engine reads the wait from the database, without starting anything.
	restarted := New(f.store, f.runner, nil, nil)
	if err := restarted.StartStage(card.ID, seg.EventID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.StartStage(card.ID, seg.EventID); err == nil {
		t.Fatal("повторный запуск должен быть отвергнут")
	}
	if f.runner.count() != 1 {
		t.Fatal("должен быть ровно один запуск")
	}
	if ribbonOf(t, f, card.ID).Segments[0].Startable {
		t.Fatal("запущенный шаг не ждёт запуска")
	}
	first := f.runner.lastJob(t)
	f.runner.finish(card.ID, model.TriggerSuccess, "")
	if _, err := f.store.UpdateCard(card.ID, store.CardEdit{Props: map[string]string{"Одобрено": "Нет"}}); err != nil {
		t.Fatal(err)
	}
	f.engine.CardChanged(card.ID, "Одобрено", "Нет")
	if f.runner.count() != 1 {
		t.Fatal("повторный визит тоже ручной")
	}
	if err := f.engine.StartStage(card.ID, first.Visit); err == nil {
		t.Fatal("старый клик не запускает новый визит")
	}
	current := ribbonOf(t, f, card.ID)
	if err := f.engine.StartStage(card.ID, current.Segments[len(current.Segments)-1].EventID); err != nil {
		t.Fatal(err)
	}
	if f.runner.count() != 2 {
		t.Fatal("новый визит запускается по кнопке")
	}
}

func recordConversation(t *testing.T, f fixture, job Job, id, conversation string) {
	t.Helper()
	// Equal timestamps are intentional: ownership must come from the visit id.
	if err := f.store.InsertSession(store.Session{ID: id, VisitID: job.Visit, CardID: job.Card.ID,
		FlowID: job.Flow.ID, StageID: job.Stage.ID, AgentName: job.Agent.Name, AgentKind: job.Agent.Kind,
		Work: model.WorkTerminal, Status: store.StatusDone, StartedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateSession(id, store.SessionUpdate{ACPSessionID: &conversation}); err != nil {
		t.Fatal(err)
	}
}

func TestReturnToConversationReplaysTheChainAndRejectsOldResults(t *testing.T) {
	flow := devFlow()
	flow.Stages[1].Action = model.ActionAgent
	flow.Edges = []model.Edge{
		{From: "work", To: "review", On: model.TriggerSuccess},
		{From: "review", To: "done", On: model.TriggerSuccess},
	}
	f := setup(t, flow)
	card := f.card(t, "Вернуться к реализации")
	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatal(err)
	}
	first := f.runner.lastJob(t)
	recordConversation(t, f, first, "first", "chosen-conversation")
	f.runner.finish(card.ID, model.TriggerSuccess, "")
	review := f.runner.lastJob(t)
	if err := f.engine.Revisit(card.ID, "first"); err != nil {
		t.Fatal(err)
	}
	returned := f.runner.lastJob(t)
	if returned.Prompt != "" || returned.ResumeID != "chosen-conversation" || returned.Stage.ID != "work" || returned.Visit == first.Visit {
		t.Fatalf("новый визит открывает выбранный разговор без реплики: %+v", returned)
	}
	recordConversation(t, f, returned, "second", "chosen-conversation")
	f.engine.FinishedVisit(review, model.TriggerSuccess, msg.Msg{}, "")
	f.engine.FinishedVisit(first, model.TriggerSuccess, msg.Msg{}, "")
	if f.stageOf(t, card.ID) != "work" {
		t.Fatal("старые отчёты сдвинули карточку")
	}
	view := ribbonOf(t, f, card.ID)
	if view.Segments[0].Screens[0].SessionID != "first" || view.Segments[2].Screens[0].SessionID != "second" {
		t.Fatalf("запуски должны принадлежать своим визитам: %+v", view.Segments)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "правки готовы")
	if f.stageOf(t, card.ID) != "review" {
		t.Fatal("новый результат должен снова запустить проверку")
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "проверено")
	if f.stateOf(t, card.ID) != model.StateDone {
		t.Fatal("цепочка должна завершиться")
	}
	if err := f.engine.Revisit(card.ID, "first"); err != nil {
		t.Fatal("завершённую задачу можно вернуть:", err)
	}
	if f.stateOf(t, card.ID) != model.StateFlow {
		t.Fatal("возврат должен открыть задачу")
	}
	other := f.card(t, "Чужая карточка")
	if err := f.engine.Revisit(other.ID, "first"); err == nil {
		t.Fatal("чужой разговор нельзя подхватить")
	}
}
