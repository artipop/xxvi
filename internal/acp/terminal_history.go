package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/artipop/xxvi/internal/model"
	"github.com/google/uuid"
)

// TerminalHistory reads saved messages without opening a CLI. Fullscreen CLIs
// keep their conversation outside the terminal's alternate screen, which has
// no scrollback of its own. Unknown file formats leave the screen alone.
func TerminalHistory(agent model.Agent, conversation string, until time.Time) []byte {
	if _, err := uuid.Parse(conversation); err != nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var roots []string
	switch agent.Kind {
	case model.KindClaude:
		roots = []string{filepath.Join(historyHome(agent, "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")), "projects")}
	case model.KindCodex:
		root := historyHome(agent, "CODEX_HOME", filepath.Join(home, ".codex"))
		roots = []string{filepath.Join(root, "sessions"), filepath.Join(root, "archived_sessions")}
	default:
		return nil
	}
	for _, root := range roots {
		var found string
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !entry.IsDir() && (entry.Name() == conversation+".jsonl" || strings.HasSuffix(entry.Name(), "-"+conversation+".jsonl")) {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		if found == "" {
			continue
		}
		file, err := os.Open(found)
		if err != nil {
			continue
		}
		history := readTerminalHistory(file, agent.Kind, until)
		file.Close()
		if len(history) > 0 {
			return history
		}
	}
	return nil
}

func historyHome(agent model.Agent, key, fallback string) string {
	if value, ok := agent.Env[key]; ok {
		if value != "" {
			return value
		}
		return fallback
	}
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type historyMessage struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Channel   string          `json:"channel"`
	Name      string          `json:"name"`
	Content   json.RawMessage `json:"content"`
	Input     json.RawMessage `json:"input"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

type historyBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

func readTerminalHistory(reader io.Reader, kind string, until time.Time) []byte {
	scanner := bufio.NewScanner(io.LimitReader(reader, 128<<20))
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var out bytes.Buffer
	for scanner.Scan() {
		var record struct {
			Type        string         `json:"type"`
			Timestamp   string         `json:"timestamp"`
			IsMeta      bool           `json:"isMeta"`
			IsSidechain bool           `json:"isSidechain"`
			Message     historyMessage `json:"message"`
			Payload     historyMessage `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.IsMeta || record.IsSidechain {
			continue
		}
		if at, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil && !until.IsZero() && at.After(until) {
			continue
		}
		message := record.Message
		if kind == model.KindCodex {
			if record.Type != "response_item" {
				continue
			}
			message = record.Payload
		} else if record.Type != "user" && record.Type != "assistant" {
			continue
		}
		if message.Channel == "analysis" {
			continue
		}
		switch message.Type {
		case "function_call", "custom_tool_call":
			input := message.Input
			if len(input) == 0 {
				input = message.Arguments
			}
			writeHistory(&out, "⚙ "+message.Name, rawHistoryText(input))
		case "function_call_output", "custom_tool_call_output":
			writeHistory(&out, "↳", rawHistoryText(message.Output))
		default:
			if message.Role != "user" && message.Role != "assistant" {
				continue
			}
			mark := "❯"
			if message.Role == "assistant" {
				mark = "●"
			}
			var text string
			if json.Unmarshal(message.Content, &text) == nil {
				writeHistory(&out, mark, text)
				continue
			}
			var blocks []historyBlock
			if json.Unmarshal(message.Content, &blocks) != nil {
				continue
			}
			for _, block := range blocks {
				switch block.Type {
				case "text", "input_text", "output_text":
					writeHistory(&out, mark, block.Text)
				case "tool_use":
					writeHistory(&out, "⚙ "+block.Name, rawHistoryText(block.Input))
				case "tool_result":
					writeHistory(&out, "↳", rawHistoryText(block.Content))
				}
			}
		}
	}
	if out.Len() == 0 {
		return nil
	}
	lines := bytes.Split(out.Bytes(), []byte("\r\n"))
	if len(lines) > 5000 {
		lines = lines[len(lines)-5000:]
	}
	return bytes.Join(lines, []byte("\r\n"))
}

func rawHistoryText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []historyBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var texts []string
		for _, block := range blocks {
			if block.Type == "text" || block.Type == "output_text" {
				texts = append(texts, block.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}

func writeHistory(out *bytes.Buffer, mark, text string) {
	if strings.TrimSpace(text) == "" || out.Len() >= 64<<20 {
		return
	}
	// Log text is content, not a VT stream: an escape in a saved message must
	// not erase the history or change the terminal's modes on replay.
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, mark+" "+text)
	out.WriteString(strings.ReplaceAll(text, "\n", "\r\n") + "\r\n\r\n")
}
