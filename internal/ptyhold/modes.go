package ptyhold

import (
	"slices"
	"strconv"
	"strings"
)

// modes follows the part of a terminal's state that is not on its screen: the
// switches a process flips once, at the start, and then relies on. Bracketed
// paste, application cursor keys, mouse reporting, the alternate screen.
//
// It exists for the tail a terminal keeps (Tail). The tail is capped, and the
// cap cuts off the start — exactly where those switches were flipped. An
// emulator fed only the tail draws the same characters but sends the wrong
// bytes back: a pasted brief arrives as lines typed and submitted one by one,
// the arrows move nothing. So what was cut off is read through this, and the
// tail is handed out behind a preamble that flips the switches back.
//
// Only switches are followed. Colours and the cursor are redrawn by the tail
// itself soon enough; a mode is set once and never said again.
type modes struct {
	state parseState
	seq   []byte // the CSI being read: parameters and intermediates

	private map[int]bool // DEC private modes, by number, as last set
	keypad  bool         // application keypad (ESC =)
}

type parseState uint8

const (
	ground parseState = iota
	escape
	escapeIntermediate
	csi
	controlString // OSC, DCS, SOS, PM, APC: skipped whole
	controlStringEscape
)

// followed are the private modes a preamble restores, alternate screen first:
// switching buffers is what the rest of the switches are set inside of.
// Everything here changes what the emulator sends or where it draws; modes that
// are about how text is drawn are left to the tail.
var followed = []int{
	1049, 1047, 47, // alternate screen
	1,                                           // application cursor keys
	7,                                           // autowrap
	25,                                          // cursor visible
	2004,                                        // bracketed paste
	9, 1000, 1002, 1003, 1004, 1005, 1006, 1015, // mouse and focus reporting
}

// defaultOn are the followed modes a fresh emulator starts with set.
var defaultOn = map[int]bool{7: true, 25: true}

// feed reads bytes the terminal printed, in order. A sequence split across two
// calls is read as one.
func (m *modes) feed(b []byte) {
	for _, c := range b {
		m.step(c)
	}
}

// settled reports that the last byte fed ended whatever sequence it was part
// of: a history cut here starts with something an emulator can read.
func (m *modes) settled() bool { return m.state == ground }

func (m *modes) step(c byte) {
	// CAN and SUB abort any sequence, and ESC starts a new one, wherever the
	// parser is: that is how a terminal reads them, so it is how a stream a
	// terminal was fed has to be read.
	switch c {
	case 0x18, 0x1a:
		m.state = ground
		return
	case 0x1b:
		if m.state == controlString {
			m.state = controlStringEscape
		} else {
			m.state = escape
		}
		return
	}

	switch m.state {
	case ground:
	case escape:
		m.escape(c)
	case escapeIntermediate:
		if c >= 0x30 && c <= 0x7e {
			m.state = ground
		}
	case csi:
		switch {
		case c >= 0x20 && c <= 0x3f:
			m.seq = append(m.seq, c)
		case c >= 0x40 && c <= 0x7e:
			m.csi(c)
			m.state = ground
		}
	case controlString:
		if c == 0x07 {
			m.state = ground
		}
	case controlStringEscape:
		// ESC \ ends the string; any other ESC was the start of a sequence
		// that ended the string before it.
		if c == '\\' {
			m.state = ground
		} else {
			m.state = escape
			m.escape(c)
		}
	}
}

func (m *modes) escape(c byte) {
	switch {
	case c == '[':
		m.state, m.seq = csi, m.seq[:0]
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		m.state = controlString
	case c >= 0x20 && c <= 0x2f:
		m.state = escapeIntermediate
	case c == 'c':
		// RIS: everything back to how a new emulator starts.
		m.private, m.keypad = nil, false
		m.state = ground
	case c == '=':
		m.keypad, m.state = true, ground
	case c == '>':
		m.keypad, m.state = false, ground
	default:
		m.state = ground
	}
}

func (m *modes) csi(final byte) {
	if final != 'h' && final != 'l' {
		return
	}
	params, ok := strings.CutPrefix(string(m.seq), "?")
	if !ok || strings.ContainsFunc(params, func(r rune) bool { return r < '0' || r > ';' }) {
		return
	}
	for _, p := range strings.Split(params, ";") {
		n, err := strconv.Atoi(p)
		if err != nil || !slices.Contains(followed, n) {
			continue
		}
		if m.private == nil {
			m.private = map[int]bool{}
		}
		m.private[n] = final == 'h'
	}
}

// preamble is what brings a new emulator to these modes: only the ones that
// differ from how it starts, so a terminal that flipped nothing costs nothing.
func (m *modes) preamble() []byte {
	var b strings.Builder
	for _, n := range followed {
		on, said := m.private[n]
		if !said || on == defaultOn[n] {
			continue
		}
		b.WriteString("\x1b[?" + strconv.Itoa(n))
		if on {
			b.WriteByte('h')
		} else {
			b.WriteByte('l')
		}
	}
	if m.keypad {
		b.WriteString("\x1b=")
	}
	return []byte(b.String())
}
