//go:build liveagent

// Works a stage in the real vendor CLIs, end to end: the hooks reach the step,
// the conversation id follows the CLI, and a return to the stage resumes that
// conversation by id. It talks to real models — tiny prompts, but not free:
//
//	XXVI_LIVE_DIR=/a/folder/both/CLIs/already/trust \
//	go test -tags liveagent ./internal/acp/ -run LiveTerminal -v -timeout 15m
//
// The folder has to be trusted beforehand: the trust dialog differs by vendor
// and by where the folder is, and answering it is a person's job anyway.
package acp

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/stagemcp"
	"github.com/artipop/xxvi/internal/store"
	"github.com/artipop/xxvi/internal/term"
)

type liveUI struct{ waits chan Attention }

func (u liveUI) Emit(event string, payload any) {
	if a, ok := payload.(Attention); ok && event == EventAttention && a.Terminal != "" {
		select {
		case u.waits <- a:
		default:
		}
	}
}

type liveReporter struct{ done chan string }

func (r liveReporter) Finished(_ string, outcome string, _ msg.Msg, text string) {
	r.done <- outcome + "|" + text
}

type liveStage struct {
	t     *testing.T
	m     *Manager
	st    *store.Store
	terms *term.Manager
	ui    liveUI
	rep   liveReporter
	tools *stagemcp.Server
	job   engine.Job
}

