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
	"errors"
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
	"github.com/artipop/xxvi/internal/msg"
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
	Finished(cardID, outcome string, detail msg.Msg, agentText string)
	// Abandoned is a step a person walked out of: the terminal was closed
	// before it reported. Nothing on the stage can pick it up again.
	Abandoned(cardID string)
	// Described is the task as the agent of a card leaving its flow put it
	// (Manager.Leave). Both empty: the run ended and nothing came.
	Described(cardID, title, text string)
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
		agent: job.Agent, prompt: job.Prompt, brief: job.Brief, cwd: cwd, launch: launch,
		policy: policyFor(job.Agent, m.opts.Policy), status: store.StatusQueued,
		work: workOf(job.Stage),
	}
	if s.work == model.WorkSession {
		s.revive = m.pausedConversation(s)
	}
	if err := m.store.InsertSession(store.Session{
		ID: s.id, CardID: s.card.ID, FlowID: s.flow.ID, StageID: s.stage.ID,
		AgentName: s.agent.Name, AgentKind: s.agent.Kind, Work: s.work,
		Status: store.StatusQueued, Cwd: cwd, StartedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("record the session: %w", err)
	}

	m.mu.Lock()
	// A card put back in work while its last conversation is still writing
	// its description: two CLIs in one conversation would write over each
	// other.
	if old := m.byCard[s.card.ID]; old != nil {
		old.cancel()
	}
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
func (m *Manager) Cancel(cardID string, reason msg.Msg) {
	m.mu.Lock()
	s := m.byCard[cardID]
	m.mu.Unlock()
	if s == nil {
		return
	}
	m.log.Info("cancelling session", "session", s.id, "card", cardID, "reason", reason.String())
	s.cancel()
}

// Leave stops what is running for a card going back to the inbox. A
// conversation in a terminal is asked first to describe the task for whoever
// picks the card up (watchTerminal); anything else has nobody to ask and is
// cancelled.
func (m *Manager) Leave(cardID string, reason msg.Msg) bool {
	m.mu.Lock()
	s := m.byCard[cardID]
	m.mu.Unlock()
	if s == nil {
		return false
	}
	if s.work != model.WorkTerminal || s.currentStatus() != store.StatusRunning {
		m.Cancel(cardID, reason)
		return false
	}
	m.log.Info("card leaving its flow, asking for a description", "session", s.id, "card", cardID)
	s.leave()
	return true
}

// ---- the session lifecycle ----

func (m *Manager) run(s *session) {
	defer m.wg.Done()
	defer m.release(s)

	if m.rootCtx.Err() != nil {
		m.finish(s, store.StatusCancelled, msg.New("session.appQuitting"))
		return
	}
	m.record(s, model.EntryMove, msg.New("journal.sessionStarted", "agent", s.agent.Name, "dir", s.cwd))

	conn, acpSessionID, cleanup, err := m.connect(s)
	if err != nil {
		m.finish(s, store.StatusFailed, failure(err))
		m.record(s, model.EntryProblem, msg.New("journal.sessionNotStarted").Because(clipped(err)))
		return
	}
	defer cleanup()

	final, err := m.turn(s, conn, acpSessionID)
	s.setFinal(final)

	switch {
	case m.rootCtx.Err() != nil && s.revivable:
		// The same as a terminal closed with the application: the agent kept
		// the conversation, and a person continues it (engine.Continue).
		m.finish(s, store.StatusPaused, msg.New("session.paused"))
		m.record(s, model.EntryProblem, msg.New("journal.sessionPaused"))
	case m.rootCtx.Err() != nil:
		m.finish(s, store.StatusCancelled, msg.New("session.appQuitting"))
	case s.wasCancelled():
		m.finish(s, store.StatusCancelled, msg.New("session.cancelled"))
		m.record(s, model.EntryProblem, msg.New("journal.sessionCancelled"))
	case err != nil:
		m.finish(s, store.StatusFailed, failure(err))
		m.record(s, model.EntryProblem, msg.New("journal.sessionFailed").Because(clipped(err)))
	default:
		m.finish(s, store.StatusDone, msg.Msg{})
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
	// Whatever ended the run, a card waiting for its description stops
	// waiting here: after the answer this says nothing new, and without one
	// it is the only word that nothing came.
	if s.isLeaving() {
		m.to.Described(s.card.ID, "", "")
	}
	if s.wasAbandoned() {
		m.to.Abandoned(s.card.ID)
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
		return nil, "", nil, msg.Err("agent.emptyCommand")
	}
	// The kind's own variables sit under the agent's, which is what lets an
	// entry override a model the table would have set.
	env := append(append([]string{}, s.launch.env...), spawnEnv(s.agent)...)
	proc, err := spawn(m.rootCtx, argv, s.cwd, env, s.launch.dropEnv...)
	if err != nil {
		return nil, "", nil, msg.Wrap(err, "agent.startFailed", "command", argv[0])
	}
	conn := acpsdk.NewClientSideConnection(&sessionClient{m: m, s: s}, proc.stdin, proc.stdout)
	conn.SetLogger(m.log.With("session", s.id))
	cleanup := func() {
		proc.killGroup(2 * time.Second)
		_ = proc.wait()
	}

	ctx, cancel := context.WithTimeout(m.rootCtx, 60*time.Second)
	defer cancel()

	init, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: clientCapabilities(),
	})
	if err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("initialize: %w", err)
	}
	caps := init.AgentCapabilities
	s.revivable = reviveBy(s.agent.Kind, caps) != ""

	var sess agentSession
	if s.revive != "" {
		sess, err = m.reviveSession(ctx, s, conn, caps)
	} else {
		var created acpsdk.NewSessionResponse
		// mcpServers is required even when there are none: claude and codex
		// take null for empty, Junie refuses the request.
		created, err = conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: s.cwd, McpServers: []acpsdk.McpServer{}})
		if err != nil {
			err = fmt.Errorf("session/new: %w", err)
		}
		sess = agentSession{id: created.SessionId, modes: created.Modes, config: created.ConfigOptions}
	}
	if err != nil {
		cleanup()
		return nil, "", nil, err
	}
	// Mode and model are asked for again on a revived conversation too:
	// claude-agent-acp and codex-acp both open it in their defaults, whatever
	// the run before had selected.
	m.selectMode(ctx, s, conn, sess)
	m.selectModel(ctx, s, conn, sess)

	acpID := string(sess.id)
	if err := m.store.UpdateSession(s.id, store.SessionUpdate{ACPSessionID: &acpID, Revivable: &s.revivable}); err != nil {
		m.log.Warn("could not record the ACP session id", "session", s.id, "err", err)
	}
	return conn, sess.id, cleanup, nil
}

