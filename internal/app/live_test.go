//go:build liveagent

// This test runs a real ACP agent. It needs an adapter installed and an
// account logged in, and it costs whatever a turn costs — which is why it is
// behind a build tag and never runs with the rest:
//
//	go test -tags liveagent ./internal/app/ -run Live -v
//
// It exists because everything else about a stage running an agent is tested
// with a stand-in, and "the card goes through the flow" is the one claim that
// is only worth as much as a real agent makes it worth.
package app

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
)

func TestLiveCardGoesThroughAFlow(t *testing.T) {
	a, err := Open(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("открыть приложение: %v", err)
	}
	defer a.Close()

	// A flow of two stages: the agent works, then a person would look. Reaching
	// the second one is the whole assertion — it means the session started,
	// finished, produced an outcome, and the edge carried the card.
	flow, err := a.Store.SaveFlow(model.Flow{
		Name: "Живая проверка", EntryStage: "live-work",
		Stages: []model.Stage{
			{
				ID: "live-work", Name: "В работе", Action: model.ActionAgent,
				// Named rather than defaulted: this test is about a real ACP
				// session, and the default is the terminal, which waits for a
				// report nobody here would give.
				Work:   model.WorkSession,
				Prompt: "Ничего не делай и ничего не создавай. Просто ответь одним словом: готово.",
			},
			{ID: "live-review", Name: "На проверке", Action: model.ActionNone},
		},
		Edges: []model.Edge{{From: "live-work", To: "live-review", On: model.TriggerSuccess}},
	})
	if err != nil {
		t.Fatalf("создать флоу: %v", err)
	}

	card, err := a.Store.CreateCard(model.Card{Title: "Проверка связи", State: model.StateInbox})
	if err != nil {
		t.Fatalf("создать карточку: %v", err)
	}
	if err := a.Engine.TakeIntoWork(card.ID, flow.ID); err != nil {
		t.Fatalf("взять в работу: %v", err)
	}

	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		st, ok, err := a.Store.FlowState(card.ID)
		if err != nil {
			t.Fatalf("положение: %v", err)
		}
		if ok && st.StageID == "live-review" {
			entries, _ := a.Store.Journal(card.ID)
			for _, c := range entries {
				t.Logf("[%s] %s", c.Author, c.Text)
			}
			return
		}
		time.Sleep(2 * time.Second)
	}

	// Failing, say why: the journal is where the session wrote what happened.
	entries, _ := a.Store.Journal(card.ID)
	for _, c := range entries {
		t.Logf("[%s] %s", c.Author, c.Text)
	}
	t.Fatal("карточка не доехала до второй стадии за отведённое время")
}
