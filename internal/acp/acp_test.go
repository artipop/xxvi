package acp

import (
	"errors"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/artipop/xxvi/internal/model"
)

// ---- what an agent may do without asking ----

func TestPolicyAllowsBareNames(t *testing.T) {
	p := ToolPolicy{"Read"}
	if !p.Allows("Read", nil) {
		t.Fatal("голое имя разрешает любой вызов инструмента")
	}
	if p.Allows("Write", nil) {
		t.Fatal("чужой инструмент разрешаться не должен")
	}
}

// Bash is the entry a policy has to be able to narrow: allowing it wholesale is
// the difference between an agent that can look around and one that can do
// anything.
func TestPolicyNarrowsBashByCommand(t *testing.T) {
	p := ToolPolicy{"Bash(git log*)"}
	if !p.Allows("Bash", map[string]any{"command": "git log --oneline"}) {
		t.Fatal("команда по шаблону должна разрешаться")
	}
	if p.Allows("Bash", map[string]any{"command": "rm -rf /"}) {
		t.Fatal("команда вне шаблона разрешаться не должна")
	}
	// A pattern is narrower than "any call", so a call we cannot read the
	// command of is not approved by it.
	if p.Allows("Bash", nil) {
		t.Fatal("без аргумента шаблон не может ничего разрешить")
	}
}

// codex runs everything through a shell, so the interesting part of the argv is
// the last element. Reading the shell instead would silently stop every pattern
// from matching.
func TestPolicyReadsAShellArgv(t *testing.T) {
	p := ToolPolicy{"Bash(git *)"}
	input := map[string]any{"command": []any{"/bin/zsh", "-lc", "git status --short"}}
	if !p.Allows("Bash", input) {
		t.Fatal("команда внутри argv должна находиться")
	}
}

func TestPolicyIgnoresCaseOfTheToolName(t *testing.T) {
	if !(ToolPolicy{"bash(git *)"}).Allows("Bash", map[string]any{"command": "git diff"}) {
		t.Fatal("имя инструмента сопоставляется без учёта регистра")
	}
}

// Commands are not paths: a path-aware matcher would treat "/" as a boundary
// and refuse `git log -- src/foo`.
func TestPatternGlobIsNotPathAware(t *testing.T) {
	if !matchPattern("git log*", "git log -- src/foo/bar.go") {
		t.Fatal("«*» покрывает и слэши — команда не путь")
	}
	if matchPattern("git log*", "git status") {
		t.Fatal("литеральная часть должна совпадать")
	}
}

func TestDefaultPolicyLooksButDoesNotTouch(t *testing.T) {
	if !DefaultPolicy.Allows("Read", nil) {
		t.Fatal("смотреть можно по умолчанию")
	}
	for _, tool := range []string{"Write", "Edit"} {
		if DefaultPolicy.Allows(tool, nil) {
			t.Fatalf("по умолчанию %s должен спрашивать", tool)
		}
	}
	if DefaultPolicy.Allows("Bash", map[string]any{"command": "rm -rf /"}) {
		t.Fatal("по умолчанию произвольная команда должна спрашивать")
	}
}

// ---- recovering the tool name ----

// ACP has no field for "which tool is this", and the policy is written in tool
// names, so the name has to be recovered from what the call plainly is.
func TestInferToolNameFromTheCallItself(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  string
		input map[string]any
		want  string
	}{
		{"команда — это Bash", "execute", map[string]any{"command": "ls"}, "Bash"},
		{"чтение файла", "read", map[string]any{"path": "/tmp/x"}, "Read"},
		{"создание файла", "edit", map[string]any{"path": "/tmp/x", "content": "новый"}, "Write"},
		{"правка файла", "edit", map[string]any{"path": "/tmp/x", "old_string": "было", "content": "стало"}, "Edit"},
		{"поиск по содержимому", "search", map[string]any{"pattern": "todo"}, "Grep"},
		{"неизвестное", "fetch", map[string]any{"url": "https://example.org"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferToolName(tc.kind, tc.input); got != tc.want {
				t.Fatalf("ожидалось %q, получено %q", tc.want, got)
			}
		})
	}
}

