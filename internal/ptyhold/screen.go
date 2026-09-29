//go:build !windows

package ptyhold

import (
	"fmt"
	"strings"

	lg "go.mitchellh.com/libghostty"
)

// Screen is what a terminal shows: an emulator (libghostty) fed everything the
// process printed, and asked for its state as bytes a new emulator can be fed
// to show the same thing.
//
// Both ends of a terminal keep one: the holder, so an application started again
// finds the screen that was there, and the application itself, so a window
// opened again does. Not safe for concurrent use.
type Screen struct {
	t *lg.Terminal
}

// scrollbackLines matches the window's own emulator (frontend terminal.tsx), so
// a screen handed over is no deeper than the window would have kept. The byte
// limit is only there because libghostty has one of its own, far lower.
const (
	scrollbackLines = 5000
	scrollbackBytes = 64 << 20
)

// NewScreen is a blank screen of the given size.
func NewScreen(cols, rows int) *Screen {
	cols, rows = sized(cols, rows)
	t, err := lg.NewTerminal(
		lg.WithSize(uint16(cols), uint16(rows)),
		lg.WithMaxScrollbackLines(scrollbackLines),
		lg.WithMaxScrollbackBytes(scrollbackBytes),
	)
	if err != nil {
		// Only allocation fails here, and a terminal we cannot allocate is not
		// one we can go on without.
		panic(fmt.Sprintf("libghostty: %v", err))
	}
	return &Screen{t: t}
}

// Write is what the process printed next.
func (s *Screen) Write(chunk []byte) { s.t.VTWrite(chunk) }

// Resize follows the pty: an emulator of another size wraps and scrolls
// differently, and the screen it hands over is then not the one on the window.
func (s *Screen) Resize(cols, rows int) {
	if cols < 2 || rows < 2 {
		return
	}
	_ = s.t.Resize(uint16(cols), uint16(rows), 0, 0)
}

// Answer sends the terminal's replies to what the process asks it — the cursor
// position, what kind of terminal this is — through reply. Only while no window
// is there to answer: two answers to one question are one too many.
func (s *Screen) Answer(reply func([]byte)) {
	if reply == nil {
		s.t.SetEffectWritePty(nil)
		return
	}
	s.t.SetEffectWritePty(func(_ *lg.Terminal, data []byte) {
		reply(append([]byte(nil), data...))
	})
}

// Text is what the screen shows now, as plain text: the visible rows only,
// none of the scrollback. What a person reading it would take it to say — a
// question on screen is one whether or not any hook said so.
func (s *Screen) Text() string {
	f, err := lg.NewFormatter(s.t, lg.WithFormatterFormat(lg.FormatterFormatPlain), lg.WithFormatterTrim(true))
	if err != nil {
		return ""
	}
	defer f.Close()
	all, err := f.FormatString()
	if err != nil {
		return ""
	}
	total, err1 := s.t.TotalRows()
	rows, err2 := s.t.Rows()
	if err1 != nil || err2 != nil {
		return all
	}
	// One line per physical row, the blank ones at the bottom left out: the
	// visible rows are the last rows of total, whichever of them were written.
	lines := strings.Split(all, "\n")
	first := int(total) - int(rows)
	if first < 0 {
		first = 0
	}
	if first >= len(lines) {
		return ""
	}
	return strings.Join(lines[first:], "\n")
}

// Close frees the emulator.
func (s *Screen) Close() { s.t.Close() }

// Bytes is the screen as a new emulator of the same size must be fed it: the
// main screen with its scrollback, and on top of it the full-screen program's,
// if one is up, with the modes it set and the cursor where it stands.
func (s *Screen) Bytes() []byte {
	screen, err := s.t.ActiveScreen()
	if err != nil || screen == lg.ScreenPrimary {
		return []byte(primary(s.t))
	}
	// The formatter sees only the active screen, and the one under a
	// full-screen program is what the shell shows again when it exits. It is
	// read from a copy that has left the alternate screen; the terminal itself
	// is not touched.
	snapshot, err := s.t.Snapshot()
	if err != nil {
		return []byte(format(s.t, true))
	}
	dec, err := lg.NewSnapshotDecoderBytes(snapshot)
	if err != nil {
		return []byte(format(s.t, true))
	}
	defer dec.Close()
	under, err := dec.Decode()
	if err != nil {
		return []byte(format(s.t, true))
	}
	defer under.Close()
	under.VTWrite([]byte("\x1b[?1049l"))
	return []byte(primary(under) + format(s.t, true))
}

// primary is the main screen with its scrollback, padded to the full height:
// the formatter leaves out blank rows at the bottom, and without them
// everything lands as many rows lower than it stood.
func primary(t *lg.Terminal) string {
	body := format(t, false)
	if total, err := t.TotalRows(); err == nil {
		if written := strings.Count(body, "\r\n") + 1; uint(written) < total {
			body += strings.Repeat("\r\n", int(total)-written)
		}
	}
	x, _ := t.CursorX()
	y, _ := t.CursorY()
	return body + fmt.Sprintf("\x1b[%d;%dH", y+1, x+1)
}

func format(t *lg.Terminal, cursor bool) string {
	f, err := lg.NewFormatter(t,
		lg.WithFormatterFormat(lg.FormatterFormatVT),
		lg.WithFormatterExtraModes(true),
		lg.WithFormatterExtraCursor(cursor),
		lg.WithFormatterExtraStyle(true),
		lg.WithFormatterExtraScrollingRegion(true),
		lg.WithFormatterExtraCharsets(true),
		lg.WithFormatterExtraKeyboard(true),
		lg.WithFormatterExtraKittyKeyboard(true),
		lg.WithFormatterExtraTabstops(true),
		lg.WithFormatterExtraHyperlink(true),
	)
	if err != nil {
		return ""
	}
	defer f.Close()
	out, err := f.FormatString()
	if err != nil {
		return ""
	}
	return out
}
