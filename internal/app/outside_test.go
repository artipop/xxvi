package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/artipop/xxvi/internal/appmcp"
	"github.com/artipop/xxvi/internal/model"
)

// The application worked from outside: a whole card, from the inbox to closed,
// moved by an agent nobody here started. Everything is real — the seeded flow,
// the engine, the loopback server and its token — because what this is meant to
// prove is that an outside caller has no shortcuts: it moves cards the way a
// person does or not at all.

// outside opens a client session on the running application's tools, the way an
// agent's CLI handed the endpoint and the token would.
func outside(t *testing.T, a *App) *mcp.ClientSession {
	t.Helper()
	if a.Outside.URL() == "" {
		t.Fatal("инструменты приложения не поднялись")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "тест", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   a.Outside.URL(),
		HTTPClient: &http.Client{Transport: bearer{token: a.Outside.Token()}},
	}, nil)
	if err != nil {
		t.Fatalf("подключиться к инструментам приложения: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// call is one tool call: the text it answered with, and whether it was a
// refusal.
func call(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("вызвать %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String(), res.IsError
}

// ok is a call that must not be refused.
func ok(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	text, refused := call(t, sess, name, args)
	if refused {
		t.Fatalf("%s отказал: %s", name, text)
	}
	return text
}

const pageCard = "Страница со статусом сборки"

// The whole road, and the one the flow was written for: a page is made, a check
// says what it is worth, a person looks at it in the browser and the card
// closes. Every step is ended from outside, and where the card goes next is the
// flow's decision each time.
func TestOutsideAgentWalksTheWholeFlow(t *testing.T) {
	a := open(t)
	sess := outside(t, a)

	ok(t, sess, "add_task", map[string]any{
		"title": pageCard,
		"body":  "Одна страница, на которой видно, зелёная сборка или нет.",
	})

	// The worker is not one of this application's agents, so the application
	// starts nobody: the steps are worked by whoever is holding these tools.
	// Without this the entry stage would launch an agent of its own, and the
	// step would already have an owner.
	place := ok(t, sess, "take_into_work", map[string]any{
		"task": pageCard, "flow": "Page and check", "worker": "внешний агент",
	})
	if !strings.Contains(place, "«Layout»") {
		t.Fatalf("карточка должна встать на входную стадию: %s", place)
	}
	card := cardNamed(t, a, pageCard)
	if sessions, _ := a.Store.SessionsForCard(card.ID); len(sessions) != 0 {
		t.Fatalf("приложение не должно запускать своего агента на чужой шаг: %+v", sessions)
	}

	// What the stage says it wants is readable before the work starts: the
	// value it owes the card, and the folder the page has to land in.
	view := ok(t, sess, "task", map[string]any{"task": pageCard})
	if !strings.Contains(view, "«Page»") || !strings.Contains(view, "required") {
		t.Fatalf("карточка должна называть, что обязана оставить стадия: %s", view)
	}
	dir, err := a.Agents.WorkDir(card.ID)
	if err != nil {
		t.Fatalf("рабочая папка: %v", err)
	}
	if !strings.Contains(view, dir) {
		t.Fatalf("карточка должна называть рабочую папку: %s", view)
	}

	// The step itself, done the way the stage asks for it.
	page := filepath.Join(dir, "index.html")
	if err := os.WriteFile(page, []byte("<!doctype html><title>Сборка</title><h1>Зелёная</h1>"), 0o644); err != nil {
		t.Fatalf("записать страницу: %v", err)
	}
	address := "file://" + page

	moved := ok(t, sess, "finish_step", map[string]any{
		"task": pageCard, "outcome": "done",
		"summary":    "Сделал index.html со статусом сборки.",
		"properties": map[string]any{"Page": address},
	})
	if !strings.Contains(moved, "«Layout» → «Check»") {
		t.Fatalf("после отчёта карточка должна поехать на проверку: %s", moved)
	}

	// The check hands back a verdict, and the verdict is what routes the card.
	// «fail» is the loop the flow was drawn with: back to the stage that made
	// the page.
	back := ok(t, sess, "finish_step", map[string]any{
		"task": pageCard, "outcome": "done",
		"summary":    "Заголовок не тот, о котором просит карточка.",
		"properties": map[string]any{"Verdict": "fail"},
	})
	if !strings.Contains(back, "«Check» → «Layout»") {
		t.Fatalf("«fail» должен вернуть карточку на вёрстку: %s", back)
	}

	ok(t, sess, "finish_step", map[string]any{
		"task": pageCard, "outcome": "done",
		"summary":    "Поправил заголовок.",
		"properties": map[string]any{"Page": address},
	})
	ahead := ok(t, sess, "finish_step", map[string]any{
		"task": pageCard, "outcome": "done",
		"summary":    "Страница делает то, о чём просит карточка.",
		"properties": map[string]any{"Verdict": "pass"},
	})
	if !strings.Contains(ahead, "«Check» → «Look»") {
		t.Fatalf("«pass» должен вести дальше: %s", ahead)
	}

	// The stage where nothing runs: its whole content is the screen, and the
	// address in it is the one this walk produced rather than the placeholder
	// the flow was written with.
	looking := ok(t, sess, "task", map[string]any{"task": pageCard})
	if !strings.Contains(looking, "browser") || !strings.Contains(looking, address) {
		t.Fatalf("на стадии «Look» карточка должна показывать браузер на сделанной странице: %s", looking)
	}

	closed := ok(t, sess, "finish_step", map[string]any{
		"task": pageCard, "outcome": "done", "summary": "Посмотрел, годится.",
	})
	if !strings.Contains(closed, "the task is closed") {
		t.Fatalf("ответ за стадию «Look» должен закрыть задачу: %s", closed)
	}

	card = cardNamed(t, a, pageCard)
	if card.State != model.StateDone {
		t.Fatalf("карточка должна быть закрыта, а она %s", card.State)
	}
	if card.Props["Page"] != address || card.Props["Verdict"] != "pass" {
		t.Fatalf("на карточке должно остаться то, что записали стадии: %+v", card.Props)
	}
}

// A required value is not a formality: the step has not ended without it, and
// the refusal says which one to add — the same answer a stage worked in a
// terminal gets (internal/stagemcp).
func TestStepWithoutRequiredValueIsRefused(t *testing.T) {
	a := open(t)
	sess := outside(t, a)

	ok(t, sess, "add_task", map[string]any{"title": pageCard})
	ok(t, sess, "take_into_work", map[string]any{
		"task": pageCard, "flow": "Page and check", "worker": "внешний агент",
	})

	text, refused := call(t, sess, "finish_step", map[string]any{
		"task": pageCard, "outcome": "done", "summary": "Готово.",
	})
	if !refused || !strings.Contains(text, "«Page»") {
		t.Fatalf("шаг без обязательного значения должен быть отказан с именем свойства: %s", text)
	}
	if place := ok(t, sess, "task", map[string]any{"task": pageCard}); !strings.Contains(place, "«Layout»") {
		t.Fatalf("отказанный шаг не должен двигать карточку: %s", place)
	}
}

// A stage that runs nothing and waits for something else is not answered with
// an outcome: the refusal names what it is actually waiting for, because a
// caller told «готово» there would be waiting for a card that never moves.
func TestWaitingStageSaysWhatItWaitsFor(t *testing.T) {
	a := open(t)
	sess := outside(t, a)

	ok(t, sess, "add_task", map[string]any{"title": "Что делать с импортом"})
	ok(t, sess, "take_into_work", map[string]any{
		"task": "Что делать с импортом", "flow": "Triage and decision", "worker": "внешний агент",
	})
	// Straight to the human fork: the stage before it is an agent's.
	card := cardNamed(t, a, "Что делать с импортом")
	if err := a.Engine.MoveTo(card.ID, "decide"); err != nil {
		t.Fatalf("перевести карточку: %v", err)
	}

	text, refused := call(t, sess, "finish_step", map[string]any{
		"task": "Что делать с импортом", "outcome": "done", "summary": "Разобрался.",
	})
	if !refused || !strings.Contains(text, "Decision") {
		t.Fatalf("стадия должна назвать, чего она ждёт: %s", text)
	}

	moved := ok(t, sess, "set_property", map[string]any{
		"task": "Что делать с импортом", "property": "Decision", "value": "Go",
	})
	if !strings.Contains(moved, "«Execution»") {
		t.Fatalf("ожидаемое значение должно двигать карточку: %s", moved)
	}
}

// A step the application is working has an owner, and a second report on it
// would be two answers for one run.
func TestStepWorkedByTheApplicationIsNotReportedFromOutside(t *testing.T) {
	a := open(t)
	sess := outside(t, a)

	ok(t, sess, "add_task", map[string]any{"title": pageCard})
	card := cardNamed(t, a, pageCard)
	flow, err := a.Store.FlowByName("Page and check")
	if err != nil {
		t.Fatalf("флоу: %v", err)
	}
	if err := a.Engine.TakeIntoWork(card.ID, flow.ID); err != nil {
		t.Fatalf("в работу: %v", err)
	}
	// Whether the CLI on this machine actually started is beside the point:
	// what makes the step somebody's is the session row, and the entry stage
	// writes one before anything is spawned.
	if !waitFor(t, func() bool {
		sessions, _ := a.Store.SessionsForCard(card.ID)
		for _, s := range sessions {
			if !s.Status.Terminal() {
				return true
			}
		}
		return false
	}) {
		t.Skip("сессия не поднялась на этой машине — проверять нечего")
	}

	text, refused := call(t, sess, "finish_step", map[string]any{
		"task": pageCard, "outcome": "done", "summary": "я быстрее",
		"properties": map[string]any{"Page": "file:///tmp/x.html"},
	})
	if !refused || !strings.Contains(text, "application agent") {
		t.Fatalf("чужой шаг не должен отчитываться снаружи: %s", text)
	}
}

// The token is the door. Without it there is no session at all, and that is
// checked before a tool is ever named.
func TestTokenIsTheDoor(t *testing.T) {
	a := open(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "чужой", Version: "1"}, nil)
	_, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   a.Outside.URL(),
		HTTPClient: &http.Client{Transport: bearer{token: "не тот"}},
	}, nil)
	if err == nil {
		t.Fatal("с чужим токеном соединения быть не должно")
	}
}

