//go:build !linux

package main

import "log"

// installDesktopIntegration is a Linux-only concern: Windows and macOS resolve
// the taskbar icon from the executable itself, so there is nothing to install.
func installDesktopIntegration(logger *log.Logger) {}
