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
	argv, taken := terminalArgv(cli, "claude", opening{}, []string{"--mcp-config", "/tmp/mcp.json"}, "почини вход")
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

// «The last conversation in this folder» is only a fallback for runs that left
// no id, and the brief cannot go on its command line — no vendor documents the
// combination — so it is typed in afterwards, and the caller has to be told.
func TestTerminalArgvResumesByFolderWithoutTheBrief(t *testing.T) {
	cli, _ := cliFor(model.KindClaude)
	open := opening{args: cli.cliResumeArgs, byFolder: true}
	argv, taken := terminalArgv(cli, "claude", open, []string{"--mcp-config", "/tmp/mcp.json"}, "продолжай")
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
	if len(handoff.files) != 0 {
		t.Fatalf("codex обходится без файла: %q", handoff.files)
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

// The conversation is opened by its id, and the brief goes on the same command
// line: both CLIs take a first message after the id, and typing it in means
// answering whatever the CLI is showing at that moment.
func TestTerminalArgvResumesAConversationByID(t *testing.T) {
	for _, kind := range []string{model.KindClaude, model.KindCodex} {
		cli, _ := cliFor(kind)
		argv, taken := terminalArgv(cli, kind, opening{args: cli.cliResumeID("abc-123"), id: "abc-123"}, nil, "дальше")
		if !taken || argv[len(argv)-1] != "дальше" {
			t.Fatalf("%s: бриф уходит на командную строку и в продолженный по id разговор: %v", kind, argv)
		}
		if !slices.Contains(argv, "abc-123") {
			t.Fatalf("%s: разговор открывается по id: %v", kind, argv)
		}
	}
}

// codex drops every -c given before its `resume` once one is given after it,
// and the MCP server and the hooks are both -c: all of them go first.
func TestCodexFlagsGoBeforeResume(t *testing.T) {
	cli, _ := cliFor(model.KindCodex)
	argv, _ := terminalArgv(cli, "codex", opening{args: cli.cliResumeID("abc-123"), id: "abc-123"},
		[]string{"-c", "mcp_servers.xxvi_step.url=\"x\""}, "дальше")
	resume := slices.Index(argv, "resume")
	for i, arg := range argv {
		if arg == "-c" && i > resume {
			t.Fatalf("-c после resume отменяет все прежние: %v", argv)
		}
	}
}

// ---- which conversation a run opens ----

// A stage's conversation is resumed by the id its last run ended on — never by
// «the last one in the folder», which another stage or a person may own.
func TestReturnToAStageResumesItsOwnConversation(t *testing.T) {
	m, st := newWorkspaceManager(t)
	card, err := st.CreateCard(model.Card{Title: "Вернуться", State: model.StateFlow})
	if err != nil {
		t.Fatal(err)
	}
	claude := model.Agent{Name: "Claude", Kind: model.KindClaude}
	cli, _ := cliFor(model.KindClaude)
	past := func(id, stage, kind, conversation string) {
		if err := st.InsertSession(store.Session{
			ID: id, CardID: card.ID, StageID: stage, AgentName: "a", AgentKind: kind,
			Work: model.WorkTerminal, Status: store.StatusDone, StartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		if conversation != "" {
			if err := st.UpdateSession(id, store.SessionUpdate{ACPSessionID: &conversation}); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	here := &session{id: "now", card: card, stage: model.Stage{ID: "plan"}, agent: claude, work: model.WorkTerminal}

	open := m.opening(here, cli)
	if !open.chosen || !slices.Contains(open.args, "--session-id") || !slices.Contains(open.args, open.id) {
		t.Fatalf("первый визит начинает разговор под нашим id: %+v", open)
	}

	past("1", "plan", model.KindClaude, "")
	if open := m.opening(here, cli); !open.byFolder {
		t.Fatalf("прогон без id оставляет только папку: %+v", open)
	}

	past("2", "plan", model.KindClaude, "first")
	past("3", "code", model.KindClaude, "other-stage")
	past("4", "plan", model.KindCodex, "codex-thread")
	open = m.opening(here, cli)
	if open.id != "first" || !slices.Equal(open.args, []string{"--resume", "first"}) || open.chosen {
		t.Fatalf("возврат открывает разговор своей стадии и своего вендора по id: %+v", open)
	}
}

// ---- what the CLI's hooks say ----

func TestHooksTellWorkFromWaiting(t *testing.T) {
	cases := []struct {
		ev   stagemcp.HookEvent
		want cliState
	}{
		{stagemcp.HookEvent{Event: "UserPromptSubmit"}, cliWorking},
		{stagemcp.HookEvent{Event: "PostToolUse", ToolName: "Bash"}, cliWorking},
		{stagemcp.HookEvent{Event: "PermissionRequest", ToolName: "Write"}, cliAsking},
		{stagemcp.HookEvent{Event: "PreToolUse", ToolName: "AskUserQuestion"}, cliAsking},
		{stagemcp.HookEvent{Event: "Notification", NotificationType: "permission_prompt"}, cliAsking},
		// The idle reminder comes after a Stop and says nothing new.
		{stagemcp.HookEvent{Event: "Notification", NotificationType: "idle_prompt"}, cliUnknown},
		{stagemcp.HookEvent{Event: "Stop"}, cliTurnEnded},
		{stagemcp.HookEvent{Event: "StopFailure"}, cliTurnEnded},
		// A subagent finishing is not the conversation's turn ending.
		{stagemcp.HookEvent{Event: "Stop", AgentID: "sub"}, cliUnknown},
		{stagemcp.HookEvent{Event: "SessionStart", Source: "startup"}, cliUnknown},
	}
	for _, c := range cases {
		if got := stateOf(c.ev); got != c.want {
			t.Errorf("%+v: %v, а надо %v", c.ev, got, c.want)
		}
	}
	if conversationOf(stagemcp.HookEvent{Event: "Stop", SessionID: "sub-session", AgentID: "sub"}) != "" {
		t.Fatal("разговор подагента — не разговор стадии")
	}
}

// The id claude moves to mid-run is the one the next visit opens.
func TestConversationFollowsTheCLI(t *testing.T) {
	m, st := newWorkspaceManager(t)
	card, err := st.CreateCard(model.Card{Title: "Сменить", State: model.StateFlow})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertSession(store.Session{
		ID: "run", CardID: card.ID, StageID: "plan", AgentName: "a", AgentKind: model.KindClaude,
		Work: model.WorkTerminal, Status: store.StatusRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	s := &session{id: "run", card: card}
	m.noteConversation(s, "first")
	m.noteConversation(s, "after-clear")
	runs, _ := st.SessionsForCard(card.ID)
	if runs[0].ACPSessionID != "after-clear" {
		t.Fatalf("записан последний разговор, а не первый: %q", runs[0].ACPSessionID)
	}
}

// codex runs a hook only when its trust hash matches, and skips it silently
// otherwise, so the value is pinned: this is the hash codex 0.157–0.158
// computed for this handler.
func TestCodexHooksCarryTheirTrust(t *testing.T) {
	hash, err := codexTrustHash("session_start", `"$XXVI_HOOK_EXE" hook`, 10)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "sha256:b5b5a2233273a62a5b029f0b95db4fee650e9f1444dbbe18721d76d0046488e6" {
		t.Fatalf("хеш доверия разошёлся с тем, что проверено на живом codex: %s", hash)
	}
	handoff, err := codexHooks()
	if err != nil {
		t.Fatal(err)
	}
	if len(handoff.args) != 2 || handoff.args[0] != "-c" || !strings.HasPrefix(handoff.args[1], "hooks={") {
		t.Fatalf("хуки — одно переопределение конфигурации: %v", handoff.args)
	}
	for _, e := range codexHookEvents {
		if !strings.Contains(handoff.args[1], `"/<session-flags>/config.toml:`+e.key+`:0:0"={trusted_hash="sha256:`) {
			t.Fatalf("у %s нет доверия: %s", e.event, handoff.args[1])
		}
	}
}

// claude's hooks come in a file of their own, and the command names this
// executable through the environment rather than spelling a path.
func TestClaudeHooksAreAFileOfTheirOwn(t *testing.T) {
	handoff, err := claudeHooks()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(handoff.files[0])
	if !slices.Equal(handoff.args, []string{"--settings", handoff.files[0]}) {
		t.Fatalf("хуки передаются через --settings: %v", handoff.args)
	}
	body, _ := os.ReadFile(handoff.files[0])
	for _, e := range claudeHookEvents {
		if !strings.Contains(string(body), `"`+e.event+`"`) {
			t.Fatalf("нет хука %s: %s", e.event, body)
		}
	}
	if !strings.Contains(string(body), `\"$XXVI_HOOK_EXE\" hook`) {
		t.Fatalf("команда хука — этот же исполняемый файл: %s", body)
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

// A conversation that is gone is a failure of its own, and only at the start: a
// CLI closed later on a resumed conversation is somebody ending it.
func TestAConversationThatIsGoneFailsTheStepByName(t *testing.T) {
	if !resumeFailed(true, errClosedWithoutReport, 2*time.Second) {
		t.Fatal("CLI, закрывшийся сразу на продолжении, — это пропавший разговор")
	}
	if resumeFailed(false, errClosedWithoutReport, 2*time.Second) {
		t.Fatal("новый разговор не может пропасть")
	}
	if resumeFailed(true, errClosedWithoutReport, terminalStartWindow+time.Second) {
		t.Fatal("закрытие после старта — это человек, а не пропавший разговор")
	}
	if resumeFailed(true, errors.New("другая ошибка"), time.Second) {
		t.Fatal("только закрытие без отчёта говорит о пропавшем разговоре")
	}
}
