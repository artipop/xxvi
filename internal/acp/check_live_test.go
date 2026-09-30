//go:build liveagent

// Asks every preset what it is, as the agents dialog does. Nothing is sent to
// a model; a preset not installed here is skipped:
//
//	go test -tags liveagent ./internal/acp/ -run LiveCheck -v
package acp

import (
	"context"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
)

func TestLiveCheckAgents(t *testing.T) {
	for _, kind := range model.Kinds {
		if _, preset := adapters[kind]; !preset {
			continue
		}
		t.Run(kind, func(t *testing.T) {
			if !adapterStatus(kind).Ready {
				t.Skip("не установлен")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			got, err := CheckAgent(ctx, model.Agent{Name: kind, Kind: kind})
			if err != nil {
				t.Fatalf("проверка: %v", err)
			}
			if got.Title == "" {
				t.Fatalf("агент должен назвать себя: %+v", got)
			}
			problem := ""
			if got.Problem != nil {
				problem = got.Problem.String()
			}
			t.Logf("%s %s: оживление=%q список=%v модель=%q из %d; %s",
				got.Title, got.Version, got.Revive, got.Lists, got.Model, len(got.Models), problem)
		})
	}
}
