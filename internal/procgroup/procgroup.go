package procgroup

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Process is a spawned agent subprocess placed in its own process group,
// so the whole tree (e.g. claude and anything it spawns) can be killed at once.
type Process struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	pgid   int
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
	cmd.Stderr = os.Stderr
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
	return &Process{Cmd: cmd, Stdin: stdin, Stdout: stdout, pgid: cmd.Process.Pid}, nil
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

// Wait blocks until the process exits.
func (p *Process) Wait() error {
	return p.Cmd.Wait()
}
