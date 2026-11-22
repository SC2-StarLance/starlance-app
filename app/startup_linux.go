//go:build linux

package main

// Linux launch-at-login via the freedesktop autostart convention
// (~/.config/autostart/*.desktop), honored by KDE, GNOME and friends. The
// file's presence is the registration state; disabling removes it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// autostartFilename is the .desktop entry name; a stable name so toggling
// never accumulates stale entries.
const autostartFilename = "starlance-app.desktop"

// autostartFilenameFor suffixes the entry for -profile instances, so two
// accounts keep their own login registration instead of overwriting one
// shared file.
func autostartFilenameFor(profile string) string {
	if profile == "" {
		return autostartFilename
	}
	return "starlance-app-" + profile + ".desktop"
}

func autostartPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "autostart", autostartFilenameFor(profileName)), nil
}

// autostartExec is the Exec value: the resolved binary plus the flags that
// pick this instance's profile and replay prefix. Desktop entries parse the
// line like a shell command, so every argument is quoted.
func autostartExec(exe string) string {
	parts := []string{fmt.Sprintf("%q", exe)}
	for _, a := range loginArgs(profileName, prefixName) {
		parts = append(parts, fmt.Sprintf("%q", a))
	}
	return strings.Join(parts, " ")
}

func setRunAtStart(exe string, enable bool) error {
	path, err := autostartPath()
	if err != nil {
		return err
	}
	if !enable {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	entry := fmt.Sprintf(
		"[Desktop Entry]\n"+
			"Type=Application\n"+
			"Name=StarLance App\n"+
			"Comment=Uploads StarLance ladder replays automatically\n"+
			"Exec=%s\n"+
			"NoDisplay=true\n", autostartExec(exe))
	if err := writeFileAtomic(path, []byte(entry), 0o644); err != nil {
		return err
	}
	return nil
}

func runAtStartEnabled() bool {
	path, err := autostartPath()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	exe, err := resolvedExecutable()
	if err != nil {
		return false
	}
	// Registered only when the entry replays this exact binary and profile,
	// so a moved binary or another account's entry never counts as ours.
	return strings.Contains(string(b), "Exec="+autostartExec(exe)+"\n")
}

// runAtStartPath reports the binary the current profile's entry launches and
// whether an entry exists at all. The entry existing with an unparsable Exec
// still counts as found, so startup rewrites it instead of assuming the player
// removed the registration.
func runAtStartPath() (string, bool) {
	path, err := autostartPath()
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "Exec=") {
			continue
		}
		arg, ok := firstExecArg(strings.TrimPrefix(line, "Exec="))
		if !ok {
			return "", true
		}
		return arg, true
	}
	return "", true
}
