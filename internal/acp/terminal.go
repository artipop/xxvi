package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/stagemcp"
	"github.com/artipop/xxvi/internal/store"
	"github.com/artipop/xxvi/internal/term"
)

// A stage worked in a terminal: the agent's own CLI in a pty of the card, with
// the brief handed to it the way a person would hand it over (docs/system.md
// §4.1.1).
//
// What that buys is the whole reason this exists. The plan, the questions, the
// permission prompts are drawn by the vendor's CLI, in the interface its own
// users know, and the person watching answers in the same window — we stop
// re-implementing somebody else's TUI worse than they did, and a question stops
// having to become a thing of ours before it can be answered.
//
// Two obligations come with it, and both are met here rather than left to the
// reader. The step ends when the agent says it does, through finish_step
// (internal/stagemcp): an interactive CLI does not exit when a turn ends, so an
// exit cannot stand in for a report. And a stuck step is noticed, because a
// question inside a TUI is invisible from out here: the CLI's own hooks say
// when it stops for a person (hooks.go), and a CLI whose hooks never spoke is
// judged by drawing nothing.

// trustPrompt is on the screen while a CLI asks whether to trust the folder:
// claude's «Yes, I trust this folder», codex's «Trust this folder?». No hook
// fires for it — it comes before the conversation does.
const trustPrompt = "trust this folder"

func trustAsked(screen string) bool {
	return strings.Contains(strings.ToLower(screen), trustPrompt)
}

// terminalQuietFor is how long a stage's CLI whose hooks have said nothing —
// none for its kind, none on Windows — must draw nothing before the card says
// it is waiting for a person. Generous on purpose: a model thinking between
// tool calls is silent for a while, and a card that cries out early is a card
// nobody believes.
const terminalQuietFor = 45 * time.Second

// promptSettle is how quiet the CLI must be before a brief is typed into it,
// and promptWait is how long that is waited for. Only for the case where the
// brief could not go on the command line: writing into a CLI that has not
// finished starting means answering whatever it is showing — and what it might
// be showing is «do you trust the files in this folder?», which must not be
// answered with a task.
const (
	promptSettle = 1500 * time.Millisecond
	promptWait   = 30 * time.Second
)

// terminalStartWindow is how soon after the launch a CLI that closed without
// reporting still counts as one that failed to start, rather than as a
// conversation somebody ended. Wide enough for «do you trust this folder?»
// to be read and answered by whoever is watching (docs/system.md §4.1.1).
const terminalStartWindow = 30 * time.Second

// errClosedWithoutReport is a CLI that ended while its step was still open.
var errClosedWithoutReport = msg.Err("terminal.closedWithoutReport")

// closedByPerson reports whether a step's error is a person ending the
// conversation rather than the step failing. The difference decides whether
// the card moves: a failure is an outcome, an intervention is not (§6).
func closedByPerson(err error, open time.Duration) bool {
	return errors.Is(err, errClosedWithoutReport) && open >= terminalStartWindow
}

// resumeFailed reports a CLI that closed at once on a conversation it was told
// to continue: it did not find it — deleted, or kept by another account. That
// is said as it is rather than as a terminal that failed, and not answered by
// starting afresh, which would quietly drop what the stage had been told.
func resumeFailed(resumed bool, err error, open time.Duration) bool {
	return resumed && errors.Is(err, errClosedWithoutReport) && open < terminalStartWindow
}

// terminalGrace is how long the CLI is left alone after it reports. The report
// is a tool call, and killing the process that made it before its result has
// been delivered is how an agent ends its turn on a broken pipe — with the step
// already counted as finished, so nobody would ever see why.
const terminalGrace = 3 * time.Second

// terminalDeps are what a terminal stage needs and a session does not: a place
// to open ptys and a door for the agent to report through. Set once at startup;
// without them a terminal stage refuses to start and says why, which is better
// than opening a window with no way out of it.
func (m *Manager) SetTerminals(terms *term.Manager, tools *stagemcp.Server) {
	m.mu.Lock()
	m.terms, m.tools = terms, tools
	m.mu.Unlock()
}

func (m *Manager) terminals() (*term.Manager, *stagemcp.Server) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.terms, m.tools
}

