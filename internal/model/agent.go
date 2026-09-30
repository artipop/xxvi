package model

import "strings"

// Agent kinds. All but the last are presets: how a known agent is started in
// ACP mode. The last carries its own launch command and is how anything else
// that speaks ACP is registered; everything past the launch — sessions,
// models, reviving a conversation — goes over the protocol the same way for
// every kind.
const (
	KindClaude = "claude"
	KindCodex  = "codex"
	KindVibe   = "vibe"
	KindJunie  = "junie"
	KindACP    = "acp"
)

// Kinds is every kind, in the order the UI offers them.
var Kinds = []string{KindClaude, KindCodex, KindVibe, KindJunie, KindACP}

// Agent is one registered ACP agent.
//
// Env is injected per process at spawn time, which is how several agents (two
// Codex accounts, say) coexist on one machine: give each its own
// CODEX_HOME/OPENAI_API_KEY or CLAUDE_CONFIG_DIR/ANTHROPIC_API_KEY.
type Agent struct {
	Name string `json:"name"` // registry key; matches a card's assignee
	Kind string `json:"kind"`

	// BinPath overrides adapter discovery. A path typed here is an instruction,
	// not a hint: if it does not work the agent fails to start rather than
	// quietly running something else.
	BinPath string `json:"binPath,omitempty"`
	Model   string `json:"model,omitempty"`
	// Prompt is prepended to every task this agent is given — who it is, before
	// what the stage and the card ask of it.
	Prompt string `json:"prompt,omitempty"`

	Env  map[string]string `json:"env,omitempty"`
	Args []string          `json:"args,omitempty"`

	// Command is the whole launch argv, required for KindACP. It replaces the
	// adapter binary we would have looked up, so a wrapper can get in front of
	// it. With it set nothing of ours is appended, so the flags the kind would
	// have carried have to be spelled out.
	Command []string `json:"command,omitempty"`

	// AutoAllowTools overrides the machine-wide policy for this agent, so a
	// trusted one can be let loose and a new one kept on a short leash without
	// changing anything for the rest. Entries take the form "Read" or
	// "Bash(git *)".
	AutoAllowTools []string `json:"autoAllowTools,omitempty"`
}

// Username is the name an agent is matched by: the registry name folded to
// something comparable, so the entry "My Agent" and the assignee "my-agent" are
// the same agent. Must stay deterministic — matching depends on it.
func Username(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// SameAgentName reports whether a name written somewhere — a card's assignee, a
// stage's crew — refers to this registry entry.
func SameAgentName(name, entryName string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if strings.EqualFold(name, entryName) {
		return true
	}
	folded := Username(name)
	return folded != "" && folded == Username(entryName)
}

// IsAgentName reports whether any registered agent answers to this name.
func IsAgentName(agents []Agent, name string) bool {
	for _, a := range agents {
		if SameAgentName(name, a.Name) {
			return true
		}
	}
	return false
}

// AgentNames is the registry as a readable list, for error messages that have
// to say what was available.
func AgentNames(agents []Agent) string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}
