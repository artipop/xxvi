//go:build liveagent

// Revives a real vendor adapter's conversation the way «Continue» does. It
// sends two short turns to a model, so the adapters must be installed and
// signed in:
//
//	go test -tags liveagent ./internal/acp/ -run LiveRevive -v
package acp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/store"
)

// Junie is left out: it can open a conversation again, but its stream then
// runs a turn late (adapters.go), so it is not revived.
func TestLiveReviveRemembersTheConversation(t *testing.T) {
	for _, kind := range []string{model.KindClaude, model.KindCodex} {
		t.Run(kind, func(t *testing.T) {
			st, err := store.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			done := make(chan string, 1)
			m := New(st, finishedRecorder{done}, nil, Options{WorkDir: t.TempDir()}, nil)
			defer m.Close()
			card, err := st.CreateCard(model.Card{Title: "Оживить", State: model.StateFlow})
			if err != nil {
				t.Fatal(err)
			}
			agent := model.Agent{Name: kind, Kind: kind}
			stage := model.Stage{ID: "plan", Work: model.WorkSession}
			turn := func(prompt string) store.Session {
				t.Helper()
				job := engine.Job{Card: card, Flow: model.Flow{ID: "flow"}, Stage: stage, Agent: agent, Prompt: prompt}
				if err := m.Start(job); err != nil {
					t.Fatalf("старт: %v", err)
				}
				select {
				case outcome := <-done:
					if outcome != model.TriggerSuccess {
						sessions, _ := st.SessionsForCard(card.ID)
						t.Fatalf("ход кончился %q: %v", outcome, sessions[0].Error)
					}
				case <-time.After(3 * time.Minute):
					t.Fatal("ход не кончился")
				}
				sessions, _ := st.SessionsForCard(card.ID)
				return sessions[0]
			}

			first := turn("Remember the word «kiwi». Reply with just: ok")
			if !first.Revivable || first.ACPSessionID == "" {
				t.Fatalf("адаптер должен быть оживляем: %+v", first)
			}
			// What the application closing leaves behind.
			paused := store.StatusPaused
			if err := st.UpdateSession(first.ID, store.SessionUpdate{Status: &paused}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)

			second := turn("Which word did I ask you to remember? Reply with that word only.")
			if second.ACPSessionID != first.ACPSessionID {
				t.Fatalf("второй ход в том же разговоре: %s ≠ %s", second.ACPSessionID, first.ACPSessionID)
			}
			events, _ := st.SessionEvents(second.ID)
			var said strings.Builder
			for _, e := range events {
				var chunk struct{ Text string }
				_ = json.Unmarshal([]byte(e.Payload), &chunk)
				said.WriteString(chunk.Text)
			}
			if !strings.Contains(strings.ToLower(said.String()), "kiwi") {
				t.Fatalf("агент не помнит разговор: %s", said.String())
			}
			if strings.Contains(said.String(), "Remember the word") {
				t.Fatalf("проигранная история попала в поток второго хода: %s", said.String())
			}
		})
	}
}
