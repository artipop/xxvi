package acp

import (
	"context"
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
		Text:    permissionText(toolName, title),
		Tool:    toolName,
		Options: permissionOptions(params),
	})

	chosen := permissionOption(params, answer.OptionID)
	if answer.Declined || chosen == nil {
		// Nobody answered — the application is closing, the turn was cancelled,
		// or the person said no. The policy is still the way to stop being asked.
		c.recordDecision(toolName, title, "reject", answer.Declined)
		c.m.log.Info("разрешение не выдано", "session", c.s.id, "card", c.s.card.ID, "tool", toolName)
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

// permissionText is the question as a person reads it: what the agent is about
// to do, in the agent's own words where it gave any.
func permissionText(toolName, title string) string {
	switch {
	case title != "" && toolName != "":
		return fmt.Sprintf("Разрешить %s: %s?", toolName, title)
	case title != "":
		return fmt.Sprintf("Разрешить: %s?", title)
	case toolName != "":
		return fmt.Sprintf("Разрешить %s?", toolName)
	}
	return "Разрешить действие агента?"
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

func (c *sessionClient) jail(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("путь должен быть абсолютным: %s", path)
	}
	clean := filepath.Clean(path)
	root := c.s.cwd
	if root == "" {
		return "", fmt.Errorf("у сессии нет рабочей папки")
	}
	// The agent may well spell the working directory differently than we do and
	// still mean it: on macOS the temp and home trees are reached through
	// symlinks (/var → /private/var), and an agent that resolved the path
	// before asking would be refused its own working directory.
	for _, candidate := range []string{clean, resolvedPath(clean)} {
		for _, r := range []string{root, resolvedPath(root)} {
			if underRoot(candidate, r) {
				return clean, nil
			}
		}
	}
	c.m.log.Warn("доступ за пределы рабочей папки запрещён", "session", c.s.id, "path", clean)
	return "", fmt.Errorf("путь %s вне рабочей папки сессии", clean)
}

func underRoot(path, root string) bool {
	if root == "" {
		return false
	}
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// resolvedPath follows symlinks, falling back to the path as given — a path
// that cannot be resolved is not a reason to refuse everything. A file being
// created does not exist yet, so its directory is resolved instead.
func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir, base := filepath.Split(path)
	if resolved, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(resolved, base)
	}
	return path
}

// Terminal capability is not advertised, so these are never reached.
func (c *sessionClient) CreateTerminal(ctx context.Context, params acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, fmt.Errorf("терминал не поддерживается")
}
func (c *sessionClient) KillTerminal(ctx context.Context, params acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, fmt.Errorf("терминал не поддерживается")
}
func (c *sessionClient) TerminalOutput(ctx context.Context, params acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, fmt.Errorf("терминал не поддерживается")
}
func (c *sessionClient) ReleaseTerminal(ctx context.Context, params acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, fmt.Errorf("терминал не поддерживается")
}
func (c *sessionClient) WaitForTerminalExit(ctx context.Context, params acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, fmt.Errorf("терминал не поддерживается")
}
