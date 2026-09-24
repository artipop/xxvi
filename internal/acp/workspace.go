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

	"github.com/artipop/xxvi/internal/hosting"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
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
		return "", msg.Err("workMode.notRepo", "project", project.Name, "mode", card.WorkMode)
	}

	switch card.WorkMode {
	case model.WorkModeWorktree:
		return m.claimWorktree(card, project)
	case model.WorkModeBranch:
		return m.claimBranch(card, project)
	case model.WorkModeReview:
		return m.claimReviewTree(card, project)
	}
	return folder, nil
}

// claimReviewTree is somebody else's MR in a working tree of its own, at the
// MR's last commit and on no branch: nothing here is going to be committed or
// pushed, and a local branch named like theirs would only be a second copy to
// fall out of date.
func (m *Manager) claimReviewTree(card model.Card, project model.Project) (string, error) {
	if card.Worktree != "" {
		if info, err := os.Stat(card.Worktree); err == nil && info.IsDir() {
			return card.Worktree, nil
		}
	}
	ref, err := fetchMR(card, project)
	if err != nil {
		return "", err
	}
	path := card.Worktree
	if path == "" {
		path = filepath.Join(m.worktreeRoot(), fmt.Sprintf("%s-%s", filepath.Base(project.Path), shortID(card.ID)))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create the working trees folder: %w", err)
	}
	_, _ = git(project.Path, "worktree", "prune")
	if _, err := git(project.Path, "worktree", "add", "--detach", path, ref); err != nil {
		return "", msg.Wrap(err, "worktree.createFailed")
	}
	if err := m.store.SetCardWorkspace(card.ID, card.Branch, card.Base, path); err != nil {
		return "", err
	}
	m.note(card.ID, msg.New("journal.reviewTreeCreated", "path", path, "branch", card.Branch))
	return path, nil
}

// RefreshReviewTree moves an MR's working tree to the MR's new last commit.
// Not over somebody's edits: a person trying the branch may have changed a
// file to see what happens, and that is theirs until they say otherwise — the
// tree stays where it is, and the journal says why.
func (m *Manager) RefreshReviewTree(cardID string) error {
	claimMu.Lock()
	defer claimMu.Unlock()
	card, err := m.store.Card(cardID)
	if err != nil {
		return err
	}
	if card.WorkMode != model.WorkModeReview || card.Worktree == "" {
		return nil
	}
	if info, err := os.Stat(card.Worktree); err != nil || !info.IsDir() {
		return nil
	}
	project, err := m.store.Project(card.Project)
	if err != nil {
		return err
	}
	ref, err := fetchMR(card, project)
	if err != nil {
		return err
	}
	if dirty, err := git(card.Worktree, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return err
	} else if dirty != "" {
		m.note(card.ID, msg.New("journal.reviewTreeDirty", "path", card.Worktree))
		return nil
	}
	if _, err := git(card.Worktree, "checkout", "-q", "--detach", ref); err != nil {
		return msg.Wrap(err, "worktree.refreshFailed")
	}
	return nil
}

