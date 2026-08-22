// XXVI: inbox → flow. Tasks arrive from sources, a person takes one into work,
// and it travels a flow whose stages are worked by ACP agents.
//
// This file is the desktop shell and nothing else: it opens the application
// (internal/app), hands the UI its API, and wires the window's event bus in as
// the place events go.
package main

import (
	"embed"
	"log"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/artipop/xxvi/internal/app"
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
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	core, err := app.Open("", logger)
	if err != nil {
		log.Fatalf("не удалось открыть приложение: %v", err)
	}
	defer core.Close()

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
		logger.Info("системные уведомления выключены", "почему", why)
	}

	wails := application.New(application.Options{
		Name:        "XXVI",
		Description: "Входящие и флоу с агентами",
		Services:    services,
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// The window's event bus is where events go, and it exists only now. Until
	// this line everything emitted is dropped — correct, because there is
	// nobody to show it to and the state it describes is in the database.
	core.SetUI(emitter{wails})
	core.SetChooser(chooser{wails})
	if notifier != nil {
		core.SetNotifier(app.NewNotifier(core, notifier))
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
