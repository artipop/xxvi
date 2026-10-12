package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

func RenamePastSession(ctx context.Context, a model.Agent, cwd, id, title string) (string, error) {
	if a.Kind != model.KindCodex {
		return "", msg.Err("sessions.notRenamable", "agent", a.Name)
	}
	conversation, err := uuid.Parse(id)
	if err != nil {
		return "", msg.Err("sessions.invalidID", "id", id)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", msg.Err("sessions.emptyTitle")
	}
	cli, _ := cliFor(a.Kind)
	bin, err := terminalBin(cli)
	if err != nil {
		return "", msg.Wrap(err, "terminal.binMissing", "bin", cli.cliBin)
	}
	proc, err := spawn(ctx, []string{bin, "app-server", "--listen", "stdio://"}, cwd, spawnEnv(a), cli.dropEnv...)
	if err != nil {
		return "", msg.Wrap(err, "agent.startFailed", "command", bin)
	}
	defer func() {
		proc.killGroup(0)
		_ = proc.wait()
	}()
	stop := context.AfterFunc(ctx, func() {
		_ = proc.stdout.Close()
		_ = proc.stdin.Close()
	})
	defer stop()

	encoder := json.NewEncoder(proc.stdin)
	decoder := json.NewDecoder(proc.stdout)
	seq := 0
	request := func(method string, params any) error {
		seq++
		if err := encoder.Encode(map[string]any{"id": seq, "method": method, "params": params}); err != nil {
			return err
		}
		for {
			var response struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := decoder.Decode(&response); err != nil {
				return err
			}
			if response.Method != "" || string(response.ID) != strconv.Itoa(seq) {
				continue
			}
			if response.Error != nil {
				return fmt.Errorf("%s (%d): %s", method, response.Error.Code, response.Error.Message)
			}
			if len(response.Result) == 0 {
				return fmt.Errorf("%s: missing result", method)
			}
			return nil
		}
	}
	failed := func(err error) (string, error) {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", msg.Err("sessions.renameFailed", "id", id, "output", tail(err.Error(), 1500))
	}
	if err := request("initialize", map[string]any{"clientInfo": map[string]string{"name": "xxvi", "version": "1"}}); err != nil {
		return failed(err)
	}
	if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return failed(err)
	}
	// ACP exposes /rename only as a prompt on a resumed session. The native
	// name setter updates stored metadata without loading a thread or a turn.
	if err := request("thread/name/set", map[string]string{"threadId": conversation.String(), "name": title}); err != nil {
		return failed(err)
	}
	return title, nil
}
