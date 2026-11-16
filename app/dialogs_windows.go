//go:build windows

package main

import "log"

// Windows stub for the native-dialog helpers. The Windows tray opens the
// per-user log in Notepad (see menu.go), so this reports that no dialog tool
// is available and the caller falls back to the editor path.

func showTextInfo(logger *log.Logger, title, body string) bool { return false }