// fetchMR brings the MR's head and its target branch from origin, and names
// the local ref the head is now under. The target too: the diff compares
// against where the MR branched from it, and a target last fetched a week ago
// would put a week of other people's work into the review.
func fetchMR(card model.Card, project model.Project) (string, error) {
	iid, ok := hosting.MRNumber(card.Prop(model.MRProperty))
	if !ok {
		return "", msg.Err("verdict.noMR")
	}
	remote := hosting.MRRef(project.Provider, iid)
	if remote == "" {
		return "", msg.Err("hosting.noProvider", "project", project.Name, "server", project.Remote)
	}
	local := fmt.Sprintf("refs/xxvi/mr/%d", iid)
	args := []string{"fetch", "-q", "origin", "+" + remote + ":" + local}
	if target := strings.TrimPrefix(card.Base, "origin/"); target != "" && target != card.Base {
		args = append(args, "+refs/heads/"+target+":refs/remotes/origin/"+target)
	}
	if _, err := gitNet(project.Path, args...); err != nil {
		return "", msg.Wrap(err, "review.fetchFailed")
	}
	return local, nil
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
			return "", msg.Wrap(err, "worktree.restoreFailed")
		}
		m.note(card.ID, msg.New("journal.worktreeRestored", "branch", card.Branch, "path", card.Worktree))
		return card.Worktree, nil
	}

	branch := card.Branch
	if branch == "" {
		branch = CardBranch(card.Title, card.ID)
	}
	base := baseBranch(folder)
	path := filepath.Join(m.worktreeRoot(), fmt.Sprintf("%s-%s", filepath.Base(folder), shortID(card.ID)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create the working trees folder: %w", err)
	}
	args := []string{"worktree", "add", "-b", branch, path, base}
	if branchExists(folder, branch) {
		args = []string{"worktree", "add", path, branch}
	}
	if _, err := git(folder, args...); err != nil {
		return "", msg.Wrap(err, "worktree.createFailed")
	}
	if err := m.store.SetCardWorkspace(card.ID, branch, base, path); err != nil {
		return "", err
	}
	m.note(card.ID, msg.New("journal.worktreeCreated", "path", path, "branch", branch, "base", base))
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
		return "", msg.Err("branch.folderTaken", "project", project.Name, "card", holder.Title, "branch", holder.Branch)
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
		return "", msg.Err("branch.folderDirty", "project", project.Name)
	}

	base := card.Base
	if branchExists(folder, branch) {
		if _, err := git(folder, "switch", branch); err != nil {
			return "", msg.Wrap(err, "branch.switchFailed")
		}
	} else {
		base = baseBranch(folder)
		if _, err := git(folder, "switch", "-c", branch, base); err != nil {
			return "", msg.Wrap(err, "branch.createFailed")
		}
	}
	if card.Branch == "" {
		if err := m.store.SetCardWorkspace(card.ID, branch, base, ""); err != nil {
			return "", err
		}
		m.note(card.ID, msg.New("journal.branchSwitched", "path", folder, "branch", branch, "base", base))
	} else {
		m.note(card.ID, msg.New("journal.branchSwitchedAgain", "path", folder, "branch", branch))
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
func (m *Manager) note(cardID string, what msg.Msg) {
	if _, err := m.store.Record(model.JournalEntry{CardID: cardID, Kind: model.EntryMove, Msg: &what}); err != nil {
		m.log.Warn("could not write the card journal", "card", cardID, "err", err)
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
	return gitFor(gitTimeout, folder, args...)
}

// gitNet is git that crosses the network, given longer and forbidden to ask
// for a password on a terminal nobody sees.
func gitNet(folder string, args ...string) (string, error) {
	return gitFor(3*time.Minute, folder, args...)
}

func gitFor(timeout time.Duration, folder string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", folder}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CardBranch names a card's branch after its title, so `git branch` reads as a
// list of tasks: «Fix the login» → fix-the-login-1a2b3c4d (Cyrillic transliterated). The card's short id
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

// ---- a closed card's tree ----

// worktreeAttention is a question per closed card whose separate working tree
// is still on disk: remove it, or keep it. Asked rather than done, because the
// tree is where a person may still want to look, and it may hold what nobody
// committed. Read off the cards every time rather than raised once, so it
// survives a restart and goes away by itself when the card is reopened.
func (m *Manager) worktreeAttention() []Attention {
	cards, err := m.store.ClosedWithWorktree()
	if err != nil {
		m.log.Warn("could not read closed cards with working trees", "err", err)
		return nil
	}
	var out []Attention
	for _, c := range cards {
		if info, err := os.Stat(c.Worktree); err != nil || !info.IsDir() {
			// Removed by hand: nothing to ask about, and the card should
			// stop pointing at it. The branch stays.
			if err := m.store.SetCardWorkspace(c.ID, c.Branch, c.Base, ""); err != nil {
				m.log.Warn("could not forget a vanished working tree", "card", c.ID, "err", err)
			}
			continue
		}
		status, _ := git(c.Worktree, "status", "--porcelain")
		out = append(out, Attention{
			Key: "w:" + c.ID, CardID: c.ID, CardTitle: c.Title,
			Worktree: c.Worktree, Branch: c.Branch, Dirty: status != "",
			Awaiting: true, Since: c.UpdatedAt,
		})
	}
	return out
}

// RemoveWorktree removes a closed card's working tree and keeps its branch:
// the committed work is on the branch, and reopening the card puts a tree back
// on it. Uncommitted changes are thrown away only when discard says the person
// was told about them — a tree that got dirty after the question was read is
// refused rather than cleaned.
func (m *Manager) RemoveWorktree(cardID string, discard bool) error {
	claimMu.Lock()
	defer claimMu.Unlock()

	card, err := m.store.Card(cardID)
	if err != nil {
		return err
	}
	if card.Worktree == "" {
		return nil
	}
	if card.State != model.StateDone && card.State != model.StateDropped {
		return msg.Err("worktree.cardInWork")
	}
	project, err := m.store.Project(card.Project)
	if err != nil {
		return err
	}
	if info, err := os.Stat(card.Worktree); err == nil && info.IsDir() {
		args := []string{"worktree", "remove", card.Worktree}
		if discard {
			args = []string{"worktree", "remove", "--force", card.Worktree}
		}
		if _, err := git(project.Path, args...); err != nil {
			if !discard && strings.Contains(err.Error(), "modified or untracked") {
				return msg.Err("worktree.becameDirty")
			}
			return msg.Wrap(err, "worktree.removeFailed")
		}
	}
	_, _ = git(project.Path, "worktree", "prune")
	if err := m.store.SetCardWorkspace(card.ID, card.Branch, card.Base, ""); err != nil {
		return err
	}
	m.note(card.ID, msg.New("journal.worktreeRemoved", "path", card.Worktree, "branch", card.Branch))
	m.emitAttention(Attention{Key: "w:" + card.ID, CardID: card.ID})
	return nil
}

// KeepWorktree is the other answer: the tree stays, and nobody asks again
// until the card is closed with a new one.
func (m *Manager) KeepWorktree(cardID string) error {
	if err := m.store.KeepWorktree(cardID); err != nil {
		return err
	}
	m.emitAttention(Attention{Key: "w:" + cardID, CardID: cardID})
	return nil
}
