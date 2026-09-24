//go:build windows

package term

import (
	"errors"
	"os"

	"github.com/aymanbagabas/go-pty"
)

// Windows has no hangup to send, so Close goes straight to the kill; and a
// ConPTY has no slave end of ours to let go of.

func hangup(*os.Process) error { return errors.New("no hangup on windows") }

func kill(p *os.Process) { _ = p.Kill() }

func releaseSlave(pty.Pty) {}
