package ptyhold

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// holder runs a server in this process on a fresh socket. Under /tmp rather
// than t.TempDir(): a test's temporary folder on macOS is longer than a socket
// path may be.
func holder(t *testing.T) (string, *Server, <-chan error) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ptyhold")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "h.sock")
	srv := NewServer(socket, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- srv.Run() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("держатель не поднялся")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(srv.shutdown)
	return socket, srv, done
}

func connect(t *testing.T, socket string) *Client {
	t.Helper()
	c, err := dial(socket)
	if err != nil {
		t.Fatalf("подключиться: %v", err)
	}
	if err := c.hello(); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return c
}

// screen collects what a session printed.
type screen struct {
	mu     sync.Mutex
	seen   strings.Builder
	exited chan struct{}
}

func newScreen() *screen { return &screen{exited: make(chan struct{})} }

func (s *screen) sink() Sink {
	return Sink{
		Out: func(b []byte) {
			s.mu.Lock()
			s.seen.Write(b)
			s.mu.Unlock()
		},
		Exited: func() { close(s.exited) },
	}
}

func (s *screen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen.String()
}

func (s *screen) wait(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(s.text(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались %q; получено: %q", want, s.text())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sh(script string) Spec {
	return Spec{Argv: []string{"sh", "-c", script}, Env: append(os.Environ(), "TERM=xterm-256color")}
}

// Keystrokes and the window size go in, output comes out.
func TestASessionTalksBothWays(t *testing.T) {
	socket, _, _ := holder(t)
	c := connect(t, socket)
	defer c.Close()

	out := newScreen()
	if err := c.Start(Label{ID: "a", Kind: KindScreen}, sh(`read line; echo "эхо $line"; stty size`), out.sink()); err != nil {
		t.Fatalf("старт: %v", err)
	}
	if err := c.Resize("a", 91, 29); err != nil {
		t.Fatalf("размер: %v", err)
	}
	if err := c.Write("a", []byte("привет\n")); err != nil {
		t.Fatalf("ввод: %v", err)
	}
	out.wait(t, "эхо привет")
	out.wait(t, "29 91")
	select {
	case <-out.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("о конце сессии должны были сказать")
	}
}

// The whole point: the application goes, the session stays, and the next
// application finds it with what it printed meanwhile.
func TestASessionOutlivesTheApplication(t *testing.T) {
	socket, _, _ := holder(t)
	first := connect(t, socket)
	label := Label{ID: "shell", Kind: KindScreen, Card: "c", Screen: "s", Command: "cat"}
	out := newScreen()
	if err := first.Start(label, sh(`echo до; read x; echo "после $x"; sleep 30`), out.sink()); err != nil {
		t.Fatalf("старт: %v", err)
	}
	out.wait(t, "до")
	first.Close()
	select {
	case <-out.exited:
		t.Fatal("уход приложения — не конец сессии")
	case <-time.After(100 * time.Millisecond):
	}

	second := connect(t, socket)
	defer second.Close()
	list, err := second.List()
	if err != nil || len(list) != 1 || list[0].Label != label || !list[0].Running {
		t.Fatalf("держатель должен помнить сессию с её метками: %+v, %v", list, err)
	}
	again := newScreen()
	running, err := second.Attach("shell", again.sink())
	if err != nil || !running {
		t.Fatalf("подключиться к живой сессии: %v, %v", running, err)
	}
	// Printed before the first application left: the tail is kept all along,
	// not only while nobody watches.
	again.wait(t, "до")
	_ = second.Write("shell", []byte("снова\n"))
	again.wait(t, "после снова")
}

// A session that ends while nobody watches keeps its tail until an application
// takes it, and says it is over when it does.
func TestASessionEndingAloneKeepsItsTail(t *testing.T) {
	socket, _, _ := holder(t)
	first := connect(t, socket)
	out := newScreen()
	if err := first.Start(Label{ID: "run", Kind: KindRun}, sh(`read x; echo "последнее $x"`), out.sink()); err != nil {
		t.Fatalf("старт: %v", err)
	}
	_ = first.Write("run", []byte("слово\n"))
	out.wait(t, "последнее слово")
	first.Close()

	second := connect(t, socket)
	defer second.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, _ := second.List()
		if len(list) == 1 && !list[0].Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("сессия должна была закончиться и остаться в списке: %+v", list)
		}
		time.Sleep(20 * time.Millisecond)
	}
	tail := newScreen()
	running, err := second.Attach("run", tail.sink())
	if err != nil || running {
		t.Fatalf("законченная сессия: %v, %v", running, err)
	}
	if !strings.Contains(tail.text(), "последнее слово") {
		t.Fatalf("хвост должен прийти до ответа: %q", tail.text())
	}
	if err := second.Forget("run"); err != nil {
		t.Fatalf("забыть: %v", err)
	}
	if list, _ := second.List(); len(list) != 0 {
		t.Fatalf("забытая сессия не должна оставаться: %+v", list)
	}
}

