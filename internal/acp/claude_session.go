package acp

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
)

// Claude keeps a conversation as projects/<folder>/<id>.jsonl, and its
// subagents' transcripts in projects/<folder>/<id>/. There is no CLI command to
// delete or rename one outside the conversation: claude-agent-acp exposes
// rename only to JetBrains AIR clients, and its session/delete only removes the
// same files. So the files are changed here the way the Agent SDK's
// deleteSession and renameSession change them.

var errClaudeSessionMissing = errors.New("conversation file not found")

// claudeTranscript finds a conversation in every project folder. The id is a
// UUID, so it names one conversation wherever the CLI filed it; the folder name
// is Claude's own encoding of the cwd, which is not worth reproducing.
func claudeTranscript(a model.Agent, id string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(historyHome(a, "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")), "projects")
	folders, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errClaudeSessionMissing
		}
		return "", err
	}
	for _, folder := range folders {
		if !folder.IsDir() {
			continue
		}
		path := filepath.Join(root, folder.Name(), id+".jsonl")
		// An empty file is what the CLI leaves before the first message, and
		// the SDK skips it the same way.
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return path, nil
		}
	}
	return "", errClaudeSessionMissing
}

func deleteClaudeSession(a model.Agent, id string) error {
	path, err := claudeTranscript(a, id)
	if err == nil {
		err = os.Remove(path)
	}
	if err == nil {
		err = os.RemoveAll(filepath.Join(filepath.Dir(path), id))
	}
	if err != nil {
		return msg.Err("sessions.deleteFailed", "id", id, "output", err.Error())
	}
	return nil
}

func renameClaudeSession(a model.Agent, id, title string) error {
	failed := func(err error) error {
		return msg.Err("sessions.renameFailed", "id", id, "output", err.Error())
	}
	path, err := claudeTranscript(a, id)
	if err != nil {
		return failed(err)
	}
	// The CLI's /rename appends the same record; the newest one is the title
	// the list and the CLI show.
	line, err := json.Marshal(map[string]string{"type": "custom-title", "customTitle": title, "sessionId": id})
	if err != nil {
		return failed(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return failed(err)
	}
	_, err = file.Write(append(line, '\n'))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return failed(err)
	}
	return nil
}
