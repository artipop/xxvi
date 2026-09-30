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
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/ptyhold"
	"github.com/google/uuid"
)

// closeGrace is how long a hung-up process has to leave on its own before it
// is killed, and killWait how long a killed one has to be heard ending.
const (
	closeGrace = 2 * time.Second
	killWait   = 2 * time.Second
)

// Session is one pty and the process in it.
type Session struct {
	ID       string
	CardID   string
	ScreenID string
	Command  string

	eng engine

	mu sync.Mutex
	// screen is what a window opened now has to show. Once the process has
	// ended it is read one last time into final and freed: it is memory
	// outside Go's reach, and nothing will draw on it again.
	screen *ptyhold.Screen
	final  []byte
	subs   map[chan []byte]struct{}
	cols   int
	rows   int
	// interrupts is told of a person stopping the process from the keyboard —
	// Esc or Ctrl+C on their own — which some CLIs answer with no word of
	// their own (Interrupts).
	interrupts chan struct{}
	// views are the windows open on the terminal, by the size each has room
	// for (View).
	views    map[int][2]int
	nextView int
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

	// holder runs the processes when there is one (Hold): they outlive the
	// application then. Without it they run here and end with it. connect is
	// how it is reached again when the connection breaks (recover).
	holder  *ptyhold.Client
	connect func() (*ptyhold.Client, error)
	// leftOver are the terminals an earlier run of the application left
	// running, as Hold found them (LeftOver).
	leftOver map[string]bool

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
	s := newSession(uuid.NewString(), 0, 0, m.log)
	s.CardID, s.ScreenID, s.Command = cardID, screenID, command
	if err := m.run(s, ptyhold.KindScreen, shellSpec(dir, command)); err != nil {
		return nil, err
	}
	m.register(s)
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

	s := newSession(id, 0, 0, m.log)
	s.CardID, s.Command = cardID, strings.Join(argv, " ")
	s.keepTail = true
	spec := ptyhold.Spec{Argv: argv, Dir: dir, Env: append(env, "TERM=xterm-256color", "COLORTERM=truecolor")}
	if err := m.run(s, ptyhold.KindRun, spec); err != nil {
		return nil, err
	}
	m.register(s)
	return s, nil
}

// run starts the process of s: in the holder when there is one, here when there
// is not or it has gone.
func (m *Manager) run(s *Session, kind string, spec ptyhold.Spec) error {
	m.mu.Lock()
	holder := m.holder
	m.mu.Unlock()
	if holder != nil {
		s.eng = held{m, s.ID}
		label := ptyhold.Label{ID: s.ID, Kind: kind, Card: s.CardID, Screen: s.ScreenID, Command: s.Command}
		err := holder.Start(label, spec, s.sink())
		if err == nil || !errors.Is(err, ptyhold.ErrClosed) {
			return err
		}
		m.log.Warn("the terminal holder is gone; terminals end with the application now", "err", err)
		m.mu.Lock()
		if m.holder == holder {
			m.holder = nil
		}
		m.mu.Unlock()
	}
	proc, err := ptyhold.Start(spec)
	if err != nil {
		return err
	}
	s.eng = local{proc}
	go func() {
		proc.Pump(s.publish)
		s.finish()
	}()
	return nil
}

// register puts a started terminal in the registry, until its process goes: a
// screen shown again after the shell exited starts a new one rather than
// attaching to a corpse.
func (m *Manager) register(s *Session) {
	m.mu.Lock()
	m.byID[s.ID] = s
	if s.ScreenID != "" {
		m.byScreen[s.ScreenID] = s
	}
	m.mu.Unlock()
	go func() {
		<-s.Done()
		m.forget(s.ID, s)
	}()
}

