//go:build windows

package ptyhold

import (
	"os/exec"
	"syscall"
)

// DETACHED_PROCESS, besides a group of its own: without it the holder shares
// the application's console, and goes when that closes.
const detachedProcess = 0x00000008

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
}
