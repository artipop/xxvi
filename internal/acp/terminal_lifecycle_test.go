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

func TestFinishedTerminalStaysVisibleAndReturnClosesOnlyThatConversation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses /bin/cat")
	}
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rep := finishedRecorder{done: make(chan string, 4)}
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
	job := engine.Job{Card: card, Flow: model.Flow{ID: "flow"},
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
	if err := first.Write([]byte("must-not-be-typed\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if strings.Contains(first.Text(), "must-not-be-typed") {
		t.Fatal("завершённый шаг принимает ввод")
	}
	if result, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: "finish_step", Arguments: map[string]any{"outcome": "done"}}); err == nil && !result.IsError {
		t.Fatal("старый грант всё ещё принимает отчёты")
	}
	job.Visit = 2
	job.Stage.ID, job.ResumeID = "review", "review-conversation"
	review, _ := start()
	job.Visit = 3
	job.Stage.ID, job.ResumeID = "work", "conversation"
	second, _ := start()
	if review.Alive() {
		t.Fatal("возврат запустился до остановки текущего шага")
	}
	if first.Alive() || !second.Alive() {
		t.Fatal("возврат должен освободить старый процесс и открыть новый")
	}
	resumed, err := st.Session(second.ID)
	if err != nil || resumed.ACPSessionID != "conversation" || resumed.VisitID != 3 {
		t.Fatalf("не тот разговор: %+v, %v", resumed, err)
	}
	manager.Close()
	if second.Alive() {
		t.Fatal("выход оставил CLI работающим")
	}
}
