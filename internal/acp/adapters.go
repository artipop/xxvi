package acp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/artipop/xxvi/internal/model"
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
		// this app may well have been launched from one.
		dropEnv: []string{"CLAUDECODE"},
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
		mode: "agent",
	},
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
	// Detail says what is missing, in the words a person needs to act on.
	Detail string `json:"detail,omitempty"`
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
	if bin, err := lookupBin(def.bin); err == nil {
		st.Path, st.Ready = bin, true
		return st
	}
	if def.npmPackage == "" {
		st.Detail = fmt.Sprintf("не найден %s — укажите путь к бинарнику или команду запуска у агента", def.bin)
		return st
	}
	if _, err := lookupBin("npx"); err == nil {
		st.Ready, st.ViaNPX = true, true
		st.Detail = fmt.Sprintf("%s не установлен — будет запускаться через npx (первый запуск дольше)", def.bin)
		return st
	}
	// Nothing to offer: npm is how both adapters are published, and installing
	// Node.js is not something to do behind a person's back.
	st.Detail = fmt.Sprintf("не найден ни %s, ни npx — поставьте Node.js и выполните `npm install -g %s`",
		def.bin, def.npmPackage)
	return st
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
		return launch{}, fmt.Errorf("у агента «%s» (тип %s) нет команды запуска", a.Name, a.Kind)
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
		return nil, fmt.Errorf("указанный путь %s: %w", override, err)
	}
	if def.npmPackage == "" {
		return nil, fmt.Errorf("не найден %s — укажите путь к бинарнику или команду запуска у агента", def.bin)
	}
	if npx, err := lookupBin("npx"); err == nil {
		return []string{npx, "--yes", def.npmPackage}, nil
	}
	return nil, fmt.Errorf("не найден %s: установите его командой `npm install -g %s` "+
		"(или поставьте Node.js, тогда адаптер запустится через npx)", def.bin, def.npmPackage)
}

// lookupBin finds an executable. PATH first, then the usual install locations,
// because launchd hands a GUI application a minimal PATH and an agent installed
// with Homebrew or npm would otherwise be invisible.
func lookupBin(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("не задано имя программы")
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
	return "", fmt.Errorf("не найден %s", name)
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
