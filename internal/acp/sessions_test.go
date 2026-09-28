package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/stagemcp"
)

// The test binary doubles as an agent that lists sessions, so the listing is
// tested over a real pipe without a vendor adapter installed.
const fakeListerEnv = "XXVI_FAKE_SESSION_LISTER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeListerEnv) == "1" {
		fakeLister()
		os.Exit(0)
	}
	// And as `xxvi hook`: a stage's hooks run os.Executable, which here is
	// this binary (terminal_live_test.go).
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		_ = stagemcp.ForwardHook(context.Background(), os.Stdin, os.Getenv)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeLister answers the way codex-acp does: it pages through every
// conversation it has and filters each page by folder itself, so the first page
// here is empty and still has more after it.
func fakeLister() {
	pages := map[string]map[string]any{
		"": {"sessions": []any{}, "nextCursor": "2"},
		"2": {"sessions": []any{map[string]any{
			"sessionId": "older", "cwd": "/p", "title": "Старый", "updatedAt": "2026-09-01T10:00:00Z",
		}}, "nextCursor": "3"},
		"3": {"sessions": []any{map[string]any{
			"sessionId": "newer", "cwd": "/p", "updatedAt": "2026-09-20T10:00:00Z",
		}}},
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion":   1,
				"authMethods":       []any{},
				"agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"list": map[string]any{}}},
			}
		case "session/list":
			result = pages[req.Params.Cursor]
		default:
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"no"}}`+"\n", req.ID)
			continue
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		fmt.Println(string(out))
	}
}

// An empty page is not the end: the agent says when the list is over.
func TestPastSessionsWalksPastAnEmptyPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	agent := model.Agent{
		Name: "fake", Kind: model.KindACP,
		Command: []string{os.Args[0]}, Env: map[string]string{fakeListerEnv: "1"},
	}
	got, err := PastSessions(ctx, agent, t.TempDir())
	if err != nil {
		t.Fatalf("список сессий: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("сессии со всех страниц, а не с первой: %+v", got)
	}
	if got[0].ID != "newer" || got[1].ID != "older" || got[1].Title != "Старый" {
		t.Fatalf("сначала новые, с заголовками: %+v", got)
	}
}
