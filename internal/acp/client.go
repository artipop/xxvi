package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
)

// sessionClient implements acpsdk.Client for one session: it receives the
// agent's stream, records it, and answers permission requests according to
// policy — asking the person about anything the policy does not cover.
type sessionClient struct {
	m *Manager
	s *session

	// toolNames remembers what each tool call was called, because the agent
	// announces a call and asks permission for it in two separate messages, and
	// only the first is required to carry a name.
	toolMu    sync.Mutex
	toolNames map[string]string // toolCallId → tool name
}

var _ acpsdk.Client = (*sessionClient)(nil)

// clientCapabilities is what we tell an agent this client can do.
//
// Form elicitation is claimed, and the claude adapter reads that as permission
// to leave AskUserQuestion enabled: an agent that needs a decision asks for it,
// and the question lands on the card it is working (question.go). URL mode is
// not claimed — it sends a person to a browser to finish something there, and
// an application that is itself where the work happens has nowhere to put that.
func clientCapabilities() acpsdk.ClientCapabilities {
	return acpsdk.ClientCapabilities{
		Fs:          acpsdk.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
		Elicitation: &acpsdk.ElicitationCapabilities{Form: &acpsdk.ElicitationFormCapabilities{}},
	}
}

// RequestPermission applies the policy and the session's accumulated "always
// allow" set, and asks the person about anything else. This is the protocol's
// own way of wanting a human, and answering it for them — which is what
// refusing it outright would be — leaves an agent that cannot do its job and a
// card that does not say why.
//
// Blocking here is safe: the SDK dispatches every inbound request on its own
// goroutine, so the agent's stream keeps flowing while the card waits, and the
// turn is still open when the answer arrives.
func (c *sessionClient) RequestPermission(ctx context.Context, params acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	toolName := c.permissionToolName(params)
	title := ""
	if params.ToolCall.Title != nil {
		title = *params.ToolCall.Title
	}

	if c.s.policy.Allows(toolName, params.ToolCall.RawInput) || c.s.toolAllowed(toolName) {
		c.recordDecision(toolName, title, "allow", true)
		return selectOption(params, acpsdk.PermissionOptionKindAllowOnce)
	}

	answer := c.m.ask(ctx, c.s, Question{
		Kind:    QuestionPermission,
		Text:    title,
		Tool:    toolName,
		Options: permissionOptions(params),
	})

	chosen := permissionOption(params, answer.OptionID)
	if answer.Declined || chosen == nil {
		// Nobody answered — the application is closing, the turn was cancelled,
		// or the person said no. The policy is still the way to stop being asked.
		c.recordDecision(toolName, title, "reject", answer.Declined)
		c.m.log.Info("permission not granted", "session", c.s.id, "card", c.s.card.ID, "tool", toolName)
		return selectOption(params, acpsdk.PermissionOptionKindRejectOnce)
	}
	// "Always" is what makes answering once enough: the rest of this session's
	// calls to the same tool go through without asking again.
	if chosen.Kind == string(acpsdk.PermissionOptionKindAllowAlways) {
		c.s.allowToolAlways(toolName)
	}
	decision := "reject"
	if strings.HasPrefix(chosen.Kind, "allow") {
		decision = "allow"
	}
	c.recordDecision(toolName, title, decision, false)
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
		Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: acpsdk.PermissionOptionId(chosen.ID)},
	}}, nil
}

// permissionOptions turns the agent's options into the card's buttons. The
// labels are the agent's own — it knows what it is asking better than we do.
func permissionOptions(params acpsdk.RequestPermissionRequest) []QuestionOption {
	out := make([]QuestionOption, 0, len(params.Options))
	for _, opt := range params.Options {
		out = append(out, QuestionOption{ID: string(opt.OptionId), Label: opt.Name, Kind: string(opt.Kind)})
	}
	return out
}

func permissionOption(params acpsdk.RequestPermissionRequest, optionID string) *QuestionOption {
	for _, opt := range params.Options {
		if string(opt.OptionId) == optionID {
			chosen := QuestionOption{ID: optionID, Label: opt.Name, Kind: string(opt.Kind)}
			return &chosen
		}
	}
	return nil
}

// recordDecision records how a permission ended up. byPolicy marks decisions
// the person was never asked about.
func (c *sessionClient) recordDecision(toolName, title, decision string, byPolicy bool) {
	c.s.event(c.m, "permission", map[string]any{
		"tool": toolName, "title": title, "decision": decision, "byPolicy": byPolicy,
	})
}

// selectOption picks the agent-offered option of the wanted kind, falling back
// to cancellation when the agent offered nothing suitable.
func selectOption(params acpsdk.RequestPermissionRequest, kind acpsdk.PermissionOptionKind) (acpsdk.RequestPermissionResponse, error) {
	for _, opt := range params.Options {
		if opt.Kind == kind {
			return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
				Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
			}}, nil
		}
	}
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
		Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{},
	}}, nil
}

// permissionToolName recovers the tool name, in the order it is trustworthy
// (see toolname.go).
func (c *sessionClient) permissionToolName(params acpsdk.RequestPermissionRequest) string {
	if name := metaToolName(params.ToolCall.Meta); name != "" {
		return normalizeToolName(name)
	}
	// The call was announced before permission was asked for it, and that
	// announcement is where an adapter puts the name.
	if name := c.recalledToolName(string(params.ToolCall.ToolCallId)); name != "" {
		return name
	}
	kind := ""
	if params.ToolCall.Kind != nil {
		kind = string(*params.ToolCall.Kind)
	}
	if name := inferToolName(kind, params.ToolCall.RawInput); name != "" {
		return name
	}
	if params.ToolCall.Title != nil {
		if name, _, found := strings.Cut(*params.ToolCall.Title, ":"); found {
			return strings.TrimSpace(name)
		}
		return *params.ToolCall.Title
	}
	return ""
}

