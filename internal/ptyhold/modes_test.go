package ptyhold

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func preambleOf(chunks ...string) string {
	var m modes
	for _, c := range chunks {
		m.feed([]byte(c))
	}
	return string(m.preamble())
}

// What a CLI flips at the start is what a tail-only emulator gets wrong, and
// the preamble is what flips it back — only where it differs from a fresh start.
func TestPreambleRestoresTheSwitches(t *testing.T) {
	got := preambleOf("\x1b[?2004h\x1b[?1h\x1b=текст\x1b[?25l\x1b[?1000;1006h")
	for _, want := range []string{"\x1b[?2004h", "\x1b[?1h", "\x1b[?25l", "\x1b[?1000h", "\x1b[?1006h", "\x1b="} {
		if !strings.Contains(got, want) {
			t.Errorf("в преамбуле нет %q: %q", want, got)
		}
	}
	if got := preambleOf("\x1b[?2004h\x1b[?2004l\x1b[?25h"); got != "" {
		t.Fatalf("режимы, вернувшиеся к исходным, не нужно восстанавливать: %q", got)
	}
}

// A sequence split between two reads of the pty is still one sequence.
func TestModesAcrossChunks(t *testing.T) {
	if got := preambleOf("abc\x1b", "[?20", "04h"); got != "\x1b[?2004h" {
		t.Fatalf("разрезанная последовательность потерялась: %q", got)
	}
}

// Sequences that are not mode switches — including a title whose text looks
// like one — change nothing.
func TestOnlyModeSwitchesCount(t *testing.T) {
	if got := preambleOf("\x1b]0;[?2004h\x07\x1b[2004h\x1b[?2004$p\x1b[31m"); got != "" {
		t.Fatalf("не переключатели режимов не должны попадать в преамбулу: %q", got)
	}
	// ESC inside a string ends the string, as it does on a terminal: what
	// follows is a sequence of its own and is carried out.
	if got := preambleOf("\x1b]0;заголовок\x1b[?2004h"); got != "\x1b[?2004h" {
		t.Fatalf("ESC внутри строки начинает новую последовательность: %q", got)
	}
	if got := preambleOf("\x1b[?2004h\x1bc"); got != "" {
		t.Fatalf("RIS сбрасывает всё: %q", got)
	}
}

// The tail is cut where a sequence and a character end, and handed out behind
// the modes its cut-off start had set.
func TestTailCutKeepsModesAndCharacters(t *testing.T) {
	tail := NewTail(1000)
	tail.Write([]byte("\x1b[?2004h\x1b[?1h"))
	line := []byte("щщщ \x1b[1mжирный\x1b[0m\n")
	for range 500 {
		tail.Write(line)
	}
	tail.Write([]byte("КОНЕЦ"))

	got := tail.Bytes()
	preamble := "\x1b[?1h\x1b[?2004h"
	if !strings.HasPrefix(string(got), preamble) {
		t.Fatalf("хвост должен начинаться с режимов отрезанного начала: %q", got[:40])
	}
	rest := got[len(preamble):]
	if len(rest) > 1000 {
		t.Fatalf("хвост должен быть обрезан: %d байт", len(rest))
	}
	if !utf8.Valid(rest) {
		t.Fatal("срез прошёл посреди символа")
	}
	if c := rest[0]; c == '[' || c == 'm' || (c >= '0' && c <= '9') {
		t.Fatalf("срез прошёл посреди последовательности: %q", rest[:20])
	}
	if !strings.HasSuffix(string(rest), "КОНЕЦ") {
		t.Fatal("конец вывода потерялся")
	}
}
