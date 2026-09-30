package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// The test binary doubles as an agent that can open a past conversation, so
// reviving a paused session run is tested over a real pipe.
const (
	fakeReviverEnv = "XXVI_FAKE_REVIVER" // resume, load, or none
	fakeReviverLog = "XXVI_FAKE_REVIVER_LOG"
	// fakeReviverHang makes a turn last until it is cancelled.
	fakeReviverHang = "XXVI_FAKE_REVIVER_HANG"
)

func init() {
	if can := os.Getenv(fakeReviverEnv); can != "" {
		fakeReviver(can)
		os.Exit(0)
	}
}

// fakeReviver answers the way the vendor adapters were seen to on 30.09:
// resume hands the conversation back silently, load replays it first.
func fakeReviver(can string) {
	var mu sync.Mutex
	write := func(v any) {
		out, _ := json.Marshal(v)
		mu.Lock()
		fmt.Println(string(out))
		mu.Unlock()
	}
	logf, _ := os.OpenFile(os.Getenv(fakeReviverLog), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	chunk := func(sid, text string) {
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": sid,
			"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}},
		}})
	}
	cancelled := make(chan struct{})
	var once sync.Once

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			continue
		}
		fmt.Fprintln(logf, req.Method)
		reply := func(result any) { write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) }
		switch req.Method {
		case "initialize":
			caps := map[string]any{"sessionCapabilities": map[string]any{}}
			switch can {
			case "resume":
				caps["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
			case "load":
				caps["loadSession"] = true
			}
			reply(map[string]any{"protocolVersion": 1, "authMethods": []any{}, "agentCapabilities": caps})
		case "session/new":
			reply(map[string]any{"sessionId": "fresh"})
		case "session/resume":
			reply(map[string]any{})
		case "session/load":
			chunk(req.Params.SessionID, "old words")
			reply(map[string]any{})
		case "session/cancel":
			once.Do(func() { close(cancelled) })
		case "session/prompt":
			sid, id := req.Params.SessionID, req.ID
			go func() {
				stop := "end_turn"
				if os.Getenv(fakeReviverHang) != "" {
					<-cancelled
					stop = "cancelled"
				} else {
					chunk(sid, "new words")
				}
				write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": stop}})
			}()
		default:
			if req.ID != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "no"}})
			}
		}
	}
}

type finishedRecorder struct {
	done chan string
}

func (r finishedRecorder) Finished(_, outcome string, _ msg.Msg, _ string) { r.done <- outcome }
func (r finishedRecorder) Abandoned(string)                                {}
func (r finishedRecorder) Described(string, string)                        {}

type reviveCase struct {
	m     *Manager
	st    *store.Store
	done  chan string
	card  model.Card
	agent model.Agent
	log   string
}

func newReviveCase(t *testing.T, can string, extraEnv map[string]string) reviveCase {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	done := make(chan string, 1)
	m := New(st, finishedRecorder{done}, nil, Options{WorkDir: t.TempDir()}, nil)
	t.Cleanup(m.Close)
	card, err := st.CreateCard(model.Card{Title: "Оживить", State: model.StateFlow})
	if err != nil {
		t.Fatal(err)
	}
	log := t.TempDir() + "/methods"
	env := map[string]string{fakeReviverEnv: can, fakeReviverLog: log}
	for k, v := range extraEnv {
		env[k] = v
	}
	agent := model.Agent{Name: "fake", Kind: model.KindACP, Command: []string{os.Args[0]}, Env: env}
	return reviveCase{m: m, st: st, done: done, card: card, agent: agent, log: log}
}

