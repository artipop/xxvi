package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
)

const fakeDeleteEnv = "GANDVIK_FAKE_SESSION_DELETE"
const deleteSessionID = "019a1de0-8765-4321-abcd-0123456789ab"

type deleteInvocation struct {
	Args []string
	Cwd  string
	Home string
}

func fakeDelete() {
	cwd, _ := os.Getwd()
	b, _ := json.Marshal(deleteInvocation{os.Args[1:], cwd, os.Getenv("CODEX_HOME")})
	if err := os.WriteFile(os.Getenv("GANDVIK_DELETE_RECORD"), b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch os.Getenv("GANDVIK_DELETE_RESULT") {
	case "error":
		fmt.Fprintln(os.Stderr, "session is still running")
		os.Exit(1)
	case "wait":
		time.Sleep(time.Minute)
	}
}

func fakeCodexBin(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	bin, err := os.OpenFile(filepath.Join(binDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(bin, source)
	closeErr := bin.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("копия тестового CLI: %v, %v", err, closeErr)
	}
	t.Setenv("PATH", binDir)
}

func TestDeletePastSession(t *testing.T) {
	fakeCodexBin(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	cwd := t.TempDir()
	home := t.TempDir()

	for _, tc := range []struct {
		name, kind, id, result, code string
	}{
		{"success", model.KindCodex, deleteSessionID, "", ""},
		{"CLI failure", model.KindCodex, deleteSessionID, "error", "sessions.deleteFailed"},
		{"timeout", model.KindCodex, deleteSessionID, "wait", "sessions.deleteFailed"},
		{"session name", model.KindCodex, "a conversation", "", "sessions.invalidID"},
		{"flag", model.KindCodex, "--last", "", "sessions.invalidID"},
		{"empty ID", model.KindCodex, "", "", "sessions.invalidID"},
		{"other agent", model.KindVibe, deleteSessionID, "", "sessions.notDeletable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "invocation.json")
			a := model.Agent{
				Name: "Codex account", Kind: tc.kind,
				BinPath: "/not-the-vendor-cli", Command: []string{"not-the-vendor-cli"}, Args: []string{"--acp-only"},
				Env: map[string]string{fakeDeleteEnv: "1", "CODEX_HOME": home, "GANDVIK_DELETE_RECORD": record, "GANDVIK_DELETE_RESULT": tc.result},
			}
			duration := 10 * time.Second
			if tc.result == "wait" {
				duration = time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			err := DeletePastSession(ctx, a, cwd, tc.id)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || msg.Of(err).Code != tc.code {
				t.Fatalf("ожидалась %s, получено %v", tc.code, err)
			}
			if tc.result == "error" && !strings.Contains(msg.Of(err).Arg("output"), "session is still running") {
				t.Fatalf("ошибка CLI потеряна: %v", err)
			}
			if tc.result == "wait" && ctx.Err() != context.DeadlineExceeded {
				t.Fatalf("CLI должен остановиться по таймауту: %v", err)
			}
			b, readErr := os.ReadFile(record)
			if tc.code == "sessions.invalidID" || tc.code == "sessions.notDeletable" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("недопустимый запрос запустил CLI: %s, %v", b, readErr)
				}
				return
			}
			var got deleteInvocation
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			wantCwd, _ := filepath.EvalSymlinks(cwd)
			if !slices.Equal(got.Args, []string{"delete", deleteSessionID, "--force"}) || got.Home != home || got.Cwd != wantCwd {
				t.Fatalf("удаление UUID в окружении выбранного агента: %+v", got)
			}
		})
	}
}
