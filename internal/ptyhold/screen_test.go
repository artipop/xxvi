package ptyhold

import (
	"strings"
	"testing"
	"time"

	lg "go.mitchellh.com/libghostty"
)

// replay feeds a screen's bytes to a fresh screen of the same size and reads it
// back as plain text, row by row — what a window opened on it would show.
func replay(t *testing.T, s *Screen, cols, rows int) *Screen {
	t.Helper()
	fresh := NewScreen(cols, rows)
	t.Cleanup(fresh.Close)
	fresh.Write(s.Bytes())
	return fresh
}

func TestAReadOnlyFullScreenFrameCanBeScrolled(t *testing.T) {
	s := NewScreen(40, 5)
	defer s.Close()
	s.Write([]byte("давний вывод\r\n1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n"))
	s.Write([]byte("\x1b[?1049h\x1b[?1000h\x1b[H\x1b[2J\x1b[31mпоследний кадр\x1b[0m\r\nответ агента"))

	window := NewScreen(40, 5)
	defer window.Close()
	window.Write(s.ReadOnlyBytes())
	if active, err := window.t.ActiveScreen(); err != nil || active != lg.ScreenPrimary {
		t.Fatalf("законченный экран должен иметь обычную прокрутку: %v (%v)", active, err)
	}
	if got := plain(window); !strings.Contains(got, "давний вывод") || !strings.Contains(got, "последний кадр") || !strings.Contains(got, "ответ агента") {
		t.Fatalf("история и последний кадр должны сохраняться: %q", got)
	}
	if got := window.Text(); !strings.Contains(got, "последний кадр") {
		t.Fatalf("последний кадр должен быть видим, а не уйти в прокрутку: %q", got)
	}
	if active, _ := s.t.ActiveScreen(); active != lg.ScreenAlternate {
		t.Fatal("сохранение хвоста не должно менять живой терминал")
	}
	if got := string(window.Bytes()); strings.Contains(got, "\x1b[?1000h") || !strings.Contains(got, "\x1b[38;5;1m") {
		t.Fatalf("снимок сохраняет цвет, но не захватывает мышь: %q", got)
	}
}

func TestAFullScreenFrameDoesNotInventEmptyHistory(t *testing.T) {
	s := NewScreen(40, 5)
	defer s.Close()
	s.Write([]byte("\x1b[?1049h\x1b[H\x1b[2Jпоследний кадр"))
	window := NewScreen(40, 5)
	defer window.Close()
	window.Write(s.ReadOnlyBytes())
	if total, _ := window.t.TotalRows(); total != 5 {
		t.Fatalf("пустой экран под CLI не должен становиться историей: %d строк вместо 5", total)
	}
}

func TestSavedMessagesAreVisibleWhenScrollingAFullScreenFrame(t *testing.T) {
	s := NewScreen(40, 5)
	defer s.Close()
	s.Write([]byte("\x1b[?1049h\x1b[H\x1b[2Jпоследний кадр"))
	window := NewScreen(40, 5)
	defer window.Close()
	window.Write(s.ReadOnlyWithHistory([]byte("первый вопрос\r\nпервый ответ\r\n1\r\n2\r\n3\r\n4\r\n5\r\n")))
	window.t.ScrollViewportTop()
	if got := renderedText(t, window); !strings.Contains(got, "первый вопрос") || !strings.Contains(got, "первый ответ") {
		t.Fatalf("прокрутка вверх должна рисовать сообщения, а не пустые строки: %q", got)
	}
	window.t.ScrollViewportBottom()
	if got := renderedText(t, window); !strings.Contains(got, "последний кадр") {
		t.Fatalf("внизу должен оставаться последний экран CLI: %q", got)
	}
}

func renderedText(t *testing.T, screen *Screen) string {
	t.Helper()
	state, err := lg.NewRenderState()
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.Update(screen.t); err != nil {
		t.Fatal(err)
	}
	rows, err := lg.NewRenderStateRowIterator()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cells, err := lg.NewRenderStateRowCells()
	if err != nil {
		t.Fatal(err)
	}
	defer cells.Close()
	if err := state.RowIterator(rows); err != nil {
		t.Fatal(err)
	}
	var text []byte
	for rows.Next() {
		if err := rows.Cells(cells); err != nil {
			t.Fatal(err)
		}
		for cells.Next() {
			var err error
			text, err = cells.AppendGraphemes(text)
			if err != nil {
				t.Fatal(err)
			}
		}
		text = append(text, '\n')
	}
	return string(text)
}

