package term

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(func(string) (string, error) { return dir, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(m.Close)
	return m
}

// read waits for the terminal to print something containing want, and says what
// it saw instead if it does not. A shell takes a moment to draw its prompt, so
// there is no single moment at which to look.
func read(t *testing.T, updates <-chan []byte, history []byte, want string) string {
	t.Helper()
	seen := string(history)
	deadline := time.After(15 * time.Second)
	for {
		if strings.Contains(seen, want) {
			return seen
		}
		select {
		case chunk, ok := <-updates:
			if !ok {
				t.Fatalf("терминал закрылся, не сказав %q; сказал: %q", want, seen)
			}
			seen += string(chunk)
		case <-deadline:
			t.Fatalf("не дождались %q; получено: %q", want, seen)
		}
	}
}

// The whole point of the resize, and the thing that is wrong when a terminal
// wraps in the wrong place: the process has to be told the size, and `stty
// size` is the process saying what it was told. A pty that never heard keeps
// the default 80 columns while the emulator draws something else.
func TestTheProcessIsToldTheWindowSize(t *testing.T) {
	m := manager(t)
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	defer cancel()

	if err := s.Resize(123, 37); err != nil {
		t.Fatalf("размер: %v", err)
	}
	if err := s.Write([]byte("stty size\n")); err != nil {
		t.Fatalf("написать в терминал: %v", err)
	}
	read(t, updates, history, "37 123")
}

// A command runs through the shell, so what a person would type is what runs —
// pipes, globs and their own PATH included.
func TestACommandRunsThroughTheShell(t *testing.T) {
	m := manager(t)
	s, err := m.Open("card", "screen", "echo раз && echo два")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	defer cancel()
	read(t, updates, history, "два")
}

// The screen is the key, because a screen id does not change and the ribbon is
// re-read on every step the agent takes. Opening it again must not leave a
// second shell behind each time.
func TestOneScreenKeepsOneShell(t *testing.T) {
	m := manager(t)
	first, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть: %v", err)
	}
	second, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть снова: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("один экран — один шелл: %s и %s", first.ID, second.ID)
	}

	// A shell that has gone is not attached to: the screen starts a new one.
	first.Close()
	<-first.Done()
	third, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть после закрытия: %v", err)
	}
	if third.ID == first.ID {
		t.Fatal("закрытый терминал не должен выдаваться снова")
	}
}

// The socket end to end: keystrokes in as binary, a resize as text, output back
// as binary — the same three things the screen sends and receives.
func TestTheSocketCarriesKeystrokesAndSize(t *testing.T) {
	m := manager(t)
	if err := m.Listen(); err != nil {
		t.Fatalf("слушать: %v", err)
	}
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, m.Endpoint()+s.ID, nil)
	if err != nil {
		t.Fatalf("подключиться: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":91,"rows":29}`)); err != nil {
		t.Fatalf("отправить размер: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("stty size\n")); err != nil {
		t.Fatalf("отправить нажатия: %v", err)
	}

	seen := ""
	for !strings.Contains(seen, "29 91") {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("читать: %v (получено %q)", err, seen)
		}
		if kind == websocket.MessageBinary {
			seen += string(data)
		}
	}
}

// An address nobody was given does not open a terminal: the port is on loopback
// and the path carries a secret minted this run.
func TestTheSocketRefusesAWrongToken(t *testing.T) {
	m := manager(t)
	if err := m.Listen(); err != nil {
		t.Fatalf("слушать: %v", err)
	}
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrong := strings.Replace(m.Endpoint(), m.token, "не-тот-токен", 1)
	if conn, _, err := websocket.Dial(ctx, wrong+s.ID, nil); err == nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("сокет не должен открываться по чужому адресу")
	}
}

// ---- the terminal a stage is worked in ----

// Attach runs an argv directly and registers it under the run's own id: the
// screen of that step addresses the terminal by the session it is.
func TestAttachRunsTheArgvUnderTheGivenID(t *testing.T) {
	m := manager(t)
	s, err := m.Attach("run-1", "card-1", t.TempDir(), []string{"echo", "привет из шага"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	if s.ID != "run-1" || m.Get("run-1") != s {
		t.Fatalf("терминал должен зваться идентификатором запуска: %q", s.ID)
	}
	history, updates, cancel := s.Subscribe()
	defer cancel()
	read(t, updates, history, "привет из шага")

	// Asking twice hands back the same terminal rather than starting a second
	// one: the ribbon is re-read on every step the agent takes.
	again, err := m.Attach("run-1", "card-1", t.TempDir(), []string{"echo", "второй"}, nil)
	if err != nil || again != s {
		t.Fatalf("второй запрос — тот же терминал: %v", err)
	}
}

// A step whose terminal has ended is still a screen of the ribbon, and the
// ribbon is a journal: what it printed is served from the tail kept on disk.
func TestFinishedTerminalIsServedFromItsTail(t *testing.T) {
	m := manager(t)
	m.KeepIn(t.TempDir())

	s, err := m.Attach("run-2", "card-1", t.TempDir(), []string{"echo", "что было"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("echo должен был закончиться")
	}
	// forget runs in a goroutine of its own, so the tail appears a moment after
	// the process goes.
	deadline := time.After(5 * time.Second)
	for m.transcript("run-2") == nil {
		select {
		case <-deadline:
			t.Fatal("хвост закончившегося терминала не сохранился")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !strings.Contains(string(m.transcript("run-2")), "что было") {
		t.Fatalf("хвост должен нести напечатанное: %q", m.transcript("run-2"))
	}
	if m.Get("run-2") != nil {
		t.Fatal("закончившийся терминал не должен оставаться в реестре")
	}
}

// Silence is the only thing a stage in a terminal says about itself without
// being asked, so it has to be measured from the last thing drawn.
func TestQuietIsMeasuredFromTheLastOutput(t *testing.T) {
	m := manager(t)
	s, err := m.Attach("run-3", "card-1", t.TempDir(), []string{"sleep", "30"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	defer s.Close()
	time.Sleep(50 * time.Millisecond)
	if s.Quiet() < 50*time.Millisecond {
		t.Fatalf("молчащий процесс молчит: %v", s.Quiet())
	}
}

// A screen's shell is started afresh when its screen opens again, so what it
// printed has no reader: keeping it would be a file per shell, forever.
func TestScreenShellKeepsNoTail(t *testing.T) {
	m := manager(t)
	m.KeepIn(t.TempDir())

	s, err := m.Open("card-1", "screen-1", "echo шелл")
	if err != nil {
		t.Fatalf("открыть терминал экрана: %v", err)
	}
	select {
	case <-s.forgotten:
	case <-time.After(15 * time.Second):
		t.Fatal("шелл должен был закончиться и уйти из реестра")
	}
	if m.transcript(s.ID) != nil {
		t.Fatal("терминал экрана не оставляет хвоста")
	}
}

// A step running when the application closes still leaves its tail: the entry
// stays in the registry until the tail is on disk, so Close finds it and waits.
func TestCloseWaitsForTheTail(t *testing.T) {
	m := manager(t)
	m.KeepIn(t.TempDir())

	s, err := m.Attach("run-4", "card-1", t.TempDir(), []string{"sh", "-c", "echo шаг идёт; sleep 30"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	read(t, updates, history, "шаг идёт")
	cancel()

	m.Close()
	if tail := m.transcript("run-4"); !strings.Contains(string(tail), "шаг идёт") {
		t.Fatalf("после Close хвост уже должен лежать на диске: %q", tail)
	}
}
