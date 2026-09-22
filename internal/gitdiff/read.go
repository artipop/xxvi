package gitdiff

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Limits. A diff nobody can scroll to the end of is not more useful than one
// that says how big it is and shows the beginning.
const (
	// maxPatchBytes is how much of git's output is read at all.
	maxPatchBytes = 4 << 20
	// maxLines is how many hunk lines are kept across every file.
	maxLines = 4000
	// maxUntrackedBytes is how much of one new file is shown.
	maxUntrackedBytes = 256 << 10
)

// ErrNoRepo is the folder having no git in it. It is a sentence to the person
// rather than a failure of the screen: a card whose project is not a repository
// is an ordinary card, and the diff screen says so and stays.
var ErrNoRepo = errors.New("в рабочей папке карточки нет git-репозитория")

// ErrNoGit is git missing from the machine.
var ErrNoGit = errors.New("git не найден на этой машине")

// Diff is everything one diff screen shows.
type Diff struct {
	// Root is the repository the folder belongs to, which is not always the
	// folder itself — a card may work in a subdirectory of one.
	Root string `json:"root"`
	// Ref is what was compared, as the screen asked for it. Empty is the
	// working copy against the last commit.
	Ref   string `json:"ref,omitempty"`
	Files []File `json:"files"`
	// Truncated says the patch was bigger than the screen keeps. The counts on
	// each file stay true regardless: they are what a person decides by.
	Truncated bool `json:"truncated,omitempty"`
}

// Read runs git in dir and returns what changed.
//
// An empty ref is the useful default and the one a review stage wants: what is
// in the working copy and not in the last commit — the agent's work, before
// anybody committed it. Anything else is passed to git as the revisions it is:
// «HEAD~1», «main...HEAD», «abc123 def456».
//
// Untracked files are part of the answer when the ref is empty. An agent that
// wrote a new file wrote it; leaving it out of the review because nobody has
// run `git add` yet would hide exactly the change most worth looking at.
func Read(ctx context.Context, dir, ref string) (Diff, error) {
	if strings.TrimSpace(dir) == "" {
		return Diff{}, fmt.Errorf("не задана папка, в которой смотреть изменения")
	}
	revs, err := revisions(ref)
	if err != nil {
		return Diff{}, err
	}
	root, err := repoRoot(ctx, dir)
	if err != nil {
		return Diff{}, err
	}

	args := []string{"diff", "--patch", "--no-color", "--no-ext-diff", "--find-renames"}
	if len(revs) > 0 {
		args = append(args, revs...)
	} else {
		// An empty repository has no HEAD to compare against, and the empty
		// tree is what git itself compares against there.
		if _, err := run(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
			args = append(args, emptyTree)
		} else {
			args = append(args, "HEAD")
		}
	}

	patch, clipped, err := output(ctx, root, args...)
	if err != nil {
		return Diff{}, err
	}
	files, overflow := Parse(patch, maxLines)
	out := Diff{Root: root, Ref: strings.TrimSpace(ref), Files: files, Truncated: clipped || overflow}

	if len(revs) == 0 {
		budget := maxLines - countLines(files)
		untracked, more, err := untrackedFiles(ctx, root, budget)
		if err != nil {
			return Diff{}, err
		}
		out.Files = append(out.Files, untracked...)
		out.Truncated = out.Truncated || more
	}
	return out, nil
}

// emptyTree is git's own hash of nothing, which is what a first commit is
// compared against.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// revisions splits a screen's ref into arguments for git.
//
// A part starting with a dash is refused rather than escaped: the ref is a
// revision, and something that looks like an option is either a mistake or an
// attempt to make the screen run a different command than it says it does.
func revisions(ref string) ([]string, error) {
	out := strings.Fields(ref)
	for _, r := range out {
		if strings.HasPrefix(r, "-") {
			return nil, fmt.Errorf("«%s» — это не ревизия: ссылка экрана говорит, что с чем сравнить", r)
		}
	}
	return out, nil
}

// repoRoot is the repository dir belongs to.
func repoRoot(ctx context.Context, dir string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", ErrNoGit
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("рабочей папки карточки нет: %s", dir)
	}
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ErrNoRepo
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", ErrNoRepo
	}
	return filepath.Clean(root), nil
}

// untrackedFiles turns what git calls "others" into added files. Nothing is
// staged to produce them: a screen that looks at the work must not change it.
func untrackedFiles(ctx context.Context, root string, budget int) (files []File, truncated bool, err error) {
	out, err := run(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, false, err
	}
	for _, name := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		file, more := readAdded(root, name, budget)
		budget -= countFileLines(file)
		truncated = truncated || more
		files = append(files, file)
	}
	return files, truncated, nil
}

// readAdded reads one new file as a patch of pure additions. There is no diff
// to take here — everything in a new file is new — so there is nothing git
// could say about it that we would be repeating.
func readAdded(root, name string, budget int) (File, bool) {
	file := File{Path: name, Status: StatusAdded}
	f, err := os.Open(filepath.Join(root, name))
	if err != nil {
		// A file that vanished between the listing and the read is not an
		// error of the screen: it says the file is there and empty.
		return file, false
	}
	defer f.Close()

	head := make([]byte, 8000)
	n, _ := io.ReadFull(f, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		file.Binary = true
		return file, false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return file, false
	}

	hunk := Hunk{Header: "@@ -0,0 +1 @@"}
	reader := bufio.NewReader(io.LimitReader(f, maxUntrackedBytes))
	truncated := false
	for {
		line, err := reader.ReadString('\n')
		if line == "" && err != nil {
			truncated = truncated || !errors.Is(err, io.EOF)
			break
		}
		file.Added++
		if budget > 0 && len(hunk.Lines) < budget {
			hunk.Lines = append(hunk.Lines, Line{
				Kind: LineAdd,
				Text: strings.TrimRight(line, "\n"),
				New:  file.Added,
			})
		} else {
			truncated = true
		}
		if err != nil {
			break
		}
	}
	if file.Added > 0 {
		hunk.Header = fmt.Sprintf("@@ -0,0 +1,%d @@", file.Added)
		file.Hunks = append(file.Hunks, hunk)
	}
	return file, truncated
}

func countLines(files []File) int {
	total := 0
	for _, f := range files {
		total += countFileLines(f)
	}
	return total
}

func countFileLines(f File) int {
	total := 0
	for _, h := range f.Hunks {
		total += len(h.Lines)
	}
	return total
}

// run is git with its answer read whole: the commands here answer in one line.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", gitArgs(args)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
}

// output is git with its answer read up to a limit, because this one is the
// patch and a patch has no size anybody promised.
func output(ctx context.Context, dir string, args ...string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "git", gitArgs(args)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	if err := cmd.Start(); err != nil {
		return "", false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, maxPatchBytes))
	// Whatever is left has to be drained, or git blocks writing into a pipe
	// nobody reads and never exits.
	extra, _ := io.Copy(io.Discard, pipe)
	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", false, fmt.Errorf("git diff: %s", msg)
		}
		return "", false, fmt.Errorf("git diff: %w", err)
	}
	if readErr != nil {
		return "", false, fmt.Errorf("git diff: %w", readErr)
	}
	return string(data), extra > 0, nil
}

// gitArgs puts the settings every call here needs in front of the command.
//
// core.quotePath off is the one that matters: with it on, a Russian filename
// comes back as a row of escapes, and the screen would show that instead of the
// name the person gave the file.
func gitArgs(args []string) []string {
	return append([]string{"-c", "core.quotePath=false"}, args...)
}
