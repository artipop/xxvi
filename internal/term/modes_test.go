package term

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

// The history is cut where a sequence and a character end, and handed out
// behind the modes its cut-off start had set: a CLI that turned on bracketed
// paste and application keys long ago still gets a paste as a paste and the
// arrows as arrows from a window opened now.
func TestCappedHistoryKeepsTheModes(t *testing.T) {
	m := manager(t)
	script := `printf '\033[?2004h\033[?1h'; L=$(printf 'щщщ \033[1mжирный\033[0m'); yes "$L" | head -n 30000; echo КОНЕЦ`
	s, err := m.Attach("run-long", "card-1", t.TempDir(), []string{"sh", "-c", script}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	<-s.Done()

	history := s.History()
	if len(history) > historyCap+64 {
		t.Fatalf("история должна быть обрезана: %d байт", len(history))
	}
	preamble := "\x1b[?1h\x1b[?2004h"
	if !strings.HasPrefix(string(history), preamble) {
		t.Fatalf("история должна начинаться с режимов отрезанного начала: %q", string(history[:40]))
	}
	rest := history[len(preamble):]
	if !utf8.Valid(rest) {
		t.Fatal("срез прошёл посреди символа")
	}
	if c := rest[0]; c == '[' || c == 'm' || (c >= '0' && c <= '9') {
		t.Fatalf("срез прошёл посреди последовательности: %q", string(rest[:20]))
	}
	if !strings.Contains(string(rest), "КОНЕЦ") {
		t.Fatal("конец вывода потерялся")
	}
}