// runTerminal works one stage in a terminal. It is the twin of run() and ends
// the same way — through release(), which is what tells the engine.
//
// It takes no slot of the machine's concurrency: that limit bounds turns in
// flight, and a conversation somebody is sitting in is not a turn. What bounds
// these is the stage's own limit, which the engine checks before it ever gets
// here.
func (m *Manager) runTerminal(s *session) {
	defer m.wg.Done()
	defer m.release(s)

	if m.rootCtx.Err() != nil {
		m.finish(s, store.StatusCancelled, msg.New("session.appQuitting"))
		return
	}

	terms, tools := m.terminals()
	cli, ok := cliFor(s.agent.Kind)
	switch {
	case terms == nil || tools == nil || tools.URL() == "":
		m.failTerminal(s, msg.New("terminal.notRunning"))
		return
	case !ok:
		m.failTerminal(s, msg.New("terminal.noCLI", "agent", s.agent.Name, "kind", s.agent.Kind))
		return
	}

	// The report and the door it arrives through. Both die with the step: a
	// grant outliving its run is a door onto a card nobody is working.
	reported := make(chan stagemcp.Report, 1)
	// Buffered generously and never blocked on: a hook holds the CLI's turn
	// until it is answered, and the watcher is the only reader.
	hooked := make(chan stagemcp.HookEvent, 64)
	token := tools.Grant(stagemcp.Step{
		CardTitle: s.card.Title,
		StageName: s.stage.Name,
		Brief:     s.brief,
		Next:      nextStages(s.flow, s.stage.ID),
		Writes:    s.stage.Writes,
		Report: func(r stagemcp.Report) error {
			if missing := missingWrites(s.stage.Writes, r); missing != "" {
				return fmt.Errorf("the step is not finished: %s missing. Add the value and call again", missing)
			}
			select {
			case reported <- r:
				return nil
			default:
				return errors.New("this step is already finished")
			}
		},
		Hook: func(ev stagemcp.HookEvent) {
			select {
			case hooked <- ev:
			default:
				m.log.Warn("a terminal hook was dropped", "session", s.id, "event", ev.Event)
			}
		},
	})
	defer tools.Revoke(token)

	handoff, err := cli.cliTools(tools.URL(), token)
	if err != nil {
		m.failTerminal(s, msg.Of(err))
		return
	}
	// Without hooks the step still works — it is watched by its silence, as
	// it was before there were any — so a failure here is a note, not a stop.
	if env, ok := hookEnv(tools.HookURL(), token); ok && cli.cliHooks != nil && hooksPossible() {
		if hooks, err := cli.cliHooks(); err != nil {
			m.log.Warn("terminal hooks not registered", "session", s.id, "err", err)
		} else {
			handoff.args = append(handoff.args, hooks.args...)
			handoff.files = append(handoff.files, hooks.files...)
			handoff.env = append(handoff.env, env...)
		}
	}
	defer func() {
		for _, f := range handoff.files {
			_ = os.Remove(f)
		}
	}()

	open := m.opening(s, cli)
	resumed := open.id != "" && !open.chosen
	if resumed {
		m.noteConversation(s, open.id)
	}
	bin, err := terminalBin(cli)
	if err != nil {
		m.failTerminal(s, msg.New("terminal.binMissing", "bin", cli.cliBin))
		return
	}
	argv, promptTaken := terminalArgv(cli, bin, open, handoff.args, s.prompt)

	sess, err := terms.Attach(s.id, s.card.ID, s.cwd, argv, append(terminalEnv(s, cli), handoff.env...))
	if err != nil {
		m.failTerminal(s, msg.Of(clipped(err)))
		return
	}
	opened := time.Now()

	ctx, cancel := context.WithCancel(m.rootCtx)
	defer cancel()
	if pending := s.beginTurn(cancel); pending {
		cancel()
	}
	m.setStatus(s, store.StatusRunning)
	m.record(s, model.EntryMove, msg.New("journal.terminalOpened", "agent", s.agent.Name, "dir", s.cwd))

	if !promptTaken {
		go deliverPrompt(m, s, sess)
	}

	report, err := m.watchTerminal(ctx, s, sess, reported, hooked)

	// An id we chose is only a conversation once the CLI has kept one under
	// it. Its hooks usually say so; when they never spoke, a CLI that stayed
	// open past its start is taken as having kept it — one that closed at
	// «do you trust this folder?» has nothing to resume, and resuming nothing
	// by id would fail the stage on every visit after.
	if open.chosen && s.conversationID() == "" && time.Since(opened) >= terminalStartWindow {
		m.noteConversation(s, open.id)
	}

	// The CLI ends with the step: this terminal belongs to the run, not to the
	// person, and one left open on a card that has moved on is a conversation
	// about work that is no longer here. What was drawn in it is kept, so the
	// segment still shows what happened (internal/term).
	if err == nil {
		time.Sleep(terminalGrace)
	}
	sess.Close()

	switch {
	case m.rootCtx.Err() != nil && s.conversationID() != "":
		// The application closing is not the step ending: the CLI was hung up
		// on and saved its conversation, and the stage waits for a person to
		// continue it there (engine.Continue).
		m.finish(s, store.StatusPaused, msg.New("session.paused"))
		m.record(s, model.EntryProblem, msg.New("journal.terminalPaused"))
	case m.rootCtx.Err() != nil:
		m.finish(s, store.StatusCancelled, msg.New("session.appQuitting"))
	case s.wasCancelled():
		m.finish(s, store.StatusCancelled, msg.New("session.stepCancelled"))
		m.record(s, model.EntryProblem, msg.New("journal.terminalCancelled"))
	case resumeFailed(resumed, err, time.Since(opened)):
		why := msg.Err("terminal.resumeFailed", "id", open.id)
		m.finish(s, store.StatusFailed, failure(why))
		m.record(s, model.EntryProblem, msg.New("journal.terminalFailed").Because(why))
	case closedByPerson(err, time.Since(opened)):
		// Somebody ended the conversation. That is not an outcome the flow
		// could move on, and the stage has nothing left to wait for: the step
		// can only be resumed from the CLI's own conversation, which a new
		// card started «from a session» does. So the card goes.
		s.markAbandoned()
		m.finish(s, store.StatusCancelled, msg.New("terminal.closedWithoutReport"))
		m.record(s, model.EntryProblem, msg.New("journal.terminalClosedByPerson"))
	case err != nil:
		m.finish(s, store.StatusFailed, failure(err))
		m.record(s, model.EntryProblem, msg.New("journal.terminalFailed").Because(clipped(err)))
	default:
		// The report is handed to the engine in the shape a session's closing
		// words would have had: the summary, then one «Property: value» line
		// per value. One currency for both modes means one place that reads it
		// (engine.ParseWrites) rather than two that must agree.
		s.setFinal(reportText(s.stage.Writes, report))
		status := store.StatusDone
		if !report.OK {
			status = store.StatusFailed
		}
		m.finish(s, status, msg.Msg{})
		m.record(s, model.EntryReport, terminalReport(report))
	}
}

