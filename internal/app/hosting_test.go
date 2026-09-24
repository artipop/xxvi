package app

import (
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

	api := NewAPI(a)
	project, err := api.SaveProject(model.Project{
		Name: "Сайт", Path: folder, Remote: srv.URL + "/group/site.git", Provider: model.ProviderGitLab,
	})
	if err != nil {
		t.Fatalf("проект: %v", err)
	}
	return hosted{app: a, api: api, srv: srv, project: project, folder: folder, bare: bare, repo: "group/site"}
}

func (h hosted) connect(t *testing.T) {
	t.Helper()
	if _, err := h.api.ConnectHosting(h.project.ID, h.srv.Token); err != nil {
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

func TestAProjectKnowsWhereItPushes(t *testing.T) {
	a := open(t)
	api := NewAPI(a)
	folder := t.TempDir()
	gitIn(t, folder, "init", "-q")
	gitIn(t, folder, "remote", "add", "origin", "git@gitlab.company.ru:team/backend/api.git")

	p, err := api.SaveProject(model.Project{Name: "API", Path: folder})
	if err != nil {
		t.Fatalf("проект: %v", err)
	}
	if p.Remote != "git@gitlab.company.ru:team/backend/api.git" || p.Provider != model.ProviderGitLab {
		t.Fatalf("remote и хостинг должны прочитаться из origin: %+v", p)
	}
	if p.Server != "https://gitlab.company.ru" || p.Repository != "team/backend/api" {
		t.Fatalf("сервер и репозиторий: %+v", p)
	}
}

func TestATokenIsKeptOnlyWhenTheServerTakesIt(t *testing.T) {
	h := newHosted(t)
	if _, err := h.api.ConnectHosting(h.project.ID, "wrong"); !msg.Is(err, "hosting.unauthorized") {
		t.Fatalf("чужой токен должен быть отвергнут, получено %v", err)
	}
	h.connect(t)
	list, _ := h.api.Projects()
	if len(list) != 1 || list[0].Account != "me" {
		t.Fatalf("после подключения проект знает, кто мы: %+v", list)
	}
	if _, err := h.api.DisconnectHosting(h.project.ID); err != nil {
		t.Fatalf("отключить: %v", err)
	}
	list, _ = h.api.Projects()
	if list[0].Account != "" {
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
		},
		Edges: []model.Edge{
			{From: "p-work", To: "p-mr", On: model.TriggerCardChanged,
				If: &model.Cond{Property: model.OutcomeProperty, Value: model.OutcomePassed}},
			{From: "p-mr", To: "p-wait", On: model.TriggerSuccess},
			{From: "p-mr", To: "p-stuck", On: model.TriggerFailure},
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
	if _, err := h.api.SetCardProject(card.ID, h.project.ID); err != nil {
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
}
