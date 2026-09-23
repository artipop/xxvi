// Package acp runs the agents. It is the implementation of engine.Runner: the
// engine decides that a stage should be worked and by whom, this package starts
// an ACP agent, streams what it says onto the card, answers or forwards its
// questions, and reports how it ended.
//
// It knows nothing about flows beyond the stage id it was handed. Which edge an
// outcome takes is the engine's business, and keeping it there is what lets the
// whole graph be tested without a process.
package acp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/stagemcp"
	"github.com/artipop/xxvi/internal/store"
	"github.com/artipop/xxvi/internal/term"
)

// Outcomes a session reports back to the engine.
const (
	statusRunning = store.StatusRunning
	statusAsking  = store.StatusAsking
)

// Options are the machine's own settings for running agents.
type Options struct {
	// WorkDir is where agents work. One directory per card, created under it,
	// so two cards never share a working copy.
	WorkDir string
	// MaxConcurrent bounds how many turns run at once on this machine. The
	// stage's own limit is checked by the engine; this is the machine's.
	MaxConcurrent int
	// TurnTimeout bounds one turn.
	TurnTimeout time.Duration
	// Policy is what an agent may do without asking, unless it carries its own.
	Policy ToolPolicy
}

// withDefaults fills in what nobody set. A zero Options has to work: it is what
// a first run has.
func (o Options) withDefaults() Options {
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 3
	}
	if o.TurnTimeout <= 0 {
		o.TurnTimeout = 15 * time.Minute
	}
	if len(o.Policy) == 0 {
		o.Policy = DefaultPolicy
	}
	return o
}

// Reporter is who a finished session tells. The engine implements it; a test
// records the calls.
type Reporter interface {
	Finished(cardID, outcome, detail, agentText string)
}

// Emitter pushes events to the UI.
type Emitter interface {
	Emit(event string, payload any)
}

// UI event names.
const (
	EventSession   = "session"
	EventAttention = "attention"
)

// Manager owns the running agents.
type Manager struct {
	store *store.Store
	to    Reporter
	ui    Emitter
	log   *slog.Logger
	opts  Options

	mu     sync.Mutex
	active map[string]*session // session id → session
	byCard map[string]*session // card id → its live session

	// questions are what agents are waiting to hear back on, keyed by id
	// (question.go). Its own lock: a question is registered from an agent's
	// inbound request and answered from the UI, and neither should queue behind
	// whatever holds mu.
	questionsMu sync.Mutex
	questions   map[string]*pendingQuestion
	// quiet is the other thing that waits for a person: a stage in a terminal
	// whose CLI has drawn nothing for a while (terminal.go). Kept beside the
	// questions because both are read as one list — one bookkeeping of "what is
	// waiting", not two.
	quiet map[string]Attention

	// terms and tools are what a stage worked in a terminal needs: somewhere to
	// open a pty, and the door the agent reports through. Nil until the
	// application wires them, and a terminal stage that finds them nil says so.
	terms *term.Manager
	tools *stagemcp.Server

	// sem bounds turns actually in flight on this machine.
	sem     chan struct{}
	rootCtx context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
}

// New builds a manager. Call Close to stop everything it started.
func New(st *store.Store, to Reporter, ui Emitter, opts Options, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	opts = opts.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		store: st, to: to, ui: ui, log: log, opts: opts,
		active: map[string]*session{}, byCard: map[string]*session{},
		quiet: map[string]Attention{},
		sem:   make(chan struct{}, opts.MaxConcurrent), rootCtx: ctx, stop: cancel,
	}
}

// Close cancels every running session and waits for them. A question left open
// is answered as a refusal, which is what the agent hears when nobody was there.
func (m *Manager) Close() {
	m.stop()
	m.wg.Wait()
}

var _ engine.Runner = (*Manager)(nil)

