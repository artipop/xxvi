package app

import "errors"

// How this application learns of a newer version of itself.
//
// The work is the desktop shell's: it is the framework's Updater that streams,
// checks the signature and swaps the bundle, and the framework lives where the
// window does. So the same seam as the emitter, the notifier and the file
// dialog — an interface here, an implementation in main.go. A headless run and
// a test have no updater, and everything else works.

// Updates is the shell's updater, seen from here.
type Updates interface {
	// State is everything the screen draws, read fresh.
	State() UpdateState
	// SetEnabled turns the automatic check on or off, for good.
	SetEnabled(enabled bool) (UpdateState, error)
	// Check asks the release feed. It returns as soon as the request is under
	// way; what came of it arrives as an event.
	Check() error
	// Install downloads what the last check found, checks its signature and
	// stages it. Nothing is replaced until Restart.
	Install() error
	// Skip stops offering the release now on offer, and remembers that.
	Skip() error
	// Restart closes this application and brings back the newer one.
	Restart() error
}

// UpdateState is what the «Обновление» screen draws.
type UpdateState struct {
	// Supported is false where there is nothing to update: a headless run, a
	// test, a build with no updater wired in.
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`

	CurrentVersion string `json:"currentVersion"`

	// Status is the framework's own state — one of unconfigured, idle,
	// checking, up-to-date, available, downloading, verifying, installing,
	// ready, error — read off the Updater rather than inferred from which
	// event arrived: the bus dispatches each event in a goroutine of its own,
	// so two can be seen out of order, and a status that went backwards would
	// be a progress bar that did.
	Status string `json:"status"`

	AvailableVersion string `json:"availableVersion,omitempty"`
	ReleaseName      string `json:"releaseName,omitempty"`
	Notes            string `json:"notes,omitempty"`
	SizeBytes        int64  `json:"sizeBytes,omitempty"`
	Downloaded       int64  `json:"downloaded,omitempty"`

	SkippedVersion string `json:"skippedVersion,omitempty"`
	LastCheckedAt  string `json:"lastCheckedAt,omitempty"`

	// Error is what went wrong, verbatim and in the framework's English:
	// "dial tcp: lookup updates.deffun.org: no such host". ErrorStage is the
	// step it went wrong at — check, download, verify, install — and it is
	// there because the screen says the actionable half itself, in Russian.
	// The verbatim text stays underneath it, in small print: it is what a bug
	// report needs and nothing on this side can supply it.
	Error      string `json:"error,omitempty"`
	ErrorStage string `json:"errorStage,omitempty"`

	// Path is where the settings file lives, so the screen can name it when
	// something has to be looked at by hand.
	Path string `json:"path,omitempty"`
}

// SetUpdates supplies the updater once the application is up.
func (a *App) SetUpdates(u Updates) {
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	a.updates = u
}

func (a *App) updater() Updates {
	a.uiMu.RLock()
	defer a.uiMu.RUnlock()
	return a.updates
}

// ErrNoUpdates is the answer of a build that cannot replace itself. It is an
// error rather than a silent no-op because every caller of it is a button
// somebody pressed.
var ErrNoUpdates = errors.New("обновление этой сборки не поддерживается")

// ---- updates ----

// UpdateState is everything the «Обновление» screen draws. A build with no
// updater answers too, and says so: the screen then shows the version and
// nothing else, rather than an empty panel.
func (s *API) UpdateState() (UpdateState, error) {
	u := s.app.updater()
	if u == nil {
		return UpdateState{Supported: false, CurrentVersion: s.app.Version, Status: "unconfigured"}, nil
	}
	return u.State(), nil
}

// SetUpdatesEnabled turns the automatic check on or off. It takes effect on the
// next tick, not at the next launch.
func (s *API) SetUpdatesEnabled(enabled bool) (UpdateState, error) {
	u := s.app.updater()
	if u == nil {
		return UpdateState{}, ErrNoUpdates
	}
	return u.SetEnabled(enabled)
}

// CheckForUpdate asks the release feed. The answer arrives as the update event,
// so the screen draws the checking state from the same place it draws
// everything else rather than from a promise.
func (s *API) CheckForUpdate() error {
	u := s.app.updater()
	if u == nil {
		return ErrNoUpdates
	}
	return u.Check()
}

// InstallUpdate downloads what the last check found, checks its signature
// against the key this binary was built with, and stages it. Nothing is
// replaced until RestartToUpdate.
func (s *API) InstallUpdate() error {
	u := s.app.updater()
	if u == nil {
		return ErrNoUpdates
	}
	return u.Install()
}

// SkipUpdate stops offering the release now on offer. Remembered across
// restarts, or the button would mean «до перезапуска».
func (s *API) SkipUpdate() error {
	u := s.app.updater()
	if u == nil {
		return ErrNoUpdates
	}
	return u.Skip()
}

// RestartToUpdate closes this application and brings back the newer one.
// Everything a card was in the middle of is in the database; a running agent is
// not, and is closed the way quitting closes it.
func (s *API) RestartToUpdate() error {
	u := s.app.updater()
	if u == nil {
		return ErrNoUpdates
	}
	return u.Restart()
}

// EventUpdate says that something about updating changed — a check finished,
// a download moved, an install is ready. Like every other event here it says
// *that*, never what it now is: the screen then asks.
const EventUpdate = "update"
