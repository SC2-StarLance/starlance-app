//go:build !windows && !darwin

package main

import (
	"log"
	"os"
	"os/exec"
	"strings"
)

// PickFolder asks the user to choose a directory. It tries zenity, then
// kdialog, and returns "" when neither exists or the user cancels — the
// caller falls back to a notification with manual instructions and never
// guesses.
func PickFolder(logger *log.Logger, title string) string {
	return linuxPickFolder(logger, title)
}

// linuxPickFolder tries zenity, then kdialog, and returns "" when neither
// exists (the caller falls back to a notification with manual instructions).
func linuxPickFolder(logger *log.Logger, title string) string {
	if zenity, err := exec.LookPath("zenity"); err == nil {
		out, err := exec.Command(zenity, "--file-selection", "--directory", "--title", title).Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "" // user cancelled
		}
		// A missing display is common for the systemd unit; log and fall through.
		logger.Printf("folder-picker: zenity failed: %v", err)
	}
	if kdialog, err := exec.LookPath("kdialog"); err == nil {
		home, _ := os.UserHomeDir()
		out, err := exec.Command(kdialog, "--getexistingdirectory", home, "--title", title).Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "" // user cancelled
		}
		logger.Printf("folder-picker: kdialog failed: %v", err)
	}
	return ""
}

// PickReplayFile asks the user to choose a .SC2Replay file. It tries zenity,
// then kdialog, and returns "" when neither exists or the user cancels - the
// caller stays quiet instead of guessing.
func PickReplayFile(logger *log.Logger, title string) string {
	if zenity, err := exec.LookPath("zenity"); err == nil {
		out, err := exec.Command(zenity, "--file-selection", "--title", title,
			"--file-filter", "StarCraft II replays | *.SC2Replay *.sc2replay").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "" // user cancelled
		}
		logger.Printf("file-picker: zenity failed: %v", err)
	}
	if kdialog, err := exec.LookPath("kdialog"); err == nil {
		home, _ := os.UserHomeDir()
		out, err := exec.Command(kdialog, "--getopenfilename", home,
			"*.SC2Replay *.sc2replay|StarCraft II replays", "--title", title).Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "" // user cancelled
		}
		logger.Printf("file-picker: kdialog failed: %v", err)
	}
	return ""
}
