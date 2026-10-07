package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
)

func TestTerminalHistoryReadsMessagesWithoutStartingAnAgent(t *testing.T) {
	for _, kind := range []string{model.KindClaude, model.KindCodex} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			id := "12345678-1234-1234-1234-123456789abc"
			agent := model.Agent{Kind: kind, BinPath: "/missing/agent", Env: map[string]string{}}
			var path, records string
			if kind == model.KindClaude {
				agent.Env["CLAUDE_CONFIG_DIR"] = root
				path = filepath.Join(root, "projects", "-project", id+".jsonl")
				records = `{"type":"user","timestamp":"2026-10-05T10:00:00Z","message":{"role":"user","content":"первый вопрос"}}
{"type":"assistant","timestamp":"2026-10-05T10:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"первый ответ"},{"type":"tool_use","name":"Bash","input":{"command":"echo test"}}]}}
{"type":"user","timestamp":"2026-10-05T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"test"}]}]}}
{"type":"user","isMeta":true,"message":{"role":"user","content":"meta"}}
{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":"sidechain"}}
{"type":"assistant","timestamp":"2026-10-05T10:00:04Z","message":{"role":"assistant","content":"более поздний запуск"}}
`
			} else {
				agent.Env["CODEX_HOME"] = root
				path = filepath.Join(root, "sessions", "2026", "10", "05", "rollout-2026-10-05-"+id+".jsonl")
				records = `{"type":"response_item","timestamp":"2026-10-05T10:00:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"первый вопрос"}]}}
{"type":"response_item","timestamp":"2026-10-05T10:00:01Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"первый ответ"}]}}
{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"meta"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","channel":"analysis","content":[{"type":"output_text","text":"private reasoning"}]}}
{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"echo test"}}
{"type":"response_item","payload":{"type":"custom_tool_call_output","output":"test"}}
{"type":"event_msg","payload":{"type":"agent_message","message":"duplicate answer"}}
{"type":"response_item","timestamp":"2026-10-05T10:00:04Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"более поздний запуск"}]}}
`
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(records), 0o600); err != nil {
				t.Fatal(err)
			}
			until, _ := time.Parse(time.RFC3339, "2026-10-05T10:00:03Z")
			history := string(TerminalHistory(agent, id, until))
			for _, text := range []string{"первый вопрос", "первый ответ", "echo test", "test"} {
				if !strings.Contains(history, text) {
					t.Fatalf("история должна содержать %q: %q", text, history)
				}
			}
			for _, text := range []string{"meta", "private reasoning", "sidechain", "duplicate answer", "более поздний запуск"} {
				if strings.Contains(history, text) {
					t.Fatalf("история этого шага не должна содержать %q", text)
				}
			}
		})
	}
}

func TestTerminalHistoryIgnoresMalformedRecordsAndTerminalControls(t *testing.T) {
	history := string(readTerminalHistory(strings.NewReader(`broken
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ответ\u001b[2J\nещё ответ"}]}}
{"type":"response_item","payload":{"type":"unknown","role":"system","content":"secret"}}
`), model.KindCodex, time.Time{}))
	if strings.Contains(history, "\x1b") || strings.Contains(history, "secret") || !strings.Contains(history, "ответ") || !strings.Contains(history, "\r\nещё ответ") {
		t.Fatalf("сохранённый текст не должен управлять терминалом: %q", history)
	}
}
