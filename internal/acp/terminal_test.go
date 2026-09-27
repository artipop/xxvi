package acp

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/stagemcp"
	"github.com/artipop/xxvi/internal/store"
)

// ---- the command line a stage's CLI is started with ----

func TestTerminalArgvPutsTheBriefLast(t *testing.T) {
	cli, ok := cliFor(model.KindClaude)
	if !ok {
		t.Fatal("у claude должен быть интерактивный CLI")
	}
	argv, taken := terminalArgv(cli, "claude", nil, []string{"--mcp-config", "/tmp/mcp.json"}, "почини вход")
	if !taken {
		t.Fatal("бриф должен уехать на командную строку")
	}
	if argv[0] != "claude" {
		t.Fatalf("терминал запускает сам CLI, а не адаптер: %v", argv)
	}
	// The separator is the whole reason the brief survives: --mcp-config is
	// variadic and would otherwise swallow the task as a second config file.
	if argv[len(argv)-2] != "--" || argv[len(argv)-1] != "почини вход" {
		t.Fatalf("бриф идёт последним и за разделителем: %v", argv)
	}
	if !slices.Contains(argv, "/tmp/mcp.json") {
		t.Fatalf("конфигурация инструментов должна быть передана: %v", argv)
	}
}

// A second visit to a stage continues the conversation the folder already
// holds. The brief cannot go on that command line — no vendor documents the
// combination — so it is typed in afterwards, and the caller has to be told.
func TestTerminalArgvResumesWithoutTheBrief(t *testing.T) {
	cli, _ := cliFor(model.KindClaude)
	argv, taken := terminalArgv(cli, "claude", cli.cliResumeArgs, []string{"--mcp-config", "/tmp/mcp.json"}, "продолжай")
	if taken {
		t.Fatal("в продолженный разговор бриф на командной строке не уходит")
	}
	if !slices.Contains(argv, "--continue") {
		t.Fatalf("продолжение разговора — флаг вендора: %v", argv)
	}
	if slices.Contains(argv, "продолжай") {
		t.Fatalf("брифа на командной строке быть не должно: %v", argv)
	}
}

// An agent whose kind has no interactive CLI cannot be worked in a terminal at
// all, and the stage says so instead of opening a window onto a process that
// only speaks JSON-RPC.
func TestOnlyKindsWithACLICanBeWorkedInATerminal(t *testing.T) {
	if _, ok := cliFor(model.KindACP); ok {
		t.Fatal("у произвольной ACP-команды нет своего терминала")
	}
	if _, ok := cliFor(model.KindCodex); !ok {
		t.Fatal("у codex есть CLI и способ передать ему инструменты")
	}
}

// codex gets the server as overrides of its own config, and the grant rides in
// the environment: the argv is what ps shows everybody on the machine.
func TestCodexGetsTheToolsWithoutShowingTheGrant(t *testing.T) {
	cli, _ := cliFor(model.KindCodex)
	handoff, err := cli.cliTools("http://127.0.0.1:1/mcp", "секрет")
	if err != nil {
		t.Fatalf("передать инструменты: %v", err)
	}
	if handoff.file != "" {
		t.Fatalf("codex обходится без файла: %q", handoff.file)
	}
	line := strings.Join(handoff.args, " ")
	if strings.Contains(line, "секрет") {
		t.Fatalf("грант не должен попадать в argv: %v", handoff.args)
	}
	if !strings.Contains(line, `mcp_servers.xxvi_step.url="http://127.0.0.1:1/mcp"`) {
		t.Fatalf("адрес сервера — переопределение конфигурации: %v", handoff.args)
	}
	// «xxvi» may already be the person's own entry with a command in it, and an
	// url merged into that is a config codex refuses to load.
	if strings.Contains(line, "mcp_servers.xxvi.") {
		t.Fatalf("имя сервера шага не должно совпадать с постоянным: %v", handoff.args)
	}
	if !slices.Contains(handoff.env, codexTokenEnv+"=секрет") {
		t.Fatalf("грант передаётся переменной: %v", handoff.env)
	}
	if _, err := cli.cliTools("", "секрет"); err == nil {
		t.Fatal("без адреса отчитываться некуда")
	}
}

// The outer session's markers must not be inherited: CLAUDE_CODE_CHILD_SESSION
// turns transcript saving off, and a CLI that inherits it leaves nothing for the
// next --continue to continue.
func TestTerminalEnvDropsTheOuterSession(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("ANTHROPIC_API_KEY", "оставить")

	cli, _ := cliFor(model.KindClaude)
	env := terminalEnv(&session{agent: model.Agent{Kind: model.KindClaude, Model: "opus"}}, cli)
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDECODE=") || strings.HasPrefix(kv, "CLAUDE_CODE_CHILD_SESSION=") {
			t.Fatalf("маркер внешней сессии не должен наследоваться: %s", kv)
		}
	}
	if !slices.Contains(env, "ANTHROPIC_API_KEY=оставить") {
		t.Fatal("настройки человека наследуются")
	}
	if !slices.Contains(env, "ANTHROPIC_MODEL=opus") {
		t.Fatal("модель агента передаётся переменной")
	}
}

// ---- what the report has to bring ----

