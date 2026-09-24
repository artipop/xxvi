package main

import (
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// menubar is the application menu of the window, worded by the UI.
//
// Wails builds its default menu in code with English titles, and macOS does not
// translate a menu somebody else built — only the items it inserts itself
// (Services, Dictation, Emoji & Symbols), and those only because Info.plist says
// which languages the bundle speaks. So the menu is built here from the same
// roles Wails uses, which keep their system actions and shortcuts, and every
// title comes from the UI, the one side that knows the language.
//
// Until the UI has sent its words the framework's default menu stands: the
// window is not open yet, and there is nobody to read it.
type menubar struct{ app *application.App }

// SetWords rebuilds the menu in the words given, keyed as the UI's «menu.»
// dictionary keys them. A word that did not come leaves the role's own title.
func (m menubar) SetWords(words map[string]string) {
	application.InvokeSync(func() {
		m.app.Menu.Set(buildMenu(m.app, words))
	})
}

// entry is one item of a submenu: a role and the key of its title. NoRole is a
// separator.
type entry struct {
	role application.Role
	key  string
}

var sep = entry{role: application.NoRole}

// EventOpenSettings asks the window to open its settings. A menu item has no
// screen of its own to open, so it says so and the UI goes there.
const EventOpenSettings = "open-settings"

func buildMenu(app *application.App, words map[string]string) *application.Menu {
	darwin := runtime.GOOS == "darwin"
	menu := application.NewMenu()

	if darwin {
		sub := menu.AddSubmenu(app.Config().Name)
		fill(sub, words, entry{application.About, "menu.about"}, sep)
		// Settings are where every Mac application keeps them, under ⌘, —
		// the one item here that is ours rather than a role's.
		sub.Add(title(words, "menu.settings", "Settings…")).
			SetAccelerator("CmdOrCtrl+,").
			OnClick(func(*application.Context) { app.Event.Emit(EventOpenSettings, nil) })
		sub.AddSeparator()
		fill(sub, words,
			entry{application.ServicesMenu, "menu.services"}, sep,
			entry{application.Hide, "menu.hide"},
			entry{application.HideOthers, "menu.hideOthers"},
			entry{application.UnHide, "menu.showAll"}, sep,
			entry{application.Quit, "menu.quit"},
		)
	}

	file := []entry{{application.Quit, "menu.quit"}}
	if darwin {
		file = []entry{{application.CloseWindow, "menu.close"}}
	}
	fill(menu.AddSubmenu(title(words, "menu.file", "File")), words, file...)

	edit := []entry{
		{application.Undo, "menu.undo"}, {application.Redo, "menu.redo"}, sep,
		{application.Cut, "menu.cut"}, {application.Copy, "menu.copy"}, {application.Paste, "menu.paste"},
	}
	if darwin {
		edit = append(edit, entry{application.PasteAndMatchStyle, "menu.pasteAndMatchStyle"})
	}
	edit = append(edit, entry{application.Delete, "menu.delete"}, entry{application.SelectAll, "menu.selectAll"})
	fill(menu.AddSubmenu(title(words, "menu.edit", "Edit")), words, edit...)

	fill(menu.AddSubmenu(title(words, "menu.view", "View")), words,
		entry{application.Reload, "menu.reload"},
		entry{application.ForceReload, "menu.forceReload"}, sep,
		entry{application.ResetZoom, "menu.resetZoom"},
		entry{application.ZoomIn, "menu.zoomIn"},
		entry{application.ZoomOut, "menu.zoomOut"}, sep,
		entry{application.ToggleFullscreen, "menu.fullscreen"},
	)

	window := []entry{{application.Minimise, "menu.minimize"}, {application.Zoom, "menu.zoom"}}
	if darwin {
		window = append(window, sep, entry{application.Front, "menu.front"})
	} else {
		window = append(window, entry{application.CloseWindow, "menu.close"})
	}
	fill(menu.AddSubmenu(title(words, "menu.window", "Window")), words, window...)

	// No Help menu: the framework's one opens the framework's website, and this
	// application has no help of its own to open yet.
	return menu
}

// fill adds the roles to a submenu and titles each from the words: the role
// carries the action and the shortcut, the title is the UI's.
func fill(sub *application.Menu, words map[string]string, entries ...entry) {
	start := 0
	for sub.ItemAt(start) != nil {
		start++
	}
	for i, e := range entries {
		i += start
		if e.role == application.NoRole {
			sub.AddSeparator()
			continue
		}
		sub.AddRole(e.role)
		if text := words[e.key]; text != "" {
			if item := sub.ItemAt(i); item != nil {
				item.SetLabel(text)
			}
		}
	}
}

func title(words map[string]string, key, fallback string) string {
	if text := words[key]; text != "" {
		return text
	}
	return fallback
}
