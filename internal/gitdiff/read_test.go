package gitdiff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// repo makes a repository with one commit in it and returns where it is.
func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не установлен")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Тест"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, message string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func find(files []File, path string) (File, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return File{}, false
}

// The default a review stage asks for: what the agent did and nobody committed
// — changed files and new ones alike.
func TestReadUncommittedIncludesUntracked(t *testing.T) {
	dir := repo(t)
	write(t, dir, "было.txt", "первая\nвторая\n")
	commit(t, dir, "начало")
	write(t, dir, "было.txt", "первая\nдругая\n")
	write(t, dir, "новый.txt", "свежая строка\n")

	diff, err := Read(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 2 {
		t.Fatalf("файлов %d, ждали 2: %+v", len(diff.Files), diff.Files)
	}
	changed, ok := find(diff.Files, "было.txt")
	if !ok || changed.Status != StatusModified || changed.Added != 1 || changed.Removed != 1 {
		t.Fatalf("изменённый файл: %+v", changed)
	}
	added, ok := find(diff.Files, "новый.txt")
	if !ok || added.Status != StatusAdded || added.Added != 1 {
		t.Fatalf("новый файл: %+v", added)
	}
	if len(added.Hunks) != 1 || added.Hunks[0].Lines[0].Text != "свежая строка" {
		t.Fatalf("содержимое нового файла: %+v", added.Hunks)
	}
}

// Looking is not touching: a screen that staged what it shows would change the
// work it was opened to judge.
func TestReadLeavesTheIndexAlone(t *testing.T) {
	dir := repo(t)
	write(t, dir, "а.txt", "раз\n")
	commit(t, dir, "начало")
	write(t, dir, "б.txt", "два\n")

	before := status(t, dir)
	if _, err := Read(context.Background(), dir, ""); err != nil {
		t.Fatal(err)
	}
	if after := status(t, dir); after != before {
		t.Fatalf("папка изменилась: было %q, стало %q", before, after)
	}
}

func status(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A ref names revisions, and then the working copy is not in the answer.
func TestReadNamedRevisions(t *testing.T) {
	dir := repo(t)
	write(t, dir, "а.txt", "раз\n")
	commit(t, dir, "первый")
	write(t, dir, "а.txt", "раз\nдва\n")
	commit(t, dir, "второй")
	write(t, dir, "мусор.txt", "не коммитили\n")

	diff, err := Read(context.Background(), dir, "HEAD~1 HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 {
		t.Fatalf("файлов %d, ждали 1: %+v", len(diff.Files), diff.Files)
	}
	if f := diff.Files[0]; f.Path != "а.txt" || f.Added != 1 {
		t.Fatalf("файл: %+v", f)
	}
}

// A repository with nothing committed yet still has an answer: everything in it
// is new.
func TestReadEmptyRepository(t *testing.T) {
	dir := repo(t)
	write(t, dir, "первый.txt", "строка\n")

	diff, err := Read(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Status != StatusAdded {
		t.Fatalf("файлы: %+v", diff.Files)
	}
}

func TestReadWithoutRepositorySaysSo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не установлен")
	}
	_, err := Read(context.Background(), t.TempDir(), "")
	if !errors.Is(err, ErrNoRepo) {
		t.Fatalf("ошибка: %v, ждали ErrNoRepo", err)
	}
}

// A ref that looks like an option is refused before git sees it.
func TestReadRefusesOptionAsRevision(t *testing.T) {
	_, err := Read(context.Background(), t.TempDir(), "--exec=rm")
	if err == nil {
		t.Fatal("ссылка, начинающаяся с дефиса, принята")
	}
	if errors.Is(err, ErrNoRepo) {
		t.Fatalf("проверка ссылки должна идти до похода в git: %v", err)
	}
}

// Files git was told to ignore are not part of the review: node_modules is not
// what the agent did.
func TestReadHonoursGitignore(t *testing.T) {
	dir := repo(t)
	write(t, dir, ".gitignore", "мусор/\n")
	commit(t, dir, "начало")
	write(t, dir, "мусор/файл.txt", "не смотреть\n")
	write(t, dir, "видно.txt", "смотреть\n")

	diff, err := Read(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "видно.txt" {
		t.Fatalf("файлы: %+v", diff.Files)
	}
}

// A card working in a subdirectory of a repository reviews that repository.
func TestReadFindsRepositoryAbove(t *testing.T) {
	dir := repo(t)
	write(t, dir, "под/файл.txt", "раз\n")
	commit(t, dir, "начало")
	write(t, dir, "под/файл.txt", "два\n")

	diff, err := Read(context.Background(), filepath.Join(dir, "под"), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "под/файл.txt" {
		t.Fatalf("файлы: %+v", diff.Files)
	}
}