// watchTerminal waits for whichever comes first: the report, the CLI ending, or
// somebody stepping in. On the way it keeps the card's mark — whether the CLI
// is waiting for a person — as its hooks say, or, while they have said
// nothing, as its silence does.
func (m *Manager) watchTerminal(
	ctx context.Context, s *session, sess *term.Session,
	reported <-chan stagemcp.Report, hooked <-chan stagemcp.HookEvent,
) (stagemcp.Report, error) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	state := cliUnknown
	waiting := ""
	wait := func(why string) {
		if why == waiting {
			return
		}
		waiting = why
		if why == "" {
			m.clearQuiet(s)
		} else {
			m.raiseQuiet(s, why)
		}
	}
	defer wait("")

	for {
		select {
		case report := <-reported:
			return report, nil

		case <-sess.Done():
			// A CLI that closed without reporting: it never started, or
			// whoever was sitting there quit. Which of the two is decided by
			// the caller, by how long it had been open (closedByPerson).
			select {
			case report := <-reported:
				return report, nil // it reported and then exited; the race is real
			default:
			}
			return stagemcp.Report{}, errClosedWithoutReport

		case <-ctx.Done():
			return stagemcp.Report{}, ctx.Err()

		case ev := <-hooked:
			if id := conversationOf(ev); id != "" {
				m.noteConversation(s, id)
			}
			switch next := stateOf(ev); next {
			case cliWorking:
				state = next
				wait("")
			case cliAsking:
				state = next
				wait(waitAsking)
			case cliTurnEnded:
				// Not a mark: a person talking to the agent in its terminal
				// would get one after every answer, and a list that fills
				// with those stops being read. It still ends "working", so
				// silence after it is not taken for a stuck turn.
				state = next
				wait("")
			}

		case <-sess.Interrupts():
			// Esc breaks a claude turn off with no hook at all, and the next
			// one that comes is the person's next prompt. Until then the turn
			// is over, the same as after Stop.
			if state == cliWorking || state == cliAsking {
				state = cliTurnEnded
				wait("")
			}

		case <-tick.C:
			if state != cliUnknown {
				continue // hooks lead once they have spoken
			}
			// Before the first hook the CLI may be asking something that no
			// hook reports — «do you trust this folder?» comes before any —
			// and the screen says so plainly. Silence is left for a CLI whose
			// hooks never speak at all.
			switch {
			case trustAsked(sess.Text()):
				wait(waitAsking)
			case sess.Quiet() >= terminalQuietFor:
				wait(waitQuiet)
			default:
				wait("")
			}
		}
	}
}