// agentSession is what session/new, session/resume and session/load have in
// common: the conversation, and the mode and options it opened in.
type agentSession struct {
	id     acpsdk.SessionId
	modes  *acpsdk.SessionModeState
	config []acpsdk.SessionConfigOption
}

// reviveSession opens the conversation a paused run stopped in. Resume is
// preferred: it hands the conversation back without a word, where load
// replays all of it as session/update — and every one of those is already in
// the paused run's stream, where the ribbon shows it. A replay is therefore
// dropped rather than recorded twice.
func (m *Manager) reviveSession(ctx context.Context, s *session, conn *acpsdk.ClientSideConnection, caps acpsdk.AgentCapabilities) (agentSession, error) {
	id := acpsdk.SessionId(s.revive)
	if !s.revivable {
		return agentSession{}, msg.Err("session.cannotRevive", "agent", s.agent.Name)
	}
	var resumeErr error
	if caps.SessionCapabilities.Resume != nil {
		resp, err := conn.ResumeSession(ctx, acpsdk.ResumeSessionRequest{SessionId: id, Cwd: s.cwd})
		if err == nil {
			return agentSession{id: id, modes: resp.Modes, config: resp.ConfigOptions}, nil
		}
		// Load opens the same conversation, so a refused resume is not the
		// end of it.
		resumeErr = fmt.Errorf("session/resume: %w", err)
		m.log.Warn("agent refused session/resume", "session", s.id, "err", err)
	}
	if caps.LoadSession {
		// The SDK answers a request only after every notification that came
		// before the answer has been handled, so the replay ends exactly here.
		s.replaying.Store(true)
		defer s.replaying.Store(false)
		resp, err := conn.LoadSession(ctx, acpsdk.LoadSessionRequest{
			SessionId: id, Cwd: s.cwd, McpServers: []acpsdk.McpServer{},
		})
		if err != nil {
			return agentSession{}, fmt.Errorf("session/load: %w", err)
		}
		return agentSession{id: id, modes: resp.Modes, config: resp.ConfigOptions}, nil
	}
	return agentSession{}, resumeErr
}

// reviveBy is how a conversation of this agent is opened again — resume,
// load, or empty when it is not — as the agent says at initialize, unless its
// row knows better.
func reviveBy(kind string, caps acpsdk.AgentCapabilities) string {
	switch {
	case adapters[kind].noRevive:
		return ""
	case caps.SessionCapabilities.Resume != nil:
		return "resume"
	case caps.LoadSession:
		return "load"
	}
	return ""
}

