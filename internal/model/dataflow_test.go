package model

import (
	"strings"
	"testing"

	"github.com/artipop/xxvi/internal/msg"
)

// A route that loops: the check sends the card back to the agent. Every question
// about "what came before" has to survive that, because a loop has no stage
// without incoming edges.
func loopFlow() Flow {
	return Flow{
		Name: "С проверкой", EntryStage: "work",
		Stages: []Stage{
			{ID: "work", Name: "В работе", Action: ActionAgent,
				Writes: []PropertyWrite{{Property: "Ветка"}}},
			{ID: "deploy", Name: "Деплой", Action: ActionAgent,
				Writes: []PropertyWrite{{Property: "Превью", Required: true}}},
			{ID: "qa", Name: "Проверка", Action: ActionAgent,
				Writes: []PropertyWrite{{Property: "Вердикт", Required: true}}},
			{ID: "done", Name: "Готово", Final: true},
		},
		Edges: []Edge{
			{From: "work", To: "deploy", On: TriggerSuccess},
			{From: "deploy", To: "qa", On: TriggerSuccess},
			{From: "qa", To: "work", On: TriggerSuccess, If: &Cond{Property: "Вердикт", Value: "fail"}},
			{From: "qa", To: "done", On: TriggerSuccess},
		},
	}
}

func TestUpstreamWritesWalkBackThroughALoop(t *testing.T) {
	f := loopFlow()

	got := f.UpstreamWrites("qa")
	if len(got) != 2 || got[0] != "Превью" || got[1] != "Ветка" {
		t.Fatalf("проверка видит то, что записали до неё, ближайшее первым: %v", got)
	}

	// The agent stage is reachable backwards only through the loop, and its own
	// output is not something it reads back.
	back := f.UpstreamWrites("work")
	for _, name := range back {
		if name == "Ветка" {
			t.Fatalf("стадия не читает собственный выход: %v", back)
		}
	}
	if len(back) != 2 {
		t.Fatalf("через петлю видно всё, что пишут остальные: %v", back)
	}
}

func TestReadsOfItsOwnWinOverTheGraph(t *testing.T) {
	f := loopFlow()
	f.Stages[2].Reads = []string{"Превью"}

	got := f.ReadsFor("qa")
	if len(got) != 1 || got[0] != "Превью" {
		t.Fatalf("свой список входов главнее графа: %v", got)
	}
}

func TestUnwrittenConditionsNameWhatNobodyProduces(t *testing.T) {
	f := loopFlow()
	f.Edges = append(f.Edges, Edge{
		From: "deploy", To: "done", On: TriggerFailure,
		If: &Cond{Property: "Срочность", Value: "высокая"},
	})

	got := f.UnwrittenConditions()
	if len(got) != 1 || got[0] != "Срочность" {
		t.Fatalf("проверка потока данных должна назвать «Срочность», получено %v", got)
	}
}

// A person's answer is not a stage's output, and neither is the field the engine
// fills in after every stage. Neither may be reported as missing.
func TestUnwrittenConditionsIgnoreWhatAPersonAnswers(t *testing.T) {
	f := loopFlow()
	f.Edges = append(f.Edges,
		Edge{From: "qa", To: "done", On: TriggerCardChanged, If: &Cond{Property: "Одобрено", Value: "Да"}},
		Edge{From: "qa", To: "work", On: TriggerFailure, If: &Cond{Property: OutcomeProperty, Value: OutcomeFailed}},
	)
	if got := f.UnwrittenConditions(); len(got) != 0 {
		t.Fatalf("ни ответ человека, ни исход не объявляются стадиями: %v", got)
	}
}

func TestOutcomeIsRefusedAsAStageOutput(t *testing.T) {
	f := loopFlow()
	f.Stages[0].Writes = append(f.Stages[0].Writes, PropertyWrite{Property: OutcomeProperty})

	_, err := ValidateFlow(f, []Agent{{Name: "Claude", Kind: KindClaude}})
	if err == nil || !msg.Is(err, "writes.outcome") {
		t.Fatalf("исход пишется сам — объявлять его нельзя, получено %v", err)
	}
}

func TestAWaitingStageCannotDeclareOutputs(t *testing.T) {
	f := loopFlow()
	f.Stages = append(f.Stages, Stage{
		ID: "review", Name: "На ревью", Action: ActionNone,
		Writes: []PropertyWrite{{Property: "Вердикт"}},
	})

	_, err := ValidateFlow(f, []Agent{{Name: "Claude", Kind: KindClaude}})
	if err == nil || !msg.Is(err, "stage.writesWithoutAgent") {
		t.Fatalf("на стадии, которая ничего не запускает, писать некому, получено %v", err)
	}
}

func TestDuplicateOutputIsRefused(t *testing.T) {
	f := loopFlow()
	f.Stages[1].Writes = append(f.Stages[1].Writes, PropertyWrite{Property: "превью"})

	_, err := ValidateFlow(f, []Agent{{Name: "Claude", Kind: KindClaude}})
	if err == nil || !strings.Contains(err.Error(), "writes.twice") {
		t.Fatalf("одно свойство дважды — отказ, получено %v", err)
	}
}
