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
// exit cannot stand in for a report. And a stuck step is noticed by the CLI
// drawing nothing, because a question inside a TUI is invisible from out here.

// terminalQuietFor is how long a stage's CLI must draw nothing before the card
// says it is waiting for a person. Generous on purpose: a model thinking
// between tool calls is silent for a while, and a card that cries out early is
// a card nobody believes.
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
	token := tools.Grant(stagemcp.Step{
		CardTitle: s.card.Title,
		StageName: s.stage.Name,
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
	})
	defer tools.Revoke(token)

	handoff, err := cli.cliTools(tools.URL(), token)
	if err != nil {
		m.failTerminal(s, msg.Of(err))
		return
	}
	if handoff.file != "" {
		defer os.Remove(handoff.file)
	}

	// A second visit to the same stage continues the conversation the folder
	// already holds: the folder is the card's, so «the last conversation here»
	// is that card's, and starting from nothing would mean asking somebody the
	// same questions twice.
	var resume []string
	switch {
	case m.continuesCardSession(s) && cli.cliResumeID != nil:
		resume = cli.cliResumeID(s.card.Session)
	case m.workedBefore(s):
		resume = cli.cliResumeArgs
	}
	bin, err := terminalBin(cli)
	if err != nil {
		m.failTerminal(s, msg.New("terminal.binMissing", "bin", cli.cliBin))
		return
	}
	argv, promptTaken := terminalArgv(cli, bin, resume, handoff.args, s.prompt)

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

	report, err := m.watchTerminal(ctx, s, sess, reported)

	// The CLI ends with the step: this terminal belongs to the run, not to the
	// person, and one left open on a card that has moved on is a conversation
	// about work that is no longer here. What was drawn in it is kept, so the
	// segment still shows what happened (internal/term).
	if err == nil {
		time.Sleep(terminalGrace)
	}
	sess.Close()

	switch {
	case m.rootCtx.Err() != nil:
		m.finish(s, store.StatusCancelled, msg.New("session.appQuitting"))
	case s.wasCancelled():
		m.finish(s, store.StatusCancelled, msg.New("session.stepCancelled"))
		m.record(s, model.EntryProblem, msg.New("journal.terminalCancelled"))
	case closedByPerson(err, time.Since(opened)):
		// Somebody ended the conversation. That is an intervention, not an
		// outcome: the card stays, and where it goes next is theirs to say.
		s.markCancelled()
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
// somebody stepping in. It also watches the silence, which is the only thing a
// stage in a terminal says about itself without being asked.
func (m *Manager) watchTerminal(
	ctx context.Context, s *session, sess *term.Session, reported <-chan stagemcp.Report,
) (stagemcp.Report, error) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	waiting := false
	defer func() {
		if waiting {
			m.clearQuiet(s)
		}
	}()

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

		case <-tick.C:
			quiet := sess.Quiet() >= terminalQuietFor
			if quiet == waiting {
				continue
			}
			waiting = quiet
			if quiet {
				m.raiseQuiet(s)
			} else {
				m.clearQuiet(s)
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

// workedBefore reports whether this card has already had a run on this stage,
// which is what makes the CLI continue rather than begin.
func (m *Manager) workedBefore(s *session) bool {
	sessions, err := m.store.SessionsForCard(s.card.ID)
	if err != nil {
		return false
	}
	for _, past := range sessions {
		if past.ID != s.id && past.StageID == s.stage.ID && past.Work == model.WorkTerminal {
			return true
		}
	}
	return false
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
func terminalArgv(cli adapter, bin string, resume, toolArgs []string, prompt string) ([]string, bool) {
	// BinPath is deliberately not consulted: for claude and codex it names the
	// vendor's ACP adapter, which is a different program with no terminal in
	// it, and running that here would open a window on a process that only
	// speaks JSON-RPC.
	argv := []string{bin}
	argv = append(argv, resume...)
	argv = append(argv, toolArgs...)
	// A resumed conversation already has a transcript, and putting a task on
	// that command line is a flag combination no vendor documents. It is typed
	// in instead, once the CLI has settled.
	if prompt != "" && len(resume) == 0 && cli.cliPromptArgs != nil {
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

// toolsHandoff is our MCP server as one CLI takes it: flags for its command
// line, variables for its environment, and the file to remove once the step is
// over.
type toolsHandoff struct {
	args []string
	env  []string
	file string
}

func claudeTools(url, token string) (toolsHandoff, error) {
	path, err := writeMCPConfig(url, token)
	if err != nil {
		return toolsHandoff{}, err
	}
	return toolsHandoff{args: []string{"--mcp-config", path}, file: path}, nil
}

// codexStepServer is not stagemcp.ServerName on purpose. codex merges `-c`
// into ~/.codex/config.toml key by key, even when the whole table is given,
// and the agents screen suggests registering this app there as «xxvi» with a
// command. Our url on top of that entry is a server that is both stdio and
// http, and codex refuses to start at all.
const codexStepServer = "xxvi_step"

// codexTokenEnv carries the grant. codex reads a bearer token from a variable
// it is told the name of, which keeps the grant out of the argv that ps shows
// to every user of the machine.
const codexTokenEnv = "XXVI_STEP_TOKEN"

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
