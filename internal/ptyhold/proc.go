package ptyhold

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
)

// drainWait is how long a process's output is read after it has gone.
const drainWait = time.Second

// Spec is what to run in a new terminal. Env is the whole environment: nothing
// is inherited, because the holder's own environment is whatever the
// application had when it started the holder, possibly several versions ago.
type Spec struct {
	Argv []string `json:"argv"`
	Dir  string   `json:"dir,omitempty"`
	Env  []string `json:"env,omitempty"`
	Cols int      `json:"cols,omitempty"`
	Rows int      `json:"rows,omitempty"`
}

// Proc is one process on a pty of its own. The holder runs its sessions as
// these, and so does the application when there is no holder to run them in.
type Proc struct {
	tty       pty.Pty
	cmd       *pty.Cmd
	closeOnce sync.Once
}

// Start opens a pty and runs spec in it.
func Start(spec Spec) (*Proc, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("nothing to run")
	}
	tty, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("open a terminal: %w", err)
	}
	// Always given a size: the emulator reading this terminal is made at a size
	// too, and a pty left at whatever the system defaults to would disagree.
	_ = tty.Resize(sized(spec.Cols, spec.Rows))
	// Resolved here rather than by go-pty: on Windows it looks a bare name up
	// in the child's working folder instead of on PATH once Dir is set.
	bin := spec.Argv[0]
	if resolved, err := exec.LookPath(bin); err == nil {
		bin = resolved
	}
	cmd := tty.Command(bin, spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	if err := cmd.Start(); err != nil {
		tty.Close()
		return nil, fmt.Errorf("start %s: %w", spec.Argv[0], err)
	}
	// Our copy of the slave end would keep the pty open after the process is
	// gone, and then the reader never hears the end: it is what made reaping
	// the only signal, and reaping closes the pty on output not yet read.
	releaseSlave(tty)
	return &Proc{tty: tty, cmd: cmd}, nil
}

// sized is the size a terminal gets when it has not been told one yet.
func sized(cols, rows int) (int, int) {
	if cols < 2 || rows < 2 {
		return 80, 24
	}
	return cols, rows
}

// Pump hands out everything the process prints and returns once it has ended
// and its output has been read. out is never called after Pump returns.
func (p *Proc) Pump(out func([]byte)) {
	var mu sync.Mutex
	over := false
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 32<<10)
		for {
			n, err := p.tty.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				mu.Lock()
				if !over {
					out(chunk)
				}
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan struct{})
	go func() {
		_ = p.cmd.Wait()
		close(exited)
	}()

	select {
	case <-drained:
	case <-exited:
		// Whatever it printed last is still in the pty. A child it left behind
		// can hold the pty open forever, so the wait for it is bounded. On
		// Windows it is the only way the reader ends: a ConPTY does not say EOF
		// when its process exits.
		select {
		case <-drained:
		case <-time.After(drainWait):
		}
	}
	mu.Lock()
	over = true
	mu.Unlock()
	p.Close()
}

// Write is a keystroke on its way to the process.
func (p *Proc) Write(data []byte) error {
	_, err := p.tty.Write(data)
	return err
}

// Resize tells the process how big the window is.
func (p *Proc) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return p.tty.Resize(cols, rows)
}

// Hangup is what a terminal window closing sends. It fails where there is no
// such thing to send, and then only Kill is left.
func (p *Proc) Hangup() error {
	if p.cmd.Process == nil {
		return errors.New("not started")
	}
	return hangup(p.cmd.Process)
}

// Kill ends the process and everything it started in this terminal.
func (p *Proc) Kill() {
	if p.cmd.Process != nil {
		kill(p.cmd.Process)
	}
}

// Close lets go of the pty. Once only: the end of the output and the end of the
// process race to it, and closing a ConPTY twice while a read is in flight can
// crash rather than fail.
func (p *Proc) Close() {
	p.closeOnce.Do(func() { _ = p.tty.Close() })
}
