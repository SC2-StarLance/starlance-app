//go:build darwin

package main

// The macOS install location is ~/Applications for the .app bundle (a copy in
// /Applications is already stable and is left alone), or the app's support
// dir for a bare binary. The bundle is copied whole, not just its executable,
// so resources and Info.plist stay together and the copy is the app the
// player launches from Finder afterwards. A quarantined app in Downloads is
// also subject to App Translocation; both problems go away once the bundle
// lives in ~/Applications.

import (
	"os"
	"path/filepath"
	"strings"
)

func installStableCopy(exe string) (string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	userApps := filepath.Join(home, "Applications")
	if bundle, ok := appBundleRoot(exe); ok {
		if withinDir(exe, userApps) || withinDir(exe, "/Applications") {
			return exe, false, nil
		}
		target := filepath.Join(userApps, filepath.Base(bundle))
		rel, err := filepath.Rel(bundle, exe)
		if err != nil {
			return "", false, err
		}
		targetExe := filepath.Join(target, rel)
		if info, err := os.Stat(targetExe); err == nil && info.Mode().IsRegular() {
			return targetExe, true, nil
		}
		if err := copyTree(bundle, target); err != nil {
			return "", false, err
		}
		return targetExe, true, nil
	}
	target := filepath.Join(home, "Library", "Application Support", "StarLance", "starlance-app")
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

// installLocation reports the app bundle this copy runs from, or "" for a bare
// binary unzipped somewhere (dev build, -portable, a manual binary).
func installLocation() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	bundle, ok := appBundleRoot(exe)
	if !ok {
		return ""
	}
	return bundle
}

// appBundleRoot walks up from the executable to the enclosing .app directory.
// A bare binary has none, which means it runs from wherever it was unpacked.
func appBundleRoot(exe string) (string, bool) {
	dir := filepath.Dir(exe)
	for i := 0; i < 4; i++ {
		if strings.HasSuffix(dir, ".app") {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}
