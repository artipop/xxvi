package acp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// Everything that differs between one ACP agent and another: the binary to look
// for, the package that provides it when it is missing, the flags that select
// ACP-over-stdio, how a model is asked for, what the process must not inherit,
// and the mode to switch it into once connected.
//
// Everything else — the connection, permissions, questions, cancellation — is
// the same code for every kind, which is the point of the table: adding an
// agent is a row, not a branch.
type adapter struct {
	// bin is the executable, looked up on PATH and in the usual install spots.
	bin string
	// pkg provides bin when it is not installed, published in from. It
	// doubles as the install instruction shown to a person and the argument
	// to the registry's runner, so a machine with that runner needs no
	// install step at all.
	pkg  string
	from registry
	// acpArgs put the CLI into ACP-over-stdio mode. Empty when it has no other
	// mode to be in.
	acpArgs []string
	// modelEnv tells the agent its model at spawn, for a kind that reads one.
	// Every agent is also asked over ACP once the session exists
	// (Manager.selectModel), so a kind without it still gets its model; the
	// variable is for ids the agent takes but does not list as options.
	modelEnv string
	// dropEnv names variables the process must not inherit from ours.
	dropEnv []string
	// mode is the session mode to select after session/new, when the agent's
	// default is not what a card wants.
	mode string
	// noRevive overrides what the agent says at initialize: it claims it can
	// open a past conversation, and does, but not well enough to work in.
	noRevive bool

	// The columns below describe the *interactive* CLI of the same agent — what
	// a stage working in a terminal runs (docs/system.md §4.1.1). They are
	// columns of this table rather than a table of their own because the answer
	// to "how is this agent started" belongs in one place; a kind that leaves
	// them empty simply cannot be worked in a terminal, and says so on the
	// stage rather than failing to open a window.

	// cliBin is the interactive binary. For claude and codex it is *not* bin:
	// bin there is a vendor ACP adapter, a different program with no terminal
	// UI at all.
	cliBin string
	// cliArgs go on every command line of the CLI.
	cliArgs []string
	// cliNewSession starts a conversation under an id we chose, so the run
	// knows what it will resume before the CLI has said a word. Nil for a CLI
	// that picks its own; its hooks say which one it picked.
	cliNewSession func(id string) []string
	// cliResumeID opens one conversation by its id — the one a stage's last
	// run was holding, or the one a card was started from. By id, never by
	// «the last one in this folder»: the folder is shared by every stage of
	// the card, and a person may have opened a CLI in it too.
	cliResumeID func(id string) []string
	// cliResumeArgs continue the last conversation in the folder. Only for a
	// stage whose earlier runs left no id — recorded before ids were, or by a
	// CLI whose hooks never spoke — where it is the only thread left to pull.
	cliResumeArgs []string
	// cliHooks registers our hook command with the CLI (hooks.go): how it
	// says which conversation it holds and whether it is waiting for a person.
	// Nil for a CLI without hooks, which is then watched by its silence.
	cliHooks func() (toolsHandoff, error)
	// cliTools hands the CLI our MCP server. A session gets its servers over
	// the protocol, where session/new has a field for them; a terminal is the
	// vendor CLI itself and has to be told in its own spelling. This is how
	// «step done» reaches the agent, so a kind without it cannot report — and
	// therefore cannot work a stage.
	cliTools func(url, token string) (toolsHandoff, error)
	// cliPromptArgs put the first message on the CLI's own command line, which
	// is how a stage hands over its brief. Typing it into the pty instead means
	// writing to a CLI that is not listening yet — and the thing it might be
	// showing is «do you trust the files in this folder?», which must not be
	// answered with a task.
	//
	// It carries its own end-of-options marker: the brief is a positional
	// argument, and everything before it on that line is flags.
	//
	// A conversation resumed by id takes it the same way — both CLIs accept a
	// first message after `--resume <id>` / `resume <id>`. Only the folder
	// fallback still has it typed in.
	cliPromptArgs func(prompt string) []string
}

// registry is where an adapter package is published: how to run it without
// installing it, and how a person installs it.
type registry struct {
	// runner fetches and runs a package in one go.
	runner string
	// runArgs follow runner, naming the package and, when it provides more
	// than one program, which of them.
	runArgs func(pkg, bin string) []string
	// install is the command a person runs to install it for good.
	install func(pkg string) string
	// needs is what provides runner, for a machine that has neither.
	needs string
}

