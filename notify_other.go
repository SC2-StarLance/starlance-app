//go:build !windows && !darwin

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// Notify sends a desktop notification via notify-send when present, falling
// back to the log file otherwise. Problems are raised as critical (usually
// persistent) toasts; infos auto-expire after a few seconds. The app name
// and tray icon make the toast recognizably StarLance at a glance.
func Notify(logger *log.Logger, level NoticeLevel, title, message string) {
	if send, err := exec.LookPath("notify-send"); err == nil {
		args := []string{"-a", "StarLance App"}
		if level == NoticeProblem {
			args = append(args, "-u", "critical")
		} else {
			args = append(args, "-t", "6000")
		}
		if icon := trayIconPath(logger); icon != "" {
			args = append(args, "-i", icon)
		}
		args = append(args, title, message)
		if cmd := exec.Command(send, args...); cmd.Start() == nil {
			// Reap the child: an un-waited notify-send would linger as a
			// zombie for the lifetime of this long-running tray app.
			go func() { _ = cmd.Wait() }()
			return
		}
	}
	if logger != nil {
		logger.Printf("notification (%v): %s: %s", level, title, message)
	}
}

// trayIconPath writes the rendered tray icon next to the app state (once) so
// desktop toasts can show the StarLance logo; returns "" when unavailable.
func trayIconPath(logger *log.Logger) string {
	dir, err := appDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "icon.png")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if err := os.WriteFile(p, encodePNG(48), 0o644); err != nil {
		if logger != nil {
			logger.Printf("notification: could not write toast icon: %v", err)
		}
		return ""
	}
	return p
}

// notifyOnWindows is a no-op on platforms without a Windows message box;
// fatal startup errors are printed to stderr there instead.
func notifyOnWindows(string, string) {}
