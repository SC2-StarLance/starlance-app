package main

import (
	"embed"
	"fmt"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

// The queued spinner frames are tiny PNGs rendered in the site accent. The
// tray icon cycles through them while a search is live and snaps back to the
// static logo the moment the search ends.
//
//go:embed assets/tray/spin-*.png
var spinFS embed.FS

var (
	traySetIconFn func([]byte)
	traySpinGen   atomic.Int64
)

// installTrayIconSetter is called from the tray's onReady callback, the only
// moment systray accepts an icon.
func installTrayIconSetter() {
	traySetIconFn = setTrayIcon
}

// setTrayIcon installs an icon on every platform. On macOS the menu bar wants
// a template image and uses only the alpha channel, so the colored artwork is
// handed over as a white-on-alpha twin that the system tints for light and
// dark menu bars; the other platforms get the artwork as-is (SetTemplateIcon
// is a plain SetIcon there).
func setTrayIcon(icon []byte) {
	systray.SetTemplateIcon(templatePNG(icon), icon)
}

// startTraySpin begins cycling the tray icon. It is a no-op before the tray is
// live, and repeated calls keep a single animation goroutine.
func startTraySpin() {
	if traySetIconFn == nil {
		return
	}
	gen := traySpinGen.Add(1)
	go func() {
		const count = 48
		frames := make([][]byte, count)
		for i := range frames {
			b, err := spinFS.ReadFile(fmt.Sprintf("assets/tray/spin-%02d.png", i))
			if err != nil {
				return
			}
			frames[i] = b
		}
		for i := 0; traySpinGen.Load() == gen; i++ {
			traySetIconFn(frames[i%len(frames)])
			time.Sleep(33 * time.Millisecond)
		}
		if traySpinGen.Load() == gen {
			return
		}
		traySetIconFn(iconBytes)
	}()
}

// stopTraySpin ends the animation and restores the static icon.
func stopTraySpin() {
	if traySetIconFn == nil {
		return
	}
	traySpinGen.Add(1)
	traySetIconFn(iconBytes)
}