var (
	npm = registry{
		runner:  "npx",
		runArgs: func(pkg, _ string) []string { return []string{"--yes", pkg} },
		install: func(pkg string) string { return "npm install -g " + pkg },
		needs:   "Node.js",
	}
	pypi = registry{
		runner:  "uvx",
		runArgs: func(pkg, bin string) []string { return []string{"--from", pkg, bin} },
		install: func(pkg string) string { return "uv tool install " + pkg },
		needs:   "uv",
	}
)

// adapters is the table of agents we know how to launch. The generic acp kind
// is deliberately absent: it carries its own Command.
var adapters = map[string]adapter{
	// The Claude adapter embeds the Claude Agent SDK, which embeds the CLI, so
	// the claude binary is not needed alongside it. It is a Node package and
	// there is no other build of it, which is why this kind needs Node.js.
	model.KindClaude: {
		bin:  "claude-agent-acp",
		pkg:  "@agentclientprotocol/claude-agent-acp",
		from: npm,
		// The adapter takes no flags at all: it is an ACP agent and nothing else.
		modelEnv: "ANTHROPIC_MODEL",
		// Claude Code refuses to start inside another Claude Code session, and
		// this app may well have been launched from one. The rest of the list is
		// that same launch seen from the other side, and
		// CLAUDE_CODE_CHILD_SESSION is the one that matters most: it turns
		// transcript saving off, so a CLI that inherits it leaves no
		// conversation behind, and the next `--continue` in that folder exits
		// saying there is nothing to continue.
		//
		// Only the markers of the outer session are dropped, never the whole
		// CLAUDE_CODE_* family: CLAUDE_CODE_USE_BEDROCK and its like are the
		// person's own configuration and have to be inherited.
		dropEnv: []string{
			"CLAUDECODE",
			"CLAUDE_CODE_CHILD_SESSION",
			"CLAUDE_CODE_SESSION_ID",
			"CLAUDE_CODE_ENTRYPOINT",
			"CLAUDE_CODE_EXECPATH",
			"CLAUDE_CODE_MESSAGING_SOCKET",
			"CLAUDE_CODE_MESSAGING_TOKEN",
		},
		// The adapter embeds the CLI but is not it: a terminal runs `claude`,
		// which has to be installed for that and only that.
		cliBin:        "claude",
		cliNewSession: func(id string) []string { return []string{"--session-id", id} },
		cliResumeID:   func(id string) []string { return []string{"--resume", id} },
		cliResumeArgs: []string{"--continue"},
		cliHooks:      claudeHooks,
		cliTools:      claudeTools,
		// `claude -- «…»` opens the TUI with that as the first message, which is
		// exactly what a stage needs: interactive from the first frame, with the
		// brief already in it. The `--` is not decoration — `--mcp-config` is
		// variadic and would otherwise read the brief as a second config file.
		cliPromptArgs: func(prompt string) []string { return []string{"--", prompt} },
	},
	// The Codex adapter drives the codex CLI it depends on, so this kind needs
	// Node.js too.
	model.KindCodex: {
		bin:  "codex-acp",
		pkg:  "@agentclientprotocol/codex-acp",
		from: npm,
		// It takes no flags either: the model is a session config option, asked
		// for over the protocol once the session exists, as for any agent.
		//
		// It starts read-only, which is not what a card asked for: a session
		// that may not edit anything would spend its turn saying so.
		mode:   "agent",
		cliBin: "codex",
		// Its offer to update itself is a menu drawn over the conversation,
		// and Enter — the key that sends a message — picks «Update now».
		cliArgs:       []string{"-c", "check_for_update_on_startup=false"},
		cliResumeID:   func(id string) []string { return []string{"resume", id} },
		cliResumeArgs: []string{"resume", "--last"},
		cliHooks:      codexHooks,
		cliTools:      codexTools,
		// The separator for the same reason as claude's: a brief that starts
		// with a dash must not be read as a flag.
		cliPromptArgs: func(prompt string) []string { return []string{"--", prompt} },
	},
	// Mistral Vibe speaks ACP through a program of its own, published on PyPI
	// beside the CLI. It has no terminal columns: a stage worked by it runs in
	// the background, where its conversation is revived over the protocol
	// (session/load) rather than by a flag of its CLI.
	model.KindVibe: {
		bin:  "vibe-acp",
		pkg:  "mistral-vibe",
		from: pypi,
	},
	// Junie is one binary for both: a flag puts it into ACP. It is installed
	// by JetBrains' own script and published in no package registry, so a
	// machine without it is told so rather than offered a download. No
	// terminal columns yet: its CLI takes MCP servers only from folders
	// (--mcp-location), not the way ours are handed over.
	//
	// Its model is not the session's alone: whichever way it is chosen —
	// the option over ACP or --model — Junie writes it to
	// ~/.junie/settings.json as the default for every launch after, the
	// person's own included. An agent entry that names a model moves that
	// default; there is no per-process way around it (seen on 26.7.27).
	model.KindJunie: {
		bin: "junie",
		// The update check is a prompt of its own, and nobody would see it.
		acpArgs: []string{"--acp=true", "--skip-update-check"},
		// It offers resume and load, and neither can be worked in (seen on
		// 26.7.27). Resume wants mcpServers, which the schema leaves optional
		// and the SDK therefore omits when empty. And once a conversation is
		// open again, every turn streams the answer to the turn before and
		// ends; its own answer arrives after the turn is over. The
		// conversation itself is intact, only the stream is a turn late, so
		// a step would end on words that answer nothing. Its stage is
		// cancelled on a restart, as before resume existed.
		noRevive: true,
	},
}