// noteToolCall files whatever the announcement of a call tells us about which
// tool it is: the name the agent gave it, else what its kind and input say.
func (c *sessionClient) noteToolCall(id string, meta map[string]any, kind string, input any) {
	name := normalizeToolName(metaToolName(meta))
	if name == "" {
		name = inferToolName(kind, input)
	}
	if id == "" || name == "" {
		return
	}
	c.toolMu.Lock()
	defer c.toolMu.Unlock()
	if c.toolNames == nil {
		c.toolNames = make(map[string]string)
	}
	// A turn's worth of tool calls is small, and nothing tells us a call is
	// finished with. Forgetting the oldest keeps a long session from growing
	// this without bound.
	if len(c.toolNames) >= maxRememberedTools {
		for k := range c.toolNames {
			delete(c.toolNames, k)
			break
		}
	}
	c.toolNames[id] = name
}

// maxRememberedTools bounds the id → name map.
const maxRememberedTools = 512

func (c *sessionClient) recalledToolName(id string) string {
	c.toolMu.Lock()
	defer c.toolMu.Unlock()
	return c.toolNames[id]
}

// SessionUpdate receives everything the agent says. What it says goes into the
// session's stream and, for its final message, into the buffer a comment
// condition on an edge is asked about.
func (c *sessionClient) SessionUpdate(ctx context.Context, params acpsdk.SessionNotification) error {
	if c.s.replaying.Load() {
		return nil
	}
	u := params.Update
	switch {
	case u.AgentMessageChunk != nil:
		if t := u.AgentMessageChunk.Content.Text; t != nil {
			c.s.appendFinal(t.Text)
			c.s.event(c.m, "chunk", map[string]any{"text": t.Text})
		}
	case u.AgentThoughtChunk != nil:
		if t := u.AgentThoughtChunk.Content.Text; t != nil {
			c.s.event(c.m, "thought", map[string]any{"text": t.Text})
		}
	case u.ToolCall != nil:
		c.noteToolCall(string(u.ToolCall.ToolCallId), u.ToolCall.Meta,
			string(u.ToolCall.Kind), u.ToolCall.RawInput)
		c.s.event(c.m, "tool_call", map[string]any{
			"toolCallId": string(u.ToolCall.ToolCallId),
			"title":      u.ToolCall.Title,
			"status":     string(u.ToolCall.Status),
		})
	case u.ToolCallUpdate != nil:
		// An update may be the first message that names the call: an adapter
		// that fills the input in stages sends the name with every one of them.
		kind := ""
		if u.ToolCallUpdate.Kind != nil {
			kind = string(*u.ToolCallUpdate.Kind)
		}
		c.noteToolCall(string(u.ToolCallUpdate.ToolCallId), u.ToolCallUpdate.Meta,
			kind, u.ToolCallUpdate.RawInput)
		status := ""
		if u.ToolCallUpdate.Status != nil {
			status = string(*u.ToolCallUpdate.Status)
		}
		c.s.event(c.m, "tool_update", map[string]any{
			"toolCallId": string(u.ToolCallUpdate.ToolCallId), "status": status,
		})
	}
	return nil
}

// File-system proxying is jailed to the session's working directory. The vendor
// adapters never call these (their CLI does its own I/O); an external ACP agent
// might.
func (c *sessionClient) ReadTextFile(ctx context.Context, params acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	path, err := c.jail(params.Path)
	if err != nil {
		return acpsdk.ReadTextFileResponse{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return acpsdk.ReadTextFileResponse{}, err
	}
	return acpsdk.ReadTextFileResponse{Content: string(b)}, nil
}

func (c *sessionClient) WriteTextFile(ctx context.Context, params acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	path, err := c.jail(params.Path)
	if err != nil {
		return acpsdk.WriteTextFileResponse{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return acpsdk.WriteTextFileResponse{}, err
	}
	if err := os.WriteFile(path, []byte(params.Content), 0o644); err != nil {
		return acpsdk.WriteTextFileResponse{}, err
	}
	return acpsdk.WriteTextFileResponse{}, nil
}

// jail is the protocol's side of the card's working folder. The absolute-path
// rule is the protocol's own — a relative path from an agent means nothing here
// — and the boundary itself is shared with the notes screen (see Within).
func (c *sessionClient) jail(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("the path must be absolute: %s", path)
	}
	inside, err := Within(c.s.cwd, path)
	if err != nil {
		c.m.log.Warn("access outside the working folder refused", "session", c.s.id, "path", filepath.Clean(path))
		return "", fmt.Errorf("the path %s is outside the session's working folder", filepath.Clean(path))
	}
	return inside, nil
}

// Terminal capability is not advertised, so these are never reached.
func (c *sessionClient) CreateTerminal(ctx context.Context, params acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, errors.New("terminals are not supported")
}
func (c *sessionClient) KillTerminal(ctx context.Context, params acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, errors.New("terminals are not supported")
}
func (c *sessionClient) TerminalOutput(ctx context.Context, params acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, errors.New("terminals are not supported")
}
func (c *sessionClient) ReleaseTerminal(ctx context.Context, params acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, errors.New("terminals are not supported")
}
func (c *sessionClient) WaitForTerminalExit(ctx context.Context, params acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, errors.New("terminals are not supported")
}
