//go:build darwin

package main

// macOS notifications go through Notification Center via osascript. The
// strings are passed as script arguments, never interpolated into the script
// text, so a player name or path containing quotes cannot break the script.
// Problems also play the system alert sound; infos stay silent.

import (
	"log"
	"os/exec"
)

const notifyInfoScript = `on run argv
	display notification (item 2 of argv) with title (item 1 of argv)
end run`

const notifyProblemScript = `on run argv
	display notification (item 2 of argv) with title (item 1 of argv) sound name "Basso"
end run`

// Notify posts a Notification Center banner, falling back to the log file
// when osascript cannot be launched.
func Notify(logger *log.Logger, level NoticeLevel, title, message string) {
	script := notifyInfoScript
	if level == NoticeProblem {
		script = notifyProblemScript
	}
	cmd := exec.Command("osascript", "-e", script, "--", title, message)
	if err := cmd.Start(); err != nil {
		if logger != nil {
			logger.Printf("notification (%v): %s: %s", level, title, message)
		}
		return
	}
	go func() { _ = cmd.Wait() }() // reap: never leave a zombie behind
}

// notifyOnWindows is a no-op on platforms without a Windows message box;
// fatal startup errors are printed to stderr there instead.
func notifyOnWindows(string, string) {}
