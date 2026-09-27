//go:build liveagent

// Asks the real vendor adapters for their conversations. Nothing is sent to a
// model, but the adapters must be installed:
//
//	go test -tags liveagent ./internal/acp/ -run LivePastSessions -v
package acp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
)

func TestLivePastSessions(t *testing.T) {
	// The repository root: the conversations about this code were held there.
	cwd, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{model.KindClaude, model.KindCodex} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			got, err := PastSessions(ctx, model.Agent{Name: kind, Kind: kind}, cwd)
			if err != nil {
				t.Fatalf("список сессий: %v", err)
			}
			for i, s := range got {
				if s.ID == "" || s.Cwd == "" {
					t.Fatalf("у сессии должны быть id и папка: %+v", s)
				}
				if i > 0 && s.UpdatedAt.After(got[i-1].UpdatedAt) {
					t.Fatalf("сначала новые: %v после %v", s.UpdatedAt, got[i-1].UpdatedAt)
				}
			}
			t.Logf("%s: %d сессий в %s", kind, len(got), cwd)
			if len(got) > 0 {
				t.Logf("новейшая: %s %s «%s»", got[0].UpdatedAt.Format(time.DateTime), got[0].ID, got[0].Title)
			}
		})
	}
}
