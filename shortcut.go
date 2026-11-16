package main

// Shortcut helpers that are pure data, kept outside the Windows-only file so
// the names, arguments and path are testable on every platform.

import (
	"os"
	"path/filepath"
	"strings"
)

// shortcutNameFor is the Start Menu entry name. A named profile gets its own
// shortcut, matching the per-profile login registration, so two accounts do
// not overwrite each other's install.
func shortcutNameFor(profile string) string {
	if profile == "" {
		return "StarLance App"
	}
	return "StarLance App (" + profile + ")"
}

// shortcutArgsFor renders the launch arguments stored in the .lnk: the same
// -profile/-prefix pair the login registration replays, quoted for a Windows
// command line (a prefix can live under "Program Files").
func shortcutArgsFor(profile, prefix string) string {
	var parts []string
	for _, a := range loginArgs(profile, prefix) {
		parts = append(parts, `"`+a+`"`)
	}
	return strings.Join(parts, " ")
}

// startMenuShortcutPath is the per-user Start Menu Programs entry for this
// profile (os.UserConfigDir is %APPDATA% on Windows).
func startMenuShortcutPath(profile string) (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "Microsoft", "Windows", "Start Menu", "Programs", shortcutNameFor(profile)+".lnk"), nil
}
