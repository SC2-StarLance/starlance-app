//go:build !windows

package main

import "log"

// Only Windows needs a Start Menu shortcut: Linux writes a desktop entry that
// also serves the app menu (desktop_linux.go), and macOS ships an app bundle.

func installStartMenuShortcut(logger *log.Logger) {}