// failTerminal ends a step that could not be opened at all. It fails rather
// than waits: a card standing on a stage whose window never appeared would wait
// for a person who has nothing to look at.
func (m *Manager) failTerminal(s *session, why msg.Msg) {
	m.finish(s, store.StatusFailed, why)
	notOpened := msg.New("journal.terminalNotOpened")
	notOpened.Cause = &why
	m.record(s, model.EntryProblem, notOpened)
}

// opening is which conversation a run starts in.
type opening struct {
	args []string
	// id is the conversation the run will hold, when that is known before
	// the CLI starts. chosen marks one we made up for a new conversation:
	// it is not a conversation until the CLI has kept one under it.
	id     string
	chosen bool
	// byFolder is the fallback that names no conversation, and the one case
	// where the brief cannot go on the command line.
	byFolder bool
}

// opening decides where a run's conversation comes from. A second visit to a
// stage continues the conversation the last one ended on, so nobody is asked
// the same questions twice; a card started from a conversation continues that
// one on its stage; anything else begins anew.
func (m *Manager) opening(s *session, cli adapter) opening {
	past, worked := m.pastConversation(s)
	switch {
	case past != "" && cli.cliResumeID != nil:
		return opening{args: cli.cliResumeID(past), id: past}
	case m.continuesCardSession(s) && cli.cliResumeID != nil:
		return opening{args: cli.cliResumeID(s.card.Session), id: s.card.Session}
	case worked && len(cli.cliResumeArgs) > 0:
		return opening{args: cli.cliResumeArgs, byFolder: true}
	case cli.cliNewSession != nil:
		id := uuid.NewString()
		return opening{args: cli.cliNewSession(id), id: id, chosen: true}
	}
	return opening{}
}

// pastConversation is the conversation the newest earlier run of this card on
// this stage ended on, and whether there was such a run at all. Only runs of the
// same kind count: the id is the vendor's, and claude cannot open codex's.
func (m *Manager) pastConversation(s *session) (id string, worked bool) {
	sessions, err := m.store.SessionsForCard(s.card.ID)
	if err != nil {
		return "", false
	}
	for _, past := range sessions { // newest first
		if past.ID == s.id || past.StageID != s.stage.ID || past.Work != model.WorkTerminal || past.AgentKind != s.agent.Kind {
			continue
		}
		if past.ACPSessionID != "" {
			return past.ACPSessionID, true
		}
		worked = true
	}
	return "", worked
}

// noteConversation records the conversation the run is holding, every time it
// changes: /clear, compaction and /resume move claude to another id in the
// middle of a run, and the next visit has to open the one it ended on.
func (m *Manager) noteConversation(s *session, id string) {
	if !s.setConversation(id) {
		return
	}
	if err := m.store.UpdateSession(s.id, store.SessionUpdate{ACPSessionID: &id}); err != nil {
		m.log.Warn("could not record the terminal's conversation", "session", s.id, "err", err)
	}
}

