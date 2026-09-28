package ptyhold

import "unicode/utf8"

// Tail is the end of what a terminal printed, capped, and ready to be fed to an
// emulator that has seen none of the rest.
//
// Both ends of a terminal keep one: the holder, so an application started again
// finds the screen that was there, and the application itself, so a window
// opened again does. Not safe for concurrent use.
type Tail struct {
	limit int
	data  []byte
	// cut is what the cap has cut off, read for the modes it set: the tail is
	// always handed out behind them.
	cut modes
}

// NewTail keeps at most about limit bytes.
func NewTail(limit int) *Tail { return &Tail{limit: limit} }

// Write appends what the terminal printed next.
func (t *Tail) Write(chunk []byte) {
	t.data = append(t.data, chunk...)
	if len(t.data) <= t.limit {
		return
	}
	drop := len(t.data) - t.limit
	t.cut.feed(t.data[:drop])
	// The cut moves on to where a sequence or a character ends: a tail
	// starting halfway through one begins with garbage printed as text.
	for drop < len(t.data) && (!t.cut.settled() || !utf8.RuneStart(t.data[drop])) {
		t.cut.step(t.data[drop])
		drop++
	}
	t.data = append([]byte(nil), t.data[drop:]...)
}

// Bytes is the tail as a new emulator must be fed it, copied.
func (t *Tail) Bytes() []byte {
	preamble := t.cut.preamble()
	out := make([]byte, 0, len(preamble)+len(t.data))
	return append(append(out, preamble...), t.data...)
}
