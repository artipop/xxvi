package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/artipop/xxvi/internal/model"
)

// A card's workspace in a repository: what its work mode (model/workmode.go)
// comes to on disk. Made the first time anything asks where the card works —
// a stage, the terminal beside it, the notes — because all of them must get
// the same answer, and the first to ask is not always the stage.
//
// What was made is written onto the card (branch, base, worktree) and from
// then on is the fact: a later stage, a restart, a person reopening the
// terminal all come back to the same branch. A working tree whose directory
// has gone is remade from the branch, since the branch is where the work is.

// claimMu serializes claims: two stages of one card starting together must not
// make two branches, and git's own index lock is not something to race on.
var claimMu sync.Mutex

// gitTimeout bounds one git command. Everything asked of git here is local.
const gitTimeout = 30 * time.Second

// claimWorkspace is the directory a card in a repository works in, making its
// branch and working tree the first time.
func (m *Manager) claimWorkspace(card model.Card, project model.Project) (string, error) {
	claimMu.Lock()
	defer claimMu.Unlock()

	// Read again under the lock: whoever held it may have just made this
	// card's branch.
	fresh, err := m.store.Card(card.ID)
	if err != nil {
		return "", err
	}
	card = fresh
	folder := project.Path
	if !isRepo(folder) {
		return "", fmt.Errorf("папка проекта «%s» — не git-репозиторий: «%s» для неё невозможно, выберите «%s»",
			project.Name, model.WorkModeLabel(card.WorkMode), model.WorkModeLabel(model.WorkModeFolder))
	}

	switch card.WorkMode {
	case model.WorkModeWorktree:
		return m.claimWorktree(card, project)
	case model.WorkModeBranch:
		return m.claimBranch(card, project)
	}
	return folder, nil
}

func (m *Manager) claimWorktree(card model.Card, project model.Project) (string, error) {
	folder := project.Path
	if card.Branch != "" && card.Worktree != "" {
		if info, err := os.Stat(card.Worktree); err == nil && info.IsDir() {
			return card.Worktree, nil
		}
		// The directory is gone; the branch is not. One git command puts it
		// back, and prune first, or git remembers the old one and refuses.
		_, _ = git(folder, "worktree", "prune")
		if _, err := git(folder, "worktree", "add", card.Worktree, card.Branch); err != nil {
			return "", fmt.Errorf("не удалось вернуть рабочее дерево карточки: %w", err)
		}
		m.note(card.ID, fmt.Sprintf("Рабочее дерево карточки восстановлено из ветки `%s`: `%s`.", card.Branch, card.Worktree))
		return card.Worktree, nil
	}

	branch := card.Branch
	if branch == "" {
		branch = CardBranch(card.Title, card.ID)
	}
	base := baseBranch(folder)
	path := filepath.Join(m.worktreeRoot(), fmt.Sprintf("%s-%s", filepath.Base(folder), shortID(card.ID)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("создать папку для рабочих деревьев: %w", err)
	}
	args := []string{"worktree", "add", "-b", branch, path, base}
	if branchExists(folder, branch) {
		args = []string{"worktree", "add", path, branch}
	}
	if _, err := git(folder, args...); err != nil {
		return "", fmt.Errorf("не удалось создать рабочее дерево: %w", err)
	}
	if err := m.store.SetCardWorkspace(card.ID, branch, base, path); err != nil {
		return "", err
	}
	m.note(card.ID, fmt.Sprintf("Карточка работает в отдельном рабочем дереве `%s`, на ветке `%s` от `%s`.", path, branch, base))
	return path, nil
}

func (m *Manager) claimBranch(card model.Card, project model.Project) (string, error) {
	folder := project.Path
	holder, held, err := m.store.FolderHolder(project.ID, card.ID)
	if err != nil {
		return "", err
	}
	if held {
		// Says what will end it, because that is the one thing a person
		// reading it can act on.
		return "", fmt.Errorf("папка «%s» занята карточкой «%s» (ветка `%s`) — освободится, когда та будет готова или отброшена; или выберите для этой карточки отдельное рабочее дерево",
			project.Name, holder.Title, holder.Branch)
	}

	branch := card.Branch
	if branch == "" {
		branch = CardBranch(card.Title, card.ID)
	}
	if current, _ := git(folder, "rev-parse", "--abbrev-ref", "HEAD"); current == branch {
		return folder, nil
	}
	// Switching moves the person's own checkout, so never under their
	// unsaved work. Untracked files are not that: git carries them across a
	// switch untouched, and git itself refuses the one case they break.
	if dirty, err := git(folder, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return "", err
	} else if dirty != "" {
		return "", fmt.Errorf("в папке «%s» есть незакоммиченные изменения — переключить её на ветку карточки нельзя, не рискуя ими; закоммитьте или спрячьте их (git stash), или выберите отдельное рабочее дерево", project.Name)
	}

	base := card.Base
	if branchExists(folder, branch) {
		if _, err := git(folder, "switch", branch); err != nil {
			return "", fmt.Errorf("не удалось переключиться на ветку карточки: %w", err)
		}
	} else {
		base = baseBranch(folder)
		if _, err := git(folder, "switch", "-c", branch, base); err != nil {
			return "", fmt.Errorf("не удалось создать ветку карточки: %w", err)
		}
	}
	if card.Branch == "" {
		if err := m.store.SetCardWorkspace(card.ID, branch, base, ""); err != nil {
			return "", err
		}
		m.note(card.ID, fmt.Sprintf("Папка `%s` переключена на ветку карточки `%s` от `%s`.", folder, branch, base))
	} else {
		m.note(card.ID, fmt.Sprintf("Папка `%s` снова переключена на ветку карточки `%s`.", folder, branch))
	}
	return folder, nil
}

// worktreeRoot is where separate working trees live: beside the cards' own
// folders, under the application's data directory rather than inside the
// repository, where they would show up as untracked files.
func (m *Manager) worktreeRoot() string {
	base := m.opts.WorkDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "xxvi-work")
	}
	return filepath.Join(base, "trees")
}

