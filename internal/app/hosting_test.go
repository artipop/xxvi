package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artipop/xxvi/internal/hosting"
	"github.com/artipop/xxvi/internal/hosting/gitlabtest"
	"github.com/artipop/xxvi/internal/model"
	"github.com/artipop/xxvi/internal/msg"
)

// hosted is a project whose origin is a bare repository on disk — what git
// pushes to and fetches from — while the hosting's API is the fake GitLab.
// The project's remote names the fake, which is what the application reads it
// by; git itself never goes there.
type hosted struct {
	app     *App
	api     *API
	srv     *gitlabtest.Server
	project model.Project
	folder  string
	bare    string
	repo    string
}

func newHosted(t *testing.T) hosted {
	t.Helper()
	a := open(t)
	a.Hosting.UseSecrets(&hosting.Memory{})
	srv := gitlabtest.New()
	t.Cleanup(srv.Close)

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	folder := filepath.Join(root, "work")
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", bare)
	gitIn(t, root, "clone", "-q", bare, folder)
	gitIn(t, folder, "config", "user.email", "t@example.com")
	gitIn(t, folder, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(folder, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, folder, "add", ".")
	gitIn(t, folder, "commit", "-q", "-m", "init")
	gitIn(t, folder, "push", "-q", "origin", "main")
	gitIn(t, folder, "remote", "set-head", "origin", "main")
	// origin names the fake server, and git is told to go to the bare
	// repository instead — the way a person rewrites a remote to a mirror.
	address := srv.URL + "/group/site.git"
	gitIn(t, folder, "remote", "set-url", "origin", address)
	gitIn(t, folder, "config", "url."+bare+".insteadOf", address)

	api := NewAPI(a)
	project, err := api.SaveProject(model.Project{Name: "Сайт", Path: folder})
	if err != nil {
		t.Fatalf("проект: %v", err)
	}
	return hosted{app: a, api: api, srv: srv, project: project, folder: folder, bare: bare, repo: "group/site"}
}

func (h hosted) connect(t *testing.T) {
	t.Helper()
	if _, err := h.api.ConnectHosting(h.project.ID, model.ProviderGitLab, "origin", h.srv.URL, h.srv.Token); err != nil {
		t.Fatalf("подключить: %v", err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Connecting offers the repository's own remotes rather than guessing one:
// origin may be a fork, and a server's name says nothing about what it runs.
func TestConnectingOffersTheRepositorysRemotes(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	folder := t.TempDir()
	gitIn(t, folder, "init", "-q")
	gitIn(t, folder, "remote", "add", "upstream", "ssh://git@git.company.ru:2222/team/backend/api.git")
	gitIn(t, folder, "remote", "add", "origin", "git@git.company.ru:me/api.git")

	p, err := api.SaveProject(model.Project{Name: "API", Path: folder})
	if err != nil {
		t.Fatalf("проект: %v", err)
	}
	if p.Remote != "" || p.Provider != "" {
		t.Fatalf("без подключения хостинга у проекта нет: %+v", p)
	}
	remotes, err := api.HostingRemotes(p.ID)
	if err != nil || len(remotes) != 2 {
		t.Fatalf("remote репозитория: %+v, %v", remotes, err)
	}
	if remotes[0].Name != "origin" || remotes[1].Name != "upstream" {
		t.Fatalf("origin первым: %+v", remotes)
	}
	if remotes[1].Server != "https://git.company.ru" || remotes[1].Repository != "team/backend/api" {
		t.Fatalf("сервер и репозиторий из адреса: %+v", remotes[1])
	}
}

func TestATokenIsKeptOnlyWhenTheServerTakesIt(t *testing.T) {
	h := newHosted(t)
	if _, err := h.api.ConnectHosting(h.project.ID, model.ProviderGitLab, "origin", h.srv.URL, "wrong"); !msg.Is(err, "hosting.unauthorized") {
		t.Fatalf("чужой токен должен быть отвергнут, получено %v", err)
	}
	if list, _ := h.api.Projects(); list[0].Provider != "" {
		t.Fatalf("отвергнутый токен ничего не подключает: %+v", list[0])
	}
	h.connect(t)
	list, _ := h.api.Projects()
	if len(list) != 1 || list[0].Account != "me" || list[0].Repository != "group/site" || list[0].Remote != "origin" {
		t.Fatalf("после подключения проект знает, кто мы: %+v", list)
	}
	if _, err := h.api.DisconnectHosting(h.project.ID); err != nil {
		t.Fatalf("отключить: %v", err)
	}
	list, _ = h.api.Projects()
	if list[0].Account != "" || list[0].Server != "" {
		t.Fatalf("после отключения аккаунта нет: %+v", list[0])
	}
}

// publishFlow: a person marks the work done, the application publishes it,
// and the card waits. Nothing here runs an agent, so the test walks it alone.
func publishFlow() model.Flow {
	return model.Flow{
		Name: "Публикация", EntryStage: "p-work",
		Stages: []model.Stage{
			{ID: "p-work", Name: "Работа", Action: model.ActionNone},
			{ID: "p-mr", Name: "MR", Action: model.ActionPublish},
			{ID: "p-wait", Name: "Ждёт", Action: model.ActionNone},
			{ID: "p-stuck", Name: "Не вышло", Action: model.ActionNone},
			{ID: "p-done", Name: "Влит", Final: true},
		},
		Edges: []model.Edge{
			{From: "p-work", To: "p-mr", On: model.TriggerCardChanged,
				If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomePassed}},
			{From: "p-mr", To: "p-wait", On: model.TriggerSuccess},
			{From: "p-mr", To: "p-stuck", On: model.TriggerFailure},
			{From: "p-wait", To: "p-done", On: model.TriggerMRMerged},
		},
	}
}

// waitStage waits for a card to leave a stage — a hosting step works in the
// background — and returns where it went.
func waitStage(t *testing.T, a *App, cardID, from string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, ok, _ := a.Store.FlowState(cardID)
		if ok && st.StageID != from {
			return st.StageID
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("карточка так и стоит на %q", from)
	return ""
}

func TestPublishPushesAndOpensOneMR(t *testing.T) {
	h := newHosted(t)
	h.connect(t)
	flow, err := h.app.Store.SaveFlow(publishFlow())
	if err != nil {
		t.Fatalf("флоу: %v", err)
	}
	card, _ := h.api.AddCard("", "Починить форму", "Форма входа падает на пустом пароле.")
	if _, err := h.api.SetCardProject(card.ID, h.project.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.SetCardWorkMode(card.ID, model.WorkModeWorktree); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.TakeIntoWork(card.ID, flow.ID); err != nil {
		t.Fatal(err)
	}
	dir, err := h.app.Agents.WorkDir(card.ID)
	if err != nil {
		t.Fatalf("рабочая папка: %v", err)
	}

	// Uncommitted work is not published: what was reviewed was the tree.
	if err := os.WriteFile(filepath.Join(dir, "form.go"), []byte("package form\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.MarkOutcome(card.ID, model.OutcomePassed, ""); err != nil {
		t.Fatal(err)
	}
	if got := waitStage(t, h.app, card.ID, "p-mr"); got != "p-stuck" {
		t.Fatalf("с незакоммиченным публикация не проходит, а карточка на %q", got)
	}
	if len(h.srv.All()) != 0 {
		t.Fatal("MR открыт, хотя публиковать было нельзя")
	}

	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "форма")
	if _, err := h.api.MoveTo(card.ID, "p-mr"); err != nil {
		t.Fatal(err)
	}
	if got := waitStage(t, h.app, card.ID, "p-mr"); got != "p-wait" {
		t.Fatalf("после коммита публикация проходит, а карточка на %q", got)
	}
	fresh, _ := h.app.Store.Card(card.ID)
	mrs := h.srv.All()
	if len(mrs) != 1 || mrs[0].Source != fresh.Branch || mrs[0].Target != "main" || mrs[0].Title != "Починить форму" {
		t.Fatalf("MR: %+v", mrs)
	}
	if !strings.Contains(mrs[0].Body, "пустом пароле") {
		t.Fatalf("описание MR — текст карточки: %q", mrs[0].Body)
	}
	if fresh.Prop(model.MRProperty) == "" {
		t.Fatal("адрес MR должен лечь на карточку")
	}
	if got := gitIn(t, h.bare, "rev-parse", fresh.Branch); got != gitIn(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("ветка не запушена: %s", got)
	}

	// Publishing again updates the same MR rather than opening a second one.
	if _, err := h.api.MoveTo(card.ID, "p-mr"); err != nil {
		t.Fatal(err)
	}
	waitStage(t, h.app, card.ID, "p-mr")
	if n := len(h.srv.All()); n != 1 {
		t.Fatalf("второй публикацией открыт второй MR: %d", n)
	}

	// Still open: asking changes nothing.
	h.app.Hosting.Poll()
	if st, _, _ := h.app.Store.FlowState(card.ID); st.StageID != "p-wait" {
		t.Fatalf("MR открыт — карточка ждёт, а она на %q", st.StageID)
	}
	h.srv.Change(h.repo, mrs[0].IID, func(m *gitlabtest.MR) { m.State = "merged" })
	h.app.Hosting.Poll()
	if c, _ := h.app.Store.Card(card.ID); c.State != model.StateDone {
		t.Fatalf("влитый MR закрывает карточку, а она %s", c.State)
	}
}

// theirCommit is somebody else's work on a branch of origin, published the way
// GitLab publishes an MR's head: under refs/merge-requests/<iid>/head.
func (h hosted) theirCommit(t *testing.T, branch, file, text string, iid int) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "them")
	gitIn(t, filepath.Dir(other), "clone", "-q", h.bare, other)
	if gitIn(t, other, "ls-remote", "--heads", "origin", branch) != "" {
		gitIn(t, other, "checkout", "-q", branch)
	} else {
		gitIn(t, other, "checkout", "-q", "-b", branch)
	}
	if err := os.WriteFile(filepath.Join(other, file), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", ".")
	gitIn(t, other, "commit", "-q", "-m", file)
	gitIn(t, other, "push", "-q", "origin", branch)
	sha := gitIn(t, other, "rev-parse", "HEAD")
	gitIn(t, h.bare, "update-ref", fmt.Sprintf("refs/merge-requests/%d/head", iid), sha)
	return sha
}

func (h hosted) stage(t *testing.T, cardID string) string {
	t.Helper()
	st, _, _ := h.app.Store.FlowState(cardID)
	return st.StageID
}

func (h hosted) mark(t *testing.T, cardID, value, remarks, from string) string {
	t.Helper()
	if _, err := h.api.MarkOutcome(cardID, value, remarks); err != nil {
		t.Fatalf("отметить %s: %v", value, err)
	}
	return waitStage(t, h.app, cardID, from)
}

func TestAReviewAssignedToMeWalksToTheMerge(t *testing.T) {
	h := newHosted(t)
	h.connect(t)
	sha := h.theirCommit(t, "their-feature", "feature.go", "package feature\n", 1)
	h.srv.Add(gitlabtest.MR{
		Repo: h.repo, IID: 1, Title: "Их фича", Body: "Посмотрите, пожалуйста",
		Source: "their-feature", Target: "main", SHA: sha, Author: "colleague", Reviewers: []string{"me"},
	})
	h.srv.Add(gitlabtest.MR{Repo: h.repo, IID: 2, Title: "Не мне", Source: "x", Target: "main", SHA: "x", Reviewers: []string{"other"}})

	if _, err := h.api.SetReviewInbox(h.project.ID, true); err != nil {
		t.Fatalf("включить ревью во входящие: %v", err)
	}
	h.app.Hosting.Poll()
	groups, _ := h.api.Inbox()
	if len(groups) != 1 || len(groups[0].Cards) != 1 || groups[0].Plugin != "review" {
		t.Fatalf("во входящих одно ревью, назначенное на меня: %+v", groups)
	}
	card := groups[0].Cards[0]
	if card.Project != h.project.ID || card.WorkMode != model.WorkModeReview || card.Branch != "their-feature" || card.Base != "origin/main" {
		t.Fatalf("карточка ревью знает, где и что смотреть: %+v", card)
	}
	if card.Prop("Flow") != "MR review" {
		t.Fatalf("предложен флоу ревью: %q", card.Prop("Flow"))
	}

	// Polled again, nothing new: still one card.
	h.app.Hosting.Poll()
	if groups, _ := h.api.Inbox(); len(groups[0].Cards) != 1 {
		t.Fatal("повторный опрос не должен заводить вторую карточку")
	}

	flow := mustFlow(t, h.app, "MR review")
	if _, err := h.api.TakeIntoWork(card.ID, flow.ID); err != nil {
		t.Fatal(err)
	}
	diff, err := h.api.Diff(card.ID, "")
	if err != nil {
		t.Fatalf("дифф: %v", err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "feature.go" || diff.Branch != "their-feature" {
		t.Fatalf("дифф ревью — работа MR против main: %+v", diff)
	}

	if got := h.mark(t, card.ID, model.OutcomePassed, "", "rmr-review"); got != "rmr-try" {
		t.Fatalf("после ревью — проверка, а карточка на %q", got)
	}
	if got := h.mark(t, card.ID, model.OutcomePassed, "", "rmr-try"); got != "rmr-approve" {
		t.Fatalf("после проверки — одобрение, а карточка на %q", got)
	}
	if got := waitStage(t, h.app, card.ID, "rmr-approve"); got != "rmr-wait" {
		t.Fatalf("одобрение ушло — ждём автора, а карточка на %q", got)
	}
	if mr, _ := h.srv.Get(h.repo, 1); len(mr.Approved) != 1 {
		t.Fatalf("MR одобрен: %+v", mr)
	}

	// The author pushes again: the card comes back to the review, on the new
	// head, and the diff says so.
	newSHA := h.theirCommit(t, "their-feature", "more.go", "package feature\n", 1)
	h.srv.Change(h.repo, 1, func(m *gitlabtest.MR) { m.SHA = newSHA; m.Approved = nil })
	h.app.Hosting.Poll()
	if got := h.stage(t, card.ID); got != "rmr-review" {
		t.Fatalf("новые коммиты возвращают на ревью, а карточка на %q", got)
	}
	dir, _ := h.app.Agents.WorkDir(card.ID)
	if head := gitIn(t, dir, "rev-parse", "HEAD"); head != newSHA {
		t.Fatalf("дерево ревью должно переехать на новую голову: %s", head)
	}

	if got := h.mark(t, card.ID, model.OutcomeFailed, "Нет тестов на more.go", "rmr-review"); got != "rmr-changes" {
		t.Fatalf("«не прошло» — замечания, а карточка на %q", got)
	}
	waitStage(t, h.app, card.ID, "rmr-changes")
	if mr, _ := h.srv.Get(h.repo, 1); len(mr.Notes) != 1 || mr.Notes[0] != "Нет тестов на more.go" {
		t.Fatalf("замечания ушли в MR: %+v", mr.Notes)
	}

	h.srv.Change(h.repo, 1, func(m *gitlabtest.MR) { m.State = "merged" })
	h.app.Hosting.Poll()
	if c, _ := h.app.Store.Card(card.ID); c.State != model.StateDone {
		t.Fatalf("влитый MR закрывает ревью, а карточка %s", c.State)
	}
}

// An MR merged or closed before anybody took it has nothing left to review.
func TestAnUntakenReviewOfAClosedMRLeavesTheInbox(t *testing.T) {
	h := newHosted(t)
	h.connect(t)
	sha := h.theirCommit(t, "f", "f.go", "package f\n", 1)
	h.srv.Add(gitlabtest.MR{Repo: h.repo, IID: 1, Title: "Их", Source: "f", Target: "main", SHA: sha, Reviewers: []string{"me"}})
	if _, err := h.api.SetReviewInbox(h.project.ID, true); err != nil {
		t.Fatal(err)
	}
	h.app.Hosting.Poll()
	h.srv.Change(h.repo, 1, func(m *gitlabtest.MR) { m.State = "closed" })
	h.app.Hosting.Poll()
	if groups, _ := h.api.Inbox(); len(groups) != 0 {
		t.Fatalf("закрытый MR уходит из входящих: %+v", groups)
	}
}