// continuesCardSession reports whether this run is where the conversation the
// card was started from goes on. That is the stage the card's agent first
// worked in a terminal — this one, if nothing came before — and every later
// visit to it: one conversation belongs to one stage, the same rule as a stage
// that began here. Other stages start their own.
func (m *Manager) continuesCardSession(s *session) bool {
	if s.card.Session == "" || s.agent.Name != s.card.Assignee {
		return false
	}
	sessions, err := m.store.SessionsForCard(s.card.ID)
	if err != nil {
		return false
	}
	// Newest first, so the last match is the first run.
	first := ""
	for _, past := range sessions {
		if past.Work == model.WorkTerminal && past.AgentName == s.agent.Name {
			first = past.StageID
		}
	}
	return first == "" || first == s.stage.ID
}

// terminalArgv assembles the CLI's command line, and says whether the brief went
// on it. Nothing is guessed: every flag here is a column of the adapters table,
// filled in for a CLI somebody has actually run.
func terminalArgv(cli adapter, bin string, open opening, toolArgs []string, prompt string) ([]string, bool) {
	// BinPath is deliberately not consulted: for claude and codex it names the
	// vendor's ACP adapter, which is a different program with no terminal in
	// it, and running that here would open a window on a process that only
	// speaks JSON-RPC.
	argv := []string{bin}
	argv = append(argv, cli.cliArgs...)
	// Flags before the conversation: codex's `resume` is a subcommand, and a
	// `-c` given after it makes codex drop every `-c` given before.
	argv = append(argv, toolArgs...)
	argv = append(argv, open.args...)
	// «The last conversation here» has no documented way to take a task on
	// its command line, so that one is typed in, once the CLI has settled.
	if prompt != "" && !open.byFolder && cli.cliPromptArgs != nil {
		return append(argv, cli.cliPromptArgs(prompt)...), true
	}
	return argv, false
}

// terminalEnv is what the CLI inherits: this process's environment minus what
// the kind says must not be passed on, plus the agent's own.
func terminalEnv(s *session, cli adapter) []string {
	env := os.Environ()
	if len(cli.dropEnv) > 0 {
		kept := env[:0]
		for _, kv := range env {
			drop := false
			for _, name := range cli.dropEnv {
				if strings.HasPrefix(kv, name+"=") {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, kv)
			}
		}
		env = kept
	}
	if s.agent.Model != "" && cli.modelEnv != "" {
		env = append(env, cli.modelEnv+"="+s.agent.Model)
	}
	return append(env, spawnEnv(s.agent)...)
}

// toolsHandoff is what one CLI is handed for a step — our MCP server, our
// hooks: flags for its command line, variables for its environment, and the
// files to remove once the step is over.
type toolsHandoff struct {
	args  []string
	env   []string
	files []string
}

func claudeTools(url, token string) (toolsHandoff, error) {
	path, err := writeMCPConfig(url, token)
	if err != nil {
		return toolsHandoff{}, err
	}
	return toolsHandoff{args: []string{"--mcp-config", path}, files: []string{path}}, nil
}

// codexStepServer is not stagemcp.ServerName on purpose. codex merges `-c`
// into ~/.codex/config.toml key by key, even when the whole table is given,
// and the agents screen suggests registering this app there as «xxvi» with a
// command. Our url on top of that entry is a server that is both stdio and
// http, and codex refuses to start at all.
const codexStepServer = "xxvi_step"

// codexTokenEnv carries the grant. codex reads a bearer token from a variable
// it is told the name of, which keeps the grant out of the argv that ps shows
// to every user of the machine. The hooks read the grant from the same
// variable, in the same environment.
const codexTokenEnv = stagemcp.TokenEnv

// codexTools hands the server over as `-c` overrides: codex has no flag for a
// file of servers, and its own config.toml is the person's, not a card's to
// rewrite.
func codexTools(url, token string) (toolsHandoff, error) {
	if url == "" || token == "" {
		return toolsHandoff{}, msg.Err("terminal.noTools")
	}
	key := "mcp_servers." + codexStepServer
	return toolsHandoff{
		args: []string{
			"-c", key + ".url=" + strconv.Quote(url),
			"-c", key + ".bearer_token_env_var=" + strconv.Quote(codexTokenEnv),
		},
		env: []string{codexTokenEnv + "=" + token},
	}, nil
}