func TestMetaToolNameReadsBothShapes(t *testing.T) {
	if got := metaToolName(map[string]any{"toolName": "Read"}); got != "Read" {
		t.Fatalf("плоский _meta: получено %q", got)
	}
	nested := map[string]any{"claudeCode": map[string]any{"toolName": "Bash"}}
	if got := metaToolName(nested); got != "Bash" {
		t.Fatalf("вложенный _meta: получено %q", got)
	}
	if got := metaToolName(nil); got != "" {
		t.Fatalf("пустой _meta: получено %q", got)
	}
}

// An adapter routing a built-in tool back through us prefixes it. The tool is
// still Read; only the road it takes differs, so the policy must not have to
// know about it.
func TestNormalizeToolNameStripsTheRoutingPrefix(t *testing.T) {
	if got := normalizeToolName("mcp__acp__Read"); got != "Read" {
		t.Fatalf("получено %q", got)
	}
	if got := normalizeToolName("mcp__playwright__click"); got != "mcp__playwright__click" {
		t.Fatalf("имя стороннего сервера трогать нельзя: %q", got)
	}
}

// ---- an agent asking in words ----

func formWithOptions() acpsdk.UnstableCreateElicitationForm {
	return acpsdk.UnstableCreateElicitationForm{
		Message: "Какую базу использовать?",
		RequestedSchema: acpsdk.UnstableElicitationSchema{
			Required: []string{"choice"},
			Properties: map[string]any{
				"choice": map[string]any{
					"oneOf": []any{
						map[string]any{"const": "postgres", "title": "PostgreSQL", "description": "как в проде"},
						map[string]any{"const": "sqlite", "title": "SQLite"},
					},
				},
				"custom": map[string]any{
					"_meta": map[string]any{
						"_askUserQuestionCustomAnswer": map[string]any{"questionId": "choice"},
					},
				},
			},
		},
	}
}

func TestQuestionFromFormReadsOptionsAndTheFreeTextField(t *testing.T) {
	q := questionFromForm(formWithOptions())
	if q.Text != "Какую базу использовать?" {
		t.Fatalf("текст вопроса: %q", q.Text)
	}
	if len(q.Options) != 2 || q.Options[0].ID != "postgres" || q.Options[0].Label != "PostgreSQL" {
		t.Fatalf("варианты не разобраны: %+v", q.Options)
	}
	if q.Options[0].Description != "как в проде" {
		t.Fatalf("пояснение варианта потеряно: %+v", q.Options[0])
	}
	if q.field != "choice" || q.freeField != "custom" {
		t.Fatalf("поля формы не найдены: field=%q freeField=%q", q.field, q.freeField)
	}
	if !q.FreeText {
		t.Fatal("рядом с вариантами предложен свой ответ — поле должно быть открыто")
	}
}

// A form with no options at all is nothing but a request for words.
func TestQuestionFromFormWithoutOptionsAsksForWords(t *testing.T) {
	form := acpsdk.UnstableCreateElicitationForm{
		Message: "Как назвать ветку?",
		RequestedSchema: acpsdk.UnstableElicitationSchema{
			Required:   []string{"name"},
			Properties: map[string]any{"name": map[string]any{"type": "string"}},
		},
	}
	q := questionFromForm(form)
	if !q.FreeText || q.field != "name" || len(q.Options) != 0 {
		t.Fatalf("форма без вариантов — это поле для ответа: %+v", q)
	}
}

// Which question a person is asked must not depend on map iteration order.
func TestSchemaPropertyOrderIsStable(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Required: []string{"второе"},
		Properties: map[string]any{
			"первое": map[string]any{}, "второе": map[string]any{}, "третье": map[string]any{},
		},
	}
	for i := 0; i < 20; i++ {
		names := schemaPropertyNames(schema)
		if names[0] != "второе" {
			t.Fatalf("обязательное свойство идёт первым, получено %v", names)
		}
	}
}

// ---- launching an agent ----

// The generic kind carries its own agent, so nothing of ours is appended: the
// flags the kind would have added have to be spelled out by whoever wrote it.
func TestLaunchForGenericACPUsesTheCommandVerbatim(t *testing.T) {
	l, err := launchFor(model.Agent{
		Name: "Свой", Kind: model.KindACP,
		Command: []string{"my-agent", "--acp"}, Args: []string{"--verbose"},
	})
	if err != nil {
		t.Fatalf("не собралась команда: %v", err)
	}
	want := []string{"my-agent", "--acp", "--verbose"}
	if len(l.argv) != len(want) {
		t.Fatalf("ожидалось %v, получено %v", want, l.argv)
	}
	for i := range want {
		if l.argv[i] != want[i] {
			t.Fatalf("ожидалось %v, получено %v", want, l.argv)
		}
	}
}

