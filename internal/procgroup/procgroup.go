package procgroup

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Process is a spawned agent subprocess placed in its own process group,
// so the whole tree (e.g. claude and anything it spawns) can be killed at once.
type Process struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	pgid   int
	stderr *tail
}

// Spawn starts argv in cwd with stdio pipes and its own process group.
// Stderr goes to the parent's stderr (captured in app logs). dropEnv names
// environment variables removed from the child's environment.
func Spawn(ctx context.Context, argv []string, cwd string, extraEnv []string, dropEnv ...string) (*Process, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	env := os.Environ()
	if len(dropEnv) > 0 {
		kept := env[:0]
		for _, kv := range env {
			drop := false
			for _, name := range dropEnv {
				if strings.HasPrefix(kv, name+"=") {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, kv)
			}
		}
		env = kept
	}
	cmd.Env = append(env, extraEnv...)
	// Kept as well as passed on: a process that dies before it says a word
	// on stdout has said why on stderr, and the app's log is not where a
	// person looks for the reason a source is broken.
	stderr := &tail{}
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr)
	setProcessGroup(cmd)
	// CommandContext's default Kill would only hit the direct child; the
	// manager kills the whole group via KillGroup instead.
	cmd.Cancel = func() error { return nil }

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Process{Cmd: cmd, Stdin: stdin, Stdout: stdout, pgid: cmd.Process.Pid, stderr: stderr}, nil
}

// KillGroup terminates the whole process tree the agent started: politely
// first, by force after grace. How that is spelled differs per platform
// (procgroup_unix.go, procgroup_windows.go); that it does not wait on Cmd does
// not, since the session goroutine stays the sole owner of Cmd.Wait. Safe to
// call more than once.
func (p *Process) KillGroup(grace time.Duration) {
	if p == nil || p.Cmd == nil || p.Cmd.Process == nil {
		return
	}
	p.killGroup(grace)
}

// StderrTail is the last of what the process wrote to stderr. Asked for when
// it has just gone silent, so it waits a moment for the copy to catch up: the
// process wrote its reason before it exited, but the goroutine carrying the
// pipe may not have read it yet.
func (p *Process) StderrTail() string {
	if p == nil || p.stderr == nil {
		return ""
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		if text := p.stderr.String(); text != "" || time.Now().After(deadline) {
			return text
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// tail keeps the end of a stream: enough for the last few lines, bounded so a
// chatty process cannot grow it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailLimit = 2048

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > tailLimit {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-tailLimit:]...)
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	text := strings.TrimSpace(string(t.buf))
	// The last lines are the reason; what came before is how it got there.
	lines := strings.Split(text, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, "\n")
}

// Wait blocks until the process exits.
func (p *Process) Wait() error {
	return p.Cmd.Wait()
}
