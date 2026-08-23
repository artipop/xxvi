package app

import (
	"errors"
	"testing"
)

// Every one of these is a button somebody pressed. A build with no updater has
// to say so — a silent no-op is a button that appears to work and does nothing,
// which is worse than a refusal written in a sentence.
func TestABuildThatCannotUpdateItselfSaysSo(t *testing.T) {
	a := open(t)
	defer a.Close()
	a.Version = "0.1.0"
	api := NewAPI(a)

	state, err := api.UpdateState()
	if err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if state.Supported {
		t.Error("сборка без апдейтера назвалась поддерживающей обновление")
	}
	// The version is answered even here: «что у меня стоит» is the question
	// this screen exists for, and it has an answer regardless.
	if state.CurrentVersion != "0.1.0" {
		t.Errorf("версия %q, ждали 0.1.0", state.CurrentVersion)
	}

	for name, act := range map[string]func() error{
		"CheckForUpdate":  api.CheckForUpdate,
		"InstallUpdate":   api.InstallUpdate,
		"SkipUpdate":      api.SkipUpdate,
		"RestartToUpdate": api.RestartToUpdate,
	} {
		if err := act(); !errors.Is(err, ErrNoUpdates) {
			t.Errorf("%s вернул %v, ждали ErrNoUpdates", name, err)
		}
	}
	if _, err := api.SetUpdatesEnabled(true); !errors.Is(err, ErrNoUpdates) {
		t.Errorf("SetUpdatesEnabled вернул %v, ждали ErrNoUpdates", err)
	}
}

// And once the shell hands one over, every call goes to it — the seam is the
// only road, so nothing else has to know whether this build can update itself.
func TestTheShellsUpdaterIsWhatGetsAsked(t *testing.T) {
	a := open(t)
	defer a.Close()
	fake := &fakeUpdates{state: UpdateState{Supported: true, CurrentVersion: "0.1.0", Status: "idle"}}
	a.SetUpdates(fake)
	api := NewAPI(a)

	state, err := api.UpdateState()
	if err != nil || !state.Supported {
		t.Fatalf("UpdateState = %+v, %v", state, err)
	}
	if err := api.CheckForUpdate(); err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if err := api.InstallUpdate(); err != nil {
		t.Fatalf("InstallUpdate: %v", err)
	}
	if err := api.SkipUpdate(); err != nil {
		t.Fatalf("SkipUpdate: %v", err)
	}
	if err := api.RestartToUpdate(); err != nil {
		t.Fatalf("RestartToUpdate: %v", err)
	}
	if _, err := api.SetUpdatesEnabled(false); err != nil {
		t.Fatalf("SetUpdatesEnabled: %v", err)
	}
	if want := []string{"check", "install", "skip", "restart", "enabled=false"}; !equal(fake.calls, want) {
		t.Errorf("апдейтер увидел %v, ждали %v", fake.calls, want)
	}
}

type fakeUpdates struct {
	state UpdateState
	calls []string
}

func (f *fakeUpdates) State() UpdateState { return f.state }
func (f *fakeUpdates) SetEnabled(enabled bool) (UpdateState, error) {
	f.calls = append(f.calls, "enabled=false")
	if enabled {
		f.calls[len(f.calls)-1] = "enabled=true"
	}
	f.state.Enabled = enabled
	return f.state, nil
}
func (f *fakeUpdates) Check() error   { f.calls = append(f.calls, "check"); return nil }
func (f *fakeUpdates) Install() error { f.calls = append(f.calls, "install"); return nil }
func (f *fakeUpdates) Skip() error    { f.calls = append(f.calls, "skip"); return nil }
func (f *fakeUpdates) Restart() error { f.calls = append(f.calls, "restart"); return nil }

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
