package stagemcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/artipop/xxvi/internal/model"
)

// call opens an MCP client on the running server with one grant and calls the
// tool, the way a vendor CLI handed the config file would.
func call(t *testing.T, s *Server, token string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	return callTool(t, s, token, "finish_step", args)
}

func callTool(t *testing.T, s *Server, token, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   s.URL(),
		HTTPClient: &http.Client{Transport: bearer{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "тест", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	return sess.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func serve(t *testing.T) *Server {
	t.Helper()
	s := New(nil)
	if err := s.Listen(); err != nil {
		t.Fatalf("поднять сервер инструментов: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// The report is the whole reason this exists: without it a card worked in a
// terminal would stand on its stage forever.
func TestReportEndsTheStep(t *testing.T) {
	s := serve(t)
	got := make(chan Report, 1)
	token := s.Grant(Step{
		StageName: "В работе",
		Report:    func(r Report) error { got <- r; return nil },
	})

	res, err := call(t, s, token, map[string]any{
		"outcome": "done", "summary": "Сделал", "properties": map[string]any{"Ветка": "fix/login"},
	})
	if err != nil {
		t.Fatalf("вызвать инструмент: %v", err)
	}
	if res.IsError {
		t.Fatalf("вызов не должен быть отказом: %+v", res.Content)
	}
	report := <-got
	if !report.OK || report.Summary != "Сделал" || report.Props["Ветка"] != "fix/login" {
		t.Fatalf("отчёт доехал не целиком: %+v", report)
	}
}

// The agent asks before it closes the step, naming where the card goes.
func TestToolAsksBeforeFinishing(t *testing.T) {
	desc := describe(Step{Next: []string{"Ревью", "Деплой"}})
	if !strings.Contains(desc, "move on to «Ревью» or «Деплой»?") {
		t.Fatalf("описание не просит спросить человека: %s", desc)
	}
	if !strings.Contains(instructions(Step{}), "move on to the next step?") {
		t.Fatalf("без следующей стадии вопрос должен остаться")
	}
}

// The refusal is written to be acted on: the agent has to know what to add, and
// the step has not ended while it is missing.
func TestMissingValueComesBackToTheAgent(t *testing.T) {
	s := serve(t)
	token := s.Grant(Step{
		Writes: []model.PropertyWrite{{Property: "Вердикт", Required: true}},
		Report: func(Report) error {
			return fmt.Errorf("шаг не закончен: не хватает «Вердикт»")
		},
	})

	res, err := call(t, s, token, map[string]any{"outcome": "done", "summary": "Проверил"})
	if err != nil {
		t.Fatalf("вызвать инструмент: %v", err)
	}
	if !res.IsError {
		t.Fatal("отчёт без обязательного значения должен быть отказан")
	}
	if text := content(res); !strings.Contains(text, "Вердикт") {
		t.Fatalf("отказ должен называть недостающее: %q", text)
	}
}

// An outcome is one of two words. A third would be a card routed by something
// the flow has no arrow for.
func TestUnknownOutcomeIsRefused(t *testing.T) {
	s := serve(t)
	called := false
	token := s.Grant(Step{Report: func(Report) error { called = true; return nil }})

	res, err := call(t, s, token, map[string]any{"outcome": "почти", "summary": "ну как-то так"})
	if err != nil {
		t.Fatalf("вызвать инструмент: %v", err)
	}
	if !res.IsError || called {
		t.Fatalf("третьего исхода нет: %+v", res.Content)
	}
}

// A grant dies with its run: a token found afterwards opens nothing.
func TestRevokedGrantOpensNothing(t *testing.T) {
	s := serve(t)
	token := s.Grant(Step{Report: func(Report) error { return nil }})
	s.Revoke(token)

	if _, err := call(t, s, token, map[string]any{"outcome": "done", "summary": "поздно"}); err == nil {
		t.Fatal("отозванный грант не должен пускать")
	}
	if _, err := call(t, s, "чужой", map[string]any{"outcome": "done", "summary": "нет"}); err == nil {
		t.Fatal("чужой токен не должен пускать")
	}
}

// What the agent reads before calling: the properties it is expected to bring,
// by name, and which of them the step cannot end without.
func TestToolNamesTheValuesItWants(t *testing.T) {
	text := describe(Step{Writes: []model.PropertyWrite{
		{Property: "Вердикт", Required: true},
		{Property: "Превью"},
	}})
	if !strings.Contains(text, "«Вердикт»") || !strings.Contains(text, "required") {
		t.Fatalf("обязательное свойство должно быть названо: %q", text)
	}
	if !strings.Contains(text, "«Превью»") {
		t.Fatalf("необязательное свойство тоже названо: %q", text)
	}
}

// An agent that reports alongside its last command reports before knowing how
// that command went, and the terminal is closed on it seconds later. Both
// places the agent reads before calling say so, the ones with values and
// without alike.
func TestFinishIsToldToBeTheLastCall(t *testing.T) {
	for _, text := range []string{
		describe(Step{}),
		describe(Step{Writes: []model.PropertyWrite{{Property: "Вердикт"}}}),
		instructions(Step{}),
	} {
		if !strings.Contains(text, lastCall) {
			t.Fatalf("агенту должно быть сказано, что это последний вызов: %q", text)
		}
	}
}

// A hook reaches its step with the grant the CLI inherited, and only the
// fields a step acts on make the trip: the prompt stays with the hook.
func TestHookReachesItsStep(t *testing.T) {
	s := serve(t)
	got := make(chan HookEvent, 1)
	token := s.Grant(Step{Report: func(Report) error { return nil }, Hook: func(ev HookEvent) { got <- ev }})
	env := map[string]string{HookURLEnv: s.HookURL(), TokenEnv: token}
	getenv := func(k string) string { return env[k] }

	payload := `{"hook_event_name":"SessionStart","session_id":"abc","source":"clear","prompt":"секрет","transcript_path":"/x"}`
	if err := ForwardHook(context.Background(), strings.NewReader(payload), getenv); err != nil {
		t.Fatalf("переслать событие: %v", err)
	}
	ev := <-got
	if ev.Event != "SessionStart" || ev.SessionID != "abc" || ev.Source != "clear" {
		t.Fatalf("событие доехало не целиком: %+v", ev)
	}

	s.Revoke(token)
	if err := ForwardHook(context.Background(), strings.NewReader(payload), getenv); err == nil {
		t.Fatal("после конца шага события не принимаются")
	}
}

// Started by hand, outside a stage, the hook has nowhere to go and says so
// without trying.
func TestHookOutsideAStep(t *testing.T) {
	err := ForwardHook(context.Background(), strings.NewReader(`{"hook_event_name":"Stop"}`), func(string) string { return "" })
	if err == nil {
		t.Fatal("без шага пересылать некуда")
	}
}

func content(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

// A card leaving its flow is described by the conversation that worked it, and
// the words reach the step as they were written.
func TestDescriptionReachesItsStep(t *testing.T) {
	s := serve(t)
	got := make(chan string, 1)
	token := s.Grant(Step{
		Report:   func(Report) error { return nil },
		Describe: func(text string) error { got <- text; return nil },
	})
	res, err := callTool(t, s, token, "describe_task", map[string]any{"description": "  Переезд на ORM: схема готова, миграции — нет.  "})
	if err != nil {
		t.Fatalf("вызвать инструмент: %v", err)
	}
	if res.IsError {
		t.Fatalf("вызов не должен быть отказом: %+v", res.Content)
	}
	if text := <-got; text != "Переезд на ORM: схема готова, миграции — нет." {
		t.Fatalf("описание доехало не тем: %q", text)
	}
}

// A step that takes no description has no such tool to call.
func TestNoDescriptionToolWithoutTaker(t *testing.T) {
	s := serve(t)
	token := s.Grant(Step{Report: func(Report) error { return nil }})
	res, err := callTool(t, s, token, "describe_task", map[string]any{"description": "что-то"})
	if err == nil && !res.IsError {
		t.Fatal("без Describe инструмента быть не должно")
	}
}
