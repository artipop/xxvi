//go:build windows

package procgroup

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// setProcessGroup gives the child its own console process group. Windows has
// no process groups in the POSIX sense; this is what keeps a Ctrl-C in our
// console from reaching the agent, and it is the handle taskkill /T walks.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killGroup ends the process and everything it started. There is no signal to
// send, so this is taskkill: /T for the whole tree, first without /F so the
// agent may exit on its own, then with it once grace has passed.
//
// The wait is on a handle rather than a poll — WaitForSingleObject on the
// process — so it costs nothing and, like the unix version, never touches
// Cmd.Wait, which the session goroutine owns.
func (p *Process) killGroup(grace time.Duration) {
	pid := strconv.Itoa(p.pgid)
	_ = exec.Command("taskkill", "/T", "/PID", pid).Run()

	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(p.pgid))
	if err == nil {
		defer func() { _ = syscall.CloseHandle(handle) }()
		if event, err := syscall.WaitForSingleObject(handle, uint32(grace.Milliseconds())); err == nil && event == syscall.WAIT_OBJECT_0 {
			return // it went quietly
		}
	}

	_ = exec.Command("taskkill", "/T", "/F", "/PID", pid).Run()
}
