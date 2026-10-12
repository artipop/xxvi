package acp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

func DeletePastSession(ctx context.Context, a model.Agent, cwd, id string) error {
	if a.Kind != model.KindCodex && a.Kind != model.KindClaude {
		return msg.Err("sessions.notDeletable", "agent", a.Name)
	}
	conversation, err := uuid.Parse(id)
	if err != nil {
		return msg.Err("sessions.invalidID", "id", id)
	}
	if a.Kind == model.KindClaude {
		return deleteClaudeSession(a, conversation.String())
	}
	cli, _ := cliFor(a.Kind)
	bin, err := terminalBin(cli)
	if err != nil {
		return msg.Wrap(err, "terminal.binMissing", "bin", cli.cliBin)
	}
	// The vendor CLI owns both the transcript and its index, including CODEX_HOME.
	cmd := exec.CommandContext(ctx, bin, "delete", id, "--force")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), spawnEnv(a)...)
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		text := strings.TrimSpace(out.String())
		if text == "" {
			text = err.Error()
		}
		return msg.Err("sessions.deleteFailed", "id", id, "output", tail(text, 1500))
	}
	return nil
}
