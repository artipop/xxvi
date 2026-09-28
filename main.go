// XXVI: inbox → flow. Tasks arrive from sources, a person takes one into work,
// and it travels a flow whose stages are worked by ACP agents.
//
// This file is the desktop shell and nothing else: it opens the application
// (internal/app), hands the UI its API, and wires the window's event bus in as
// the place events go.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/wailsapp/wails/v3/pkg/updater"

	"github.com/artipop/xxvi/internal/app"
	"github.com/artipop/xxvi/internal/appmcp"
	"github.com/artipop/xxvi/internal/launch"
	"github.com/artipop/xxvi/internal/msg"
	"github.com/artipop/xxvi/internal/ptyhold"
	"github.com/artipop/xxvi/internal/stagemcp"
)

//go:embed all:frontend/dist
var assets embed.FS

// The window never goes below this: narrower than it the flow editor's canvas
// and its inspector stop fitting side by side, and shorter than it a ribbon
// stops being a band.
const (
	minWidth  = 960
	minHeight = 600
)

func main() {
	// First line of main, and it has to be: this process may be the helper the
	// updater spawned, whose whole job is to wait for the old copy to die and
	// swap the bundle. application.New calls this too, but by then we would
	// have opened SQLite, taken the terminal socket and started agents.
	updater.HandleHelperMode()

	// Second line, and before anything is opened: this executable is also the
	// MCP server an outside agent spawns (`xxvi mcp`), and in that mode stdout
	// belongs to the JSON-RPC stream. It never returns — a window, a database
	// and a terminal socket are exactly what must not happen here.
	maybeRunMCP(os.Args[1:])
	maybeRunHook(os.Args[1:])
	// And the process that keeps terminals alive between runs (internal/ptyhold),
	// for the same reasons: nothing of the application is to be opened in it.
	maybeHoldTerminals(os.Args[1:])
	app.HoldTerminals = func(socket string) *exec.Cmd {
		self, err := os.Executable()
		if err != nil {
			self = os.Args[0]
		}
		return exec.Command(self, "pty-hold", socket)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	core, err := app.Open("", logger)
	if err != nil {
		log.Fatalf("could not open the application: %v", err)
	}
	defer core.Close()
	core.Version = appVersion

	// A question an agent asked has to reach somebody who is not looking at the
	// window, so notifications are a service of the application rather than
	// something the UI arranges for itself.
	//
	// Asked before it is registered: the service refuses to start without an
	// application bundle, and that refusal would stop everything else — over a
	// road to an answer that the card and the panel already provide.
	services := []application.Service{application.NewService(app.NewAPI(core))}
	var notifier *notifications.NotificationService
	if ok, why := app.NotificationsPossible(); ok {
		notifier = notifications.New()
		services = append(services, application.NewService(notifier))
	} else {
		logger.Info("system notifications disabled", "why", why)
	}

	wails := application.New(application.Options{
		Name:        "XXVI",
		Description: "Inbox and flows with agents",
		Services:    services,
		// What a refusal sends the window is its message — a code and its
		// values — and never a sentence: the window words it in the person's
		// language. An error nobody gave a code travels as its text under
		// msg.CodeInternal, which the window shows as a detail.
		MarshalError: func(err error) []byte {
			b, _ := json.Marshal(msg.Of(err))
			return b
		},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// The window's event bus is where events go, and it exists only now. Until
	// this line everything emitted is dropped — correct, because there is
	// nobody to show it to and the state it describes is in the database.
	core.SetUI(emitter{wails})

	core.SetChooser(chooser{wails})
	core.SetMenu(menubar{wails})
	if notifier != nil {
		core.SetNotifier(app.NewNotifier(core, notifier))
	}
	// Replacing this application with a newer one. Wired after the event sink,
	// because the first thing it does is say what state it is in, and before
	// the window, because a check on a timer does not need one.
	if updates := newUpdateController(wails, core, logger); updates != nil {
		defer updates.close()
		core.SetUpdates(updates)
	}

	core.Start()

	win := wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "XXVI",
		Width:            1280,
		Height:           820,
		MinWidth:         minWidth,
		MinHeight:        minHeight,
		BackgroundColour: application.NewRGB(17, 18, 22),
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 44,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		URL: "/",
	})

	core.SetWindows(&windows{win: win})

	// The size above is a wish, not a measurement: on a display shorter than it
	// the window opens with its bottom edge past the screen, and the ribbon —
	// which is exactly one window tall on purpose — goes over the edge with it.
	// The screen is only knowable once the window is on one, so it is asked for
	// then.
	wails.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		screen, err := win.GetScreen()
		if err != nil || screen == nil || screen.WorkArea.Height == 0 {
			return
		}
		// WorkArea comes back in device pixels while a window is sized in
		// points, so the two have to be brought to the same units before they
		// can be compared — 1598 pixels of usable height on a Retina display is
		// 799 points, and a window of 820 does not fit in it.
		scale := float64(screen.ScaleFactor)
		if scale <= 0 {
			scale = 1
		}
		room := func(pixels int) int { return int(float64(pixels) / scale) }
		width := max(minWidth, min(1280, room(screen.WorkArea.Width)-40))
		height := max(minHeight, min(820, room(screen.WorkArea.Height)-40))
		win.SetSize(width, height)

		// A window's frame is taller than the content it was asked for, by
		// however much its title bar takes, and that is not ours to know. So it
		// is measured rather than guessed: whatever still hangs past the screen
		// comes off the height.
		if _, frame := win.Size(); frame > room(screen.WorkArea.Height) {
			win.SetSize(width, max(minHeight, height-(frame-room(screen.WorkArea.Height))))
		}
		win.Center()
	})

	if err := wails.Run(); err != nil {
		log.Fatal(err)
	}
}

