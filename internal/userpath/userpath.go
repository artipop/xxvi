// Package userpath gives this process the PATH its owner actually has.
//
// A GUI application is started by launchd, not by a shell, so it inherits
// `/usr/bin:/bin:/usr/sbin:/sbin` and nothing else — while everything it runs
// on the user's behalf (npx, node, the agent CLIs, git, a source plugin) was
// installed by the user and is somewhere else. A dev build started from a
// terminal finds all of it; the packaged .app finds none.
//
// Guessing directories does not answer it: a version manager's node lives under
// a version number only the user's rc files know, and there are half a dozen
// managers. The login shell is the one place PATH is assembled, so it is asked
// rather than imitated.
//
// The process environment is the fix, not a lookup helper: npx is a
// `#!/usr/bin/env node` script and the codex adapter drives the codex CLI, so
// finding a binary by absolute path and spawning it with launchd's PATH only
// moves the failure one process along.
package userpath

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The shell's answer is fenced by markers because an interactive rc file is
// free to print anything it likes on the way — a greeting, a version notice, a
// warning about a plugin — and that noise arrives on the same stdout.
const (
	startMark = "__xxvi_path_start__"
	endMark   = "__xxvi_path_end__"
)

// query is what the shell is asked to run. `printenv PATH` rather than
// "$PATH" because fish keeps PATH as a list and would print it space-separated;
// what it exports to a child process is the colon-separated form every shell
// agrees on.
const query = "printf '" + startMark + "'; printenv PATH; printf '" + endMark + "'"

// queryTimeout bounds the shell. An rc file can do arbitrary work at startup,
// and a slow one must cost the app a delay at worst, never the launch.
const queryTimeout = 10 * time.Second

// Restore merges the login shell's PATH into this process's own, so everything
// spawned from here — and everything those processes spawn in turn — searches
// where the user's own commands live. It reports whether the PATH changed.
//
// An error here is worth logging and not worth failing on: the app still runs,
// and the agents dialog already says when an adapter cannot be found.
func Restore() (bool, error) {
	current := os.Getenv("PATH")
	// Windows GUI processes inherit the user's environment, and there is no
	// login shell to ask.
	if runtime.GOOS == "windows" {
		return false, nil
	}
	// A PATH with the user's own directories in it came from the user's own
	// shell: `wails3 dev`, `go run`, the headless build under systemd with an
	// environment somebody wrote. Asking the shell again would only cost a
	// shell startup to arrive at what we already have.
	if hasUserDir(current) {
		return false, nil
	}

	shellPath, err := loginShellPath()
	if err != nil {
		// Better than nothing: the three directories a single-user machine
		// installs into most often. It will not cover a version manager, which
		// is exactly why the shell is asked first.
		merged := merge(fallbackDirs(), current)
		if merged == current {
			return false, err
		}
		if setErr := os.Setenv("PATH", merged); setErr != nil {
			return false, setErr
		}
		return true, fmt.Errorf("could not ask the shell for PATH (%w); took the usual install directories", err)
	}
	merged := merge(shellPath, current)
	if merged == current {
		return false, nil
	}
	if err := os.Setenv("PATH", merged); err != nil {
		return false, err
	}
	return true, nil
}

// loginShellPath asks the user's login shell what PATH it composes. It is a
// login *and* interactive shell (`-ilc`) because that is where the answer is:
// nvm, mise and friends are sourced from ~/.zshrc and ~/.bashrc, which a
// non-interactive shell does not read.
func loginShellPath() (string, error) {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return "", fmt.Errorf("SHELL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	out, err := runShell(ctx, shell)
	if err != nil {
		return "", fmt.Errorf("%s: %w", shell, err)
	}
	path, err := extract(string(out))
	if err != nil {
		return "", fmt.Errorf("%s: %w", shell, err)
	}
	return path, nil
}

// runShell is a variable so a test can answer for the shell without one.
var runShell = func(ctx context.Context, shell string) ([]byte, error) {
	// Stdin is left unset, which gives the child /dev/null: an interactive
	// shell handed a terminal can stop and wait for input, and this one has
	// nobody to answer it. Output() keeps stderr out of the answer — the rc
	// files' own complaints are not ours to repeat.
	cmd := exec.CommandContext(ctx, shell, "-ilc", query)
	return cmd.Output()
}

// extract pulls the PATH out of the shell's output, ignoring whatever the rc
// files printed around it.
func extract(out string) (string, error) {
	i := strings.Index(out, startMark)
	if i < 0 {
		return "", fmt.Errorf("the shell did not print PATH")
	}
	rest := out[i+len(startMark):]
	j := strings.Index(rest, endMark)
	if j < 0 {
		return "", fmt.Errorf("the shell did not print PATH")
	}
	path := strings.TrimSpace(rest[:j])
	if path == "" {
		return "", fmt.Errorf("the shell printed an empty PATH")
	}
	return path, nil
}

// merge puts the shell's directories first and keeps ours after them, dropping
// duplicates. The shell's come first because they are the answer to "which node
// does this user mean" — a version manager works by shadowing what is further
// down — and ours are kept because the system directories are still where the
// system's own tools are.
func merge(shellPath, current string) string {
	seen := map[string]bool{}
	var out []string
	for _, list := range []string{shellPath, current} {
		for _, dir := range filepath.SplitList(list) {
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return strings.Join(out, string(filepath.ListSeparator))
}

// hasUserDir reports whether PATH names anything under the user's home, which
// is what tells a shell-given PATH from launchd's.
func hasUserDir(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	prefix := home + string(filepath.Separator)
	for _, dir := range filepath.SplitList(path) {
		if strings.HasPrefix(dir, prefix) {
			return true
		}
	}
	return false
}

// fallbackDirs are the usual install locations, the same ones the agent manager
// looks in by hand when a binary is not on PATH.
func fallbackDirs() string {
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append([]string{filepath.Join(home, ".local", "bin")}, dirs...)
	}
	return strings.Join(dirs, string(filepath.ListSeparator))
}
