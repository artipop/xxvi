package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
)

func claudeHome(t *testing.T) (model.Agent, string) {
	t.Helper()
	home := t.TempDir()
	// The agent's own CLAUDE_CONFIG_DIR wins over the app's environment.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	return model.Agent{Name: "Claude", Kind: model.KindClaude, Env: map[string]string{"CLAUDE_CONFIG_DIR": home}}, home
}

func writeClaudeConversation(t *testing.T, home, folder, id, content string) string {
	t.Helper()
	dir := filepath.Join(home, "projects", folder)
	if err := os.MkdirAll(filepath.Join(dir, id, "subagents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id, "subagents", "agent-1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeleteClaudeSession(t *testing.T) {
	a, home := claudeHome(t)
	other := writeClaudeConversation(t, home, "-elsewhere", "11111111-2222-3333-4444-555555555555", `{"type":"user"}`+"\n")
	path := writeClaudeConversation(t, home, "-Users-me-project", deleteSessionID, `{"type":"user"}`+"\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := DeletePastSession(ctx, a, t.TempDir(), strings.ToUpper(deleteSessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("файл разговора остался: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, ".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("транскрипты субагентов остались: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("удалён чужой разговор: %v", err)
	}

	err := DeletePastSession(ctx, a, t.TempDir(), deleteSessionID)
	if err == nil || msg.Of(err).Code != "sessions.deleteFailed" {
		t.Fatalf("удаление пропавшего разговора: %v", err)
	}
	for _, id := range []string{"", "../x", "--last"} {
		if err := DeletePastSession(ctx, a, t.TempDir(), id); err == nil || msg.Of(err).Code != "sessions.invalidID" {
			t.Fatalf("%q: %v", id, err)
		}
	}
}

func TestDeleteClaudeSessionSkipsEmptyFile(t *testing.T) {
	a, home := claudeHome(t)
	empty := writeClaudeConversation(t, home, "-a", deleteSessionID, "")
	path := writeClaudeConversation(t, home, "-b", deleteSessionID, `{"type":"user"}`+"\n")

	if err := DeletePastSession(context.Background(), a, t.TempDir(), deleteSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("непустой разговор остался: %v", err)
	}
	if _, err := os.Stat(empty); err != nil {
		t.Fatalf("пустая заготовка CLI тронута: %v", err)
	}
}

func TestRenameClaudeSession(t *testing.T) {
	a, home := claudeHome(t)
	before := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	path := writeClaudeConversation(t, home, "-Users-me-project", deleteSessionID, before)

	got, err := RenamePastSession(context.Background(), a, t.TempDir(), deleteSessionID, "  Новое имя \n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Новое имя" {
		t.Fatalf("название: %q", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte(before)) || !bytes.HasSuffix(b, []byte("\n")) {
		t.Fatalf("история переписана, а не дополнена: %q", b)
	}
	var record map[string]string
	if err := json.Unmarshal(bytes.TrimPrefix(bytes.TrimSpace(b), []byte(before)), &record); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"type": "custom-title", "customTitle": "Новое имя", "sessionId": deleteSessionID}
	if len(record) != len(want) {
		t.Fatalf("запись: %v", record)
	}
	for k, v := range want {
		if record[k] != v {
			t.Fatalf("запись: %v", record)
		}
	}
	if history := TerminalHistory(a, deleteSessionID, time.Time{}); !strings.Contains(string(history), "hi") {
		t.Fatalf("предпросмотр после переименования: %q", history)
	}

	_, err = RenamePastSession(context.Background(), a, t.TempDir(), "11111111-2222-3333-4444-555555555555", "x")
	if err == nil || msg.Of(err).Code != "sessions.renameFailed" {
		t.Fatalf("переименование пропавшего разговора: %v", err)
	}
	if _, err := RenamePastSession(context.Background(), a, t.TempDir(), deleteSessionID, " "); err == nil || msg.Of(err).Code != "sessions.emptyTitle" {
		t.Fatalf("пустое название: %v", err)
	}
}
