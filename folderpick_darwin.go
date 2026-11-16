//go:build darwin

package main

// Native pickers on macOS are AppleScript's choose folder / choose file,
// driven through osascript. They are part of the OS, so there is nothing to
// install and no fallback ordering to reason about: a cancel (exit 128+ or a
// non-zero status) returns "" and the caller never guesses.

import (
	"log"
	"os/exec"
	"strings"
)

// PickFolder asks the user to choose a directory, returning "" on cancel.
func PickFolder(logger *log.Logger, title string) string {
	out, err := exec.Command("osascript", "-e",
		`POSIX path of (choose folder with prompt `+appleScriptLiteral(title)+`)`).Output()
	if err != nil {
		if !isUserCancel(err) {
			logger.Printf("folder-picker: osascript failed: %v", err)
		}
		return ""
	}
	return strings.TrimSpace(string(out))
}

// PickReplayFile asks the user to choose a .SC2Replay file, returning "" on
// cancel or when the chosen file is not a replay.
func PickReplayFile(logger *log.Logger, title string) string {
	out, err := exec.Command("osascript", "-e",
		`POSIX path of (choose file with prompt `+appleScriptLiteral(title)+`)`).Output()
	if err != nil {
		if !isUserCancel(err) {
			logger.Printf("file-picker: osascript failed: %v", err)
		}
		return ""
	}
	path := strings.TrimSpace(string(out))
	if path == "" || !isReplayName(path) {
		return ""
	}
	return path
}

// isUserCancel reports whether osascript exited because the user dismissed
// the panel: AppleScript maps a cancel to error -128, which osascript
// surfaces as a non-zero exit with "-128" on stderr.
func isUserCancel(err error) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	return strings.Contains(string(exitErr.Stderr), "-128")
}