// note writes a journal line that no run is behind: the workspace is the
// card's, not any one stage's.
func (m *Manager) note(cardID, text string) {
	if _, err := m.store.Record(model.JournalEntry{CardID: cardID, Kind: model.EntryMove, Text: text}); err != nil {
		m.log.Warn("не удалось записать в журнал карточки", "card", cardID, "err", err)
	}
}

// IsRepo says whether a folder is inside a git repository. Asked when it
// matters rather than stored on the project: a folder can become one later.
func IsRepo(folder string) bool { return isRepo(folder) }

func isRepo(folder string) bool {
	if strings.TrimSpace(folder) == "" {
		return false
	}
	_, err := git(folder, "rev-parse", "--git-dir")
	return err == nil
}

// baseBranch is what a card's branch is cut from: the remote's main branch if
// it has told us one, since that is where the work will land, else whatever is
// checked out. A literal "main" would be wrong for half the repositories there
// are.
func baseBranch(folder string) string {
	if out, err := git(folder, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimPrefix(out, "origin/"); b != "" && branchExists(folder, b) {
			return b
		}
	}
	if out, err := git(folder, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && out != "HEAD" {
		return out
	}
	return "HEAD"
}

func branchExists(folder, branch string) bool {
	_, err := git(folder, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func git(folder string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", folder}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CardBranch names a card's branch after its title, so `git branch` reads as a
// list of tasks: «Почини логин» → pochini-login-1a2b3c4d. The card's short id
// ends it, so two cards with one title never share a branch.
func CardBranch(title, cardID string) string {
	slug := slugify(title, 40)
	if slug == "" {
		return "card-" + shortID(cardID)
	}
	return slug + "-" + shortID(cardID)
}

// slugify folds a title into what a branch name may carry: lowercase Latin,
// digits and single dashes, with Cyrillic transliterated — the titles here
// are mostly Russian, and a branch is there to be read.
func slugify(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if t, ok := translit[r]; ok {
			if t != "" {
				b.WriteString(t)
				dash = false
			}
			continue
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > max {
		out = strings.Trim(out[:max], "-")
	}
	return out
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu",
	'я': "ya",
}

func shortID(id string) string {
	id = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return -1
	}, id)
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "x"
	}
	return id
}