// The shell's screen under a full-screen program is what comes back when the
// program exits, so a screen handed over mid-program has to carry it too.
func TestTheScreenUnderAFullScreenProgramIsKept(t *testing.T) {
	s := NewScreen(40, 5)
	defer s.Close()
	s.Write([]byte("$ ls\r\nпервый\r\nвторой\r\n$ vim\r\n"))
	s.Write([]byte("\x1b[?1049h\x1b[H\x1b[2Jредактор"))

	window := replay(t, s, 40, 5)
	if got := string(window.Bytes()); !strings.Contains(got, "редактор") {
		t.Fatalf("окно должно показывать программу: %q", got)
	}
	// The program exits, in the window and in the original alike.
	s.Write([]byte("\x1b[?1049l$ "))
	window.Write([]byte("\x1b[?1049l$ "))
	want, got := plain(s), plain(window)
	if got != want {
		t.Fatalf("после выхода из программы экран шелла должен быть прежним:\nждали %q\nвышло %q", want, got)
	}
	if !strings.Contains(got, "второй") {
		t.Fatalf("вывод шелла до программы потерялся: %q", got)
	}
}

// A cursor on an empty last row — the line after a command was entered — is
// where the next output lands; a screen handed over without that row puts
// everything one row lower than it stood.
func TestBlankRowsAtTheBottomAreKept(t *testing.T) {
	s := NewScreen(20, 4)
	defer s.Close()
	for _, l := range []string{"а", "б", "в", "г", "д"} {
		s.Write([]byte(l + "\r\n"))
	}
	window := replay(t, s, 20, 4)
	s.Write([]byte("е"))
	window.Write([]byte("е"))
	if want, got := plain(s), plain(window); got != want {
		t.Fatalf("экран сдвинулся:\nждали %q\nвышло %q", want, got)
	}
}

func plain(s *Screen) string {
	f := format(s.t, false)
	var b strings.Builder
	inEsc := false
	for _, r := range f {
		switch {
		case r == 0x1b:
			inEsc = true
		case inEsc && (r >= '@' && r <= '~') && r != '[':
			inEsc = false
		case !inEsc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// A program that asks its terminal where the cursor is while no application is
// attached gets its answer from the holder, instead of hanging until it gives
// up.
func TestTheHolderAnswersWhileNobodyWatches(t *testing.T) {
	socket, _, _ := holder(t)
	first := connect(t, socket)
	out := newScreen()
	script := `stty -icanon -echo; read x; printf '\033[6n'; pos=$(dd bs=1 count=6 2>/dev/null); echo; echo "ответ:${pos#?}"; sleep 30`
	if err := first.Start(Label{ID: "q", Kind: KindScreen}, sh(script), out.sink()); err != nil {
		t.Fatalf("старт: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = first.Write("q", []byte("\n"))
	first.Close()

	time.Sleep(1500 * time.Millisecond)
	second := connect(t, socket)
	defer second.Close()
	again := newScreen()
	if _, err := second.Attach("q", again.sink()); err != nil {
		t.Fatalf("подключиться: %v", err)
	}
	again.wait(t, "ответ:[")
}

// Text is the visible screen only: a question answered and scrolled away is no
// longer on it.
func TestTextIsTheVisibleScreenOnly(t *testing.T) {
	s := NewScreen(20, 3)
	defer s.Close()
	s.Write([]byte("давний вопрос\r\nа\r\nб\r\nв\r\nг"))
	got := s.Text()
	if strings.Contains(got, "давний") {
		t.Fatalf("ушедшее в прокрутку не на экране: %q", got)
	}
	if !strings.Contains(got, "в") || !strings.Contains(got, "г") {
		t.Fatalf("видимые строки должны быть: %q", got)
	}
	blank := NewScreen(20, 3)
	defer blank.Close()
	blank.Write([]byte("одна\r\n"))
	if got := blank.Text(); !strings.Contains(got, "одна") {
		t.Fatalf("строка над пустыми внизу на экране: %q", got)
	}
}
