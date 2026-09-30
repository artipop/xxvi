package acp

import (
	"context"
	"os"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// AgentCheck is what an agent says about itself when asked over ACP. It is the
// same question for every agent, a preset or a command somebody typed: which
// program answered, and whether it can do what the application leans on.
//
// Asked in the agents dialog rather than found out on a card: an agent that
// needs a login or a key otherwise fails its first stage, minutes after it was
// registered.
type AgentCheck struct {
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
	// Revive is how a paused conversation of this agent is opened again:
	// resume, load, or empty when it cannot be, and a stage of it that the
	// application closed on is cancelled rather than paused.
	Revive string `json:"revive,omitempty"`
	// Lists reports that its past conversations can be listed, which is what
	// starting a card «from a session» needs.
	Lists bool `json:"lists"`
	// Models are what its model option offers, and Model the one it starts on.
	// Empty for an agent that has no such option.
	Models []string `json:"models,omitempty"`
	Model  string   `json:"model,omitempty"`
	// Problem is why it would not open a session: no key, not signed in. The
	// agent started and answered, so the rest of the check still stands.
	Problem *msg.Msg `json:"problem,omitempty"`
}

// CheckAgent starts an agent, opens one session in an empty folder and closes
// it again. Nothing is sent to a model.
func CheckAgent(ctx context.Context, a model.Agent) (AgentCheck, error) {
	// An empty folder of its own: an agent keeps what it opens, and a check
	// must not leave a conversation behind in somebody's project.
	dir, err := os.MkdirTemp("", "xxvi-check-")
	if err != nil {
		return AgentCheck{}, err
	}
	defer os.RemoveAll(dir)

	conn, init, hangUp, err := dial(ctx, a, dir)
	if err != nil {
		return AgentCheck{}, err
	}
	defer hangUp()

	caps := init.AgentCapabilities
	out := AgentCheck{Lists: caps.SessionCapabilities.List != nil}
	if info := init.AgentInfo; info != nil {
		out.Title, out.Version = info.Name, info.Version
		if info.Title != nil && *info.Title != "" {
			out.Title = *info.Title
		}
	}
	out.Revive = reviveBy(a.Kind, caps)

	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: dir, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		problem := msg.New("check.sessionRefused").Because(clipped(a, err))
		out.Problem = &problem
		return out, nil
	}
	if sel := modelOption(sess.ConfigOptions); sel != nil {
		out.Model = string(sel.CurrentValue)
		for _, opt := range configSelectOptions(sel.Options) {
			out.Models = append(out.Models, string(opt.Value))
		}
	}
	if caps.SessionCapabilities.Close != nil {
		_, _ = conn.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: sess.SessionId})
	}
	return out, nil
}
