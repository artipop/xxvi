package acp

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/artipop/gandvik/internal/engine"
	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
	"github.com/artipop/gandvik/internal/stagemcp"
	"github.com/artipop/gandvik/internal/store"
	"github.com/artipop/gandvik/internal/term"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type terminalBearer struct{ token string }

func (b terminalBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestFinishedTerminalStaysWorkableAndReturnClosesOnlyThatConversation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses /bin/cat")
	}
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rep := &rejoinRecorder{finishedRecorder: finishedRecorder{done: make(chan string, 4)}, rejoins: make(chan engine.Again, 4)}
	manager := New(st, rep, nil, Options{WorkDir: t.TempDir()}, nil)
	terms := term.NewManager(manager.WorkDir, nil)
	defer terms.Close()
	defer manager.Close()
	server := stagemcp.New(nil)
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	manager.SetTerminals(terms, server)
	tokens := make(chan string, 4)
	const kind = "test-terminal-lifecycle"
	adapters[kind] = adapter{
		cliBin:      "/bin/cat",
		cliTools:    func(_, token string) (toolsHandoff, error) { tokens <- token; return toolsHandoff{}, nil },
		cliResumeID: func(string) []string { return nil },
	}
	defer delete(adapters, kind)
	card, err := st.CreateCard(model.Card{Title: "Терминал"})
	if err != nil {
		t.Fatal(err)
	}
	job := engine.Job{Card: card, Flow: model.Flow{ID: "flow", Stages: []model.Stage{
		{ID: "work", Name: "Работа", Action: model.ActionAgent, Prompt: "Сделай задачу\nподробности"},
		{ID: "review", Name: "Ревью", Action: model.ActionAgent},
		{ID: "deploy", Name: "Деплой", Action: model.ActionPublish},
	}},
		Stage: model.Stage{ID: "work", Work: model.WorkTerminal}, Agent: model.Agent{Name: "test", Kind: kind},
		ResumeID: "conversation", Visit: 1,
	}
	start := func() (*term.Session, string) {
		t.Helper()
		if err := manager.Start(job); err != nil {
			t.Fatal(err)
		}
		var token string
		select {
		case token = <-tokens:
		case <-time.After(5 * time.Second):
			t.Fatal("нет гранта")
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			runs, _ := st.SessionsForCard(card.ID)
			if len(runs) > 0 {
				if terminal := terms.Get(runs[0].ID); terminal != nil {
					return terminal, token
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("терминал не открылся")
		return nil, ""
	}
	first, token := start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "lifecycle-test", Version: "1"}, nil)
	connection, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL(), HTTPClient: &http.Client{Transport: terminalBearer{token}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	result, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: "finish_step", Arguments: map[string]any{"outcome": "done", "summary": "готово"}})
	if err != nil || result.IsError {
		t.Fatalf("отчёт: %+v, %v", result, err)
	}
	select {
	case outcome := <-rep.done:
		if outcome != model.TriggerSuccess {
			t.Fatal(outcome)
		}
	case <-ctx.Done():
		t.Fatal("отчёт не доставлен")
	}
	if !first.Alive() {
		t.Fatal("переход закрыл терминал")
	}
	if manager.RunningOnStage("work") != 0 {
		t.Fatal("законченный терминал занимает слот")
	}
	run, err := st.Session(first.ID)
	if err != nil || run.Status != store.StatusDone || run.VisitID != 1 {
		t.Fatalf("прогон: %+v, %v", run, err)
	}
	// The finished step's CLI is still the person's to talk to.
	if err := first.Write([]byte("still-typed\n")); err != nil {
		t.Fatal(err)
	}
	waitText(t, first, "still-typed")
	if err := first.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	result, err = connection.CallTool(ctx, &mcp.CallToolParams{Name: "finish_step", Arguments: map[string]any{"outcome": "done", "summary": "ещё раз"}})
	if err != nil || result.IsError {
		t.Fatalf("повторный отчёт: %+v, %v", result, err)
	}
	again := <-rep.rejoins
	if again.Outcome != model.TriggerSuccess || again.Target != "" || again.Reported.IsZero() || !again.Asked.After(again.Reported) {
		t.Fatalf("повторный отчёт идёт в движок с репликой человека: %+v", again)
	}
	tools, err := connection.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	described := ""
	for _, tool := range tools.Tools {
		if tool.Name == "move_to" {
			described = tool.Description
		}
	}
	if !strings.Contains(described, "«Деплой» — the branch is pushed") || !strings.Contains(described, "«Работа» — an agent works on the task in a terminal: Сделай задачу") {
		t.Fatalf("move_to описывает ноды флоу: %q", described)
	}
	result, err = connection.CallTool(ctx, &mcp.CallToolParams{Name: "move_to", Arguments: map[string]any{"stage": "Деплой", "summary": "задеплой"}})
	if err != nil || result.IsError {
		t.Fatalf("move_to: %+v, %v", result, err)
	}
	if moved := <-rep.rejoins; moved.Target != "Деплой" || moved.Outcome != "" {
		t.Fatalf("move_to: %+v", moved)
	}
	job.Visit = 2
	job.Stage.ID, job.ResumeID = "review", "review-conversation"
	review, _ := start()
	// The card went on without the review: that one keeps working.
	job.Visit = 3
	job.Stage.ID, job.ResumeID = "deploy", "deploy-conversation"
	deploy, _ := start()
	if !review.Alive() || !deploy.Alive() {
		t.Fatal("новый шаг не должен закрывать работающий")
	}
	// Revisit cancels what runs before it reopens a conversation.
	manager.Cancel(card.ID, msg.Msg{})
	job.Visit = 4
	job.Stage.ID, job.ResumeID = "work", "conversation"
	second, _ := start()
	if review.Alive() || deploy.Alive() {
		t.Fatal("возврат запустился до остановки текущих шагов")
	}
	if first.Alive() || !second.Alive() {
		t.Fatal("возврат должен освободить старый процесс и открыть новый")
	}
	resumed, err := st.Session(second.ID)
	if err != nil || resumed.ACPSessionID != "conversation" || resumed.VisitID != 4 {
		t.Fatalf("не тот разговор: %+v, %v", resumed, err)
	}
	manager.Close()
	if second.Alive() {
		t.Fatal("выход оставил CLI работающим")
	}
}

// rejoinRecorder takes every first report as the current visit's and keeps
// what the terminal asks the engine to do after that.
type rejoinRecorder struct {
	finishedRecorder
	rejoins chan engine.Again
}

func (r *rejoinRecorder) Rejoin(_ engine.Job, a engine.Again) error {
	r.rejoins <- a
	return nil
}

func waitText(t *testing.T, s *term.Session, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(s.Text(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("на экране нет %q", text)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