// cliFor is the interactive CLI of a kind, and whether there is one.
func cliFor(kind string) (adapter, bool) {
	a, ok := adapters[kind]
	if !ok || a.cliBin == "" || a.cliTools == nil {
		return adapter{}, false
	}
	return a, true
}

// AdapterStatus is what the UI shows next to a kind.
//
// Answering "is this agent usable on this machine" in the agents dialog is the
// point. A missing adapter otherwise surfaces as a session that fails at its
// first turn, on a card, minutes after somebody configured the agent — while
// the fact was knowable the moment the dialog was opened.
type AdapterStatus struct {
	Kind string `json:"kind"`
	// Package is the package that provides the adapter, empty for a kind
	// whose CLI is installed some other way.
	Package string `json:"package,omitempty"`
	// Path is the adapter binary we found, empty when there is none.
	Path string `json:"path,omitempty"`
	// Ready reports that a session of this kind can start right now.
	Ready bool `json:"ready"`
	// Via names the runner — npx, uvx — of a kind that is not installed but
	// will be run through it: it works, only the first run pays for the
	// download.
	Via string `json:"via,omitempty"`
	// Detail says what is missing, with what a person needs to act on.
	Detail *msg.Msg `json:"detail,omitempty"`

	// Terminal reports that a stage can be *worked in a terminal* by this kind,
	// which is a different question from Ready and has a different answer: the
	// vendor adapter and the vendor's interactive CLI are two programs, and a
	// machine can have one without the other (docs/system.md §4.1.1).
	Terminal bool `json:"terminal"`
	// TerminalDetail says why not, or what it will run.
	TerminalDetail *msg.Msg `json:"terminalDetail,omitempty"`
}

// AdapterStatuses reports every kind we know how to launch, in the order the UI
// offers them. The generic acp kind is absent: it carries its own command, so
// there is nothing to check.
func AdapterStatuses() []AdapterStatus {
	out := make([]AdapterStatus, 0, len(adapters))
	for _, kind := range model.Kinds {
		if _, known := adapters[kind]; !known {
			continue
		}
		out = append(out, adapterStatus(kind))
	}
	return out
}

func adapterStatus(kind string) AdapterStatus {
	def := adapters[kind]
	st := AdapterStatus{Kind: kind, Package: def.pkg}
	st.Terminal, st.TerminalDetail = terminalStatus(kind)
	if bin, err := lookupBin(def.bin); err == nil {
		st.Path, st.Ready = bin, true
		return st
	}
	if def.pkg == "" {
		st.Detail = detail("adapter.missing", "bin", def.bin)
		return st
	}
	if _, err := lookupBin(def.from.runner); err == nil {
		st.Ready, st.Via = true, def.from.runner
		st.Detail = detail("adapter.viaRunner", "bin", def.bin, "runner", def.from.runner)
		return st
	}
	// Nothing to offer: installing Node.js or uv is not something to do behind
	// a person's back.
	st.Detail = detail("adapter.noRunner", "bin", def.bin, "runner", def.from.runner,
		"needs", def.from.needs, "install", def.from.install(def.pkg))
	return st
}

func detail(code string, kv ...string) *msg.Msg {
	m := msg.New(code, kv...)
	return &m
}

// terminalStatus answers "can a stage be worked in a terminal by this kind, on
// this machine". Asked in the agents dialog for the same reason the adapter is:
// the alternative is finding out on a card, after somebody built a flow around
// a stage that will never open.
func terminalStatus(kind string) (bool, *msg.Msg) {
	cli, ok := cliFor(kind)
	if !ok {
		return false, detail("adapter.noCLI")
	}
	bin, err := lookupBin(cli.cliBin)
	if err != nil {
		return false, detail("terminal.binMissing", "bin", cli.cliBin)
	}
	return true, detail("adapter.terminalReady", "bin", bin)
}

