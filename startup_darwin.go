//go:build darwin

package main

// macOS launch-at-login via a per-user LaunchAgent in ~/Library/LaunchAgents.
// The plist's presence is the registration state (like the Linux autostart
// entry); enabling also bootstraps it into the current GUI session so the
// toggle takes effect without a re-login. A named profile gets its own label
// and plist, so two test accounts never fight over one entry.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabelFor(profileName)+".plist"), nil
}

func setRunAtStart(exe string, enable bool) error {
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if !enable {
		// Best effort: an agent that was never bootstrapped, or one already
		// gone, must not stop the toggle from clearing the file.
		_ = exec.Command("launchctl", "bootout", launchAgentService(os.Getuid(), profileName)).Run()
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(launchAgentPlist(exe, profileName, prefixName)), 0o644); err != nil {
		return err
	}
	// Take effect now. A second bootstrap of the same label fails with
	// "service already loaded", which is exactly the desired end state.
	_ = exec.Command("launchctl", "bootstrap", launchAgentDomain(os.Getuid()), path).Run()
	return nil
}

func runAtStartEnabled() bool {
	path, err := launchAgentPath()
	if err != nil {
		return false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	exe, err := resolvedExecutable()
	if err != nil {
		return false
	}
	// Registered only when the entry points at this binary, so a moved app
	// does not keep launching a stale path.
	return strings.Contains(string(body), "<string>"+xmlEscape(exe)+"</string>")
}

// runAtStartPath reports the binary the current profile's LaunchAgent
// launches and whether a plist exists at all. An unparsable plist still
// counts as found, so startup rewrites it instead of assuming the player
// removed the registration.
func runAtStartPath() (string, bool) {
	path, err := launchAgentPath()
	if err != nil {
		return "", false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	arg, ok := plistFirstProgramArg(string(body))
	if !ok {
		return "", true
	}
	return arg, true
}
