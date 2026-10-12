package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

const fakeRenameEnv = "XXVI_FAKE_SESSION_RENAME"

type renameInvocation struct {
	Args     []string
	Cwd      string
	Home     string
	Requests []json.RawMessage
}

func fakeRename() {
	cwd, _ := os.Getwd()
	invocation := renameInvocation{Args: os.Args[1:], Cwd: cwd, Home: os.Getenv("CODEX_HOME")}
	writeRecord := func() {
		b, _ := json.Marshal(invocation)
		if err := os.WriteFile(os.Getenv("XXVI_RENAME_RECORD"), b, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	writeRecord()
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		invocation.Requests = append(invocation.Requests, append(json.RawMessage(nil), input.Bytes()...))
		writeRecord()
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(input.Bytes(), &req) != nil {
			os.Exit(1)
		}
		switch req.Method {
		case "initialize":
			fmt.Println(`{"method":"account/updated","params":{}}`)
			fmt.Println(`{"id":999,"result":{}}`)
			fmt.Printf(`{"id":%d,"result":{"userAgent":"fake"}}`+"\n", req.ID)
		case "initialized":
		case "thread/name/set":
			switch os.Getenv("XXVI_RENAME_RESULT") {
			case "error":
				fmt.Printf(`{"id":%d,"error":{"code":-32000,"message":"thread not found"}}`+"\n", req.ID)
			case "exit":
				os.Exit(1)
			case "wait":
				time.Sleep(time.Minute)
			default:
				fmt.Println(`{"method":"thread/name/updated","params":{"threadName":"changed"}}`)
				fmt.Printf(`{"id":%d,"result":{}}`+"\n", req.ID)
			}
		default:
			fmt.Printf(`{"id":%d,"error":{"code":-32601,"message":"must not resume or start a turn"}}`+"\n", req.ID)
		}
	}
}

func TestRenamePastSession(t *testing.T) {
	fakeCodexBin(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	cwd, home := t.TempDir(), t.TempDir()
	title := `  Новый «разговор» "с кавычками" 🦉  `
	for _, tc := range []struct{ name, kind, id, title, result, code string }{
		{"success", model.KindCodex, deleteSessionID, title, "", ""},
		{"RPC error", model.KindCodex, deleteSessionID, title, "error", "sessions.renameFailed"},
		{"closed server", model.KindCodex, deleteSessionID, title, "exit", "sessions.renameFailed"},
		{"timeout", model.KindCodex, deleteSessionID, title, "wait", "sessions.renameFailed"},
		{"empty name", model.KindCodex, deleteSessionID, " \t\n ", "", "sessions.emptyTitle"},
		{"invalid ID", model.KindCodex, "../conversation", title, "", "sessions.invalidID"},
		{"other agent", model.KindVibe, deleteSessionID, title, "", "sessions.notRenamable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "invocation.json")
			a := model.Agent{Name: "Codex account", Kind: tc.kind,
				BinPath: "/not-the-vendor-cli", Command: []string{"not-the-vendor-cli"}, Args: []string{"--acp-only"},
				Env: map[string]string{fakeRenameEnv: "1", "CODEX_HOME": home, "XXVI_RENAME_RECORD": record, "XXVI_RENAME_RESULT": tc.result},
			}
			duration := 10 * time.Second
			if tc.result == "wait" {
				duration = time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			gotTitle, err := RenamePastSession(ctx, a, cwd, tc.id, tc.title)
			if tc.code == "" {
				if err != nil || gotTitle != strings.TrimSpace(title) {
					t.Fatalf("сохранённое название: %q, %v", gotTitle, err)
				}
			} else if err == nil || msg.Of(err).Code != tc.code || gotTitle != "" {
				t.Fatalf("ожидалась %s, получено %q, %v", tc.code, gotTitle, err)
			}
			if tc.result == "error" && !strings.Contains(msg.Of(err).Arg("output"), "thread not found") {
				t.Fatalf("ошибка Codex потеряна: %v", err)
			}
			if tc.result == "wait" && !strings.Contains(msg.Of(err).Arg("output"), context.DeadlineExceeded.Error()) {
				t.Fatalf("зависший сервер должен остановиться по таймауту: %v", err)
			}
			b, readErr := os.ReadFile(record)
			if tc.code != "" && tc.code != "sessions.renameFailed" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("недопустимый запрос запустил CLI: %s, %v", b, readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			var got renameInvocation
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			wantCwd, _ := filepath.EvalSymlinks(cwd)
			if !slices.Equal(got.Args, []string{"app-server", "--listen", "stdio://"}) || got.Home != home || got.Cwd != wantCwd {
				t.Fatalf("переименование в окружении выбранного агента: %+v", got)
			}
			if len(got.Requests) != 3 {
				t.Fatalf("только initialize, initialized и смена имени: %s", b)
			}
			for i, method := range []string{"initialize", "initialized", "thread/name/set"} {
				var req struct {
					Method string `json:"method"`
					Params struct {
						ThreadID string `json:"threadId"`
						Name     string `json:"name"`
					} `json:"params"`
				}
				if err := json.Unmarshal(got.Requests[i], &req); err != nil || req.Method != method {
					t.Fatalf("запрос %d: %s, %v", i, got.Requests[i], err)
				}
				if i == 2 && (req.Params.ThreadID != deleteSessionID || req.Params.Name != strings.TrimSpace(title)) {
					t.Fatalf("UUID и название должны сохраняться без shell-экранирования: %s", got.Requests[i])
				}
			}
		})
	}
}
