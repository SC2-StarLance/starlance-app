//go:build darwin

package main

// macOS side of the Settings uninstall: boot out and delete every StarLance
// LaunchAgent, then let a detached shell remove the app bundle, its state
// folder and the WebKit data after the process exits. A bundle in
// /Applications may be owned by an admin account; when the cleaner cannot
// remove it the files stay in place, but the login item and state are still
// gone.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func removeUninstallRegistrations(logger *log.Logger) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home folder: %w", err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	entries, _ := filepath.Glob(filepath.Join(dir, launchAgentLabel+"*.plist"))
	var firstErr error
	for _, path := range entries {
		label := strings.TrimSuffix(filepath.Base(path), ".plist")
		_ = exec.Command("launchctl", "bootout", launchAgentDomain(os.Getuid())+"/"+label).Run()
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("remove login item: %w", firstErr)
	}
	return nil
}

// uninstallPlatformPaths adds the app bundle (or the bare binary the app
// copied into Application Support) and the WebKit data folder. A bare binary
// launched from Downloads is left where the player put it.
func uninstallPlatformPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	if bundle := installLocation(); bundle != "" {
		out = append(out, bundle)
	} else if exe, err := resolvedExecutable(); err == nil {
		support := filepath.Join(home, "Library", "Application Support", "StarLance")
		if withinDir(exe, support) {
			out = append(out, filepath.Dir(exe))
		}
	}
	out = append(out, filepath.Join(home, "Library", "Application Support", "StarLance"))
	return out
}

// startUninstallCleanup waits for this process to exit, then removes the
// paths. Setsid lets the cleaner outlive the app.
func startUninstallCleanup(paths []string) error {
	const script = `pid=$1; shift
while kill -0 "$pid" 2>/dev/null; do sleep 0.2; done
for path; do
	[ -n "$path" ] && rm -rf -- "$path"
done
`
	args := []string{"-c", script, "starlance-uninstall", strconv.Itoa(os.Getpid())}
	args = append(args, paths...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
