//go:build darwin

package main

// The tray's "Recent activity" viewer on macOS opens the feed in TextEdit:
// the OS has no zenity/kdialog equivalent, and an AppleScript dialog would
// clip a long activity feed. Returns false only when TextEdit cannot be
// launched, so the caller falls back to opening the raw log.

import (
	"log"
	"os"
	"os/exec"
)

// showTextInfo writes body to a temp file and opens it in TextEdit.
func showTextInfo(logger *log.Logger, title, body string) bool {
	tmp, err := os.CreateTemp("", "starlance-activity-*.txt")
	if err != nil {
		logger.Printf("dialog: create temp activity file: %v", err)
		return false
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(name)
		logger.Printf("dialog: write activity file: %v", err)
		return false
	}
	tmp.Close()
	cmd := exec.Command("open", "-e", name)
	if err := cmd.Start(); err != nil {
		os.Remove(name)
		logger.Printf("dialog: open activity file: %v", err)
		return false
	}
	// The file stays behind: TextEdit keeps reading it lazily and offers to
	// save over it, so deleting it under the editor breaks that flow. The
	// OS temp directory owns the cleanup.
	go func() { _ = cmd.Wait() }()
	_ = title // TextEdit shows the file name in its title bar instead.
	return true
}