func TestRequiredValueIsRefusedRatherThanMissed(t *testing.T) {
	writes := []model.PropertyWrite{{Property: "Вердикт", Required: true}, {Property: "Превью"}}

	missing := missingWrites(writes, stagemcp.Report{OK: true, Props: map[string]string{"Превью": "http://x"}})
	if !strings.Contains(missing, "Вердикт") {
		t.Fatalf("недостающее свойство должно называться: %q", missing)
	}
	if got := missingWrites(writes, stagemcp.Report{OK: true, Props: map[string]string{"Вердикт": "pass"}}); got != "" {
		t.Fatalf("необязательное свойство ничего не требует: %q", got)
	}
	// A failed step is telling us why the values do not exist. Demanding them
	// would be demanding the result of work that did not happen.
	if got := missingWrites(writes, stagemcp.Report{OK: false}); got != "" {
		t.Fatalf("упавший шаг ничего не должен: %q", got)
	}
}

// The report reaches the engine in the shape a session's closing words would
// have had, so one place reads both (engine.ParseWrites).
func TestReportReadsAsClosingWords(t *testing.T) {
	text := reportText(
		[]model.PropertyWrite{{Property: "Вердикт"}, {Property: "Превью"}},
		stagemcp.Report{
			OK:      true,
			Summary: "Починил вход.",
			Props:   map[string]string{"Вердикт": "pass", "Превью": "http://localhost:3000", "Чужое": "нет"},
		},
	)
	if !strings.HasPrefix(text, "Починил вход.") {
		t.Fatalf("итог идёт первым: %q", text)
	}
	if !strings.Contains(text, "\nВердикт: pass") || !strings.Contains(text, "\nПревью: http://localhost:3000") {
		t.Fatalf("объявленные значения идут строками: %q", text)
	}
	// Nothing an agent invents lands on the card: only what the stage declared.
	if strings.Contains(text, "Чужое") {
		t.Fatalf("необъявленное свойство не должно проходить: %q", text)
	}
}

// The file carries a grant, and a grant is a door.
func TestMCPConfigIsWrittenOnlyForTheProcess(t *testing.T) {
	path, err := writeMCPConfig("http://127.0.0.1:1/mcp", "секрет")
	if err != nil {
		t.Fatalf("записать конфигурацию: %v", err)
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("файл конфигурации: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("файл с грантом читает только этот пользователь: %v", perm)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "Bearer секрет") {
		t.Fatalf("грант должен быть в конфигурации: %s", body)
	}
	if _, err := writeMCPConfig("", "секрет"); err == nil {
		t.Fatal("без адреса отчитываться некуда — это отказ, а не пустая конфигурация")
	}
}

// A terminal closed without a report is two different facts, told apart by when:
// a CLI that died at once did not start, and the step fails; one closed later
// is a person ending the conversation, and the card must not move for it.
func TestClosedTerminalIsAFailureOnlyAtTheStart(t *testing.T) {
	if closedByPerson(errClosedWithoutReport, 5*time.Second) {
		t.Fatal("CLI, умерший сразу, не запустился — это провал шага")
	}
	if !closedByPerson(errClosedWithoutReport, 10*time.Minute) {
		t.Fatal("терминал, закрытый после разговора, — вмешательство человека, а не исход")
	}
	if closedByPerson(errors.New("что-то другое"), 10*time.Minute) {
		t.Fatal("другая ошибка — не закрытый терминал")
	}
}

// ---- a card started from a conversation somebody already had ----

// The conversation is opened by its id, and the brief is typed in afterwards,
// as it is for any conversation that already has a transcript.
func TestTerminalArgvResumesAConversationByID(t *testing.T) {
	for _, kind := range []string{model.KindClaude, model.KindCodex} {
		cli, _ := cliFor(kind)
		argv, taken := terminalArgv(cli, kind, cli.cliResumeID("abc-123"), nil, "дальше")
		if taken {
			t.Fatalf("%s: в продолженный разговор бриф на командной строке не уходит", kind)
		}
		if !slices.Contains(argv, "abc-123") {
			t.Fatalf("%s: разговор открывается по id: %v", kind, argv)
		}
	}
}

// The conversation belongs to the first stage the card's agent worked, and to
// every return to it. Another stage, or another agent, starts its own.
func TestCardSessionGoesOnInTheStageThatFirstTookIt(t *testing.T) {
	m, st := newWorkspaceManager(t)
	card, err := st.CreateCard(model.Card{Title: "Продолжить", State: model.StateFlow, Assignee: "Claude", Session: "abc-123"})
	if err != nil {
		t.Fatal(err)
	}
	claude := model.Agent{Name: "Claude", Kind: model.KindClaude}
	run := func(id, stage string, agent model.Agent) *session {
		s := &session{id: id, card: card, stage: model.Stage{ID: stage}, agent: agent, work: model.WorkTerminal}
		if err := st.InsertSession(store.Session{
			ID: id, CardID: card.ID, StageID: stage, AgentName: agent.Name, AgentKind: agent.Kind,
			Work: model.WorkTerminal, Status: store.StatusDone, StartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // started_at orders the runs
		return s
	}

	if !m.continuesCardSession(run("1", "plan", claude)) {
		t.Fatal("первая терминальная стадия агента продолжает разговор карточки")
	}
	if m.continuesCardSession(run("2", "code", claude)) {
		t.Fatal("следующая стадия начинает свой разговор")
	}
	if !m.continuesCardSession(run("3", "plan", claude)) {
		t.Fatal("возврат на ту же стадию продолжает тот же разговор")
	}
	if m.continuesCardSession(run("4", "plan", model.Agent{Name: "Codex", Kind: model.KindCodex})) {
		t.Fatal("чужой разговор другой агент не открывает")
	}
}
