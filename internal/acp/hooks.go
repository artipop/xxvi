package acp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/artipop/xxvi/internal/stagemcp"
)

// What a stage's CLI says about itself, through its own hooks (stagemcp's
// hook.go carries it here). Two things are read from it: which conversation the
// CLI is holding, and whether it is working or waiting for the person.

// cliState is what the CLI is doing, as far as its hooks have said.
type cliState int

const (
	cliUnknown cliState = iota
	cliWorking
	// cliAsking is the CLI stopped mid-turn on a person: a permission, a
	// question of its own.
	cliAsking
	// cliTurnEnded is a turn over without finish_step: the agent waiting for
	// the next remark, since the step is not done until it says so.
	cliTurnEnded
)

// Why a terminal's row is in the attention list. The UI words each one.
const (
	waitAsking = "asking"
)

// askingTools are claude's tools whose whole purpose is a person answering:
// PreToolUse on them is the question appearing, and no permission hook fires
// for it.
var askingTools = map[string]bool{"AskUserQuestion": true, "ExitPlanMode": true}

func stateOf(ev stagemcp.HookEvent) cliState {
	switch ev.Event {
	case "UserPromptSubmit", "PostToolUse":
		// PostToolUse is what follows an approved permission: approving it
		// fires nothing of its own.
		return cliWorking
	case "PreToolUse":
		if askingTools[ev.ToolName] {
			return cliAsking
		}
		return cliWorking
	case "PermissionRequest":
		return cliAsking
	case "Notification":
		// Its other kinds — «still waiting for your input» after a turn, a
		// login going through — say nothing a Stop has not said already.
		switch ev.NotificationType {
		case "permission_prompt", "elicitation_dialog":
			return cliAsking
		}
	case "Stop", "StopFailure":
		if ev.AgentID == "" {
			return cliTurnEnded
		}
	}
	return cliUnknown
}

// conversationOf is the conversation an event speaks for, or nothing. Every
// event of the main thread carries it, so a conversation the CLI swapped for
// another — /clear, /resume, a new thread — is seen at its next event, not only
// at a SessionStart a vendor may or may not send for it.
func conversationOf(ev stagemcp.HookEvent) string {
	if ev.AgentID != "" {
		return ""
	}
	return strings.TrimSpace(ev.SessionID)
}

// hookTimeout bounds each hook, in seconds. The CLI waits for it before going
// on, and the worst it does is one request over loopback.
const hookTimeout = 10

// hooksPossible reports whether this machine can run the hook command at all.
// It is spelled for a POSIX shell, which is what both CLIs run hooks through
// everywhere but Windows; there the card is never marked as asking.
func hooksPossible() bool { return runtime.GOOS != "windows" }

// claudeHookEvents are what claude is asked to report. PreToolUse only for the
// tools that ask a person: a process of ours on every tool call is a price for
// nothing, since PostToolUse already says the agent is moving.
var claudeHookEvents = []struct{ event, matcher string }{
	{"SessionStart", ""},
	{"UserPromptSubmit", ""},
	{"PreToolUse", "AskUserQuestion|ExitPlanMode"},
	{"PostToolUse", ""},
	{"PermissionRequest", ""},
	{"Notification", ""},
	{"Stop", ""},
	{"StopFailure", ""},
}

// claudeHooks hands claude the hooks as a settings file of their own. Through
// --settings rather than any settings.json: those are the person's, and what a
// card's run needs dies with it. claude merges hooks from every source, so the
// person's own keep running beside ours.
func claudeHooks() (toolsHandoff, error) {
	hooks := map[string]any{}
	for _, e := range claudeHookEvents {
		group := map[string]any{"hooks": []map[string]any{{
			"type": "command", "command": stagemcp.HookCommand, "timeout": hookTimeout,
		}}}
		if e.matcher != "" {
			group["matcher"] = e.matcher
		}
		hooks[e.event] = []any{group}
	}
	f, err := os.CreateTemp("", "xxvi-hooks-*.json")
	if err != nil {
		return toolsHandoff{}, fmt.Errorf("write the hooks: %w", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(map[string]any{"hooks": hooks}); err != nil {
		_ = os.Remove(f.Name())
		return toolsHandoff{}, fmt.Errorf("write the hooks: %w", err)
	}
	return toolsHandoff{args: []string{"--settings", f.Name()}, files: []string{f.Name()}}, nil
}

// codexHookEvents pair codex's event name with the spelling its trust keys use.
// No Notification or StopFailure: codex has neither, and its asking is all
// PermissionRequest.
var codexHookEvents = []struct{ event, key string }{
	{"SessionStart", "session_start"},
	{"UserPromptSubmit", "user_prompt_submit"},
	{"PostToolUse", "post_tool_use"},
	{"PermissionRequest", "permission_request"},
	{"Stop", "stop"},
}

// codexHooks hands codex the hooks as one `-c hooks=…` override, for the reason
// codexTools does: codex has no file of settings to point at, and its
// config.toml is the person's.
//
// codex runs a hook only once it is trusted — a hash of the handler, recorded
// under hooks.state — and an untrusted one is skipped without a word. So the
// trust goes along in the same override, computed the way codex computes it:
// sha256 over the handler's canonical JSON, keyed by the config layer the
// handler came from, which for `-c` is a synthetic «<session-flags>» file. Not
// a documented contract; read off codex's source and checked against 0.157
// and 0.158 (docs/system.md §4.1.1). If a later codex hashes differently, its
// hooks go quiet and the step stops marking questions.
func codexHooks() (toolsHandoff, error) {
	var events, state []string
	for _, e := range codexHookEvents {
		hash, err := codexTrustHash(e.key, stagemcp.HookCommand, hookTimeout)
		if err != nil {
			return toolsHandoff{}, err
		}
		events = append(events, fmt.Sprintf(`%s=[{hooks=[{type="command",command=%s,timeout=%d}]}]`,
			e.event, tomlString(stagemcp.HookCommand), hookTimeout))
		state = append(state, fmt.Sprintf(`%s={trusted_hash=%s}`,
			tomlString("/<session-flags>/config.toml:"+e.key+":0:0"), tomlString(hash)))
	}
	value := "{" + strings.Join(events, ",") + ",state={" + strings.Join(state, ",") + "}}"
	return toolsHandoff{args: []string{"-c", "hooks=" + value}}, nil
}

// codexTrustHash is codex's trusted_hash for one handler: its identity as JSON
// with sorted keys and nothing escaped that serde would leave alone.
func codexTrustHash(eventKey, command string, timeout int) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(map[string]any{
		"event_name": eventKey,
		"hooks": []map[string]any{{
			"async": false, "command": command, "timeout": timeout, "type": "command",
		}},
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// tomlString is a TOML basic string. Go's quoting is one for everything we put
// in one — printable ASCII — which the tests hold it to.
func tomlString(s string) string { return strconv.Quote(s) }

// hookEnv is where the hook finds its way back: this executable, the step's
// door, and the grant.
func hookEnv(url, token string) ([]string, bool) {
	exe, err := os.Executable()
	if err != nil || url == "" || token == "" {
		return nil, false
	}
	return []string{
		stagemcp.HookExeEnv + "=" + exe,
		stagemcp.HookURLEnv + "=" + url,
		stagemcp.TokenEnv + "=" + token,
	}, true
}
