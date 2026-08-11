//go:build !windows

package acp

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup puts the child in a process group of its own, so the whole
// tree it goes on to spawn can be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends SIGTERM to the group, then SIGKILL after grace. It polls
// with signal 0 rather than waiting, so it never competes with cmd.Wait.
func (p *process) signalGroup(grace time.Duration) {
	_ = syscall.Kill(-p.pgid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-p.pgid, 0); err != nil {
			return // the group is gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
}