// Start begins a stage's work for a card. It returns as soon as the agent is
// under way — the engine holds a lock while it calls this, and the outcome
// comes back later through Reporter.
func (m *Manager) Start(job engine.Job) error {
	// A terminal stage runs the vendor's own CLI and has no ACP adapter to
	// resolve; asking for one would refuse a card because a program it is never
	// going to run is not installed.
	var launch launch
	if workOf(job.Stage) == model.WorkSession {
		var err error
		if launch, err = launchFor(job.Agent); err != nil {
			return err
		}
	}
	cwd, err := m.workDir(job.Card.ID)
	if err != nil {
		return err
	}

	s := &session{
		id: uuid.NewString(), card: job.Card, flow: job.Flow, stage: job.Stage,
		agent: job.Agent, prompt: job.Prompt, cwd: cwd, launch: launch,
		policy: policyFor(job.Agent, m.opts.Policy), status: store.StatusQueued,
		work: workOf(job.Stage),
	}
	if err := m.store.InsertSession(store.Session{
		ID: s.id, CardID: s.card.ID, FlowID: s.flow.ID, StageID: s.stage.ID,
		AgentName: s.agent.Name, AgentKind: s.agent.Kind, Work: s.work,
		Status: store.StatusQueued, Cwd: cwd, StartedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("записать сессию: %w", err)
	}

	m.mu.Lock()
	m.active[s.id] = s
	m.byCard[s.card.ID] = s
	m.mu.Unlock()
	m.emitSession(s)

	m.wg.Add(1)
	if s.work == model.WorkTerminal {
		go m.runTerminal(s)
	} else {
		go m.run(s)
	}
	return nil
}

// workOf is how a stage is worked, with the same default the model applies: a
// stage that named no mode is one somebody meant to watch.
func workOf(stage model.Stage) string {
	if stage.Work == model.WorkSession {
		return model.WorkSession
	}
	return model.WorkTerminal
}

// Busy is the agents with a live session, keyed the way names are matched.
func (m *Manager) Busy() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.active))
	for _, s := range m.active {
		out[model.Username(s.agent.Name)] = true
	}
	return out
}

// RunningOnStage counts live sessions of one stage.
func (m *Manager) RunningOnStage(stageID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.active {
		if s.stage.ID == stageID {
			n++
		}
	}
	return n
}

// Cancel stops whatever is running for a card. A cancelled session produces no
// outcome: somebody intervened, so the flow waits for them.
func (m *Manager) Cancel(cardID, reason string) {
	m.mu.Lock()
	s := m.byCard[cardID]
	m.mu.Unlock()
	if s == nil {
		return
	}
	m.log.Info("сессия отменяется", "session", s.id, "card", cardID, "reason", reason)
	s.cancel(reason)
}

// ---- the session lifecycle ----

func (m *Manager) run(s *session) {
	defer m.wg.Done()
	defer m.release(s)

	if m.rootCtx.Err() != nil {
		m.finish(s, store.StatusCancelled, "приложение завершается")
		return
	}
	m.record(s, model.EntryMove, fmt.Sprintf("Агент %s начал работу в папке `%s`.", s.agent.Name, s.cwd))

	conn, acpSessionID, cleanup, err := m.connect(s)
	if err != nil {
		m.finish(s, store.StatusFailed, err.Error())
		m.record(s, model.EntryProblem, fmt.Sprintf("Сессия агента не запустилась: %s", truncate(err.Error(), 1500)))
		return
	}
	defer cleanup()

	final, err := m.turn(s, conn, acpSessionID)
	s.setFinal(final)

	switch {
	case m.rootCtx.Err() != nil:
		m.finish(s, store.StatusCancelled, "приложение завершается")
	case s.wasCancelled():
		m.finish(s, store.StatusCancelled, "сессия отменена")
		m.record(s, model.EntryProblem, "Сессия агента отменена.")
	case err != nil:
		m.finish(s, store.StatusFailed, err.Error())
		m.record(s, model.EntryProblem, fmt.Sprintf("Сессия агента завершилась с ошибкой: %s", truncate(err.Error(), 1500)))
	default:
		m.finish(s, store.StatusDone, "")
		m.record(s, model.EntryReport, doneReport(final))
	}
}

// release takes the session out of the live set and tells the engine how it
// ended. Order matters: the session must stop counting as running before the
// engine decides what to do next, or the stage it just freed still looks full.
func (m *Manager) release(s *session) {
	m.mu.Lock()
	delete(m.active, s.id)
	if m.byCard[s.card.ID] == s {
		delete(m.byCard, s.card.ID)
	}
	m.mu.Unlock()
	m.emitSession(s)

	if m.to == nil {
		return
	}
	outcome, detail := s.outcome()
	if outcome == "" {
		return // cancelled: a person is deciding what happens next
	}
	m.to.Finished(s.card.ID, outcome, detail, s.finalText())
}