func newLiveStage(t *testing.T, kind, modelName string) *liveStage {
	dir := os.Getenv("XXVI_LIVE_DIR")
	if dir == "" {
		t.Skip("XXVI_LIVE_DIR is not set")
	}
	// Run from inside Claude Code, a child claude inherits its session and
	// saves no transcript: nothing would be left to resume.
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CLAUDE") {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	l := &liveStage{t: t, st: st, ui: liveUI{make(chan Attention, 100)}, rep: liveReporter{make(chan string, 1)}}
	l.m = New(st, l.rep, l.ui, Options{WorkDir: t.TempDir()}, nil)
	t.Cleanup(l.m.Close)
	l.terms = term.NewManager(l.m.WorkDir, nil)
	t.Cleanup(l.terms.Close)
	tools := stagemcp.New(nil)
	if err := tools.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tools.Close)
	l.m.SetTerminals(l.terms, tools)
	l.tools = tools

	project, err := st.SaveProject(model.Project{Name: "live", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	card, err := st.CreateCard(model.Card{Title: "Живая проверка " + kind, State: model.StateFlow, Project: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	stage := model.Stage{ID: "plan", Name: "План", Work: model.WorkTerminal}
	l.job = engine.Job{
		Card:  card,
		Flow:  model.Flow{ID: "flow", Stages: []model.Stage{stage}},
		Stage: stage,
		Agent: model.Agent{Name: kind, Kind: kind, Model: modelName},
	}
	return l
}

// start opens the stage with a brief and returns its run.
func (l *liveStage) start(prompt string) string {
	l.job.Prompt = prompt
	if err := l.m.Start(l.job); err != nil {
		l.t.Fatal(err)
	}
	runs, err := l.st.SessionsForCard(l.job.Card.ID)
	if err != nil {
		l.t.Fatal(err)
	}
	return runs[0].ID
}

func (l *liveStage) conversation(run string) string {
	runs, _ := l.st.SessionsForCard(l.job.Card.ID)
	for _, r := range runs {
		if r.ID == run {
			return r.ACPSessionID
		}
	}
	return ""
}

// resumed is the conversation a run was opened on, which it records as soon as
// its goroutine has got that far.
func (l *liveStage) resumed(run string) string {
	deadline := time.Now().Add(10 * time.Second)
	for l.conversation(run) == "" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	return l.conversation(run)
}

func (l *liveStage) screen(run string) string {
	if s := l.terms.Get(run); s != nil {
		h := s.History()
		if len(h) > 3000 {
			h = h[len(h)-3000:]
		}
		return string(h)
	}
	return "(no terminal)"
}

// idle waits for the CLI to have ended its turn: its conversation is on record
// and it has drawn nothing for a while. The end of a turn is no mark of its own
// on the card (a person talking to the agent would get one per answer), so the
// test reads it the way a person would.
func (l *liveStage) idle(run string) {
	l.t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if s := l.terms.Get(run); s != nil && l.conversation(run) != "" && s.Quiet() >= 4*time.Second {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	l.t.Fatalf("the turn did not end; screen:\n%s", l.screen(run))
}

func (l *liveStage) say(run, text string) {
	l.t.Helper()
	s := l.terms.Get(run)
	if s == nil {
		l.t.Fatal("the terminal is gone")
	}
	time.Sleep(time.Second)
	if strings.HasPrefix(text, "/") {
		_ = s.Write([]byte(text))
		time.Sleep(time.Second)
		_ = s.Write([]byte("\r"))
		return
	}
	_ = s.Write([]byte("\x1b[200~" + text + "\x1b[201~"))
	time.Sleep(500 * time.Millisecond)
	_ = s.Write([]byte("\r"))
}

// finish asks for the report and approves the tool when the CLI asks.
func (l *liveStage) finish(run, summary string) string {
	l.t.Helper()
	l.say(run, "Now call the finish_step tool right away with outcome done and summary: "+summary+". Do not ask me first.")
	for {
		select {
		case done := <-l.rep.done:
			return done
		case a := <-l.ui.waits:
			l.t.Logf("mark: %s awaiting=%v", a.Terminal, a.Awaiting)
			if a.Awaiting && a.Terminal == waitAsking {
				time.Sleep(time.Second)
				_ = l.terms.Get(run).Write([]byte("\r"))
			}
		case <-time.After(3 * time.Minute):
			l.t.Fatalf("the step did not finish; screen:\n%s", l.screen(run))
		}
	}
}

func TestLiveTerminalClaude(t *testing.T) {
	l := newLiveStage(t, model.KindClaude, "haiku")

	run := l.start("Reply with just the word pong. Do not use any tools.")
	l.idle(run)
	first := l.conversation(run)
	if first == "" {
		t.Fatal("the conversation id was not recorded")
	}
	t.Logf("first conversation: %s", first)

	l.say(run, "/clear")
	deadline := time.Now().Add(30 * time.Second)
	for l.conversation(run) == first && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
	}
	second := l.conversation(run)
	if second == first {
		t.Fatalf("/clear moved claude to another conversation, and the row did not follow")
	}
	t.Logf("after /clear: %s", second)

	l.say(run, "Reply with just the word ping. Do not use any tools.")
	l.idle(run)
	if done := l.finish(run, "ok"); !strings.HasPrefix(done, model.TriggerSuccess) {
		t.Fatalf("the step did not succeed: %s", done)
	}
	time.Sleep(4 * time.Second) // release() runs after Finished

	again := l.start("Which single word did you reply with earlier in this conversation? Answer with that word only. Do not use any tools.")
	if got := l.resumed(again); got != second {
		t.Fatalf("the return resumes the conversation the last run ended on: %q, want %q", got, second)
	}
	l.idle(again)
	done := l.finish(again, "the word you replied with earlier")
	t.Logf("second run: %s", done)
	if !strings.Contains(strings.ToLower(done), "ping") || strings.Contains(strings.ToLower(done), "pong") {
		t.Fatalf("the resumed conversation is not the one after /clear: %s", done)
	}
}

func TestLiveTerminalCodex(t *testing.T) {
	l := newLiveStage(t, model.KindCodex, "")

	run := l.start("Reply with just the word pong. Do not run any commands.")
	l.idle(run)
	first := l.conversation(run)
	if first == "" {
		t.Fatal("the conversation id was not recorded")
	}
	t.Logf("conversation: %s", first)
	if done := l.finish(run, "ok"); !strings.HasPrefix(done, model.TriggerSuccess) {
		t.Fatalf("the step did not succeed: %s", done)
	}
	time.Sleep(4 * time.Second)

	again := l.start("Which single word did you reply with at the start of this conversation? Answer with that word only. Do not run any commands.")
	if got := l.resumed(again); got != first {
		t.Fatalf("the return resumes the stage's conversation by id: %q, want %q", got, first)
	}
	l.idle(again)
	done := l.finish(again, "the word you replied with at the start")
	t.Logf("second run: %s", done)
	if !strings.Contains(strings.ToLower(done), "pong") {
		t.Fatalf("the resumed conversation does not remember its start: %s", done)
	}
}

// The application closing mid-step pauses the step instead of cancelling it,
// and the next run of the application continues it in the same conversation.
func TestLiveTerminalPauseClaude(t *testing.T) {
	l := newLiveStage(t, model.KindClaude, "haiku")

	run := l.start("Remember the word kumquat. Reply with just OK. Do not use any tools.")
	l.idle(run)
	conversation := l.conversation(run)

	l.m.Close() // the application quitting
	runs, _ := l.st.SessionsForCard(l.job.Card.ID)
	if runs[0].ID != run || runs[0].Status != store.StatusPaused {
		t.Fatalf("closing the application pauses the step: %s is %s", runs[0].ID, runs[0].Status)
	}

	// The next run of the application, and a person pressing «Continue»: the
	// stage is started again, told only what the person said.
	l.m = New(l.st, l.rep, l.ui, Options{WorkDir: t.TempDir()}, nil)
	t.Cleanup(l.m.Close)
	l.m.SetTerminals(l.terms, l.tools)
	again := l.start("Which word did I ask you to remember? Answer with that word only. Do not use any tools.")
	if got := l.resumed(again); got != conversation {
		t.Fatalf("continuing resumes the paused conversation: %q, want %q", got, conversation)
	}
	l.idle(again)
	done := l.finish(again, "the word you were asked to remember")
	t.Logf("continued: %s", done)
	if !strings.Contains(strings.ToLower(done), "kumquat") {
		t.Fatalf("the continued conversation does not remember the paused one: %s", done)
	}
}
