package plugin

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The checker is run here against plugins written the way somebody else would
// write them — a few lines against sources/sdk — so both ends of the contract
// are exercised by the same test. That is the point of there being one checker:
// what an author runs and what we test with cannot disagree.

func check(t *testing.T, mode string) []Finding {
	t.Helper()
	return Check(context.Background(), CheckOptions{
		Command: []string{os.Args[0]},
		Env:     []string{pluginModeEnv + "=" + mode},
		Config:  map[string]string{"label": "INBOX"},
	})
}

func TestAWellBehavedPluginPassesWithNothingToSay(t *testing.T) {
	if got := check(t, "sdk-good"); len(got) != 0 {
		t.Fatalf("findings: %+v", got)
	}
}

// None of these stops a source, and every one of them costs items in a way
// nobody would connect to the plugin months later — which is exactly why the
// checker says them out loud.
func TestTheQuietMistakesAreReported(t *testing.T) {
	got := check(t, "sdk-sloppy")
	if len(got) == 0 {
		t.Fatal("a plugin with no ids and an unreturned cursor passed")
	}

	var sawID, sawTitle, sawCursor bool
	for _, f := range got {
		if f.Fatal {
			t.Errorf("не смертельная ошибка помечена смертельной: %s", f)
		}
		switch {
		case strings.Contains(f.Message, "нет id"):
			sawID = true
		case strings.Contains(f.Message, "нет заголовка"):
			sawTitle = true
		case strings.Contains(f.Message, "cursor"):
			sawCursor = true
		}
	}
	if !sawID || !sawTitle || !sawCursor {
		t.Fatalf("что-то не названо: id=%v title=%v cursor=%v (%+v)", sawID, sawTitle, sawCursor, got)
	}
}

// A plugin that claims neither poll nor push can never bring anything, and the
// app would keep a process alive for nothing.
func TestAPluginThatCanBringNothingIsFatal(t *testing.T) {
	got := check(t, "sdk-idle")
	if len(got) == 0 {
		t.Fatal("a plugin that claims nothing passed")
	}
	if !got[0].Fatal {
		t.Fatalf("это должно быть смертельным: %+v", got)
	}
}

// A command that is not there is the commonest thing to get wrong, and the
// message has to say so rather than blame the protocol.
func TestAPluginThatDoesNotStartSaysWhy(t *testing.T) {
	got := Check(context.Background(), CheckOptions{Command: []string{"/nonexistent/plugin"}})
	if len(got) != 1 || !got[0].Fatal || got[0].Step != "initialize" {
		t.Fatalf("findings: %+v", got)
	}
}

func TestAFindingReadsAsALine(t *testing.T) {
	fatal := Finding{Fatal: true, Step: "poll", Message: "плагин молчит"}
	if got := fatal.String(); !strings.HasPrefix(got, "✗ poll: ") {
		t.Fatalf("%q", got)
	}
	warning := Finding{Step: "poll", Message: "нет id"}
	if got := warning.String(); !strings.HasPrefix(got, "— poll: ") {
		t.Fatalf("%q", got)
	}
}