// Hold hands the terminals to a holder (internal/ptyhold), so that closing the
// application no longer closes them, and takes back what an earlier run of the
// application left in it. Called once, at startup, after KeepIn: a stage's
// terminal that ended while nobody watched still has its tail to write.
//
// connect reaches the holder, starting one if there is none; it is used again
// whenever the connection breaks.
func (m *Manager) Hold(connect func() (*ptyhold.Client, error)) error {
	holder, err := connect()
	if err != nil {
		return err
	}
	left, err := holder.List()
	if err != nil {
		holder.Close()
		return err
	}
	m.mu.Lock()
	m.holder, m.connect = holder, connect
	m.mu.Unlock()
	holder.OnLost(func() { m.recover(holder) })
	for _, info := range left {
		m.adopt(holder, info)
	}
	return nil
}

// client is the holder connection as it stands now, nil while there is none.
func (m *Manager) client() *ptyhold.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.holder
}

// recover follows a broken connection to the holder. A connection breaks when
// the holder dies — its sessions are gone with it — or when the holder gave up
// on an application that stopped reading, and then they are all still there.
// So the holder is reached again, a new one started if need be, and each
// terminal is either taken back or ended by what the holder says of it.
func (m *Manager) recover(lost *ptyhold.Client) {
	m.mu.Lock()
	if m.holder != lost {
		m.mu.Unlock()
		return
	}
	m.holder = nil
	connect := m.connect
	var mine []*Session
	for _, s := range m.byID {
		if _, ok := s.eng.(held); ok {
			mine = append(mine, s)
		}
	}
	m.mu.Unlock()
	m.log.Warn("lost the terminal holder; connecting again")

	holder, err := connect()
	var left []ptyhold.Info
	if err == nil {
		if left, err = holder.List(); err != nil {
			holder.Close()
		}
	}
	if err != nil {
		m.log.Warn("the terminal holder is gone; terminals end with the application now", "err", err)
		for _, s := range mine {
			s.finish()
		}
		return
	}
	m.mu.Lock()
	m.holder = holder
	m.mu.Unlock()
	holder.OnLost(func() { m.recover(holder) })

	sizes := make(map[string]ptyhold.Info, len(left))
	for _, info := range left {
		sizes[info.Label.ID] = info
	}
	for _, s := range mine {
		info, ok := sizes[s.ID]
		if !ok {
			// Started in a holder that has died since: the process went with it.
			s.finish()
			continue
		}
		s.reattach(holder, info)
	}
}

func (m *Manager) adopt(holder *ptyhold.Client, info ptyhold.Info) {
	l := info.Label
	// At the size the holder draws it at: the screen it hands over, and every
	// byte after, is laid out for that size until a window says otherwise.
	s := newSession(l.ID, info.Cols, info.Rows, m.log)
	s.CardID, s.ScreenID, s.Command = l.Card, l.Screen, l.Command
	s.keepTail = l.Kind == ptyhold.KindRun
	s.eng = held{m, l.ID}
	running, err := holder.Attach(l.ID, s.sink())
	if err != nil {
		m.log.Warn("could not take back a terminal", "terminal", l.ID, "err", err)
		_ = holder.Forget(l.ID)
		return
	}
	m.register(s)
	if !running {
		s.finish()
		return
	}
	if l.Kind == ptyhold.KindScreen {
		m.mu.Lock()
		if m.leftOver == nil {
			m.leftOver = map[string]bool{}
		}
		m.leftOver[s.ID] = true
		m.mu.Unlock()
	}
	if l.Kind == ptyhold.KindRun {
		// A stage's CLI with no stage to report to: the session it worked was
		// paused when the application opened, and continuing it starts the CLI
		// afresh in the same conversation. Hung up on, it saves that
		// conversation; left running, it would spend tokens nobody asked for.
		go s.Close()
	}
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

// Known reports whether id can be shown: a terminal still running, or one that
// ended and left its tail. A run whose process went down with the application
// has neither — nobody was there to write the tail.
func (m *Manager) Known(id string) bool {
	return m.Get(id) != nil || m.transcript(id) != nil
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

// LeftOver is what has been running since before this run of the application
// started and still is: shells and started projects a person left behind when
// they closed it, and may have forgotten about.
func (m *Manager) LeftOver() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Session
	for id := range m.leftOver {
		if s := m.byID[id]; s != nil && s.Alive() {
			out = append(out, s)
		}
	}
	return out
}

// StopLeftOver ends what LeftOver lists.
func (m *Manager) StopLeftOver() { closeAll(m.LeftOver()) }

// StopAll ends every terminal, the ones that would outlive the application
// included: quitting with nothing left behind.
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.byID))
	for _, s := range m.byID {
		all = append(all, s)
	}
	m.mu.Unlock()
	closeAll(all)
}

