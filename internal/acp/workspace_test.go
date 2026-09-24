package acp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/store"
)

// newRepo is a repository with one commit on main.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не установлен")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "start"},
	} {
		if _, err := git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newWorkspaceManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := New(st, nil, nil, Options{WorkDir: t.TempDir()}, nil)
	t.Cleanup(m.Close)
	return m, st
}

func cardIn(t *testing.T, st *store.Store, title string, project model.Project, mode string) model.Card {
	t.Helper()
	c, err := st.CreateCard(model.Card{Title: title, State: model.StateFlow, Project: project.ID, WorkMode: mode})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func projectAt(t *testing.T, st *store.Store, path string) model.Project {
	t.Helper()
	p, err := st.SaveProject(model.Project{Name: filepath.Base(path), Path: path})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFolderModeWorksInTheFolderAsItStands(t *testing.T) {
	m, st := newWorkspaceManager(t)
	repo := newRepo(t)
	c := cardIn(t, st, "Как есть", projectAt(t, st, repo), model.WorkModeFolder)

	dir, err := m.WorkDir(c.ID)
	if err != nil || dir != repo {
		t.Fatalf("карточка без своей ветки работает в самой папке: %q, %v", dir, err)
	}
	if head, _ := git(repo, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
		t.Fatalf("папку никто не переключал, а она на %q", head)
	}
}

// Two cards of one repository at once, and the person's own checkout never
// moves: that is what a separate working tree is for.
func TestWorktreeModeGivesEachCardItsOwnTree(t *testing.T) {
	m, st := newWorkspaceManager(t)
	repo := newRepo(t)
	p := projectAt(t, st, repo)
	a := cardIn(t, st, "Почини логин", p, model.WorkModeWorktree)
	b := cardIn(t, st, "Почини логин", p, model.WorkModeWorktree)

	dirA, err := m.WorkDir(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	dirB, err := m.WorkDir(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dirA == repo || dirA == dirB {
		t.Fatalf("у каждой карточки своё дерево, не папка: %q и %q", dirA, dirB)
	}
	if head, _ := git(repo, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
		t.Fatalf("своя папка человека не должна переключаться, а она на %q", head)
	}

	a, _ = st.Card(a.ID)
	if !strings.HasPrefix(a.Branch, "pochini-login-") || a.Base != "main" || a.Worktree != dirA {
		t.Fatalf("на карточке записано не то: %+v", a)
	}
	if head, _ := git(dirA, "rev-parse", "--abbrev-ref", "HEAD"); head != a.Branch {
		t.Fatalf("дерево стоит на %q, а ветка карточки %q", head, a.Branch)
	}

	// The next ask is the same tree, and a tree that went is put back from
	// the branch — the work is there.
	if again, _ := m.WorkDir(a.ID); again != dirA {
		t.Fatalf("второй раз — то же дерево, а не %q", again)
	}
	if _, err := git(repo, "worktree", "remove", "--force", dirA); err != nil {
		t.Fatal(err)
	}
	if back, err := m.WorkDir(a.ID); err != nil || back != dirA {
		t.Fatalf("пропавшее дерево возвращается из ветки: %q, %v", back, err)
	}
}

// One card at a time in the folder itself, and never over somebody's unsaved
// work.
func TestBranchModeSwitchesTheFolderForOneCardAtATime(t *testing.T) {
	m, st := newWorkspaceManager(t)
	repo := newRepo(t)
	p := projectAt(t, st, repo)
	a := cardIn(t, st, "Первая", p, model.WorkModeBranch)
	b := cardIn(t, st, "Вторая", p, model.WorkModeBranch)

	dir, err := m.WorkDir(a.ID)
	if err != nil || dir != repo {
		t.Fatalf("ветка в этой же папке — это сама папка: %q, %v", dir, err)
	}
	a, _ = st.Card(a.ID)
	if head, _ := git(repo, "rev-parse", "--abbrev-ref", "HEAD"); head != a.Branch || a.Worktree != "" {
		t.Fatalf("папка на %q, карточка %+v", head, a)
	}

	if _, err := m.WorkDir(b.ID); err == nil || !msg.Is(err, "branch.folderTaken") {
		t.Fatalf("вторая карточка ждёт, пока папка занята: %v", err)
	}

	// The first is done: the folder is free, and it is not switched while
	// something tracked in it is uncommitted.
	done := model.StateDone
	if _, err := st.UpdateCard(a.ID, store.CardEdit{State: &done}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = git(repo, "add", "f.txt")
	if _, err := m.WorkDir(b.ID); err == nil || !msg.Is(err, "branch.folderDirty") {
		t.Fatalf("под несохранённой работой папку не переключают: %v", err)
	}
	_, _ = git(repo, "commit", "-q", "-m", "f")
	if _, err := m.WorkDir(b.ID); err != nil {
		t.Fatalf("после первой папка свободна: %v", err)
	}
}

func TestBranchIsNamedAfterTheTitle(t *testing.T) {
	cases := map[string]string{
		"Почини логин":            "pochini-login-abcdef12",
		"Fix SSO: login (again)!": "fix-sso-login-again-abcdef12",
		"   ":                     "card-abcdef12",
		"Объединить щёки и ёжиков": "obedinit-scheki-i-ezhikov-abcdef12",
	}
	for title, want := range cases {
		if got := CardBranch(title, "abcdef12-3456"); got != want {
			t.Errorf("%q → %q, ожидалось %q", title, got, want)
		}
	}
}

// A closed card's tree is asked about, not removed: «оставить» is remembered,
// «удалить» keeps the branch, and uncommitted work goes only when the person
// was told about it.
func TestClosedCardsTreeIsRemovedOnlyWhenAsked(t *testing.T) {
	m, st := newWorkspaceManager(t)
	repo := newRepo(t)
	p := projectAt(t, st, repo)
	a := cardIn(t, st, "Первая", p, model.WorkModeWorktree)
	b := cardIn(t, st, "Вторая", p, model.WorkModeWorktree)
	dirA, _ := m.WorkDir(a.ID)
	dirB, _ := m.WorkDir(b.ID)

	asked := func() map[string]Attention {
		out := map[string]Attention{}
		for _, at := range m.Attention() {
			if at.Worktree != "" {
				out[at.CardID] = at
			}
		}
		return out
	}
	if len(asked()) != 0 {
		t.Fatal("пока задачи в работе, об их деревьях не спрашивают")
	}
	if err := m.RemoveWorktree(a.ID, true); err == nil {
		t.Fatal("дерево задачи в работе не удаляется")
	}

	done, dropped := model.StateDone, model.StateDropped
	st.UpdateCard(a.ID, store.CardEdit{State: &done})
	st.UpdateCard(b.ID, store.CardEdit{State: &dropped})
	if err := os.WriteFile(filepath.Join(dirA, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := asked()
	if len(got) != 2 || !got[a.ID].Dirty || got[b.ID].Dirty {
		t.Fatalf("спрашивают о двух, и про незакоммиченное говорят: %+v", got)
	}

	// Not told about the changes — refused, and the tree is there.
	if err := m.RemoveWorktree(a.ID, false); err == nil {
		t.Fatal("незакоммиченное без предупреждения не выбрасывают")
	}
	if _, err := os.Stat(dirA); err != nil {
		t.Fatal("дерево на месте после отказа")
	}
	if err := m.RemoveWorktree(a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dirA); !os.IsNotExist(err) {
		t.Fatal("дерево удалено")
	}
	a, _ = st.Card(a.ID)
	if a.Worktree != "" || !branchExists(repo, a.Branch) {
		t.Fatalf("ветка остаётся, карточка на дерево больше не указывает: %+v", a)
	}

	if err := m.KeepWorktree(b.ID); err != nil {
		t.Fatal(err)
	}
	if len(asked()) != 0 {
		t.Fatalf("«оставить» — тоже ответ: %+v", asked())
	}
	if _, err := os.Stat(dirB); err != nil {
		t.Fatal("оставленное дерево на месте")
	}

	// Reopened, the removed card gets its tree back on the same branch.
	flow := model.StateFlow
	st.UpdateCard(a.ID, store.CardEdit{State: &flow})
	back, err := m.WorkDir(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if head, _ := git(back, "rev-parse", "--abbrev-ref", "HEAD"); head != a.Branch {
		t.Fatalf("вернувшееся дерево на ветке карточки, а не на %q", head)
	}
}