// A command wraps the adapter — a proxy launcher, a per-account shim — but it
// replaces the argv, not the model.
func TestLaunchForKeepsTheModelWhenACommandReplacesTheArgv(t *testing.T) {
	l, err := launchFor(model.Agent{
		Name: "Клод в обёртке", Kind: model.KindClaude,
		Command: []string{"proxychains4", "claude-agent-acp"}, Model: "opus",
	})
	if err != nil {
		t.Fatalf("не собралась команда: %v", err)
	}
	if len(l.env) != 1 || l.env[0] != "ANTHROPIC_MODEL=opus" {
		t.Fatalf("модель должна дойти через окружение: %v", l.env)
	}
	// Claude Code refuses to start inside another Claude Code session, and this
	// application may well have been launched from one.
	if len(l.dropEnv) == 0 || l.dropEnv[0] != "CLAUDECODE" {
		t.Fatalf("переменная, мешающая запуску, должна отбрасываться: %v", l.dropEnv)
	}
}

func TestLaunchForRefusesAnAgentItCannotStart(t *testing.T) {
	if _, err := launchFor(model.Agent{Name: "Ничей", Kind: "тамагочи"}); err == nil {
		t.Fatal("неизвестный тип без команды запустить нечем")
	}
}

// The model is found by its category, whatever the agent calls the option:
// that is what lets any ACP agent be told its model without a row saying how.
func TestModelOptionIsFoundByItsCategory(t *testing.T) {
	category := acpsdk.SessionConfigOptionCategoryModel
	sel := func(id string, cat *acpsdk.SessionConfigOptionCategory) acpsdk.SessionConfigOption {
		return acpsdk.SessionConfigOption{Select: &acpsdk.SessionConfigOptionSelect{Id: acpsdk.SessionConfigId(id), Category: cat}}
	}
	got := modelOption([]acpsdk.SessionConfigOption{sel("model", nil), sel("llm", &category)})
	if got == nil || got.Id != "llm" {
		t.Fatalf("опция с категорией model важнее имени: %+v", got)
	}
	got = modelOption([]acpsdk.SessionConfigOption{sel("effort", nil), sel("model", nil)})
	if got == nil || got.Id != "model" {
		t.Fatalf("без категорий модель ищется по имени: %+v", got)
	}
	if modelOption([]acpsdk.SessionConfigOption{sel("effort", nil)}) != nil {
		t.Fatal("чужая опция за модель не принимается")
	}
}

func TestAdapterStatusesCoverEveryLaunchableKind(t *testing.T) {
	got := AdapterStatuses()
	if len(got) != len(adapters) {
		t.Fatalf("ожидалось %d записей, получено %d", len(adapters), len(got))
	}
	for _, st := range got {
		// Whatever the machine has installed, the answer has to be actionable:
		// either it is ready, or it says what is missing.
		if !st.Ready && st.Detail == nil {
			t.Fatalf("недоступный адаптер должен объяснять, чего не хватает: %+v", st)
		}
	}
}

// A policy on the agent overrides the machine's, so a trusted agent can be let
// loose without changing anything for the rest.
func TestAgentPolicyOverridesTheMachineOne(t *testing.T) {
	machine := ToolPolicy{"Read"}
	got := policyFor(model.Agent{AutoAllowTools: []string{"Read", "Write"}}, machine)
	if !got.Allows("Write", nil) {
		t.Fatal("собственный список агента должен применяться")
	}
	if policyFor(model.Agent{}, machine).Allows("Write", nil) {
		t.Fatal("без собственного списка действует машинный")
	}
}

// A CLI that cannot reach its proxy echoes the URL, password included; that
// text goes to a comment and the log, so it is scrubbed on the way out.
func TestClippedHidesTheProxyPassword(t *testing.T) {
	a := model.Agent{Network: &model.Proxy{URL: "http://h:1", Username: "u", Password: "s3cr:et/"}}
	addr, _ := a.Network.Address()
	got := clipped(a, errors.New("connect to "+addr+" refused")).Error()
	if strings.Contains(got, "s3cr") || strings.Contains(got, "et%2F") {
		t.Fatalf("пароль остался в тексте ошибки: %q", got)
	}
}
