package main

import (
	"context"
	"log"
	"runtime"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

// lastTrayTap debounces the second half of a double click: a double click is
// one toggle, not hide-then-show.
var lastTrayTap atomic.Int64

func trayTapped() {
	now := time.Now().UnixMilli()
	if prev := lastTrayTap.Swap(now); now-prev < 350 {
		return
	}
	toggleWindow()
}

// RunTray hosts the system-tray icon on every supported platform (Windows,
// Linux, macOS) through fyne.io/systray, which renders a native menu
// everywhere: Win32 menus on Windows, a DBus StatusNotifierItem + DBusMenu
// served to the desktop shell on Linux/BSD, an NSStatusItem on macOS. The
// menu is rendered from the shared trayMenu model in menu.go and every click
// routes through handleMenuAction, so there is no per-platform tray code and
// behavior is identical everywhere.
func RunTray(ctx context.Context, stop context.CancelFunc, logger *log.Logger, deps TrayDeps) {
	systray.Run(onReady(ctx, stop, logger, deps), func() { /* cleanup handled by the library */ })
}

// onReady builds the tray icon and menu once the systray runtime is live.
func onReady(ctx context.Context, stop context.CancelFunc, logger *log.Logger, deps TrayDeps) func() {
	return func() {
		setTrayIcon(iconBytes)
		installTrayIconSetter()
		if runtime.GOOS != "darwin" {
			// macOS renders SetTitle as text beside the menu-bar icon, which
			// squeezes the menu bar; the tooltip carries the name there.
			systray.SetTitle("StarLance App")
		}
		systray.SetTooltip("StarLance App")
		// Left click brings the window back. The menu stays on right click
		// (the desktop renders the exported DBus menu for it).
		systray.SetOnTapped(trayTapped)

		var statusItem *systray.MenuItem
		handles := map[MenuID]*systray.MenuItem{}
		refresh := func() { applyTrayMenu(statusItem, handles, trayMenu(deps)) }

		var build func(items []trayItem, parent *systray.MenuItem)
		build = func(items []trayItem, parent *systray.MenuItem) {
			for _, it := range items {
				switch it.kind {
				case traySeparator:
					if parent == nil {
						systray.AddSeparator()
					} else {
						parent.AddSeparator()
					}
				case trayStatus:
					// The status row is always the first top-level item.
					statusItem = systray.AddMenuItem(it.label, it.tip)
					statusItem.Disable()
				default:
					var h *systray.MenuItem
					if parent == nil {
						if it.kind == trayToggle {
							h = systray.AddMenuItemCheckbox(it.label, it.tip, it.checked)
						} else {
							h = systray.AddMenuItem(it.label, it.tip)
						}
					} else {
						if it.kind == trayToggle {
							h = parent.AddSubMenuItemCheckbox(it.label, it.tip, it.checked)
						} else {
							h = parent.AddSubMenuItem(it.label, it.tip)
						}
					}
					if it.disabled {
						h.Disable()
					}
					if it.hidden {
						h.Hide()
					}
					handles[it.id] = h
					id := it.id
					go func() {
						for range h.ClickedCh {
							handleMenuAction(logger, deps, stop, id)
							refresh() // e.g. login/logout flip without waiting for the tick
						}
					}()
					build(it.children, h)
				}
			}
		}
		build(trayMenu(deps), nil)

		// Shared status ticker: re-derive the menu from app state every
		// second and apply only what changed to the rendered items.
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					systray.Quit()
					return
				case <-ticker.C:
					refresh()
				}
			}
		}()
	}
}

// applyTrayMenu syncs the rendered tray with a freshly built shared model:
// the status row's title/tooltip and the hidden flag of each item.
func applyTrayMenu(statusItem *systray.MenuItem, handles map[MenuID]*systray.MenuItem, items []trayItem) {
	for _, it := range items {
		switch it.kind {
		case trayStatus:
			if statusItem != nil {
				statusItem.SetTitle(it.label)
				statusItem.SetTooltip(it.tip)
			}
		case trayAction, traySubmenu, trayToggle:
			h, ok := handles[it.id]
			if !ok {
				continue
			}
			if it.kind == trayToggle {
				// Checkbox state is derived from config; clicking toggles the
				// real state and the next tick re-syncs the mark.
				if it.checked {
					h.Check()
				} else {
					h.Uncheck()
				}
			}
			if it.disabled {
				h.Disable()
				h.SetTitle(it.label) // info rows (Replays: …) update in place
				continue
			}
			h.Enable() // a previously disabled row must be able to re-enable
			if it.hidden {
				h.Hide()
			} else {
				h.Show()
			}
		}
		if it.children != nil {
			applyTrayMenu(statusItem, handles, it.children)
		}
	}
}
