package app

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
)

type previewEvents struct{ emitted int }

func (e *previewEvents) Emit(string, any) { e.emitted++ }

func TestPastSessionPreviewDoesNotImportOrResumeConversation(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	events := &previewEvents{}
	a.SetUI(events)
	observer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(a.DataDir, "gandvik.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	ctx := context.Background()
	conn, err := observer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	version := func() int {
		t.Helper()
		var n int
		if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, tc := range []struct{ kind, folder string }{
		{model.KindCodex, "sessions"},
		{model.KindCodex, "archived_sessions"},
		{model.KindClaude, "projects"},
	} {
		t.Run(tc.kind+"/"+tc.folder, func(t *testing.T) {
			id := "12345678-1234-1234-1234-123456789abc"
			home := t.TempDir()
			key := "CODEX_HOME"
			filename := "rollout-2026-10-09-" + id + ".jsonl"
			records := `{"type":"response_item","timestamp":"2026-10-01T10:00:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"первый вопрос"}]}}
{"type":"response_item","timestamp":"2026-10-09T10:00:00Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"последний ответ"}]}}
`
			if tc.kind == model.KindClaude {
				key = "CLAUDE_CONFIG_DIR"
				filename = id + ".jsonl"
				records = `{"type":"user","message":{"role":"user","content":"первый вопрос"}}
{"type":"assistant","message":{"role":"assistant","content":"последний ответ"}}
`
			}
			path := filepath.Join(home, tc.folder, "project", filename)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(records), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Store.SaveAgent(model.Agent{
				Name: "Preview", Kind: tc.kind, BinPath: "/missing/agent", Command: []string{"/missing/agent"},
				Env: map[string]string{key: home},
			}); err != nil {
				t.Fatal(err)
			}
			before := version()
			history, err := api.PastSessionPreview("Preview", id)
			if err != nil || !strings.Contains(history, "первый вопрос") || !strings.Contains(history, "последний ответ") {
				t.Fatalf("предпросмотр сохранённого разговора без CLI: %q, %v", history, err)
			}
			for _, missing := range []string{"12345678-1234-1234-1234-111111111111", "../not-a-session"} {
				if _, err := api.PastSessionPreview("Preview", missing); err == nil || msg.Of(err).Code != "sessions.previewUnavailable" {
					t.Fatalf("отсутствующая история не должна оживлять агента: %v", err)
				}
			}
			if after := version(); after != before {
				t.Fatalf("предпросмотр записал данные в БД: версия %d → %d", before, after)
			}
			if events.emitted != 0 {
				t.Fatalf("предпросмотр отправил %d событий в UI", events.emitted)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, []byte(records)) {
				t.Fatalf("предпросмотр изменил сохранённый разговор: %v", err)
			}
		})
	}
}
