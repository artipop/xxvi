package main

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type PopupItem struct {
	Label     string `json:"label"`
	Checked   bool   `json:"checked"`
	Separator bool   `json:"separator"`
}

// PopupMenu uses a system popup because web menus cannot cover native terminal views.
type PopupMenu struct {
	app  *application.App
	win  *application.WebviewWindow
	mu   sync.Mutex
	menu *application.ContextMenu
}

func (p *PopupMenu) Open(request string, items []PopupItem, x, y float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Wails retains the native NSMenu when its items are destroyed; reuse it.
	if p.menu == nil {
		p.menu = p.app.ContextMenu.New()
		p.app.ContextMenu.Add("ribbon-popup", p.menu)
	} else {
		application.InvokeSync(p.menu.Menu.Destroy)
	}
	menu := p.menu
	for i, entry := range items {
		if entry.Separator {
			menu.AddSeparator()
			continue
		}
		var item *application.MenuItem
		if entry.Checked {
			item = menu.AddCheckbox(entry.Label, true)
		} else {
			item = menu.Add(entry.Label)
		}
		item.OnClick(func(*application.Context) {
			p.app.Event.Emit("popup-selected", map[string]any{"request": request, "index": i})
		})
	}
	zoom := p.win.GetZoom()
	p.win.OpenContextMenu(&application.ContextMenuData{Id: "ribbon-popup", X: int(x * zoom), Y: int(y * zoom)})
}