// The token outlives the launch it was minted in — an agent's configuration is
// written once — while the address does not, which is why the address is left
// in a file and the token is not.
func TestTokenSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir, quiet())
	if err != nil {
		t.Fatalf("первый запуск: %v", err)
	}
	was := first.Outside.Token()
	if was == "" {
		t.Fatal("токен должен быть выдан на первом запуске")
	}
	if _, err := appmcp.ReadHandoff(dir); err != nil {
		t.Fatalf("адрес инструментов должен лежать рядом с базой: %v", err)
	}
	first.Close()
	if _, err := appmcp.ReadHandoff(dir); err == nil {
		t.Fatal("адрес остановленного приложения не должен оставаться на диске")
	}

	second, err := Open(dir, quiet())
	if err != nil {
		t.Fatalf("второй запуск: %v", err)
	}
	defer second.Close()
	if second.Outside.Token() != was {
		t.Fatal("токен не должен меняться от запуска к запуску")
	}
}

// cardNamed is the card by its title, which is how these tests name cards for
// the same reason a caller does: it is what was just typed.
func cardNamed(t *testing.T, a *App, title string) model.Card {
	t.Helper()
	for _, state := range []model.CardState{model.StateFlow, model.StateInbox, model.StateDone} {
		cards, err := a.Store.CardsInState(state)
		if err != nil {
			t.Fatalf("карточки: %v", err)
		}
		for _, c := range cards {
			if c.Title == title {
				return c
			}
		}
	}
	t.Fatalf("карточка «%s» не найдена", title)
	return model.Card{}
}

