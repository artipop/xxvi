package stagemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The second thing a stage in a terminal says back: what its CLI is doing.
//
// claude and codex both run a command of their own choosing at the points of a
// conversation — it starts, a turn begins, a permission is asked for, a turn
// ends — and hand it that moment as JSON on stdin. The command is this same
// executable (`xxvi hook`), which forwards the moment here, to the step it
// belongs to. It is what replaces guessing from a silent terminal: silence is
// forty-five seconds late and cannot tell a question from a model thinking,
// while a hook is the CLI saying so the moment it happens.

// The environment a hook finds the step through. The CLI passes its own
// environment on to the commands it runs, so these travel from our spawn to the
// hook without ever being on a command line: argv is what ps shows every user
// of the machine, and TokenEnv is the grant.
const (
	TokenEnv   = "XXVI_STEP_TOKEN"
	HookURLEnv = "XXVI_HOOK_URL"
	// HookExeEnv names this executable. The hook's command refers to it by
	// variable rather than spelling the path, so a path with a space or a
	// quote in it needs no escaping in two vendors' configuration languages —
	// and codex's trust in the command, which is a hash of its text, does not
	// change when the application moves.
	HookExeEnv = "XXVI_HOOK_EXE"
)

// HookCommand is the command both CLIs are told to run, through a POSIX shell.
const HookCommand = `"$` + HookExeEnv + `" hook`

// HookEvent is one moment of the CLI's conversation, in the vendors' own field
// names — claude and codex share them. Only what a step acts on is kept: the
// payload also carries the prompt and the transcript's path, and neither has
// any business travelling further than the hook.
type HookEvent struct {
	Event     string `json:"hook_event_name"`
	SessionID string `json:"session_id,omitempty"`
	// Source is why a conversation started: startup, resume, clear, compact.
	Source string `json:"source,omitempty"`
	// NotificationType tells claude's Notification apart: a permission prompt
	// is a person being asked, an idle reminder is a turn that already ended.
	NotificationType string `json:"notification_type,omitempty"`
	// ToolName is the tool a tool event is about.
	ToolName string `json:"tool_name,omitempty"`
	// AgentID is set when a subagent is the one acting, whose session is not
	// the conversation a stage resumes.
	AgentID string `json:"agent_id,omitempty"`
}

// maxHookInput bounds what the hook reads: the payload carries the whole prompt,
// and a hook that blocks the CLI's turn must not be the thing that runs out of
// memory.
const maxHookInput = 4 << 20

// ForwardHook is `xxvi hook`: it reads one hook payload and hands it to the
// step named by the environment. It never fails the CLI — every error is the
// caller's to swallow — and writes nothing to stdout, which some events feed
// back to the model as context.
func ForwardHook(ctx context.Context, in io.Reader, getenv func(string) string) error {
	url, token := getenv(HookURLEnv), getenv(TokenEnv)
	if url == "" || token == "" {
		return errors.New("not started by a stage")
	}
	raw, err := io.ReadAll(io.LimitReader(in, maxHookInput))
	if err != nil {
		return err
	}
	var ev HookEvent
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &ev); err != nil {
		return fmt.Errorf("read the hook payload: %w", err)
	}
	if ev.Event == "" {
		return errors.New("the hook payload names no event")
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	// Short: the CLI waits for its hooks, and a step that is gone answers
	// at once anyway.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the step refused the event: %s", resp.Status)
	}
	return nil
}

// HookURL is where `xxvi hook` delivers. Empty until the port is open.
func (s *Server) HookURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addr == "" {
		return ""
	}
	return "http://" + s.addr + "/hook"
}

// serveHook takes one event for the step whose grant it carries.
func (s *Server) serveHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	step, ok := s.step(r)
	if !ok {
		http.Error(w, "this step is already finished", http.StatusForbidden)
		return
	}
	var ev HookEvent
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&ev); err != nil || strings.TrimSpace(ev.Event) == "" {
		http.Error(w, "not a hook event", http.StatusBadRequest)
		return
	}
	if step.Hook != nil {
		step.Hook(ev)
	}
	w.WriteHeader(http.StatusNoContent)
}
