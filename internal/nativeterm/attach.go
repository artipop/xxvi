//go:build !windows

// Package nativeterm shows a terminal in a native view drawn by Ghostty
// instead of xterm.js in the page. Ghostty runs a process of its own on a pty
// of its own and has no way to be fed bytes instead, so the process it runs is
// Attach: a bridge from that pty to the terminal's socket, the same one the
// page's emulator talks to.
package nativeterm

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
)

// Attach connects this process's terminal to a terminal socket (term.Manager's
// endpoint plus an id) until the terminal ends or the socket closes.
func Attach(url string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	conn.SetReadLimit(64 << 20)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Raw: every key goes as it is, and nothing is echoed or cooked here —
	// the terminal at the other end does all of that.
	fd := int(os.Stdin.Fd())
	if old, err := unix.IoctlGetTermios(fd, unix.TIOCGETA); err == nil {
		raw := *old
		raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		raw.Oflag &^= unix.OPOST
		raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		raw.Cflag &^= unix.CSIZE | unix.PARENB
		raw.Cflag |= unix.CS8
		raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
		_ = unix.IoctlSetTermios(fd, unix.TIOCSETA, &raw)
		defer unix.IoctlSetTermios(fd, unix.TIOCSETA, old)
	}

	resize := func() {
		ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		if err != nil {
			return
		}
		msg, _ := json.Marshal(map[string]any{"type": "resize", "cols": ws.Col, "rows": ws.Row})
		_ = conn.Write(ctx, websocket.MessageText, msg)
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			resize()
		}
	}()
	resize()

	go func() {
		defer cancel()
		buf := make([]byte, 32<<10)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return nil
		}
		if kind == websocket.MessageBinary {
			if _, err := os.Stdout.Write(data); err != nil {
				return err
			}
			continue
		}
		var m struct{ Type string }
		if json.Unmarshal(data, &m) == nil && m.Type == "exit" {
			// The terminal is over, and what it last showed is the point of
			// looking at it — a step scrolled back to is shown by its tail.
			// Exiting would have Ghostty close the surface and that screen
			// with it, so the bridge stays until the view is closed and its
			// pty hangs up on it.
			cancel()
			hup := make(chan os.Signal, 1)
			signal.Notify(hup, syscall.SIGHUP, syscall.SIGTERM)
			<-hup
			return nil
		}
	}
}