// connect builds the ACP stack for a session and negotiates the agent session.
func (m *Manager) connect(s *session) (*acpsdk.ClientSideConnection, acpsdk.SessionId, func(), error) {
	argv := resolveArgv0(s.launch.argv)
	if len(argv) == 0 {
		return nil, "", nil, fmt.Errorf("пустая команда запуска агента")
	}
	// The kind's own variables sit under the agent's, which is what lets an
	// entry override a model the table would have set.
	env := append(append([]string{}, s.launch.env...), spawnEnv(s.agent)...)
	proc, err := spawn(m.rootCtx, argv, s.cwd, env, s.launch.dropEnv...)
	if err != nil {
		return nil, "", nil, fmt.Errorf("запустить агента %q: %w", argv[0], err)
	}
	conn := acpsdk.NewClientSideConnection(&sessionClient{m: m, s: s}, proc.stdin, proc.stdout)
	conn.SetLogger(m.log.With("session", s.id))
	cleanup := func() {
		proc.killGroup(2 * time.Second)
		_ = proc.wait()
	}

	ctx, cancel := context.WithTimeout(m.rootCtx, 60*time.Second)
	defer cancel()

	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: clientCapabilities(),
	}); err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("initialize: %w", err)
	}
	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: s.cwd})
	if err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("session/new: %w", err)
	}
	m.selectMode(ctx, s, conn, sess)
	m.selectModel(ctx, s, conn, sess)

	acpID := string(sess.SessionId)
	if err := m.store.UpdateSession(s.id, store.SessionUpdate{ACPSessionID: &acpID}); err != nil {
		m.log.Warn("не удалось записать идентификатор ACP-сессии", "session", s.id, "err", err)
	}
	return conn, sess.SessionId, cleanup, nil
}

// turn sends the prompt and returns the agent's final message. It holds a
// concurrency slot only while the agent is actually working.
func (m *Manager) turn(s *session, conn *acpsdk.ClientSideConnection, acpSessionID acpsdk.SessionId) (string, error) {
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-m.rootCtx.Done():
		return "", m.rootCtx.Err()
	}

	ctx, cancel := context.WithTimeout(m.rootCtx, m.opts.TurnTimeout)
	defer cancel()

	// A cancel that arrived before the turn existed still applies to it: the
	// card was moved while the agent was starting up, and starting the work
	// anyway would leave a session nobody asked for.
	if pending := s.beginTurn(cancel); pending {
		cancel()
	}
	m.setStatus(s, store.StatusRunning)

	// Telling the agent to stop is a protocol message, and it has to go out
	// once however the turn was ended.
	stop := context.AfterFunc(ctx, func() {
		if s.markCancelSent() {
			_ = conn.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: acpSessionID})
		}
	})
	defer stop()

	resp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpSessionID,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock(s.prompt)},
	})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded && !s.wasCancelled() {
			return s.finalText(), fmt.Errorf("таймаут хода (%s)", m.opts.TurnTimeout)
		}
		return s.finalText(), fmt.Errorf("session/prompt: %w", err)
	}
	if resp.StopReason == acpsdk.StopReasonCancelled {
		s.markCancelled()
	}
	return s.finalText(), nil
}

// selectMode switches the agent into the mode its kind asks for. Advisory: an
// agent that offers no such mode is left in the one it chose, and a refusal is
// logged rather than failing the session — the mode is a preference and the
// turn may well work without it.
func (m *Manager) selectMode(ctx context.Context, s *session, conn *acpsdk.ClientSideConnection, sess acpsdk.NewSessionResponse) {
	mode := s.launch.mode
	if mode == "" || sess.Modes == nil || string(sess.Modes.CurrentModeId) == mode {
		return
	}
	offered := false
	for _, available := range sess.Modes.AvailableModes {
		if string(available.Id) == mode {
			offered = true
			break
		}
	}
	if !offered {
		return
	}
	if _, err := conn.SetSessionMode(ctx, acpsdk.SetSessionModeRequest{
		SessionId: sess.SessionId, ModeId: acpsdk.SessionModeId(mode),
	}); err != nil {
		m.log.Warn("агент отказал в режиме сессии", "session", s.id, "mode", mode, "err", err)
	}
}

