// Package term is the terminal behind a ribbon screen: a real pty running a
// real shell, and the bytes going both ways between it and the emulator in the
// window.
//
// Two kinds of terminal live here, and they differ by owner rather than by
// machinery. A screen's terminal belongs to the pane somebody is looking at,
// runs a shell, and outlives every turn an agent takes. A stage's terminal
// belongs to the run inside it: it is the agent's own CLI (docs/system.md
// §4.1.1), it is opened by internal/acp with an argv of its own, and it dies
// when the step does.
//
// The pty, the scrollback, the socket and the resize are the same for both,
// which is why one package holds them. What differs is who starts it and who
// ends it, and that is expressed by which door was used: Open for a screen,
// Attach for a run.
package term

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/artipop/xxvi/internal/msg"
	"github.com/aymanbagabas/go-pty"
	"github.com/google/uuid"
)

// closeGrace is how long a hung-up process has to leave on its own before it
// is killed, and drainWait how long its output is read after it has gone.
const (
	closeGrace = 2 * time.Second
	drainWait  = time.Second
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
	// cut is what the history's cap has cut off, read for the modes it set
	// (modes.go): the history is always handed out behind them.
	cut  modes
	subs map[chan []byte]struct{}
	cols int
	rows int
	// spoke is when the process last drew anything. It is what a stage in a
	// terminal is watched by: its CLI asks a person inside its own interface,
	// where nothing of ours can see the question, so silence is the only signal
	// there is (docs/system.md §4.1.1).
	spoke time.Time
	// keepTail says the tail outlives the process. Only a run's terminal has
	// one worth keeping: a screen's shell is started afresh when its screen is
	// opened again, so its tail would be a file nobody ever reads.
	keepTail bool
	// forgotten closes once the registry has let go of a dead terminal and its
	// tail, if any, is on disk — what Close waits for.
	forgotten chan struct{}

	// drained closes when everything the process printed has been read.
	drained  chan struct{}
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
	// keep is where the tail of a finished terminal is written, so a segment
	// whose step ended long ago shows the last thing that stood there rather
	// than nothing. The pty is gone; the ribbon is a journal, and a journal is
	// not erased by a process exiting.
	keep string

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
		m.forget(s.ID, s)
	}()
	return s, nil
}

// KeepIn says where the tail of a finished terminal is written. Called once, at
// startup, with a folder of the application's own — never one an agent works
// in: a file of ours inside somebody's repository is ours to clean up and
// theirs to find in `git status`.
func (m *Manager) KeepIn(dir string) {
	m.mu.Lock()
	m.keep = dir
	m.mu.Unlock()
}

// Attach starts the terminal of a run: an argv executed directly, in a folder
// the caller chose, under an id the caller already knows.
//
// Directly rather than through a shell, which is the whole difference from Open.
// A screen's terminal runs what a person types, and that is shell syntax; this
// one runs a CLI we assembled ourselves, argument by argument, and putting a
// shell between us and it would only give a folder name with a space in it a
// chance to become two arguments.
//
// The id is the caller's because the run has one already — the session the
// stage is being worked in — and a terminal with an identity of its own would
// mean the ribbon holding a second answer to "which terminal is this step".
func (m *Manager) Attach(id, cardID, dir string, argv, env []string) (*Session, error) {
	if id == "" {
		return nil, errors.New("a terminal needs an id")
	}
	if len(argv) == 0 {
		return nil, msg.Err("terminal.nothingToRun")
	}
	m.mu.Lock()
	if s, ok := m.byID[id]; ok && s.Alive() {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()

	s, err := startArgv(dir, argv, env, m.log)
	if err != nil {
		return nil, err
	}
	s.ID, s.CardID, s.Command = id, cardID, strings.Join(argv, " ")
	s.keepTail = true

	m.mu.Lock()
	m.byID[id] = s
	m.mu.Unlock()

	go func() {
		<-s.Done()
		m.forget(id, s)
	}()
	return s, nil
}

// forget takes a dead terminal out of the registry.
//
// The tail is written *before* the entry goes, and that order is the point:
// Close waits only for the terminals it can still find, so a step that ended
// while the application was closing would otherwise be removed, not waited for,
// and lose the tail its segment is shown from.
func (m *Manager) forget(id string, s *Session) {
	defer close(s.forgotten)

	m.mu.Lock()
	dir := m.keep
	m.mu.Unlock()
	if dir != "" && s.keepTail {
		m.writeTail(dir, id, s)
	}

	m.mu.Lock()
	if m.byID[id] == s {
		delete(m.byID, id)
	}
	if m.byScreen[s.ScreenID] == s {
		delete(m.byScreen, s.ScreenID)
	}
	m.mu.Unlock()
}

func (m *Manager) writeTail(dir, id string, s *Session) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.log.Warn("could not save the terminal tail", "terminal", id, "err", err)
		return
	}
	if err := os.WriteFile(transcriptPath(dir, id), s.History(), 0o600); err != nil {
		m.log.Warn("could not save the terminal tail", "terminal", id, "err", err)
	}
}

// transcript is what a finished terminal left behind, or nothing when it left
// nothing and nothing is what should be shown.
func (m *Manager) transcript(id string) []byte {
	m.mu.Lock()
	dir := m.keep
	m.mu.Unlock()
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(transcriptPath(dir, id))
	if err != nil {
		return nil
	}
	return data
}

// transcriptPath keeps an id from naming a file outside the folder: ids here are
// generated, but a path built from one is a path built from data.
func transcriptPath(dir, id string) string {
	return filepath.Join(dir, url.PathEscape(id)+".term")
}