// maybeRunMCP handles `xxvi mcp`: the same executable doubles as the MCP server
// an agent's session spawns, which keeps this a single application with nothing
// extra to install.
//
// It bridges to the application that is already running rather than opening one
// of its own: the cards it moves are the cards on the screen, and a second copy
// of XXVI over the same database would be a second application disagreeing with
// the first (internal/appmcp).
func maybeRunMCP(args []string) {
	if len(args) == 0 || args[0] != "mcp" {
		return
	}
	dataDir, err := app.DefaultDataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		os.Exit(1)
	}
	// The agent closes stdio to end the session, and that is success.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = appmcp.ServeStdio(ctx, dataDir, os.Stdin, os.Stdout)
	switch {
	case err == nil, errors.Is(err, context.Canceled), errors.Is(err, io.EOF):
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		os.Exit(1)
	}
}

// maybeRunHook handles `xxvi hook`: the command a stage's CLI runs at the turns
// of its conversation, which hands each one to the step it belongs to
// (internal/stagemcp). It exits 0 whatever happened and prints nothing: the CLI
// waits on it, some events feed stdout back to the model, and a card's mark
// being late is no reason to break the conversation it marks.
func maybeRunHook(args []string) {
	if len(args) == 0 || args[0] != "hook" {
		return
	}
	if err := stagemcp.ForwardHook(context.Background(), os.Stdin, os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "xxvi hook: %v\n", err)
	}
	os.Exit(0)
}

// maybeHoldTerminals handles `xxvi pty-hold <socket>`: the holder the
// application starts for its terminals, and which outlives it.
func maybeHoldTerminals(args []string) {
	if len(args) != 2 || args[0] != "pty-hold" {
		return
	}
	if err := ptyhold.Serve(args[1]); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// emitter adapts the Wails event bus to what the packages expect.
type emitter struct{ app *application.App }

func (e emitter) Emit(event string, payload any) { e.app.Event.Emit(event, payload) }

// chooser adapts the window's own file dialog. It lives here rather than in the
// application because asking a person to point at a folder needs a window, and
// the window is what this file is.
type chooser struct{ app *application.App }

func (c chooser) Folder(title, from string) (string, error) {
	dialog := c.app.Dialog.OpenFile().
		SetTitle(title).
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true)
	if from != "" {
		dialog = dialog.SetDirectory(from)
	}
	// A dialog closed without choosing is not a failure: it is a person
	// deciding not to, and the caller carries on with what it had.
	return dialog.PromptForSingleSelection()
}

// windows moves the window aside for an application a run screen started, and
// back again.
type windows struct {
	win *application.WebviewWindow

	mu    sync.Mutex
	saved *application.Rect
}

// roomMinWidth is how narrow the window may get while it shares the screen:
// below minWidth the flow editor stops fitting, but a ribbon of one column is
// still a ribbon, and the room is for looking at somebody else's window.
const roomMinWidth = 480

func (w *windows) MakeRoom(split func(work launch.Rect) (ours, theirs launch.Rect)) (launch.Rect, error) {
	screen, err := w.win.GetScreen()
	if err != nil {
		return launch.Rect{}, err
	}
	if screen == nil || screen.WorkArea.Width == 0 {
		return launch.Rect{}, errors.New("the window is on no screen")
	}
	// Device pixels, as at startup (main): brought to points before they meet
	// a window's position.
	scale := float64(screen.ScaleFactor)
	if scale <= 0 {
		scale = 1
	}
	pt := func(v int) int { return int(float64(v) / scale) }
	work := launch.Rect{X: pt(screen.WorkArea.X), Y: pt(screen.WorkArea.Y),
		W: pt(screen.WorkArea.Width), H: pt(screen.WorkArea.Height)}

	w.mu.Lock()
	if w.saved == nil {
		if w.win.IsFullscreen() {
			w.win.UnFullscreen()
		}
		if w.win.IsMaximised() {
			w.win.UnMaximise()
		}
		b := w.win.Bounds()
		w.saved = &b
	}
	w.mu.Unlock()

	ours, theirs := split(work)
	w.win.SetMinSize(roomMinWidth, minHeight)
	w.win.SetBounds(application.Rect{X: ours.X, Y: ours.Y, Width: ours.W, Height: ours.H})
	return theirs, nil
}

func (w *windows) GiveBack() {
	w.mu.Lock()
	saved := w.saved
	w.saved = nil
	w.mu.Unlock()
	if saved == nil {
		return
	}
	w.win.SetBounds(*saved)
	w.win.SetMinSize(minWidth, minHeight)
	w.win.Focus()
}
