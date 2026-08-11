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

	"github.com/artipop/xxvi/internal/app"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	core, err := app.Open("", logger)
	if err != nil {
		log.Fatalf("не удалось открыть приложение: %v", err)
	}
	defer core.Close()

	wails := application.New(application.Options{
		Name:        "XXVI",
		Description: "Входящие и флоу с агентами",
		Services:    []application.Service{application.NewService(app.NewAPI(core))},
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// The window's event bus is where events go, and it exists only now. Until
	// this line everything emitted is dropped — correct, because there is
	// nobody to show it to and the state it describes is in the database.
	core.SetUI(emitter{wails})
	core.Start()

	wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "XXVI",
		Width:            1280,
		Height:           820,
		MinWidth:         960,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(17, 18, 22),
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 44,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		URL: "/",
	})

	if err := wails.Run(); err != nil {
		log.Fatal(err)
	}
}

// emitter adapts the Wails event bus to what the packages expect.
type emitter struct{ app *application.App }

func (e emitter) Emit(event string, payload any) { e.app.Event.Emit(event, payload) }
