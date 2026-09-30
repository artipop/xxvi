package userpath

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An rc file that prints a greeting must not turn into a directory on PATH:
// the answer is what the markers fence, and nothing else.
func TestPathIsReadBetweenTheMarkers(t *testing.T) {
	got, err := extract("Welcome back!\n" + startMark + "/opt/homebrew/bin:/usr/bin\n" + endMark + "\n")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "/opt/homebrew/bin:/usr/bin" {
		t.Errorf("got %q", got)
	}
}

// A shell that fails to answer must say so rather than hand back its noise as
// a PATH, which would leave the app searching directories that do not exist.
func TestAnAnswerlessShellIsAnError(t *testing.T) {
	for name, out := range map[string]string{
		"nothing at all":  "zsh: command not found: printenv\n",
		"no closing mark": startMark + "/usr/bin",
		"an empty PATH":   startMark + "  \n" + endMark,
	} {
		if _, err := extract(out); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// The user's own directories go in front of the system's: a version manager
// works by shadowing, so a node in ~/.nvm has to win over one in /usr/bin.
func TestTheShellsDirectoriesComeFirstAndNothingIsLost(t *testing.T) {
	got := merge("/home/a/.nvm/bin:/usr/bin", "/usr/bin:/bin:/usr/sbin")
	want := "/home/a/.nvm/bin:/usr/bin:/bin:/usr/sbin"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A PATH that already names something under the home directory came from the
// user's own shell — `wails3 dev` and `go run` both do — and asking the shell
// again would only cost a shell startup to arrive at what we have.
func TestOnlyALaunchdPathIsWorthAsking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if hasUserDir("/usr/bin:/bin:/usr/sbin:/sbin") {
		t.Error("launchd's PATH should not look like a shell's")
	}
	if !hasUserDir("/usr/bin:" + filepath.Join(home, ".local", "bin")) {
		t.Error("a directory under the home directory should")
	}
}

// The point of the whole package: a GUI launch ends up with the PATH the user's
// shell has, and everything spawned from here inherits it.
func TestAGUIProcessEndsUpWithTheUsersPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "/usr/bin:/bin")
	shellPath := filepath.Join(home, ".nvm/versions/node/v24.18.0/bin") + ":/usr/bin:/bin"
	stub(t, func(context.Context, string) ([]byte, error) {
		return []byte("nvm loaded\n" + startMark + shellPath + endMark), nil
	})

	changed, err := Restore()
	if err != nil || !changed {
		t.Fatalf("Restore() = %v, %v", changed, err)
	}
	if got := os.Getenv("PATH"); got != shellPath {
		t.Errorf("PATH is %q, want %q", got, shellPath)
	}
}

// A shell that cannot be asked — none configured, an rc file that hangs, a
// login shell this build has never heard of — must still leave the app able to
// find what a plain install puts in the usual places.
func TestAnUnaskableShellStillGetsTheUsualPlaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "/usr/bin:/bin")
	stub(t, func(context.Context, string) ([]byte, error) {
		return nil, fmt.Errorf("signal: killed")
	})

	changed, err := Restore()
	if !changed || err == nil {
		t.Fatalf("Restore() = %v, %v — want a changed PATH and a reason", changed, err)
	}
	if got := os.Getenv("PATH"); !strings.Contains(got, "/opt/homebrew/bin") {
		t.Errorf("PATH is %q", got)
	}
}

func stub(t *testing.T, fn func(context.Context, string) ([]byte, error)) {
	t.Helper()
	prev := runShell
	runShell = fn
	t.Cleanup(func() { runShell = prev })
}