// pausedConversation is the conversation to revive, when the run is a person
// continuing a paused one: the newest earlier run on this stage paused, by
// the same kind of agent — the id is the vendor's, and claude cannot open
// codex's. Anything else starts a conversation of its own, as a session run
// always has.
func (m *Manager) pausedConversation(s *session) string {
	sessions, err := m.store.SessionsForCard(s.card.ID)
	if err != nil {
		return ""
	}
	for _, past := range sessions { // newest first
		if past.StageID != s.stage.ID {
			continue
		}
		if past.Status == store.StatusPaused && past.Work == model.WorkSession && past.AgentKind == s.agent.Kind {
			return past.ACPSessionID
		}
		return ""
	}
	return ""
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
			return s.finalText(), msg.Err("session.turnTimeout", "timeout", m.opts.TurnTimeout.String())
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
func (m *Manager) selectMode(ctx context.Context, s *session, conn *acpsdk.ClientSideConnection, sess agentSession) {
	mode := s.launch.mode
	if mode == "" || sess.modes == nil || string(sess.modes.CurrentModeId) == mode {
		return
	}
	offered := false
	for _, available := range sess.modes.AvailableModes {
		if string(available.Id) == mode {
			offered = true
			break
		}
	}
	if !offered {
		return
	}
	if _, err := conn.SetSessionMode(ctx, acpsdk.SetSessionModeRequest{
		SessionId: sess.id, ModeId: acpsdk.SessionModeId(mode),
	}); err != nil {
		m.log.Warn("agent refused the session mode", "session", s.id, "mode", mode, "err", err)
	}
}

// selectModel asks for the agent's model over ACP, the same way for every
// agent: the protocol marks the option that picks it with a category, and
// claude-agent-acp, codex-acp and junie all set it. Advisory for the same
// reason as the mode: failing a card over a model name would be worse than
// running it on the default.
func (m *Manager) selectModel(ctx context.Context, s *session, conn *acpsdk.ClientSideConnection, sess agentSession) {
	if s.agent.Model == "" {
		return
	}
	if sel := modelOption(sess.config); sel != nil {
		value, ok := matchConfigValue(sel.Options, s.agent.Model)
		if !ok {
			m.log.Warn("agent does not offer this model", "session", s.id, "model", s.agent.Model)
			return
		}
		if string(sel.CurrentValue) == value {
			return
		}
		if _, err := conn.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{
			ValueId: &acpsdk.SetSessionConfigOptionValueId{
				SessionId: sess.id, ConfigId: sel.Id, Value: acpsdk.SessionConfigValueId(value),
			},
		}); err != nil {
			m.log.Warn("agent refused the model", "session", s.id, "model", value, "err", err)
		}
	}
}

