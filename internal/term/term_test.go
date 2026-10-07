package term

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/artipop/xxvi/internal/ptyhold"
	"github.com/coder/websocket"
)

// holderEnv makes this test binary a holder, the way `xxvi pty-hold` makes the
// application one.
const holderEnv = "XXVI_TEST_PTYHOLD"

func TestMain(m *testing.M) {
	if socket := os.Getenv(holderEnv); socket != "" {
		if err := ptyhold.Serve(socket); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// holderSocket is a fresh socket path, under /tmp: a test's own temporary
// folder on macOS is longer than a socket path may be.
func holderSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "term")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "h.sock")
	t.Cleanup(func() {
		// What the holder was left holding goes with the test.
		if c, err := ptyhold.Dial(socket); err == nil {
			_ = c.Shutdown()
			c.Close()
		}
		os.RemoveAll(dir)
	})
	return socket
}

// bare is a registry with no holder: processes run in the test itself.
func bare(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	return NewManager(func(string) (string, error) { return dir, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// holding hands a registry to the holder on socket, starting one if none is there.
func holding(t *testing.T, m *Manager, socket string) {
	t.Helper()
	connect := func() (*ptyhold.Client, error) {
		return ptyhold.Connect(socket, func() *exec.Cmd {
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), holderEnv+"="+socket)
			return cmd
		})
	}
	if err := m.Hold(connect); err != nil {
		t.Fatalf("передать терминалы держателю: %v", err)
	}
}

// manager is a registry as the application has it: its terminals in a holder.
func manager(t *testing.T) *Manager {
	t.Helper()
	m := bare(t)
	holding(t, m, holderSocket(t))
	t.Cleanup(m.Close)
	return m
}

// unheld is a registry whose holder could not be started.
func unheld(t *testing.T) *Manager {
	t.Helper()
	m := bare(t)
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
	// Kept alive: a terminal whose process has ended is replaced, not handed back.
	s, err := m.Attach("run-1", "card-1", t.TempDir(), []string{"sh", "-c", "echo привет из шага; sleep 30"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	defer s.Close()
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
	if m.transcript(s.ID) != nil || m.Lost("screen-1") {
		t.Fatal("терминал экрана, закончившийся на глазах, не оставляет хвоста")
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

// The last thing a CLI prints before it exits is usually the one that matters —
// its answer, its final frame — and it is still in the pty when the process is
// reaped. Reaping must not be what closes the pty on it.
func TestTheLastOutputOutlivesTheProcess(t *testing.T) {
	m := manager(t)
	script := `i=0; while [ $i -lt 3000 ]; do echo "строка $i"; i=$((i+1)); done; echo КОНЕЦ`
	for run := 0; run < 50; run++ {
		s, err := m.Attach(fmt.Sprintf("run-tail-%d", run), "card-1", t.TempDir(), []string{"sh", "-c", script}, nil)
		if err != nil {
			t.Fatalf("открыть терминал шага: %v", err)
		}
		select {
		case <-s.Done():
		case <-time.After(15 * time.Second):
			t.Fatal("скрипт должен был закончиться")
		}
		if !strings.Contains(string(s.History()), "КОНЕЦ") {
			t.Fatalf("прогон %d: последняя строка потерялась, хвост: %q", run, tail(s.History()))
		}
	}
}

func tail(b []byte) string {
	if len(b) > 200 {
		b = b[len(b)-200:]
	}
	return string(b)
}

// Windows come and go while the process prints, and each one leaving closes a
// channel the reader may be sending to that very moment. That race used to be
// a panic, and a panic here is the whole application gone.
func TestViewersComingAndGoingDuringOutput(t *testing.T) {
	m := manager(t)
	s, err := m.Attach("run-busy", "card-1", t.TempDir(), []string{"yes", "шум"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	defer s.Close()
	for i := 0; i < 20000; i++ {
		_, _, cancel := s.Subscribe()
		cancel()
	}
}

// A viewer that stops reading is let go rather than skipped past: a skipped
// chunk is a screen drawn wrong for the rest of the session, a closed
// subscription is a signal to start over from the history.
func TestASlowViewerIsLetGoNotSkipped(t *testing.T) {
	m := manager(t)
	s, err := m.Attach("run-flood", "card-1", t.TempDir(), []string{"yes", "шум"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	defer s.Close()
	_, updates, cancel := s.Subscribe()
	defer cancel()

	time.Sleep(500 * time.Millisecond)
	deadline := time.After(15 * time.Second)
	for {
		select {
		case _, ok := <-updates:
			if !ok {
				if !s.Alive() {
					t.Fatal("подписка должна закрыться из-за отставания, а не из-за конца процесса")
				}
				return
			}
		case <-deadline:
			t.Fatal("отстающего зрителя должны были отпустить")
		}
	}
}

// Close hangs up before it kills, so a CLI gets to leave the way it would when
// its window is closed: saving what it has and drawing its last frame.
func TestCloseHangsUpBeforeKilling(t *testing.T) {
	m := manager(t)
	s, err := m.Attach("run-hup", "card-1", t.TempDir(),
		[]string{"sh", "-c", `trap 'echo прощай; exit 0' HUP; echo готов; while :; do sleep 0.1; done`}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	read(t, updates, history, "готов")
	cancel()

	started := time.Now()
	s.Close()
	if took := time.Since(started); took >= closeGrace {
		t.Fatalf("процесс, ушедший сам, не должен ждать убийства: %v", took)
	}
	if !strings.Contains(string(s.History()), "прощай") {
		t.Fatalf("процесс должен был успеть ответить на hangup: %q", tail(s.History()))
	}
}

// A window opened on a long-running terminal gets its screen, not its whole
// output: the history is as deep as the window's own scrollback, and carries the
// modes the process set long ago — a paste still arrives as a paste and the
// arrows as arrows.
func TestHistoryIsTheScreenWithItsModes(t *testing.T) {
	m := manager(t)
	script := `printf '\033[?2004h\033[?1h'; L=$(printf 'щщщ \033[1mжирный\033[0m'); yes "$L" | head -n 30000; echo КОНЕЦ`
	s, err := m.Attach("run-long", "card-1", t.TempDir(), []string{"sh", "-c", script}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	<-s.Done()

	history := string(s.History())
	for _, mode := range []string{"\x1b[?2004h", "\x1b[?1h"} {
		if !strings.Contains(history, mode) {
			t.Fatalf("в истории нет режима %q: %q", mode, history[:min(80, len(history))])
		}
	}
	if !utf8.ValidString(history) {
		t.Fatal("история должна быть целым текстом")
	}
	if lines := strings.Count(history, "\n"); lines > 5000+24 {
		t.Fatalf("история глубже прокрутки окна: %d строк", lines)
	}
	if !strings.Contains(history, "КОНЕЦ") {
		t.Fatal("конец вывода потерялся")
	}
}

func TestACompletedFullScreenTerminalKeepsScrollableHistory(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprintf("holder=%t", held), func(t *testing.T) {
			m := unheld(t)
			if held {
				holding(t, m, holderSocket(t))
			}
			m.KeepIn(t.TempDir())
			m.HistoryFrom(func(id string) []byte {
				if id == "fullscreen" {
					return []byte("сохранённый вопрос\r\nсохранённый ответ\r\n")
				}
				return nil
			})
			if err := m.Listen(); err != nil {
				t.Fatal(err)
			}
			script := `printf 'история до CLI\r\n'; printf '\033[?1049h\033[?1000h\033[H\033[2Jпоследний кадр\r\n'; read line`
			s, err := m.Attach("fullscreen", "card", t.TempDir(), []string{"sh", "-c", script}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, m.Endpoint()+s.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(websocket.StatusNormalClosure, "")
			window := ptyhold.NewScreen(80, 24)
			defer window.Close()
			for !strings.Contains(window.Text(), "последний кадр") {
				kind, data, err := conn.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if kind == websocket.MessageBinary {
					window.Write(data)
				}
			}
			if err := conn.Write(ctx, websocket.MessageBinary, []byte("\n")); err != nil {
				t.Fatal(err)
			}
			readEnd := func(conn *websocket.Conn, window *ptyhold.Screen) {
				t.Helper()
				for {
					kind, data, err := conn.Read(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if kind == websocket.MessageBinary {
						window.Write(data)
					} else if strings.Contains(string(data), "exit") {
						break
					}
				}
				got := string(window.Bytes())
				if strings.Contains(got, "\x1b[?1049h") || strings.Contains(got, "\x1b[?1000h") {
					t.Fatalf("законченный терминал должен разрешать прокрутку и выделение: %q", got)
				}
				if !strings.Contains(got, "история до CLI") || !strings.Contains(got, "последний кадр") || !strings.Contains(got, "сохранённый вопрос") || !strings.Contains(got, "сохранённый ответ") {
					t.Fatalf("история и последний кадр должны сохраниться: %q", got)
				}
			}
			readEnd(conn, window)
			select {
			case <-s.forgotten:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			again, _, err := websocket.Dial(ctx, m.Endpoint()+s.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close(websocket.StatusNormalClosure, "")
			replay := ptyhold.NewScreen(80, 24)
			defer replay.Close()
			readEnd(again, replay)
		})
	}
}

func TestAnOlderFullScreenTailCanBeScrolled(t *testing.T) {
	m := unheld(t)
	dir := t.TempDir()
	m.KeepIn(dir)
	m.HistoryFrom(func(id string) []byte {
		if id != "old" {
			t.Errorf("история должна читаться по id шага, получен %q", id)
			return nil
		}
		return []byte("первый вопрос\r\nпервый ответ\r\n")
	})
	if err := m.Listen(); err != nil {
		t.Fatal(err)
	}
	screen := ptyhold.NewScreen(120, 5)
	defer screen.Close()
	screen.Write([]byte("история до CLI\r\n1\r\n2\r\n3\r\n4\r\n5\r\n"))
	frame := "последний кадр " + strings.Repeat("я", 90)
	screen.Write([]byte("\x1b[?1049h\x1b[?1000h\x1b[H\x1b[2J" + frame))
	if err := os.WriteFile(transcriptPath(dir, "old"), screen.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, m.Endpoint()+"old", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":5}`)); err != nil {
		t.Fatal(err)
	}
	window := ptyhold.NewScreen(120, 5)
	defer window.Close()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.MessageBinary {
			window.Write(data)
		} else if strings.Contains(string(data), "exit") {
			break
		}
	}
	got := string(window.Bytes())
	if strings.Contains(got, "\x1b[?1049h") || strings.Contains(got, "\x1b[?1000h") || !strings.Contains(got, "история до CLI") || !strings.Contains(got, frame) || !strings.Contains(got, "первый вопрос") || !strings.Contains(got, "первый ответ") {
		t.Fatalf("старый снимок должен читаться с историей и последним кадром без переноса строки: %q", got)
	}
}

// ---- the holder ----

// The point of the holder: the application closes, the shell of a screen does
// not, and the next start finds it with what it printed — and what it printed
// meanwhile.
func TestAScreenOutlivesTheApplication(t *testing.T) {
	socket := holderSocket(t)
	first := bare(t)
	holding(t, first, socket)
	s, err := first.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	_ = s.Write([]byte("echo до-$((40+2)); sleep 1; echo после-$((40+3))\n"))
	read(t, updates, history, "до-42")
	cancel()
	first.Close()
	if !s.Alive() {
		t.Fatal("закрытие приложения не закрывает шелл экрана")
	}

	second := bare(t)
	holding(t, second, socket)
	t.Cleanup(second.Close)
	again := second.OnScreen("screen")
	if again == nil || again.ID != s.ID || again.CardID != "card" {
		t.Fatalf("экран должен найти свой шелл: %+v", again)
	}
	if opened, err := second.Open("card", "screen", ""); err != nil || opened != again {
		t.Fatalf("Open должен вернуть тот же шелл, а не начать новый: %v", err)
	}
	history, updates, cancel = again.Subscribe()
	defer cancel()
	seen := read(t, updates, history, "после-43")
	if !strings.Contains(seen, "до-42") {
		t.Fatalf("напечатанное до перезапуска должно остаться: %q", seen)
	}
	if err := again.Resize(100, 30); err != nil {
		t.Fatalf("размер: %v", err)
	}
	_ = again.Write([]byte("stty size\n"))
	read(t, updates, history, "30 100")
}

// A stage's CLI does not outlive the application: nothing would be there to
// take its report. Its tail is kept as it would be for any ended step.
func TestARunEndsWithTheApplication(t *testing.T) {
	socket := holderSocket(t)
	m := bare(t)
	m.KeepIn(t.TempDir())
	holding(t, m, socket)
	s, err := m.Attach("run-q", "card", t.TempDir(), []string{"sh", "-c", "echo работаю; sleep 30"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	read(t, updates, history, "работаю")
	cancel()
	m.Close()
	if s.Alive() {
		t.Fatal("терминал шага должен закрыться вместе с приложением")
	}
	if tail := m.transcript("run-q"); !strings.Contains(string(tail), "работаю") {
		t.Fatalf("хвост шага должен остаться: %q", tail)
	}
	c, err := ptyhold.Dial(socket)
	if err != nil {
		return // the holder had nothing left and went: that is fine too
	}
	defer c.Close()
	if list, _ := c.List(); len(list) != 0 {
		t.Fatalf("в держателе не должно остаться сессий: %+v", list)
	}
}

// An application that did not close properly leaves a stage's CLI running in
// the holder. The next start ends it — its stage was marked cancelled — and
// keeps its tail.
func TestARunLeftBehindIsEndedAndItsTailKept(t *testing.T) {
	socket := holderSocket(t)
	keep := t.TempDir()
	first := bare(t)
	first.KeepIn(keep)
	holding(t, first, socket)
	s, err := first.Attach("run-crash", "card", t.TempDir(),
		[]string{"sh", "-c", `trap 'echo сохраняю; exit 0' HUP; echo работаю; while :; do sleep 0.1; done`}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	read(t, updates, history, "работаю")
	cancel()
	first.holder.Close() // the application gone without its Close

	second := bare(t)
	second.KeepIn(keep)
	holding(t, second, socket)
	t.Cleanup(second.Close)
	adopted := second.Get("run-crash")
	if adopted == nil {
		t.Fatal("оставленный шаг должен найтись")
	}
	select {
	case <-adopted.forgotten:
	case <-time.After(10 * time.Second):
		t.Fatal("оставленный шаг должен быть закрыт")
	}
	if tail := second.transcript("run-crash"); !strings.Contains(string(tail), "сохраняю") {
		t.Fatalf("CLI должен был услышать hangup, а хвост — остаться: %q", tail)
	}
}

// With no holder the terminals still work — they just end with the application.
func TestWithoutAHolderTerminalsRunHere(t *testing.T) {
	m := unheld(t)
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	defer cancel()
	if err := s.Resize(77, 21); err != nil {
		t.Fatalf("размер: %v", err)
	}
	_ = s.Write([]byte("stty size\n"))
	read(t, updates, history, "21 77")
	m.Close()
	if s.Alive() {
		t.Fatal("без держателя шелл закрывается вместе с приложением")
	}
}

func TestWithoutAHolderTheLastOutputOutlivesTheProcess(t *testing.T) {
	m := unheld(t)
	s, err := m.Attach("run-local", "card-1", t.TempDir(),
		[]string{"sh", "-c", `i=0; while [ $i -lt 3000 ]; do echo "строка $i"; i=$((i+1)); done; echo КОНЕЦ`}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	<-s.Done()
	if !strings.Contains(string(s.History()), "КОНЕЦ") {
		t.Fatalf("последняя строка потерялась: %q", tail(s.History()))
	}
}

// A connection that breaks with the holder still alive — it gives up on an
// application that stopped reading — loses nothing: the terminals are taken
// back, with what they printed meanwhile, and keep working.
func TestTerminalsComeBackWhenTheConnectionBreaks(t *testing.T) {
	m := manager(t)
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	_ = s.Write([]byte("echo до-$((40+2))\n"))
	read(t, updates, history, "до-42")
	cancel()

	lost := m.client()
	lost.Drop()
	deadline := time.Now().Add(10 * time.Second)
	for m.client() == nil || m.client() == lost {
		if time.Now().After(deadline) {
			t.Fatal("соединение с держателем должно было восстановиться")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !s.Alive() || m.OnScreen("screen") != s {
		t.Fatal("терминал должен пережить разрыв соединения")
	}
	history, updates, cancel = s.Subscribe()
	defer cancel()
	if !strings.Contains(string(history), "до-42") {
		t.Fatalf("экран должен вернуться от держателя: %q", tail(history))
	}
	_ = s.Write([]byte("echo после-$((40+3))\n"))
	read(t, updates, history, "после-43")
}

// A holder that dies takes its terminals with it; the registry says so, starts
// a new holder, and terminals opened after still outlive the application.
func TestAHolderThatDiesIsReplaced(t *testing.T) {
	m := manager(t)
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	old := m.client()
	proc, err := os.FindProcess(old.Pid())
	if err != nil {
		t.Fatalf("найти держателя: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("убить держателя: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("терминал умершего держателя должен закончиться")
	}
	deadline := time.Now().Add(10 * time.Second)
	for m.client() == nil || m.client() == old {
		if time.Now().After(deadline) {
			t.Fatal("должен был подняться новый держатель")
		}
		time.Sleep(20 * time.Millisecond)
	}
	again, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал заново: %v", err)
	}
	if _, inHolder := again.eng.(held); !inHolder {
		t.Fatal("новый терминал должен жить в новом держателе, а не в приложении")
	}
}

// A terminal shown in two windows takes the size both have room for, and grows
// back when the smaller one closes.
func TestTwoWindowsAgreeOnTheSmallerSize(t *testing.T) {
	m := manager(t)
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	history, updates, cancel := s.Subscribe()
	defer cancel()
	big, small := s.View(), s.View()
	_ = big.Resize(120, 40)
	_ = small.Resize(90, 50)
	_ = s.Write([]byte("stty size\n"))
	read(t, updates, history, "40 90")

	small.Close()
	_ = s.Write([]byte("stty size\n"))
	read(t, updates, history, "40 120")
	big.Close()
}

// Esc or Ctrl+C on their own are a person stopping the process; an arrow,
// which starts with the same byte, is not.
func TestInterruptsAreTheKeysOnTheirOwn(t *testing.T) {
	m := unheld(t)
	s, err := m.Open("card", "screen", "cat")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	_ = s.Write([]byte("\x1b[A"))
	select {
	case <-s.Interrupts():
		t.Fatal("стрелка — не прерывание")
	case <-time.After(100 * time.Millisecond):
	}
	_ = s.Write([]byte("\x1b"))
	select {
	case <-s.Interrupts():
	case <-time.After(time.Second):
		t.Fatal("Esc должен быть замечен")
	}
}

// What the last run left running is listed once the application starts again,
// and stopping it ends it — only it, not what this run started.
func TestTerminalsLeftOverAreListedAndStopped(t *testing.T) {
	socket := holderSocket(t)
	first := bare(t)
	holding(t, first, socket)
	if _, err := first.Open("card", "old", ""); err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	first.Close()

	second := bare(t)
	holding(t, second, socket)
	t.Cleanup(second.Close)
	fresh, err := second.Open("card", "new", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	left := second.LeftOver()
	if len(left) != 1 || left[0].ScreenID != "old" {
		t.Fatalf("оставшимся с прошлого запуска считается только старый терминал: %+v", left)
	}
	second.StopLeftOver()
	if left[0].Alive() {
		t.Fatal("старый терминал должен быть остановлен")
	}
	if len(second.LeftOver()) != 0 || !fresh.Alive() {
		t.Fatal("остановка оставшихся не трогает терминалы этого запуска")
	}
}

// Quitting with nothing left behind ends even what a plain quit would leave
// running.
func TestStopAllEndsWhatWouldOutliveTheApplication(t *testing.T) {
	m := manager(t)
	s, err := m.Open("card", "screen", "")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	m.StopAll()
	if s.Alive() {
		t.Fatal("после «остановить всё» шелл не живёт")
	}
}

// A run whose process went down with the application left no tail, and the
// ribbon must say so rather than open a socket that answers 404.
func TestKnownIsLiveOrKept(t *testing.T) {
	m := manager(t)
	m.KeepIn(t.TempDir())

	if m.Known("run-lost") {
		t.Fatal("терминал, которого не было и чей хвост не записан, не должен считаться известным")
	}
	s, err := m.Attach("run-k", "card-1", t.TempDir(), []string{"echo", "было"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	if !m.Known("run-k") {
		t.Fatal("идущий терминал известен")
	}
	select {
	case <-s.forgotten:
	case <-time.After(15 * time.Second):
		t.Fatal("echo должен был закончиться")
	}
	if !m.Known("run-k") {
		t.Fatal("закончившийся терминал с хвостом известен")
	}
}

// The tail is on disk while the step still runs: an application that goes down
// together with the holder leaves nobody to write it at the end.
func TestRunningTerminalKeepsItsTailOnDisk(t *testing.T) {
	m := manager(t)
	m.KeepIn(t.TempDir())

	s, err := m.Attach("run-live", "card-1", t.TempDir(), []string{"sh", "-c", "echo на лету; sleep 30"}, nil)
	if err != nil {
		t.Fatalf("открыть терминал шага: %v", err)
	}
	t.Cleanup(s.Close)
	deadline := time.After(3 * tailEvery)
	for !strings.Contains(string(m.transcript("run-live")), "на лету") {
		select {
		case <-deadline:
			t.Fatal("хвост идущего терминала должен появиться на диске, пока он идёт")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !s.Alive() {
		t.Fatal("терминал должен быть ещё жив — хвост записан по ходу, а не в конце")
	}
}

// A screen whose terminal went down with the holder keeps its last screen, and
// its command is not run again until somebody opens it anew — as zellij does
// on resurrection.
func TestAScreenLostWithTheHolderKeepsItsLastScreen(t *testing.T) {
	m := manager(t)
	m.KeepIn(t.TempDir())
	s, err := m.Open("card", "screen", "echo было на экране; sleep 30")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	for !strings.Contains(string(s.History()), "было на экране") {
		time.Sleep(20 * time.Millisecond)
	}
	proc, err := os.FindProcess(m.client().Pid())
	if err != nil {
		t.Fatalf("найти держателя: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("убить держателя: %v", err)
	}
	select {
	case <-s.forgotten:
	case <-time.After(10 * time.Second):
		t.Fatal("терминал умершего держателя должен закончиться")
	}
	if !m.Lost("screen") {
		t.Fatal("экран, потерянный с держателем, должен это помнить")
	}
	if !strings.Contains(string(m.transcript(LostID("screen"))), "было на экране") {
		t.Fatal("сохранённый экран должен нести напечатанное")
	}

	again, err := m.Open("card", "screen", "echo заново; sleep 30")
	if err != nil {
		t.Fatalf("открыть заново: %v", err)
	}
	t.Cleanup(again.Close)
	if m.Lost("screen") {
		t.Fatal("новый запуск заменяет потерянный экран")
	}
}

// Without a holder a screen's shell ends with the application — because the
// application closed, not because anybody asked — and is kept as lost.
func TestWithoutAHolderAScreenClosedWithTheApplicationIsLost(t *testing.T) {
	m := unheld(t)
	m.KeepIn(t.TempDir())
	s, err := m.Open("card", "screen", "echo до выхода; sleep 30")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	for !strings.Contains(string(s.History()), "до выхода") {
		time.Sleep(20 * time.Millisecond)
	}
	m.Close()
	if !m.Lost("screen") {
		t.Fatal("экран, закрытый вместе с приложением, должен остаться потерянным, а не законченным")
	}
}

// The lost screen is served through the socket like any finished terminal —
// under an escaped address, since a screen id carries «|».
func TestALostScreenIsServedThroughTheSocket(t *testing.T) {
	m := unheld(t)
	m.KeepIn(t.TempDir())
	if err := m.Listen(); err != nil {
		t.Fatalf("слушать: %v", err)
	}
	screen := "42|terminal"
	s, err := m.Open("card", screen, "echo последний экран; sleep 30")
	if err != nil {
		t.Fatalf("открыть терминал: %v", err)
	}
	for !strings.Contains(string(s.History()), "последний экран") {
		time.Sleep(20 * time.Millisecond)
	}
	m.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, m.Endpoint()+url.PathEscape(LostID(screen)), nil)
	if err != nil {
		t.Fatalf("подключиться к потерянному экрану: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	seen, exited := "", false
	for !exited {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("читать: %v (получено %q)", err, seen)
		}
		if kind == websocket.MessageBinary {
			seen += string(data)
		} else {
			exited = strings.Contains(string(data), "exit")
		}
	}
	if !strings.Contains(seen, "последний экран") {
		t.Fatalf("сокет должен отдать последний экран: %q", seen)
	}
}