// terminalBin is the interactive CLI as it will actually be run: found on PATH
// or in the usual install spots, because launchd hands a GUI application a
// minimal PATH and a CLI installed with Homebrew or npm is invisible in it.
func terminalBin(cli adapter) (string, error) {
	return lookupBin(cli.cliBin)
}

// launch is how one agent process is started, once the table row and the
// registry entry have been folded together.
type launch struct {
	argv    []string
	env     []string // kind-specific, overridden by the agent's own env
	dropEnv []string
	mode    string
}

// launchFor resolves how an agent is started.
//
// Command overrides everything, because it is how a wrapper — a proxy launcher,
// a per-account shim — gets in front of the adapter; a known kind is otherwise
// its binary plus whatever selects ACP. Args are appended in both cases.
func launchFor(a model.Agent) (launch, error) {
	def, known := adapters[a.Kind]
	var argv []string
	switch {
	case len(a.Command) > 0:
		argv = append(argv, a.Command...)
	case known:
		bin, err := adapterArgv(a.Kind, a.BinPath)
		if err != nil {
			return launch{}, err
		}
		argv = append(argv, bin...)
		argv = append(argv, def.acpArgs...)
	default:
		return launch{}, msg.Err("agent.noLaunch", "agent", a.Name, "kind", a.Kind)
	}
	l := launch{argv: append(argv, a.Args...), dropEnv: def.dropEnv, mode: def.mode}
	// A model asked for through the environment is set whichever way the agent
	// was launched: a wrapper replaces the argv, not the model.
	if a.Model != "" && def.modelEnv != "" {
		l.env = []string{def.modelEnv + "=" + a.Model}
	}
	return l, nil
}

// adapterArgv resolves the adapter binary for a kind. An installed binary wins;
// failing that, the package is run through its registry's runner, so a machine
// with Node.js or uv needs no install step at all. Nothing else is guessed:
// an agent that cannot be started says so here rather than at its first turn.
func adapterArgv(kind, override string) ([]string, error) {
	def := adapters[kind]
	name := def.bin
	if override != "" {
		name = override
	}
	if bin, err := lookupBin(name); err == nil {
		return []string{bin}, nil
	} else if override != "" {
		// A path a person typed is an instruction, not a hint: falling back
		// from it would silently run something else.
		return nil, msg.Wrap(err, "adapter.badPath", "path", override)
	}
	if def.pkg == "" {
		return nil, msg.Err("adapter.missing", "bin", def.bin)
	}
	if runner, err := lookupBin(def.from.runner); err == nil {
		return append([]string{runner}, def.from.runArgs(def.pkg, def.bin)...), nil
	}
	return nil, msg.Err("adapter.install", "bin", def.bin, "runner", def.from.runner,
		"needs", def.from.needs, "install", def.from.install(def.pkg))
}

// lookupBin finds an executable. PATH first, then the usual install locations,
// because launchd hands a GUI application a minimal PATH and an agent installed
// with Homebrew or npm would otherwise be invisible.
func lookupBin(name string) (string, error) {
	if name == "" {
		return "", errors.New("no program name given")
	}
	if strings.ContainsRune(name, filepath.Separator) {
		if _, err := os.Stat(name); err != nil {
			return "", err
		}
		return name, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".local", "bin", name),
		filepath.Join(home, ".npm-global", "bin", name),
		"/opt/homebrew/bin/" + name,
		"/usr/local/bin/" + name,
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found", name)
}

// resolveArgv0 makes an argv runnable from a GUI process, for the same reason
// lookupBin searches beyond PATH. Left as written when nothing matches, so the
// spawn error names the command a person actually typed.
func resolveArgv0(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	resolved, err := lookupBin(argv[0])
	if err != nil {
		return argv
	}
	out := append([]string(nil), argv...)
	out[0] = resolved
	return out
}

// spawnEnv is the environment one agent process gets on top of ours. The
// proxy's variables come first and the agent's own Env last, so Env can
// override or blank any of them — how one agent opts out of an inherited proxy.
// Appended after ours, they win: a duplicate name resolves to the last one.
func spawnEnv(a model.Agent) []string {
	var env []string
	if a.Network != nil {
		env = a.Network.Env()
	}
	for k, v := range a.Env {
		if k = strings.TrimSpace(k); k != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
