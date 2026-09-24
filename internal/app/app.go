// Package app wires the halves together and is the surface the UI talks to.
//
// Nothing here decides anything: the flow engine moves cards, the acp manager
// runs agents, the inbox pipeline files what sources bring. This package owns
// the objects, the data directory, and the order things start and stop in.
package app

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/artipop/xxvi/internal/acp"
	"github.com/artipop/xxvi/internal/appmcp"
	"github.com/artipop/xxvi/internal/engine"
	"github.com/artipop/xxvi/internal/hosting"
	"github.com/artipop/xxvi/internal/inbox"
	"github.com/artipop/xxvi/internal/stagemcp"
	"github.com/artipop/xxvi/internal/store"
	"github.com/artipop/xxvi/internal/term"
)

// App is everything the application is made of.
type App struct {
	DataDir string
	// Version is what this build calls itself. It is stated in package main,
	// beside the build assets that have to agree with it, and handed here by
	// the shell — this package has no way to know how it was built.
	Version string

	Store     *store.Store
	Engine    *engine.Engine
	Agents    *acp.Manager
	Terminals *term.Manager
	// Tools is the one thing an agent working in a terminal can say back: that
	// its step is over (docs/system.md §4.1.1).
	Tools *stagemcp.Server
	// Outside is this application offered to an agent nobody here started: the
	// same moves a person makes, as MCP tools (docs/system.md §13).
	Outside  *appmcp.Server
	Pipeline *inbox.Pipeline
	Poller   *inbox.Poller
	// Hosting is where projects push: merge requests opened, watched and
	// reviewed (docs/system.md §15).
	Hosting *hosting.Service

	log *slog.Logger

	// ui is set once the window exists. Until then events are dropped, which is
	// correct: there is nobody to show them to, and the state they describe is
	// in the database anyway.
	uiMu sync.RWMutex
	ui   Emitter
	// notifier is the second road to an open question, for a person who is not
	// looking at the application. Optional: without it a question is still
	// shown on its card and in the attention panel.
	notifier *Notifier
	// chooser is the native file dialog, set once the window exists.
	chooser Chooser
	// menu is the window's application menu, worded by the UI.
	menu Menu
	// updates is how the application replaces itself; nil where there is
	// nothing to replace — a test, a headless run.
	updates Updates
}

// Emitter is how events reach the UI. The Wails application implements it;
// tests and a headless run leave it unset.
type Emitter interface {
	Emit(event string, payload any)
}

// Chooser asks a person for something only the window can ask for: a folder on
// their own disk. The same seam as the emitter and the notifier — the window
// implements it, and everything here works without one, so a headless run and a
// test simply have no picker.
type Chooser interface {
	Folder(title, from string) (string, error)
}

// Menu is the window's application menu. Its words are the UI's, like every
// other word on the screen: the UI hands them over, and the window rebuilds the
// menu in them.
type Menu interface {
	SetWords(words map[string]string)
}