// paused records an earlier run of the stage that the application closed on.
func (c reviveCase) paused(t *testing.T, conversation string) {
	t.Helper()
	if err := c.st.InsertSession(store.Session{
		ID: "paused", CardID: c.card.ID, StageID: "plan", AgentName: "fake", AgentKind: model.KindACP,
		Work: model.WorkSession, Status: store.StatusRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	status, yes := store.StatusPaused, true
	if err := c.st.UpdateSession("paused", store.SessionUpdate{Status: &status, ACPSessionID: &conversation, Revivable: &yes}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
}

func (c reviveCase) start(t *testing.T) {
	t.Helper()
	stage := model.Stage{ID: "plan", Work: model.WorkSession}
	job := engine.Job{Card: c.card, Flow: model.Flow{ID: "flow"}, Stage: stage, Agent: c.agent, Prompt: "Continue where you left off."}
	if err := c.m.Start(job); err != nil {
		t.Fatalf("старт: %v", err)
	}
}

func (c reviveCase) wait(t *testing.T) string {
	t.Helper()
	select {
	case outcome := <-c.done:
		return outcome
	case <-time.After(20 * time.Second):
		t.Fatal("шаг не закончился")
		return ""
	}
}

// latest is the run started by the test: the newest on the card.
func (c reviveCase) latest(t *testing.T) store.Session {
	t.Helper()
	sessions, err := c.st.SessionsForCard(c.card.ID)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("прогоны карточки: %v", err)
	}
	return sessions[0]
}

func (c reviveCase) methods(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(c.log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func (c reviveCase) said(t *testing.T, sessionID string) string {
	t.Helper()
	events, err := c.st.SessionEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, string(e.Payload))
	}
	return strings.Join(out, " ")
}

// A paused run is continued in its own conversation, and resume is preferred
// because it replays nothing.
func TestPausedSessionIsResumedInItsConversation(t *testing.T) {
	c := newReviveCase(t, "resume", nil)
	c.paused(t, "old-conversation")
	c.start(t)
	if outcome := c.wait(t); outcome != model.TriggerSuccess {
		t.Fatalf("шаг должен закончиться успехом, а не %q", outcome)
	}
	got := c.methods(t)
	if !slices.Contains(got, "session/resume") || slices.Contains(got, "session/new") {
		t.Fatalf("оживление идёт через session/resume, а не новый разговор: %v", got)
	}
	run := c.latest(t)
	if run.ACPSessionID != "old-conversation" || !run.Revivable {
		t.Fatalf("новый прогон держит тот же разговор и сам оживляем: %+v", run)
	}
}

// An agent that can only load replays the conversation first. Every word of
// it is already in the paused run's stream; recording it again would show the
// ribbon the same conversation twice.
func TestLoadedConversationIsNotRecordedTwice(t *testing.T) {
	c := newReviveCase(t, "load", nil)
	c.paused(t, "old-conversation")
	c.start(t)
	c.wait(t)
	if got := c.methods(t); !slices.Contains(got, "session/load") {
		t.Fatalf("без resume оживление идёт через session/load: %v", got)
	}
	said := c.said(t, c.latest(t).ID)
	if strings.Contains(said, "old words") || !strings.Contains(said, "new words") {
		t.Fatalf("в поток прогона попадает только новый ход: %s", said)
	}
}

// Only a paused run is continued: after a finished one the stage starts a
// conversation of its own, as a session run always has.
func TestSessionAfterAFinishedRunStartsAnew(t *testing.T) {
	c := newReviveCase(t, "resume", nil)
	c.paused(t, "old-conversation")
	status := store.StatusDone
	if err := c.st.UpdateSession("paused", store.SessionUpdate{Status: &status}); err != nil {
		t.Fatal(err)
	}
	c.start(t)
	c.wait(t)
	if got := c.methods(t); slices.Contains(got, "session/resume") || !slices.Contains(got, "session/new") {
		t.Fatalf("после законченного прогона разговор новый: %v", got)
	}
}

// The application closing on a run whose agent can open its conversation again
// pauses it, as it does a terminal; one whose agent cannot is cancelled.
func TestClosingPausesOnlyARevivableSession(t *testing.T) {
	for _, tc := range []struct {
		can  string
		want store.SessionStatus
	}{
		{"resume", store.StatusPaused},
		{"none", store.StatusCancelled},
	} {
		t.Run(tc.can, func(t *testing.T) {
			c := newReviveCase(t, tc.can, map[string]string{fakeReviverHang: "1"})
			c.start(t)
			deadline := time.Now().Add(20 * time.Second)
			for c.latest(t).Status != store.StatusRunning {
				if time.Now().After(deadline) {
					t.Fatal("шаг не начал работать")
				}
				time.Sleep(20 * time.Millisecond)
			}
			c.m.Close()
			if got := c.latest(t).Status; got != tc.want {
				t.Fatalf("после закрытия приложения прогон %s, ждали %s", got, tc.want)
			}
		})
	}
}

// What the agent says decides, and resume wins over load; a row that knows
// the agent's claim does not hold overrides it.
func TestReviveFollowsTheAgentUnlessItsRowKnowsBetter(t *testing.T) {
	both := acpsdk.AgentCapabilities{LoadSession: true}
	both.SessionCapabilities.Resume = &acpsdk.SessionResumeCapabilities{}
	if got := reviveBy(model.KindACP, both); got != "resume" {
		t.Fatalf("resume предпочтительнее load: %q", got)
	}
	if got := reviveBy(model.KindACP, acpsdk.AgentCapabilities{LoadSession: true}); got != "load" {
		t.Fatalf("без resume — load: %q", got)
	}
	if got := reviveBy(model.KindACP, acpsdk.AgentCapabilities{}); got != "" {
		t.Fatalf("без обоих не оживляется: %q", got)
	}
	if got := reviveBy(model.KindJunie, both); got != "" {
		t.Fatalf("junie не оживляется, что бы он ни заявлял: %q", got)
	}
}
