//go:build !windows

package procgroup

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

// killGroup sends SIGTERM to the group, then SIGKILL after grace. It polls
// with signal 0 rather than waiting, so it never competes with Cmd.Wait.
func (p *Process) killGroup(grace time.Duration) {
	_ = syscall.Kill(-p.pgid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-p.pgid, 0); err != nil {
			return // group is gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
}