// selectModel asks for the agent's model over ACP, for the kinds that have no
// flag or variable for it. Advisory for the same reason as the mode: failing a
// card over a model name would be worse than running it on the default.
func (m *Manager) selectModel(ctx context.Context, s *session, conn *acpsdk.ClientSideConnection, sess acpsdk.NewSessionResponse) {
	configID := adapters[s.agent.Kind].modelConfig
	if configID == "" || s.agent.Model == "" {
		return
	}
	for _, opt := range sess.ConfigOptions {
		sel := opt.Select
		if sel == nil || string(sel.Id) != configID {
			continue
		}
		value, ok := matchConfigValue(sel.Options, s.agent.Model)
		if !ok {
			m.log.Warn("агент не предлагает такую модель", "session", s.id, "model", s.agent.Model)
			return
		}
		if string(sel.CurrentValue) == value {
			return
		}
		if _, err := conn.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{
			ValueId: &acpsdk.SetSessionConfigOptionValueId{
				SessionId: sess.SessionId, ConfigId: sel.Id, Value: acpsdk.SessionConfigValueId(value),
			},
		}); err != nil {
			m.log.Warn("агент отказал в модели", "session", s.id, "model", value, "err", err)
		}
		return
	}
}

// matchConfigValue finds the option value somebody meant: its id, or the name
// shown for it, either way ignoring case.
func matchConfigValue(options acpsdk.SessionConfigSelectOptions, want string) (string, bool) {
	flat := configSelectOptions(options)
	for _, opt := range flat {
		if string(opt.Value) == want {
			return string(opt.Value), true
		}
	}
	for _, opt := range flat {
		if strings.EqualFold(string(opt.Value), want) || strings.EqualFold(opt.Name, want) {
			return string(opt.Value), true
		}
	}
	return "", false
}

// configSelectOptions flattens the two shapes a select takes, grouped and not.
func configSelectOptions(options acpsdk.SessionConfigSelectOptions) []acpsdk.SessionConfigSelectOption {
	if options.Ungrouped != nil {
		return *options.Ungrouped
	}
	var out []acpsdk.SessionConfigSelectOption
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			out = append(out, group.Options...)
		}
	}
	return out
}

// ---- bookkeeping ----

func (m *Manager) finish(s *session, status store.SessionStatus, errText string) {
	s.setStatus(status)
	now := time.Now().UTC()
	if err := m.store.UpdateSession(s.id, store.SessionUpdate{
		Status: &status, ErrorText: &errText, FinishedAt: &now,
	}); err != nil {
		m.log.Warn("не удалось сохранить состояние сессии", "session", s.id, "err", err)
	}
	m.emitSession(s)
}

func (m *Manager) setStatus(s *session, status store.SessionStatus) {
	s.setStatus(status)
	if err := m.store.UpdateSession(s.id, store.SessionUpdate{Status: &status}); err != nil {
		m.log.Warn("не удалось сохранить состояние сессии", "session", s.id, "err", err)
	}
	m.emitSession(s)
}

func (m *Manager) record(s *session, kind model.EntryKind, text string) {
	if _, err := m.store.Record(model.JournalEntry{
		CardID: s.card.ID, Kind: kind, Author: s.agent.Name, SessionID: s.id, Text: text,
	}); err != nil {
		m.log.Warn("не удалось записать в журнал карточки", "card", s.card.ID, "err", err)
	}
}

func (m *Manager) emitSession(s *session) {
	if m.ui == nil {
		return
	}
	m.ui.Emit(EventSession, map[string]any{
		"sessionId": s.id, "cardId": s.card.ID, "stageId": s.stage.ID,
		"agent": s.agent.Name, "status": string(s.currentStatus()),
	})
}

func (m *Manager) emitAttention(a Attention) {
	if m.ui == nil {
		return
	}
	m.ui.Emit(EventAttention, a)
}

// WorkDir is a card's working folder from outside — what the ribbon's notes
// screen opens files in. The same folder the agent is confined to, on purpose:
// a plan the agent writes and a plan a person edits are one file rather than
// two copies of one intention.
func (m *Manager) WorkDir(cardID string) (string, error) { return m.workDir(cardID) }

// workDir is where a card's agent works.
//
// The card's project if it names one, and then it is a place that already
// exists and belongs to somebody: it is not created here and not created if it
// has gone. A card pointing at a project that is missing is an error rather
// than a reason to quietly open somewhere else — working in the wrong place is
// worse than not working, and the stage that could not start says which.
//
// Otherwise a directory of the card's own, so two cards never share a working
// copy and the agent's file jail means something. That is the right answer for
// work that starts from a blank page, which is why an empty project is a real
// answer rather than an unfinished one.
func (m *Manager) workDir(cardID string) (string, error) {
	card, err := m.store.Card(cardID)
	if err == nil && card.Project != "" {
		project, err := m.store.Project(card.Project)
		if err != nil {
			return "", fmt.Errorf("проект карточки не найден в реестре: %w", err)
		}
		info, err := os.Stat(project.Path)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("папка проекта «%s» не найдена: %s", project.Name, project.Path)
		}
		return project.Path, nil
	}

	base := m.opts.WorkDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "xxvi-work")
	}
	dir := filepath.Join(base, cardID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("создать рабочую папку: %w", err)
	}
	return dir, nil
}

