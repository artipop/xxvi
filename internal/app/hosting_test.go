package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
