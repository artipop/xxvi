// Package term is the terminal behind a ribbon screen: a real pty running a
// real shell, and the bytes going both ways between it and the emulator in the
// window.
//
// It is a package of its own rather than part of internal/acp because the two
// terminals a person meets have different owners. An agent's terminal belongs
// to its session and dies with it; this one belongs to a screen somebody is
// looking at, and outlives every turn the agent takes.
package term

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/google/uuid"
)

// historyCap is how much of what a terminal printed is kept for a screen that
// is opened again. Enough to see how a build ended, not so much that a chatty
// process becomes the application's memory profile.
const historyCap = 256 << 10

// Session is one pty and the process in it.
type Session struct {
	ID       string
	CardID   string
	ScreenID string
	Command  string

	tty pty.Pty
	cmd *pty.Cmd

	mu      sync.Mutex
	history []byte
	subs    map[chan []byte]struct{}
	cols    int
	rows    int

	done     chan struct{}
	closeOne sync.Once
	log      *slog.Logger
}

// Manager keeps the live terminals and hands them out by screen.
type Manager struct {
	mu       sync.Mutex
	byID     map[string]*Session
	byScreen map[string]*Session

	workDir func(cardID string) (string, error)
	log     *slog.Logger

	// Where the sockets live: an address the operating system chose and a
	// secret this run minted.
	token string
	addr  string
}

// NewManager builds the registry. workDir says where a card's terminal opens —
// the same folder its agent works in, so what a person types and what the agent
// did are the same working copy.
func NewManager(workDir func(cardID string) (string, error), log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		byID:     map[string]*Session{},
		byScreen: map[string]*Session{},
		workDir:  workDir,
		log:      log,
	}
}

// Open starts the terminal of one screen, or hands back the one already
// running there.
//
// Keyed by screen rather than by anything of its own: a screen id is derived
// from the card's journal and does not change, so re-reading the ribbon — which
// happens on every step the agent takes — must not leave a second shell behind
// each time.
func (m *Manager) Open(cardID, screenID, command string) (*Session, error) {
	m.mu.Lock()
	if s, ok := m.byScreen[screenID]; ok && s.Alive() {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()

	dir, err := m.workDir(cardID)
	if err != nil {
		return nil, err
	}
	s, err := start(dir, command, m.log)
	if err != nil {
		return nil, err
	}
	s.CardID, s.ScreenID, s.Command = cardID, screenID, command

	m.mu.Lock()
	m.byID[s.ID] = s
	m.byScreen[screenID] = s
	m.mu.Unlock()

	// The registry forgets a terminal when its process goes, so a screen shown
	// again after the shell exited starts a new one rather than attaching to a
	// corpse.
	go func() {
		<-s.Done()
		m.mu.Lock()
		if m.byID[s.ID] == s {
			delete(m.byID, s.ID)
		}
		if m.byScreen[screenID] == s {
			delete(m.byScreen, screenID)
		}
		m.mu.Unlock()
	}()
	return s, nil
}

// Get finds a live terminal by id.
func (m *Manager) Get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

// CloseCard ends every terminal of one card — what taking a card off a flow
// means for the shells that were opened on it.
func (m *Manager) CloseCard(cardID string) {
	m.mu.Lock()
	var doomed []*Session
	for _, s := range m.byID {
		if s.CardID == cardID {
			doomed = append(doomed, s)
		}
	}
	m.mu.Unlock()
	for _, s := range doomed {
		s.Close()
	}
}

// Close ends everything. Called when the application does.
func (m *Manager) Close() {
	m.mu.Lock()
	doomed := make([]*Session, 0, len(m.byID))
	for _, s := range m.byID {
		doomed = append(doomed, s)
	}
	m.mu.Unlock()
	for _, s := range doomed {
		s.Close()
		s.waitBrief()
	}
}

// start opens a pty and runs a shell in it.
//
// A command is run through the shell rather than executed directly, and with a
// login shell at that: what a person types into a terminal is shell syntax —
// pipes, globs, their own aliases and PATH — and a terminal that only ran argv
// would be a terminal in name.
func start(dir, command string, log *slog.Logger) (*Session, error) {
	tty, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("открыть терминал: %w", err)
	}

	shell := loginShell()
	var args []string
	if command != "" {
		args = []string{"-l", "-c", command}
	} else {
		args = []string{"-l"}
	}

	cmd := tty.Command(shell, args...)
	cmd.Dir = dir
	// TERM is what makes a CLI draw at all; without it everything falls back to
	// dumb output and the colours and the cursor moves arrive as escape codes
	// printed literally.
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)
	if err := cmd.Start(); err != nil {
		tty.Close()
		return nil, fmt.Errorf("запустить %s: %w", shell, err)
	}

	s := &Session{
		ID:   uuid.NewString(),
		tty:  tty,
		cmd:  cmd,
		subs: map[chan []byte]struct{}{},
		done: make(chan struct{}),
		log:  log,
	}
	go s.pump()
	go func() {
		_ = cmd.Wait()
		s.finish()
	}()
	return s, nil
}

func loginShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	if _, err := exec.LookPath("zsh"); err == nil {
		return "zsh"
	}
	return "/bin/sh"
}

// pump reads the pty until it ends, keeping the tail for whoever opens the
// screen next and handing every chunk to whoever is watching now.
func (s *Session) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.tty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.publish(chunk)
		}
		if err != nil {
			s.finish()
			return
		}
	}
}

func (s *Session) publish(chunk []byte) {
	s.mu.Lock()
	s.history = append(s.history, chunk...)
	if len(s.history) > historyCap {
		s.history = append([]byte(nil), s.history[len(s.history)-historyCap:]...)
	}
	subs := make([]chan []byte, 0, len(s.subs))
	for c := range s.subs {
		subs = append(subs, c)
	}
	s.mu.Unlock()

	for _, c := range subs {
		// A viewer that cannot keep up is dropped from this chunk rather than
		// allowed to stall the pty: the terminal is a live thing, and blocking
		// its reader to spare one window would freeze the process itself.
		select {
		case c <- chunk:
		default:
		}
	}
}

// Subscribe hands back what the terminal has printed so far and a channel of
// what it prints next. The scrollback comes first so a screen opened again is
// the screen that was there, not an empty one.
func (s *Session) Subscribe() (history []byte, updates <-chan []byte, cancel func()) {
	ch := make(chan []byte, 64)
	s.mu.Lock()
	history = append([]byte(nil), s.history...)
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	return history, ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

// Write is a keystroke on its way to the process.
func (s *Session) Write(data []byte) error {
	_, err := s.tty.Write(data)
	return err
}

// Resize tells the process how wide the window is. This is the whole of why a
// terminal wraps where it should: a shell breaks its lines at the column count
// it was told, and one that was never told keeps the default 80 while the
// emulator draws something else entirely.
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	return s.tty.Resize(cols, rows)
}

// Alive reports a terminal whose process has not ended.
func (s *Session) Alive() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// Done closes when the process ends.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close ends the process and the pty.
func (s *Session) Close() {
	_ = s.tty.Close()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.finish()
}

// finish is idempotent: the reader ending and the process exiting are two
// events for one fact, and they race.
func (s *Session) finish() {
	s.closeOne.Do(func() {
		close(s.done)
		_ = s.tty.Close()

		s.mu.Lock()
		for c := range s.subs {
			delete(s.subs, c)
			close(c)
		}
		s.mu.Unlock()
	})
}

// waitBrief gives a closing process a moment to be reaped before the caller
// moves on. Used when the application is shutting down.
func (s *Session) waitBrief() {
	select {
	case <-s.done:
	case <-time.After(500 * time.Millisecond):
	}
}