// A hangup is heard by the process, so a CLI gets to save its work.
func TestHangupReachesTheProcess(t *testing.T) {
	socket, _, _ := holder(t)
	c := connect(t, socket)
	defer c.Close()
	out := newScreen()
	err := c.Start(Label{ID: "hup", Kind: KindRun},
		sh(`trap 'echo прощай; exit 0' HUP; echo готов; while :; do sleep 0.1; done`), out.sink())
	if err != nil {
		t.Fatalf("старт: %v", err)
	}
	out.wait(t, "готов")
	if err := c.Hangup("hup"); err != nil {
		t.Fatalf("hangup: %v", err)
	}
	select {
	case <-out.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("процесс должен был уйти сам")
	}
	if !strings.Contains(out.text(), "прощай") {
		t.Fatalf("процесс должен был успеть ответить: %q", out.text())
	}
}

// A holder with nothing to hold and nobody to serve leaves: it is a process in
// the background, and one that lingers for nothing is one nobody asked for.
func TestAnIdleHolderLeaves(t *testing.T) {
	socket, _, done := holder(t)
	c := connect(t, socket)
	out := newScreen()
	if err := c.Start(Label{ID: "x", Kind: KindScreen}, sh(`sleep 30`), out.sink()); err != nil {
		t.Fatalf("старт: %v", err)
	}
	c.Close()
	select {
	case <-done:
		t.Fatal("держатель с живой сессией должен остаться")
	case <-time.After(200 * time.Millisecond):
	}

	c = connect(t, socket)
	if err := c.Forget("x"); err != nil {
		t.Fatalf("забыть: %v", err)
	}
	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("держатель без сессий и без приложения должен уйти")
	}
}

// A second application replaces the first rather than sharing: the first is
// gone or on its way out.
func TestANewApplicationReplacesTheOld(t *testing.T) {
	socket, _, _ := holder(t)
	first := connect(t, socket)
	out := newScreen()
	if err := first.Start(Label{ID: "s", Kind: KindScreen}, sh(`sleep 30`), out.sink()); err != nil {
		t.Fatalf("старт: %v", err)
	}
	second := connect(t, socket)
	defer second.Close()
	if _, err := second.List(); err != nil {
		t.Fatalf("новое приложение должно обслуживаться: %v", err)
	}
	if _, err := first.List(); err == nil {
		t.Fatal("старое соединение должно быть закрыто")
	}
	// The old connection going is not the session ending.
	if list, _ := second.List(); len(list) != 1 || !list[0].Running {
		t.Fatalf("сессия должна жить дальше: %+v", list)
	}
}

func TestFramesSurviveTheWire(t *testing.T) {
	r, w := io.Pipe()
	go func() {
		_ = writeFrame(w, frame{typ: tOut, id: "терминал", payload: []byte("байты")})
		_ = writeFrame(w, frame{typ: tCtrl})
	}()
	f, err := readFrame(r)
	if err != nil || f.typ != tOut || f.id != "терминал" || string(f.payload) != "байты" {
		t.Fatalf("кадр: %+v, %v", f, err)
	}
	f, err = readFrame(r)
	if err != nil || f.typ != tCtrl || f.id != "" || len(f.payload) != 0 {
		t.Fatalf("пустой кадр: %+v, %v", f, err)
	}
}
