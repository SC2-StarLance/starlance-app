//go:build windows

package main

// The Windows install location is %LOCALAPPDATA%\Programs\StarLance
// (os.UserCacheDir is %LOCALAPPDATA%), the per-user programs folder players
// and uninstall instructions expect.
// The website hands out a bare .exe, which lands in Downloads; browsers and
// cleanup tools delete that folder, so the first launch copies itself here
// and the Start Menu shortcut plus the Run-key registration point at the
// copy. A copy that already exists is never overwritten: it may be the
// running instance, and self-update owns replacing it.

import (
	"os"
	"path/filepath"
)

func stableInstallPath() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "Programs", "StarLance", "sc2-replay-uploader.exe"), nil
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
