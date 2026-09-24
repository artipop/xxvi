package hosting

import (
	"context"
	"testing"

	"github.com/artipop/xxvi/internal/hosting/gitlabtest"
	"github.com/artipop/xxvi/internal/msg"
)

func TestGitLabWalksAnMR(t *testing.T) {
	srv := gitlabtest.New()
	defer srv.Close()
	gl := NewGitLab(srv.URL, srv.Token, nil)
	ctx := context.Background()
	repo := "group/sub/repo"

	if me, err := gl.Me(ctx); err != nil || me != "me" {
		t.Fatalf("кто я: %q, %v", me, err)
	}
	if _, found, err := gl.OpenMR(ctx, repo, "feature"); err != nil || found {
		t.Fatalf("MR ещё нет, а найден (%v, %v)", found, err)
	}
	mr, err := gl.CreateMR(ctx, repo, NewMR{Source: "feature", Target: "main", Title: "Фича", Body: "текст"})
	if err != nil {
		t.Fatalf("создать MR: %v", err)
	}
	if mr.IID != 1 || mr.State != StateOpen || mr.URL == "" {
		t.Fatalf("созданный MR: %+v", mr)
	}
	found, ok, err := gl.OpenMR(ctx, repo, "feature")
	if err != nil || !ok || found.IID != mr.IID {
		t.Fatalf("MR из ветки не найден: %+v %v %v", found, ok, err)
	}
	if err := gl.UpdateMRBody(ctx, repo, mr.IID, "новый текст"); err != nil {
		t.Fatalf("обновить описание: %v", err)
	}
	if got, _ := srv.Get(repo, mr.IID); got.Body != "новый текст" {
		t.Fatalf("описание не обновилось: %q", got.Body)
	}
}

func TestGitLabReviewAndVerdict(t *testing.T) {
	srv := gitlabtest.New()
	defer srv.Close()
	gl := NewGitLab(srv.URL, srv.Token, nil)
	ctx := context.Background()
	srv.Add(gitlabtest.MR{Repo: "g/r", Title: "Чужой", Source: "их-ветка", Target: "main", SHA: "aaa", Reviewers: []string{"me"}})
	srv.Add(gitlabtest.MR{Repo: "g/r", Title: "Не мне", Source: "другая", Target: "main", SHA: "bbb", Reviewers: []string{"kto-to"}})

	queue, err := gl.ReviewQueue(ctx, "g/r", "me")
	if err != nil || len(queue) != 1 || queue[0].Title != "Чужой" || queue[0].Head != "aaa" {
		t.Fatalf("очередь ревью: %+v, %v", queue, err)
	}

	// The author pushed after the review: approving the old head is refused.
	srv.Change("g/r", 1, func(m *gitlabtest.MR) { m.SHA = "ccc" })
	if err := gl.Approve(ctx, "g/r", 1, "aaa"); !msg.Is(err, "hosting.headMoved") {
		t.Fatalf("одобрение устаревшей головы должно отказывать, получено %v", err)
	}
	if err := gl.Approve(ctx, "g/r", 1, "ccc"); err != nil {
		t.Fatalf("одобрить: %v", err)
	}
	if err := gl.RequestChanges(ctx, "g/r", 1, "поправь тесты"); err != nil {
		t.Fatalf("вернуть с замечаниями: %v", err)
	}
	got, _ := srv.Get("g/r", 1)
	if len(got.Approved) != 0 || len(got.Notes) != 1 || got.Notes[0] != "поправь тесты" {
		t.Fatalf("после замечаний одобрение снято и есть комментарий: %+v", got)
	}
	// Not approved to begin with is the ordinary case, not a failure.
	if err := gl.RequestChanges(ctx, "g/r", 1, "ещё"); err != nil {
		t.Fatalf("вернуть без одобрения: %v", err)
	}
}

func TestGitLabSaysTheTokenIsWrong(t *testing.T) {
	srv := gitlabtest.New()
	defer srv.Close()
	_, err := NewGitLab(srv.URL, "wrong", nil).Me(context.Background())
	if !msg.Is(err, "hosting.unauthorized") {
		t.Fatalf("неверный токен: %v", err)
	}
}
