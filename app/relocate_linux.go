//go:build linux

package main

// The Linux install location is the app's own data dir,
// $XDG_DATA_HOME/starlance-uploader (default ~/.local/share/...). The desktop
// entry written by desktop_linux.go is the launcher, so this does not need to
// be on PATH; one folder also means one folder to remove. A binary already
// inside it (a self-update swap, or a player who put it there) is left alone.

import (
	"errors"
	"os"
	"path/filepath"
)

func stableInstallPath() (string, error) {
	data := xdgDataHome()
	if data == "" {
		return "", errors.New("cannot resolve the XDG data dir")
	}
	return filepath.Join(data, "starlance-uploader", "sc2-replay-uploader"), nil
}

// installLocation reports the binary this copy installed itself as, or ""
// when it runs from wherever the player downloaded it (dev build, -portable,
// or a copy that has not relocated yet).
func installLocation() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	target, err := stableInstallPath()
	if err != nil {
		return ""
	}
	if !withinDir(exe, filepath.Dir(target)) {
		return ""
	}
	return target
}

// installStableCopy returns the stable binary path and whether the caller
// must hand over to it. An existing copy is never overwritten: it may be the
// running instance, and a second launch must not replace a live binary.
func installStableCopy(exe string) (string, bool, error) {
	target, err := stableInstallPath()
	if err != nil {
		return "", false, err
	}
	if withinDir(exe, filepath.Dir(target)) {
		return target, false, nil
	}
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
		return target, true, nil
	}
	if err := copyExecutable(exe, target); err != nil {
		return "", false, err
	}
	return target, true, nil
}
