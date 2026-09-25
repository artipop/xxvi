//go:build !windows

package procgroup

import (
	"bufio"
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestKillGroupKillsGrandchildren spawns a shell that spawns a grandchild
// sleep, then verifies KillGroup takes down the whole tree: closing the app
// must leave no live plugin processes.
func TestKillGroupKillsGrandchildren(t *testing.T) {
	// The shell prints the grandchild's PID, then sleeps itself.
	script := `sleep 300 & echo $! ; sleep 300`
	proc, err := Spawn(context.Background(), []string{"/bin/sh", "-c", script}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(proc.Stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading grandchild pid: %v", err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parsing grandchild pid %q: %v", line, err)
	}
	shell := proc.Cmd.Process.Pid

	proc.KillGroup(500 * time.Millisecond)
	_ = proc.Wait() // reap

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		shellDead := syscall.Kill(shell, 0) != nil
		grandDead := syscall.Kill(grandchild, 0) != nil
		if shellDead && grandDead {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process tree survived KillGroup: shell=%d alive=%v grandchild=%d alive=%v",
		shell, syscall.Kill(shell, 0) == nil, grandchild, syscall.Kill(grandchild, 0) == nil)
}

func TestSpawnDropEnv(t *testing.T) {
	t.Setenv("ACPTEST_DROPME", "1")
	proc, err := Spawn(context.Background(), []string{"/bin/sh", "-c", `printf '%s' "${ACPTEST_DROPME:-unset}"`}, t.TempDir(), nil, "ACPTEST_DROPME")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 16)
	n, _ := proc.Stdout.Read(out)
	_ = proc.Wait()
	if got := string(out[:n]); got != "unset" {
		t.Fatalf("expected env var dropped, child saw %q", got)
	}
}
