//go:build liveagent

package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artipop/gandvik/internal/model"
)

func TestLiveRenamePastSession(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	a := model.Agent{Kind: model.KindCodex, Env: map[string]string{"CODEX_HOME": home, "CODEX_SQLITE_HOME": home}}
	id := "12345678-1234-1234-1234-123456789abc"
	path := filepath.Join(home, "sessions", "2026", "10", "12", "rollout-2026-10-12T08-00-00-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{
		"type": "session_meta", "timestamp": "2026-10-12T08:00:00Z",
		"payload": map[string]any{
			"id": id, "cwd": cwd, "timestamp": "2026-10-12T08:00:00Z",
			"originator": "codex_cli_rs", "cli_version": "0.160.1", "source": "cli", "model_provider": "openai",
		},
	})
	records := string(meta) + "\n" + `{"type":"response_item","timestamp":"2026-10-12T08:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Read-only rename fixture"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(records), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	title := "Разговор для проверки переименования 🦉"
	if got, err := RenamePastSession(ctx, a, cwd, id, title); err != nil || got != title {
		t.Fatalf("переименовать тестовый разговор: %q, %v", got, err)
	}

	// Read from a fresh server: an optimistic UI update alone would not survive
	// closing the list and opening it again. The fixture uses an isolated home.
	cli, _ := cliFor(a.Kind)
	bin, err := terminalBin(cli)
	if err != nil {
		t.Fatal(err)
	}
	proc, err := spawn(ctx, []string{bin, "app-server", "--listen", "stdio://"}, cwd, spawnEnv(a))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { proc.killGroup(0); _ = proc.wait() }()
	stop := context.AfterFunc(ctx, func() { _ = proc.stdout.Close(); _ = proc.stdin.Close() })
	defer stop()
	encoder, decoder := json.NewEncoder(proc.stdin), json.NewDecoder(proc.stdout)
	read := func(id int) json.RawMessage {
		t.Helper()
		for {
			var response struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := decoder.Decode(&response); err != nil {
				t.Fatal(err)
			}
			if string(response.ID) != fmt.Sprint(id) {
				continue
			}
			if len(response.Error) != 0 {
				t.Fatalf("ошибка Codex: %s", response.Error)
			}
			return response.Result
		}
	}
	if err := encoder.Encode(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "gandvik_test", "version": "1"}}}); err != nil {
		t.Fatal(err)
	}
	read(1)
	if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{"id": 2, "method": "thread/read", "params": map[string]any{"threadId": id, "includeTurns": false}}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Thread struct {
			Name string `json:"name"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(read(2), &result); err != nil || result.Thread.Name != title {
		t.Fatalf("имя должно сохраниться у Codex после закрытия сервера: %+v, %v", result, err)
	}
}
