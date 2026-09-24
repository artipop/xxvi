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
	// npmPackage provides bin when it is not installed. The two vendor adapters
	// are published there and nowhere else, so it doubles as the install
	// instruction shown to a person and the argument to npx.
	npmPackage string
	// acpArgs put the CLI into ACP-over-stdio mode. Empty when it has no other
	// mode to be in.
	acpArgs []string
	// modelEnv and modelConfig are the two ways an agent is told which model to
	// use: a variable at spawn, or a session config option asked for over ACP
	// once the session exists.
	modelEnv    string
	modelConfig string
	// dropEnv names variables the process must not inherit from ours.
	dropEnv []string
	// mode is the session mode to select after session/new, when the agent's
	// default is not what a card wants.
	mode string

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
	// cliResumeArgs continue the conversation the last CLI left in this folder.
	// The folder is the card's, so "the last conversation here" is that card's
	// — which is what makes a second visit to a stage a continuation rather
	// than a stranger asking the same questions again.
	cliResumeArgs []string
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
	cliPromptArgs func(prompt string) []string
}

// adapters is the table of agents we know how to launch. The generic acp kind
// is deliberately absent: it carries its own Command.
var adapters = map[string]adapter{
	// The Claude adapter embeds the Claude Agent SDK, which embeds the CLI, so
	// the claude binary is not needed alongside it. It is a Node package and
	// there is no other build of it, which is why this kind needs Node.js.
	model.KindClaude: {
		bin:        "claude-agent-acp",
		npmPackage: "@agentclientprotocol/claude-agent-acp",
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
		cliResumeArgs: []string{"--continue"},
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
		bin:        "codex-acp",
		npmPackage: "@agentclientprotocol/codex-acp",
		// It takes no flags either: the model is a session config option, asked
		// for over the protocol once the session exists.
		modelConfig: "model",
		// It starts read-only, which is not what a card asked for: a session
		// that may not edit anything would spend its turn saying so.
		mode:   "agent",
		cliBin: "codex",
		// `codex resume --last` picks up the newest conversation of this folder,
		// the same rule as claude's --continue.
		cliResumeArgs: []string{"resume", "--last"},
		cliTools:      codexTools,
		// The separator for the same reason as claude's: a brief that starts
		// with a dash must not be read as a flag.
		cliPromptArgs: func(prompt string) []string { return []string{"--", prompt} },
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
	// Package is the npm package that provides the adapter, empty for a kind
	// whose CLI is installed some other way.
	Package string `json:"package,omitempty"`
	// Path is the adapter binary we found, empty when there is none.
	Path string `json:"path,omitempty"`
	// Ready reports that a session of this kind can start right now.
	Ready bool `json:"ready"`
	// ViaNPX marks a kind that is not installed but will be run through npx —
	// it works, only the first run pays for the download.
	ViaNPX bool `json:"viaNpx,omitempty"`
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
	st := AdapterStatus{Kind: kind, Package: def.npmPackage}
	st.Terminal, st.TerminalDetail = terminalStatus(kind)
	if bin, err := lookupBin(def.bin); err == nil {
		st.Path, st.Ready = bin, true
		return st
	}
	if def.npmPackage == "" {
		st.Detail = detail("adapter.missing", "bin", def.bin)
		return st
	}
	if _, err := lookupBin("npx"); err == nil {
		st.Ready, st.ViaNPX = true, true
		st.Detail = detail("adapter.viaNpx", "bin", def.bin)
		return st
	}
	// Nothing to offer: npm is how both adapters are published, and installing
	// Node.js is not something to do behind a person's back.
	st.Detail = detail("adapter.noNpx", "bin", def.bin, "package", def.npmPackage)
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
// failing that, a vendor adapter published on npm is run through npx, so a
// machine with Node.js needs no install step at all. Nothing else is guessed:
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
	if def.npmPackage == "" {
		return nil, msg.Err("adapter.missing", "bin", def.bin)
	}
	if npx, err := lookupBin("npx"); err == nil {
		return []string{npx, "--yes", def.npmPackage}, nil
	}
	return nil, msg.Err("adapter.install", "bin", def.bin, "package", def.npmPackage)
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

// spawnEnv is the environment one agent process gets on top of ours, and what
// it must not inherit.
func spawnEnv(a model.Agent) []string {
	env := make([]string, 0, len(a.Env))
	for k, v := range a.Env {
		if k = strings.TrimSpace(k); k != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
