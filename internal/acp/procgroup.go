package acp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// An agent is a process that spawns processes: the adapter starts a CLI, the
// CLI starts tools. Killing the one we launched would leave the rest running,
// so everything goes into a process group of its own and the group is what gets
// signalled.

// process is a spawned agent subprocess in its own process group.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	pgid   int
}

// spawn starts argv in cwd with stdio pipes and its own process group. Stderr
// goes to ours, where the application log picks it up. dropEnv names variables
// removed from the child's environment.
func spawn(ctx context.Context, argv []string, cwd string, extraEnv []string, dropEnv ...string) (*process, error) {
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
	// CommandContext's own Kill would reach only the direct child; the group is
	// killed through killGroup instead.
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
	return &process{cmd: cmd, stdin: stdin, stdout: stdout, pgid: cmd.Process.Pid}, nil
}

// killGroup terminates the whole tree: politely first, by force after grace.
// It does not wait on cmd, so it never competes with the session goroutine that
// owns Wait. Safe to call more than once.
func (p *process) killGroup(grace time.Duration) {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.signalGroup(grace)
}

func (p *process) wait() error { return p.cmd.Wait() }