// modelOption is the session option that picks the model: the one the agent
// put in the model category, or, from an agent that sets no categories, the
// one called model.
func modelOption(config []acpsdk.SessionConfigOption) *acpsdk.SessionConfigOptionSelect {
	var named *acpsdk.SessionConfigOptionSelect
	for _, opt := range config {
		sel := opt.Select
		if sel == nil {
			continue
		}
		if sel.Category != nil && *sel.Category == acpsdk.SessionConfigOptionCategoryModel {
			return sel
		}
		if named == nil && string(sel.Id) == "model" {
			named = sel
		}
	}
	return named
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

func (m *Manager) finish(s *session, status store.SessionStatus, why msg.Msg) {
	s.setStatus(status)
	now := time.Now().UTC()
	if err := m.store.UpdateSession(s.id, store.SessionUpdate{
		Status: &status, Error: &why, FinishedAt: &now,
	}); err != nil {
		m.log.Warn("could not save the session state", "session", s.id, "err", err)
	}
	m.emitSession(s)
}

func (m *Manager) setStatus(s *session, status store.SessionStatus) {
	s.setStatus(status)
	if err := m.store.UpdateSession(s.id, store.SessionUpdate{Status: &status}); err != nil {
		m.log.Warn("could not save the session state", "session", s.id, "err", err)
	}
	m.emitSession(s)
}

// record writes one entry of a run to its card's journal. A message whose code
// is msg.CodeText is somebody else's words — the agent's report, a person's
// answer — and is kept as the text it is rather than as something to word.
func (m *Manager) record(s *session, kind model.EntryKind, what msg.Msg) {
	entry := model.JournalEntry{CardID: s.card.ID, Kind: kind, Author: s.agent.Name, SessionID: s.id}
	if what.Code == msg.CodeText {
		entry.Text = what.Arg("text")
	} else {
		entry.Msg = &what
	}
	if _, err := m.store.Record(entry); err != nil {
		m.log.Warn("could not write the card journal", "card", s.card.ID, "err", err)
	}
}

// failure is why a run failed, as its session keeps it.
func failure(err error) msg.Msg { return msg.Of(clipped(err)) }

// clipped keeps a failure nobody wrote a code for to a length a journal entry
// can carry: a CLI that printed its whole stack trace is still one line of
// history.
func clipped(err error) error {
	if m := msg.Of(err); m.Code == msg.CodeInternal {
		return errors.New(truncate(m.Arg("text"), 1500))
	}
	return err
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
// A card that asked for a branch of its own gets it here, in a separate
// working tree or in the folder itself (workspace.go).
//
// Otherwise a directory of the card's own, so two cards never share a working
// copy and the agent's file jail means something. That is the right answer for
// work that starts from a blank page, which is why an empty project is a real
// answer rather than an unfinished one.
func (m *Manager) workDir(cardID string) (string, error) {
	card, err := m.store.Card(cardID)
	if err == nil && card.Project != "" {
		project, err := m.store.CardProject(card)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(project.Path)
		if err != nil || !info.IsDir() {
			return "", msg.Err("project.folderMissing", "project", project.Name, "path", project.Path)
		}
		if card.WorkMode != model.WorkModeFolder {
			return m.claimWorkspace(card, project)
		}
		return project.Path, nil
	}

	base := m.opts.WorkDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "xxvi-work")
	}
	dir := filepath.Join(base, cardID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create working folder: %w", err)
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
func doneReport(final string) msg.Msg {
	if t := strings.TrimSpace(final); t != "" {
		return msg.New(msg.CodeText, "text", truncate(t, 4000))
	}
	return msg.New("report.silent")
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
	brief  string
	cwd    string
	launch launch
	policy ToolPolicy
	// work is how this run is worked. Fixed at start, like the policy: a stage
	// edited mid-run does not change what is already running.
	work string
	// revive is the conversation a session run continues: a paused run's,
	// when a person said «Continue». Empty for a run that starts its own.
	revive string
	// revivable is what the agent said at initialize: its conversation can be
	// opened again, so an application closing on it pauses the run.
	revivable bool
	// replaying is set while session/load plays the revived conversation
	// back (reviveSession).
	replaying atomic.Bool

	mu         sync.Mutex
	status     store.SessionStatus
	turnCancel context.CancelFunc
	cancelSent bool
	// cancelPending records a cancel that arrived before a turn existed.
	cancelPending bool
	cancelled     bool
	abandoned     bool
	allowTools    map[string]bool
	final         strings.Builder
	// conversation is the vendor's id of the conversation a terminal run is
	// holding, as last recorded.
	conversation string
	// leaving is closed when the card leaves its flow (Manager.Leave).
	leaving chan struct{}

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
func (s *session) cancel() {
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

// markAbandoned is a cancel nobody will follow up on: see Reporter.Abandoned.
func (s *session) markAbandoned() {
	s.mu.Lock()
	s.cancelled = true
	s.abandoned = true
	s.mu.Unlock()
}

func (s *session) wasAbandoned() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abandoned
}

func (s *session) leaveSignal() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaving == nil {
		s.leaving = make(chan struct{})
	}
	return s.leaving
}

func (s *session) leave() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leaving == nil {
		s.leaving = make(chan struct{})
	}
	select {
	case <-s.leaving:
	default:
		close(s.leaving)
	}
}

func (s *session) isLeaving() bool {
	select {
	case <-s.leaveSignal():
		return true
	default:
		return false
	}
}

func (s *session) wasCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

// setConversation reports whether id is news.
func (s *session) setConversation(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.conversation {
		return false
	}
	s.conversation = id
	return true
}

func (s *session) conversationID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversation
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
func (s *session) outcome() (trigger string, detail msg.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.cancelled:
		return "", msg.Msg{}
	case s.status == store.StatusDone:
		return model.TriggerSuccess, msg.New("outcome.agentDone")
	case s.status == store.StatusFailed:
		if s.work == model.WorkTerminal {
			return model.TriggerFailure, msg.New("outcome.terminalFailed")
		}
		return model.TriggerFailure, msg.New("outcome.sessionFailed")
	default:
		return "", msg.Msg{}
	}
}

// event persists one thing the session did, in order.
func (s *session) event(m *Manager, kind string, payload any) {
	if err := m.store.AppendSessionEvent(s.id, s.seq.Add(1), kind, payload); err != nil {
		m.log.Warn("could not record a session event", "session", s.id, "err", err)
	}
}
