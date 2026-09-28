//go:build !windows

package ptyhold

import (
	"os"
	"syscall"

	"github.com/aymanbagabas/go-pty"
)

// The process is started as the leader of a session of its own (go-pty sets
// Setsid), so its group is everything it started in this terminal: a CLI's
// tools and a shell's jobs go with it rather than outliving it as orphans.

func hangup(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGHUP) }

func kill(p *os.Process) {
	if syscall.Kill(-p.Pid, syscall.SIGKILL) != nil {
		_ = p.Kill()
	}
}

func releaseSlave(tty pty.Pty) {
	if u, ok := tty.(pty.UnixPty); ok {
		_ = u.Slave().Close()
	}
}