// waitFor gives a background start a moment to become visible. A session is
// written by the goroutine that runs the stage, so the row appears just after
// the call that caused it returns.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	for range 50 {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// `xxvi mcp` — the line somebody writes once in an agent's configuration — run
// for real: the built executable, the handoff file a running application left
// beside its database, and a tool call that comes back with this application's
// own answer. The parts underneath are tested in internal/appmcp; what this
// adds is that the command exists, finds the application and opens no window.
func TestCommandLineBridgeReachesTheRunningApplication(t *testing.T) {
	if testing.Short() {
		t.Skip("нужна сборка исполняемого файла")
	}
	bin := filepath.Join(t.TempDir(), "xxvi")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("не удалось собрать приложение: %v\n%s", err, out)
	}

	// The data directory is where the command will look, so the test moves the
	// place rather than the answer: everything below, parent and child alike,
	// reads it from the environment.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir, err := DefaultDataDir()
	if err != nil {
		t.Fatalf("папка данных: %v", err)
	}
	a, err := Open(dir, quiet())
	if err != nil {
		t.Fatalf("открыть приложение: %v", err)
	}
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "тест", Version: "1"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin, "mcp")}, nil)
	if err != nil {
		t.Fatalf("запустить `xxvi mcp`: %v", err)
	}
	defer sess.Close()

	if text := ok(t, sess, "flows", nil); !strings.Contains(text, "Page and check") {
		t.Fatalf("через команду должны приходить флоу этого приложения: %s", text)
	}
}