// Close is the application closing. What a holder runs for a screen is left to
// it — that is what the holder is for — and everything else ends: a stage's CLI
// has nobody to report to once the application is gone.
func (m *Manager) Close() {
	m.mu.Lock()
	doomed := make([]*Session, 0, len(m.byID))
	for _, s := range m.byID {
		if !s.outlivesUs() {
			doomed = append(doomed, s)
		}
	}
	holder := m.holder
	m.mu.Unlock()
	closeAll(doomed)
	for _, s := range doomed {
		s.waitBrief()
	}
	if holder != nil {
		holder.Close()
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

// shellSpec runs a command through a login shell, or just the shell.
//
// A command is run through the shell rather than executed directly, and with a
// login shell at that: what a person types into a terminal is shell syntax —
// pipes, globs, their own aliases and PATH — and a terminal that only ran argv
// would be a terminal in name.
func shellSpec(dir, command string) ptyhold.Spec {
	argv := []string{loginShell(), "-l"}
	if command != "" {
		argv = append(argv, "-c", command)
	}
	// TERM is what makes a CLI draw at all; without it everything falls back to
	// dumb output and the colours and the cursor moves arrive as escape codes
	// printed literally.
	env := append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	return ptyhold.Spec{Argv: argv, Dir: dir, Env: env}
}

// newSession is a terminal whose screen starts at cols×rows; zero is the size a
// pty gets when nobody has said one (ptyhold.Start).
func newSession(id string, cols, rows int, log *slog.Logger) *Session {
	return &Session{
		ID:         id,
		screen:     ptyhold.NewScreen(cols, rows),
		interrupts: make(chan struct{}, 1),
		subs:       map[chan []byte]struct{}{},
		done:       make(chan struct{}),
		forgotten:  make(chan struct{}),
		log:        log,
		spoke:      time.Now(),
	}
}

// reattach takes the terminal back after the connection to the holder broke.
// What it printed meanwhile never arrived, so the screen is started over from
// the holder's, and every window watching it is let go to start over too — the
// same thing a window that fell behind gets (ws.go).
func (s *Session) reattach(holder *ptyhold.Client, info ptyhold.Info) {
	s.mu.Lock()
	if s.screen == nil {
		s.mu.Unlock()
		return
	}
	s.screen.Close()
	s.screen = ptyhold.NewScreen(info.Cols, info.Rows)
	for c := range s.subs {
		delete(s.subs, c)
		close(c)
	}
	s.mu.Unlock()
	running, err := holder.Attach(s.ID, s.sink())
	if err != nil || !running {
		s.finish()
	}
}

// sink is where the holder sends what this terminal prints, and its end.
func (s *Session) sink() ptyhold.Sink {
	return ptyhold.Sink{Out: s.publish, Exited: s.finish}
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

// publish keeps a chunk for whoever opens the screen next and hands it to
// whoever is watching now.
func (s *Session) publish(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spoke = time.Now()
	if s.screen != nil {
		s.screen.Write(chunk)
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

// snapshot is the screen as a new window must be fed it. Called with mu held.
func (s *Session) snapshot() []byte {
	if s.screen == nil {
		return append([]byte(nil), s.final...)
	}
	return s.screen.Bytes()
}

// Write is a keystroke on its way to the process.
func (s *Session) Write(data []byte) error {
	// A key on its own, not the start of an escape sequence: an arrow is
	// ESC [ A, and arrives in one piece.
	if len(data) == 1 && (data[0] == 0x1b || data[0] == 0x03) {
		select {
		case s.interrupts <- struct{}{}:
		default:
		}
	}
	return s.eng.Write(data)
}

// Interrupts tells of a person pressing Esc or Ctrl+C in the terminal. claude
// breaks off a turn on Esc and fires no hook for it, so this is the only way to
// know the turn is over.
func (s *Session) Interrupts() <-chan struct{} { return s.interrupts }

// Text is what the terminal shows now, as plain text, without the scrollback.
func (s *Session) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.screen == nil {
		return ""
	}
	return s.screen.Text()
}

// Resize tells the process how wide the window is. This is the whole of why a
// terminal wraps where it should: a shell breaks its lines at the column count
// it was told, and one that was never told keeps the default 80 while the
// emulator draws something else entirely.
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	// The screen first: output the process draws for the new size must find it
	// already there.
	s.mu.Lock()
	if cols == s.cols && rows == s.rows {
		s.mu.Unlock()
		return nil
	}
	s.cols, s.rows = cols, rows
	if s.screen != nil {
		s.screen.Resize(cols, rows)
	}
	s.mu.Unlock()
	return s.eng.Resize(cols, rows)
}

// View is one window open on the terminal. A terminal has one size and may be
// shown in several places at once, so each window's size is a vote and the
// smallest wins, as in tmux: a larger window shows the picture with room to
// spare, where a smaller one given the larger size would show it cut and
// wrapped wrong.
type View struct {
	s  *Session
	id int
}

// View opens a window on the terminal. Close it when the window goes.
func (s *Session) View() *View {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.views == nil {
		s.views = map[int][2]int{}
	}
	s.nextView++
	return &View{s: s, id: s.nextView}
}

// Resize says how much room this window has.
func (v *View) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	v.s.mu.Lock()
	v.s.views[v.id] = [2]int{cols, rows}
	cols, rows = v.s.smallestLocked()
	v.s.mu.Unlock()
	return v.s.Resize(cols, rows)
}

// Close takes the window's vote back: the terminal grows to what the windows
// still open have room for.
func (v *View) Close() {
	v.s.mu.Lock()
	delete(v.s.views, v.id)
	cols, rows := v.s.smallestLocked()
	v.s.mu.Unlock()
	if cols > 0 {
		_ = v.s.Resize(cols, rows)
	}
}

// smallestLocked is the size every open window has room for, zero with none.
func (s *Session) smallestLocked() (cols, rows int) {
	for _, size := range s.views {
		if cols == 0 || size[0] < cols {
			cols = size[0]
		}
		if rows == 0 || size[1] < rows {
			rows = size[1]
		}
	}
	return cols, rows
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
	if s.eng.Hangup() == nil {
		select {
		case <-s.done:
			return
		case <-time.After(closeGrace):
		}
	}
	s.eng.Kill()
	select {
	case <-s.done:
	case <-time.After(killWait):
		s.finish()
	}
}

// finish is idempotent: the output ending, the process exiting and a Close
// that stopped waiting for either are three events for one fact, and they race.
func (s *Session) finish() {
	s.closeOne.Do(func() {
		close(s.done)

		s.mu.Lock()
		for c := range s.subs {
			delete(s.subs, c)
			close(c)
		}
		s.final = s.screen.Bytes()
		s.screen.Close()
		s.screen = nil
		s.mu.Unlock()
		s.eng.Release()
	})
}

// outlivesUs reports a terminal that goes on after the application closes: a
// screen's, run by a holder.
func (s *Session) outlivesUs() bool {
	_, ok := s.eng.(held)
	return ok && s.ScreenID != ""
}

// waitBrief gives a closing process a moment to be reaped and its tail written
// before the caller moves on. Used when the application is shutting down.
func (s *Session) waitBrief() {
	select {
	case <-s.forgotten:
	case <-time.After(2 * time.Second):
	}
}
