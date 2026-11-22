//go:build linux

package main

// Linux side of the Settings uninstall: remove the XDG autostart entries, the
// app-menu entry and its icon while the process still runs, then let a
// detached shell remove the state folder and the installed copy after exit.
// The installed copy lives in a dedicated data folder, so removing that folder
// also removes an updater's .old leftover.

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
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home folder: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	entries, _ := filepath.Glob(filepath.Join(base, "autostart", "starlance-app*.desktop"))
	var firstErr error
	for _, path := range entries {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}

	data := xdgDataHome()
	if data != "" {
		appsDir := filepath.Join(data, "applications")
		iconDir := filepath.Join(data, "icons", "hicolor", fmt.Sprintf("%dx%d", desktopIconSize, desktopIconSize), "apps")
		for _, path := range []string{
			filepath.Join(appsDir, linuxAppID+".desktop"),
			filepath.Join(iconDir, linuxAppID+".png"),
		} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
		}
		// Older builds used other names. Remove them only when the content is
		// ours, the same guard the startup rewrite uses.
		for _, name := range strings.Split(desktopEntryOldNames, ",") {
			removeStaleDesktopEntry(filepath.Join(appsDir, name), logger)
		}
		removeStaleDesktopIcon(filepath.Join(iconDir, desktopIconOldName), logger)
		refreshIconCache(filepath.Join(data, "icons", "hicolor"), logger)
	}
	if firstErr != nil {
		return fmt.Errorf("remove startup entry: %w", firstErr)
	}
	return nil
}

// uninstallPlatformPaths adds the installed copy's folder and the WebKit data
// the window creates under the XDG data dir. A copy the player launched from a
// download folder is left alone: the app did not install it, and deleting an
// arbitrary launch folder is not this button's call.
func uninstallPlatformPaths() []string {
	var out []string
	if installed := installLocation(); installed != "" {
		out = append(out, filepath.Dir(installed))
	}
	if data := xdgDataHome(); data != "" {
		out = append(out, filepath.Join(data, "StarLance"))
	}
	return out
}

// startUninstallCleanup waits for this process to exit, then removes the
// paths. Setsid detaches it from the window's process group, so quitting the
// app does not kill the cleaner.
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
