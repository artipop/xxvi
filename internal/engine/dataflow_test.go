package engine

import (
	"strings"
	"testing"

	"github.com/artipop/xxvi/internal/model"
)

// A route whose middle stage owes the card a verdict, and whose fork reads it.
// The loop is the point: a verdict of «fail» sends the card back to the agent
// rather than to a person.
func dataFlow() model.Flow {
	return model.Flow{
		Name: "С проверкой", EntryStage: "work",
		Stages: []model.Stage{
			{ID: "work", Name: "В работе", Action: model.ActionAgent},
			{
				ID: "qa", Name: "Проверка", Action: model.ActionAgent,
				Writes: []model.PropertyWrite{{Property: "Вердикт", Required: true}, {Property: "Превью"}},
			},
			{ID: "done", Name: "Готово", Final: true},
		},
		Edges: []model.Edge{
			{From: "work", To: "qa", On: model.TriggerSuccess},
			{From: "qa", To: "work", On: model.TriggerSuccess, If: &model.Cond{Property: "Вердикт", Value: "fail"}},
			{From: "qa", To: "done", On: model.TriggerSuccess},
			{From: "qa", To: "work", On: model.TriggerFailure},
		},
	}
}

func propOf(t *testing.T, f fixture, cardID, name string) string {
	t.Helper()
	card, err := f.store.Card(cardID)
	if err != nil {
		t.Fatalf("карточка: %v", err)
	}
	return card.Prop(name)
}

// The contract in both directions: what the stage owes is asked for in its
// brief, and what it answered lands on the card before the fork reads it. In
// the background, where the brief is the one message and the answer is the
// agent's closing words; a terminal stage is asked through its tool (stagemcp).
func TestStageOutputsReachTheCardBeforeTheFork(t *testing.T) {
	flow := dataFlow()
	flow.Stages[1].Work = model.WorkSession
	f := setup(t, flow)
	card := f.card(t, "Проверить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")
	if got := f.stageOf(t, card.ID); got != "qa" {
		t.Fatalf("карточка должна стоять на проверке, получено %q", got)
	}

	brief := f.runner.lastJob(t).Prompt
	if !strings.Contains(brief, "Вердикт") || !strings.Contains(brief, "required") {
		t.Fatalf("бриф стадии должен назвать обязательный выход, получено:\n%s", brief)
	}

	f.runner.finish(card.ID, model.TriggerSuccess, "Проверил.\n\nВердикт: fail\nПревью: https://preview.example/1")

	if got := propOf(t, f, card.ID, "Вердикт"); got != "fail" {
		t.Fatalf("вердикт должен лежать на карточке, получено %q", got)
	}
	if got := propOf(t, f, card.ID, "Превью"); got != "https://preview.example/1" {
		t.Fatalf("превью должно лежать на карточке, получено %q", got)
	}
	// …and the fork that reads it must have been read after the write.
	if got := f.stageOf(t, card.ID); got != "work" {
		t.Fatalf("«fail» возвращает карточку агенту, получено %q", got)
	}
}

// A stage that owed a required value and did not deliver it has not finished,
// whatever it said about itself.
func TestMissingRequiredWriteFailsTheStage(t *testing.T) {
	f := setup(t, dataFlow())
	card := f.card(t, "Проверить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")
	f.runner.finish(card.ID, model.TriggerSuccess, "Всё посмотрел, вроде хорошо.")

	if got := f.stageOf(t, card.ID); got != "work" {
		t.Fatalf("шаг без обязательного значения — неудачный, карточка должна уехать по failure, получено %q", got)
	}
	if got := propOf(t, f, card.ID, model.OutcomeProperty); got != model.OutcomeFailed {
		t.Fatalf("исход должен быть «%s», получено %q", model.OutcomeFailed, got)
	}
}

// Every stage writes how it ended, without anybody declaring it.
func TestOutcomeLandsOnTheCard(t *testing.T) {
	f := setup(t, devFlow())
	card := f.card(t, "Починить кран")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "сделал")
	if got := propOf(t, f, card.ID, model.OutcomeProperty); got != model.OutcomePassed {
		t.Fatalf("после удачного шага на карточке должно стоять «%s», получено %q", model.OutcomePassed, got)
	}
}

// A person answering a waiting stage does it with the same field, and the flow
// offers the two answers as the moves they are.
func TestMarksAreTheTwoAnswersOfAWaitingStage(t *testing.T) {
	flow := model.Flow{
		Name: "С ревью", EntryStage: "work",
		Stages: []model.Stage{
			{ID: "work", Name: "В работе", Action: model.ActionAgent},
			{ID: "review", Name: "На ревью", Action: model.ActionNone},
			{ID: "done", Name: "Готово", Final: true},
		},
		Edges: []model.Edge{
			{From: "work", To: "review", On: model.TriggerSuccess},
			{From: "review", To: "done", On: model.TriggerCardChanged,
				If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomePassed}},
			{From: "review", To: "work", On: model.TriggerCardChanged,
				If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomeFailed}},
		},
	}
	f := setup(t, flow)
	card := f.card(t, "Свёрстать страницу")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")

	view, err := f.engine.CardFlowFor(card.ID)
	if err != nil || view == nil {
		t.Fatalf("карточка на флоу: %v", err)
	}
	if len(view.Marks) != 2 {
		t.Fatalf("на ждущей стадии два ответа, получено %d", len(view.Marks))
	}
	forward, back := view.Marks[0], view.Marks[1]
	if !forward.Forward || forward.Stage != "Готово" {
		t.Fatalf("«%s» должно вести вперёд в «Готово», получено %+v", model.OutcomePassed, forward)
	}
	if back.Forward || back.Stage != "В работе" {
		t.Fatalf("«%s» должно возвращать в «В работе», получено %+v", model.OutcomeFailed, back)
	}
}

// A card sent back arrives with the reason, so the second attempt is not the
// first one again.
func TestReturnedCardTellsTheAgentWhatFailed(t *testing.T) {
	f := setup(t, dataFlow())
	card := f.card(t, "Проверить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")
	f.runner.finish(card.ID, model.TriggerSuccess, "Вердикт: fail")

	brief := f.runner.lastJob(t).Prompt
	if !strings.Contains(brief, "came back here") || !strings.Contains(brief, "Проверка") {
		t.Fatalf("вернувшаяся карточка должна сказать агенту, откуда и почему:\n%s", brief)
	}
}

// A stage that declares no reads is handed what the stages ahead of it write.
func TestReadsFallBackToWhatTheRouteWrote(t *testing.T) {
	f := setup(t, dataFlow())
	card := f.card(t, "Проверить форму")

	if err := f.engine.TakeIntoWork(card.ID, f.flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}
	f.runner.finish(card.ID, model.TriggerSuccess, "готово")
	f.runner.finish(card.ID, model.TriggerSuccess, "Вердикт: fail\nПревью: https://preview.example/1")

	brief := f.runner.lastJob(t).Brief
	if !strings.Contains(brief, "From the task:") || !strings.Contains(brief, "https://preview.example/1") {
		t.Fatalf("стадия без своих входов получает то, что записали до неё:\n%s", brief)
	}
}