// writeMCPConfig writes the file the CLI is pointed at. It carries the grant,
// so it is the process's own temporary file and not something left in the folder
// the agent works in: a file of ours inside somebody's repository is ours to
// clean up and theirs to find in `git status`.
func writeMCPConfig(url, token string) (string, error) {
	if url == "" || token == "" {
		return "", msg.Err("terminal.noTools")
	}
	f, err := os.CreateTemp("", "xxvi-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("write the tools configuration: %w", err)
	}
	defer f.Close()
	err = json.NewEncoder(f).Encode(map[string]any{"mcpServers": map[string]any{
		stagemcp.ServerName: map[string]any{
			"type":    "http",
			"url":     url,
			"headers": map[string]string{"Authorization": "Bearer " + token},
		},
	}})
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write the tools configuration: %w", err)
	}
	return f.Name(), nil
}

// deliverPrompt types the brief into a CLI that could not take it on its command
// line. It waits for the terminal to fall quiet first, and gives up waiting
// rather than never delivering: a CLI that keeps drawing is a CLI that started.
//
// Bracketed paste, so a brief with newlines in it arrives as one message rather
// than as a message and several stray commands.
func deliverPrompt(m *Manager, s *session, sess *term.Session) {
	if strings.TrimSpace(s.prompt) == "" {
		return
	}
	deadline := time.Now().Add(promptWait)
	for {
		select {
		case <-sess.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		if sess.Quiet() >= promptSettle || time.Now().After(deadline) {
			break
		}
	}
	if err := sess.Write([]byte("\x1b[200~" + s.prompt + "\x1b[201~\r")); err != nil {
		m.log.Warn("could not type the brief into the terminal", "session", s.id, "err", err)
	}
}

// nextStages is where a finished step can go. All the success targets rather
// than the one the flow will pick: which one depends on what the step leaves
// behind, and it has not left it yet.
func nextStages(flow model.Flow, stageID string) []string {
	var names []string
	seen := map[string]bool{}
	for _, e := range flow.OutgoingFrom(stageID) {
		if e.On != model.TriggerSuccess || seen[e.To] {
			continue
		}
		seen[e.To] = true
		if stage, ok := flow.Stage(e.To); ok && stage.Name != "" {
			names = append(names, stage.Name)
		}
	}
	return names
}

// missingWrites names the required values a report came without, or nothing.
// The answer goes back to the agent, so it names properties rather than
// counting them: the agent has to know what to add.
func missingWrites(writes []model.PropertyWrite, r stagemcp.Report) string {
	// Only a step that succeeded owes anything: a failed one is telling us why
	// the values do not exist.
	if !r.OK {
		return ""
	}
	var missing []string
	for _, w := range writes {
		if !w.Required {
			continue
		}
		if strings.TrimSpace(r.Props[w.Property]) == "" {
			missing = append(missing, "«"+w.Property+"»")
		}
	}
	return strings.Join(missing, ", ")
}

// reportText is the report in the shape the engine already reads: what was done,
// then the declared values, one line each.
func reportText(writes []model.PropertyWrite, r stagemcp.Report) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(r.Summary))
	declared := map[string]bool{}
	for _, w := range writes {
		declared[w.Property] = true
	}
	names := make([]string, 0, len(r.Props))
	for name := range r.Props {
		if declared[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if value := strings.TrimSpace(r.Props[name]); value != "" {
			fmt.Fprintf(&b, "\n%s: %s", name, value)
		}
	}
	return strings.TrimSpace(b.String())
}

func terminalReport(r stagemcp.Report) msg.Msg {
	summary := truncate(strings.TrimSpace(r.Summary), 4000)
	switch {
	case r.OK && summary == "":
		return msg.New("report.silentStep")
	case r.OK:
		return msg.New(msg.CodeText, "text", summary)
	case summary == "":
		return msg.New("report.failedSilent")
	default:
		return msg.New("report.failed", "text", summary)
	}
}