// OnScreen finds the live terminal of one screen.
func (m *Manager) OnScreen(screenID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byScreen[screenID]
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
	closeAll(doomed)
}

// Close ends everything. Called when the application does.
func (m *Manager) Close() {
	m.mu.Lock()
	doomed := make([]*Session, 0, len(m.byID))
	for _, s := range m.byID {
		doomed = append(doomed, s)
	}
	m.mu.Unlock()
	closeAll(doomed)
	for _, s := range doomed {
		s.waitBrief()
	}
}

// closeAll hangs up on every terminal at once: each is given closeGrace, and
// giving it to them one after another would make quitting take that many times
// as long.
func closeAll(doomed []*Session) {
	var wg sync.WaitGroup
	for _, s := range doomed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Close()
		}()
	}
	wg.Wait()
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
		return nil, fmt.Errorf("open a terminal: %w", err)
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
		return nil, fmt.Errorf("start %s: %w", shell, err)
	}

	return newSession(tty, cmd, log), nil
}

// newSession wires a started process to its readers. Both doors end here: the
// pumping, the scrollback and the reaping are the same whatever was started.
func newSession(tty pty.Pty, cmd *pty.Cmd, log *slog.Logger) *Session {
	s := &Session{
		ID:        uuid.NewString(),
		tty:       tty,
		cmd:       cmd,
		subs:      map[chan []byte]struct{}{},
		done:      make(chan struct{}),
		drained:   make(chan struct{}),
		forgotten: make(chan struct{}),
		log:       log,
		spoke:     time.Now(),
	}
	// Our copy of the slave end would keep the pty open after the process is
	// gone, and then the reader never hears the end: it is what made reaping
	// the only signal, and reaping closes the pty on output not yet read.
	releaseSlave(tty)
	go s.pump()
	go func() {
		_ = cmd.Wait()
		// Whatever it printed last is still in the pty. A child it left behind
		// can hold the pty open forever, so the wait for it is bounded.
		select {
		case <-s.drained:
		case <-time.After(drainWait):
		}
		s.finish()
	}()
	return s
}

// startArgv opens a pty and runs one argv in it, with the environment the
// caller assembled: a stage's CLI is told which folder, which tools and which
// variables it must not inherit, and none of that survives a trip through a
// shell's own startup files.
func startArgv(dir string, argv, env []string, log *slog.Logger) (*Session, error) {
	tty, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("open a terminal: %w", err)
	}
	cmd := tty.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	if err := cmd.Start(); err != nil {
		tty.Close()
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	return newSession(tty, cmd, log), nil
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
	defer close(s.drained)
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
	defer s.mu.Unlock()
	s.spoke = time.Now()
	s.history = append(s.history, chunk...)
	if len(s.history) > historyCap {
		drop := len(s.history) - historyCap
		s.cut.feed(s.history[:drop])
		// The cut moves on to where a sequence or a character ends: a history
		// starting halfway through one begins with garbage printed as text.
		for drop < len(s.history) && (!s.cut.settled() || !utf8.RuneStart(s.history[drop])) {
			s.cut.step(s.history[drop])
			drop++
		}
		s.history = append([]byte(nil), s.history[drop:]...)
	}

	// Sent under the lock, because the lock is what closes these channels: a
	// viewer leaving and the process ending both close them, and a send racing
	// either is a panic that takes the application down. The sends never block,
	// so holding it costs nothing.
	for c := range s.subs {
		select {
		case c <- chunk:
		default:
			// A viewer that cannot keep up loses its subscription, not a chunk.
			// Blocking would freeze the process on one slow window, and a chunk
			// skipped is an escape sequence cut in half — a screen drawn wrong
			// for the rest of the session. A closed subscription on a live
			// terminal is how the viewer learns to start over from the history.
			delete(s.subs, c)
			close(c)
		}
	}
}

// Subscribe hands back what the terminal has printed so far and a channel of
// what it prints next. The scrollback comes first so a screen opened again is
// the screen that was there, not an empty one.
func (s *Session) Subscribe() (history []byte, updates <-chan []byte, cancel func()) {
	ch := make(chan []byte, 64)
	s.mu.Lock()
	history = s.snapshot()
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

// Quiet is how long the process has drawn nothing. It is the whole of what a
// stage in a terminal is watched by, and it says nothing about a terminal that
// has ended: a finished step is not a silent one.
func (s *Session) Quiet() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.spoke)
}

// History is what the terminal has printed so far, copied.
func (s *Session) History() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

// snapshot is the history as a new emulator must be fed it. Called with mu held.
func (s *Session) snapshot() []byte {
	preamble := s.cut.preamble()
	out := make([]byte, 0, len(preamble)+len(s.history))
	return append(append(out, preamble...), s.history...)
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

// Close ends the process and the pty. It hangs up first, the way a terminal
// window closing does, and kills only what is still there after closeGrace: a
// CLI killed outright gets no chance to save its conversation or draw its last
// frame, and a conversation it did not save is one the next visit cannot
// continue.
func (s *Session) Close() {
	if !s.Alive() {
		return
	}
	if s.cmd != nil && s.cmd.Process != nil && hangup(s.cmd.Process) == nil {
		select {
		case <-s.done:
			return
		case <-time.After(closeGrace):
		}
	}
	if s.cmd != nil && s.cmd.Process != nil {
		kill(s.cmd.Process)
	}
	_ = s.tty.Close()
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

// waitBrief gives a closing process a moment to be reaped and its tail written
// before the caller moves on. Used when the application is shutting down.
func (s *Session) waitBrief() {
	select {
	case <-s.forgotten:
	case <-time.After(2 * time.Second):
	}
}
