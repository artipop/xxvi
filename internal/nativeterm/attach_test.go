//go:build !windows

package nativeterm

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/ptyhold"
	"github.com/artipop/xxvi/internal/term"
)

// attachEnv makes this test binary the bridge, the way `xxvi term-attach`
// makes the application one.
const attachEnv = "XXVI_TEST_ATTACH_URL"

func TestMain(m *testing.M) {
	if url := os.Getenv(attachEnv); url != "" {
		if err := Attach(url); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The bridge end to end, in the pty Ghostty would run it in: what is typed
// there reaches the shell behind the terminal's socket, a resize there reaches
// it as the window's size, and what the shell prints comes back.
func TestTheBridgeCarriesKeysSizeAndOutput(t *testing.T) {
	dir := t.TempDir()
	m := term.NewManager(func(string) (string, error) { return dir, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(m.Close)
	if err := m.Listen(); err != nil {
		t.Fatalf("слушать: %v", err)
	}
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}

	bridge, err := ptyhold.Start(ptyhold.Spec{
		Argv: []string{os.Args[0]},
		Env:  append(os.Environ(), attachEnv+"="+m.Endpoint()+s.ID),
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("мост: %v", err)
	}
	var mu sync.Mutex
	var seen strings.Builder
	go bridge.Pump(func(b []byte) {
		mu.Lock()
		seen.Write(b)
		mu.Unlock()
	})
	t.Cleanup(bridge.Kill)
	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			mu.Lock()
			got := seen.String()
			mu.Unlock()
			if strings.Contains(got, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("не дождались %q; получено: %q", want, got)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	time.Sleep(500 * time.Millisecond)
	_ = bridge.Write([]byte("echo мост-$((6*7))\r"))
	wait("мост-42")

	_ = bridge.Resize(91, 29)
	time.Sleep(300 * time.Millisecond)
	_ = bridge.Write([]byte("stty size\r"))
	wait("29 91")
}

// A terminal that ends does not end the bridge: what it last showed stays on
// screen until the view is closed and its pty hangs up.
func TestTheBridgeOutlivesTheTerminalUntilHungUp(t *testing.T) {
	dir := t.TempDir()
	m := term.NewManager(func(string) (string, error) { return dir, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(m.Close)
	if err := m.Listen(); err != nil {
		t.Fatalf("слушать: %v", err)
	}
	s, err := m.Open("card", "screen", "echo конец-$((40+2))")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	bridge, err := ptyhold.Start(ptyhold.Spec{
		Argv: []string{os.Args[0]},
		Env:  append(os.Environ(), attachEnv+"="+m.Endpoint()+s.ID),
	})
	if err != nil {
		t.Fatalf("мост: %v", err)
	}
	var mu sync.Mutex
	var seen strings.Builder
	done := make(chan struct{})
	go func() {
		bridge.Pump(func(b []byte) {
			mu.Lock()
			seen.Write(b)
			mu.Unlock()
		})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("мост не должен уходить вместе с терминалом")
	case <-time.After(3 * time.Second):
	}
	mu.Lock()
	got := seen.String()
	mu.Unlock()
	if !strings.Contains(got, "конец-42") {
		t.Fatalf("последний экран должен быть показан: %q", got)
	}
	_ = bridge.Hangup()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("после hangup мост должен уйти")
	}
}