// policyFor is the policy one session runs under: the agent's own if it carries
// one, else the machine's. Resolved once at start, so a registry edit mid-run
// cannot widen what a working agent may do.
func policyFor(a model.Agent, fallback ToolPolicy) ToolPolicy {
	if len(a.AutoAllowTools) > 0 {
		return ToolPolicy(a.AutoAllowTools)
	}
	return fallback
}

// doneReport is the agent's closing words as they are. What they are and who
// said them is on the entry itself, and the working folder is on the entry that
// opened the step; the ribbon sets this under the agent's screen as it stands.
func doneReport(final string) string {
	if t := strings.TrimSpace(final); t != "" {
		return truncate(t, 4000)
	}
	return "Агент завершил работу и ничего не сказал."
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---- one session ----

// session is one run of an agent against one card on one stage.
type session struct {
	id     string
	card   model.Card
	flow   model.Flow
	stage  model.Stage
	agent  model.Agent
	prompt string
	cwd    string
	launch launch
	policy ToolPolicy
	// work is how this run is worked. Fixed at start, like the policy: a stage
	// edited mid-run does not change what is already running.
	work string

	mu         sync.Mutex
	status     store.SessionStatus
	turnCancel context.CancelFunc
	cancelSent bool
	// cancelPending records a cancel that arrived before a turn existed.
	cancelPending bool
	cancelled     bool
	allowTools    map[string]bool
	final         strings.Builder

	seq atomic.Int64
}

func (s *session) currentStatus() store.SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *session) setStatus(status store.SessionStatus) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

// beginTurn records the turn's cancel and reports whether a cancel is already
// waiting for it.
func (s *session) beginTurn(cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnCancel = cancel
	pending := s.cancelPending
	s.cancelPending = false
	return pending
}

// cancel stops the session. Before a turn exists the cancel is remembered, so
// a card moved while its agent was starting up does not get worked anyway.
func (s *session) cancel(reason string) {
	s.mu.Lock()
	s.cancelled = true
	if s.turnCancel == nil {
		s.cancelPending = true
		s.mu.Unlock()
		return
	}
	cancel := s.turnCancel
	s.mu.Unlock()
	cancel()
}

func (s *session) markCancelled() {
	s.mu.Lock()
	s.cancelled = true
	s.mu.Unlock()
}

// markCancelSent reports whether this is the first cancel, so the protocol
// message goes out once.
func (s *session) markCancelSent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	already := s.cancelSent
	s.cancelSent = true
	return !already
}

func (s *session) wasCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

func (s *session) allowToolAlways(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allowTools == nil {
		s.allowTools = map[string]bool{}
	}
	s.allowTools[name] = true
}

func (s *session) toolAllowed(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allowTools[name]
}

func (s *session) appendFinal(text string) {
	s.mu.Lock()
	s.final.WriteString(text)
	s.mu.Unlock()
}

func (s *session) setFinal(text string) {
	s.mu.Lock()
	s.final.Reset()
	s.final.WriteString(text)
	s.mu.Unlock()
}

// finalText is the agent's closing words — what a comment condition on an edge
// is asked about.
func (s *session) finalText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.final.String()
}

// outcome is the event this session hands its stage. A cancelled session yields
// nothing at all: a person stepped in, so the flow waits for them.
func (s *session) outcome() (trigger, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.cancelled:
		return "", ""
	case s.status == store.StatusDone:
		return model.TriggerSuccess, "агент завершил работу"
	case s.status == store.StatusFailed:
		if s.work == model.WorkTerminal {
			return model.TriggerFailure, "шаг в терминале не прошёл"
		}
		return model.TriggerFailure, "сессия агента упала"
	default:
		return "", ""
	}
}

// event persists one thing the session did, in order.
func (s *session) event(m *Manager, kind string, payload any) {
	if err := m.store.AppendSessionEvent(s.id, s.seq.Add(1), kind, payload); err != nil {
		m.log.Warn("не удалось записать событие сессии", "session", s.id, "err", err)
	}
}