// Open builds the application over a data directory, creating and seeding the
// database if this is a first run.
func Open(dataDir string, log *slog.Logger) (*App, error) {
	if log == nil {
		log = slog.Default()
	}
	if dataDir == "" {
		var err error
		if dataDir, err = DefaultDataDir(); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create the data folder: %w", err)
	}

	st, err := store.Open(filepath.Join(dataDir, "xxvi.db"))
	if err != nil {
		return nil, err
	}
	a := &App{DataDir: dataDir, Store: st, log: log}

	// A session left running by a previous run is not still working: a row that
	// says otherwise would make a card look busy forever.
	if n, err := st.AbandonRunningSessions(); err != nil {
		log.Warn("could not close the sessions of the previous run", "err", err)
	} else if n > 0 {
		log.Info("sessions of the previous run marked cancelled", "count", n)
	}

	a.Engine = engine.New(st, nil, a, log)
	a.Agents = acp.New(st, a.Engine, a, acp.Options{
		WorkDir: filepath.Join(dataDir, "work"),
	}, log)
	// The two know about each other, so one of them is wired second.
	a.Engine.SetRunner(a.Agents)

	// A terminal opens in the card's own working folder — the same one its
	// agent works in, so what a person types and what the agent did are one
	// working copy rather than two.
	a.Terminals = term.NewManager(a.Agents.WorkDir, log)
	// The tail of a finished terminal is kept beside the database rather than
	// in the folder an agent worked in: a file of ours inside somebody's
	// repository is ours to clean up and theirs to find in `git status`.
	a.Terminals.KeepIn(filepath.Join(dataDir, "terminals"))
	if err := a.Terminals.Listen(); err != nil {
		// A terminal that cannot be opened is a screen that says so. Everything
		// else in the application works without one, and refusing to start over
		// it would be the wrong size of failure.
		log.Warn("terminals disabled", "why", err)
	}

	a.Tools = stagemcp.New(log)
	if err := a.Tools.Listen(); err != nil {
		// The same size of failure, one step further along: without this port a
		// stage cannot be worked in a terminal at all, and the stage that tries
		// says so on its card instead of the application refusing to open.
		log.Warn("agent tools disabled", "why", err)
	}
	a.Agents.SetTerminals(a.Terminals, a.Tools)

	a.Pipeline = inbox.NewPipeline(st, a, log)
	a.Hosting = hosting.New(st, hosting.Keyring{}, log)
	a.Hosting.SetReporter(a.Engine)
	a.Engine.SetPublisher(a.Hosting)
	a.Poller = inbox.NewPoller(st, a.Pipeline, log)

	// The application from outside. Opened last of the three listeners because
	// it hands out everything above it, and left out of the way when it cannot
	// open: an agent in another window is a second road into this application,
	// never the only one.
	a.Outside = appmcp.New(appmcp.Deps{
		Store: st, Engine: a.Engine, Filer: a.Pipeline, Folders: a.Agents, Emit: a.Emit,
	}, log)
	if err := a.Outside.Listen(); err != nil {
		log.Warn("application tools disabled", "why", err)
	} else if err := appmcp.WriteHandoff(dataDir, a.Outside.URL(), a.Outside.Token()); err == nil {
		// The address, not the token: a log is a file people paste into issues.
		log.Info("application tools open", "url", a.Outside.URL())
	} else {
		// The port is open and the tools work; what failed is the note saying
		// where they are. Said out loud, because the symptom without it is
		// `xxvi mcp` insisting the application is not running.
		log.Warn("could not write the application tools address", "err", err)
	}

	if err := a.seed(); err != nil {
		st.Close()
		return nil, err
	}
	return a, nil
}

// Start begins the background work: reading sources on a timer.
func (a *App) Start() {
	a.Poller.Start()
}

// Close stops everything, agents first: a session still writing while the
// database closes under it is the one ordering mistake worth spelling out.
func (a *App) Close() error {
	a.Poller.Stop()
	a.Agents.Close()
	a.Terminals.Close()
	a.Tools.Close()
	if a.Outside != nil {
		a.Outside.Close()
		appmcp.RemoveHandoff(a.DataDir)
	}
	return a.Store.Close()
}

// SetMenu supplies the window's application menu once the window exists.
func (a *App) SetMenu(m Menu) {
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	a.menu = m
}

// SetChooser supplies the native file dialog once the window exists.
func (a *App) SetChooser(c Chooser) {
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	a.chooser = c
}

// SetUI supplies the event sink once the window exists.
func (a *App) SetUI(ui Emitter) {
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	a.ui = ui
}

// SetNotifier supplies system notifications once the application is up.
func (a *App) SetNotifier(n *Notifier) {
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	a.notifier = n
}

// Emit forwards an event to the UI if there is one. Every subsystem emits
// through the App rather than holding the window itself, so "there is no UI
// yet" is answered in one place.
func (a *App) Emit(event string, payload any) {
	a.uiMu.RLock()
	ui := a.ui
	a.uiMu.RUnlock()
	if ui != nil {
		ui.Emit(event, payload)
	}
	// A question is the one event that has to reach somebody who is not
	// looking at the window.
	if event == acp.EventAttention {
		a.notifyAttention(payload)
	}
}

// DefaultDataDir is where the database and the demo source's file live.
func DefaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("could not find the settings folder: %w", err)
	}
	return filepath.Join(base, "XXVI"), nil
}
