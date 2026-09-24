package appmcp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/store"
)

// The bridge is what makes `xxvi mcp` a line somebody can write once: the port
// is new on every launch, the command is not. What it must not be is a second
// place the tools are described — so what it forwards is whatever the running
// application says it has.

func serve(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "xxvi.db"))
	if err != nil {
		t.Fatalf("открыть базу: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.SaveFlow(model.Flow{
		Name: "Пример", EntryStage: "one",
		Stages: []model.Stage{
			{ID: "one", Name: "Шаг", Action: model.ActionNone},
			{ID: "two", Name: "Готово", Final: true},
		},
		Edges: []model.Edge{{From: "one", To: "two", On: model.TriggerCardChanged,
			If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomePassed}}},
	}); err != nil {
		t.Fatalf("сохранить флоу: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(Deps{Store: st, Engine: engine.New(st, nil, nil, quiet)}, quiet)
	if err := s.Listen(); err != nil {
		t.Fatalf("поднять сервер: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestBridgeCarriesTheToolsThrough(t *testing.T) {
	s := serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, client := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- Bridge(ctx, Handoff{URL: s.URL(), Token: s.Token()}, server) }()

	sess, err := mcp.NewClient(&mcp.Implementation{Name: "тест", Version: "1"}, nil).
		Connect(ctx, client, nil)
	if err != nil {
		t.Fatalf("подключиться через мост: %v", err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("список инструментов: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"flows", "cards", "card", "add_card", "take_into_work", "finish_step", "set_property"} {
		if !names[want] {
			t.Fatalf("мост не пронёс инструмент %s: %v", want, names)
		}
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "flows"})
	if err != nil {
		t.Fatalf("вызвать через мост: %v", err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	if !strings.Contains(b.String(), "Пример") {
		t.Fatalf("через мост должен приходить ответ приложения: %q", b.String())
	}
}

// A bridge with nobody to bridge to says what is wrong in the sentence somebody
// will read in their agent's log: the tools live inside the application, so the
// answer is to open it.
func TestBridgeWithoutARunningApplication(t *testing.T) {
	_, err := ReadHandoff(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("без запущенного приложения ожидается внятный отказ, получено: %v", err)
	}
}

func TestHandoffRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteHandoff(dir, "http://127.0.0.1:1/mcp", "секрет"); err != nil {
		t.Fatalf("записать адрес: %v", err)
	}
	h, err := ReadHandoff(dir)
	if err != nil {
		t.Fatalf("прочитать адрес: %v", err)
	}
	if h.URL != "http://127.0.0.1:1/mcp" || h.Token != "секрет" {
		t.Fatalf("адрес прочитался не тем: %+v", h)
	}
	RemoveHandoff(dir)
	if _, err := ReadHandoff(dir); err == nil {
		t.Fatal("убранный адрес не должен читаться")
	}
}

// An agent ends a session by closing stdio, and that is how every session ends.
// It must come back as success: a non-zero exit there would make every finished
// session look like a broken server in somebody's agent log.
func TestClosedStdioIsHowASessionEnds(t *testing.T) {
	s := serve(t)
	dir := t.TempDir()
	if err := WriteHandoff(dir, s.URL(), s.Token()); err != nil {
		t.Fatalf("записать адрес: %v", err)
	}

	in, inWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("канал: %v", err)
	}
	outReader, out, err := os.Pipe()
	if err != nil {
		t.Fatalf("канал: %v", err)
	}
	defer outReader.Close()
	defer out.Close()
	// Nothing is ever said: the caller opened the session and closed it, which
	// is what a CLI shutting down does.
	inWriter.Close()

	if err := ServeStdio(context.Background(), dir, in, out); err != nil {
		t.Fatalf("закрытый stdio — это конец сессии, а не ошибка: %v", err)
	}
}
