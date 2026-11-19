package main

// uninstall.go - the Settings button that removes the app from this PC.
// The window confirms first because the action deletes every StarLance login
// and setting on the machine. Everything the app created outside its running
// process (launch-at-login registration, app-menu entry, Start Menu shortcut)
// is removed before the window closes. The files the running process holds
// (state dir, WebView data, the installed binary or app bundle) are removed by
// a small detached helper after the process has exited, because Windows cannot
// delete a running executable and every platform can keep writing its log
// until the last goroutine stops.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// Uninstall removes the app from this PC. It is the one destructive action in
// the window, so the confirmation lives in the frontend and this method only
// runs after the player has chosen Uninstall there.
func (s *WindowService) Uninstall() error {
	if !s.uninstalling.CompareAndSwap(false, true) {
		return errors.New("StarLance App is already being removed")
	}
	logger := s.logger
	if logger == nil {
		logger = log.Default()
	}

	// Registrations and launchers are safe to remove while the process runs,
	// and a failure here stops the whole action before any stored data goes.
	if err := removeUninstallRegistrations(logger); err != nil {
		s.uninstalling.Store(false)
		return err
	}

	targets, err := uninstallTargets()
	if err != nil {
		s.uninstalling.Store(false)
		return err
	}
	if err := startUninstallCleanup(targets); err != nil {
		s.uninstalling.Store(false)
		return fmt.Errorf("schedule app removal: %w", err)
	}

	logger.Printf("uninstall: removing %d path(s) after exit", len(targets))
	s.Quit()
	return nil
}

// uninstallTargets is the exact list of files the cleanup helper deletes.
// Every path comes from the app's own install and state rules, never from a
// window argument, and a path that would take the player's home folder with
// it is refused before anything is scheduled.
func uninstallTargets() ([]string, error) {
	state, err := appStateRoot()
	if err != nil {
		return nil, fmt.Errorf("resolve app data folder: %w", err)
	}
	paths := append([]string{state}, uninstallPlatformPaths()...)
	return compactUninstallPaths(paths)
}

// appStateRoot is the per-user folder that holds every profile. A named
// profile's appDir is <root>/profiles/<name>, so uninstall walks up to the
// root instead of leaving the other accounts' logins behind.
func appStateRoot() (string, error) {
	dir, err := appDir()
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "profiles" {
		return filepath.Dir(parent), nil
	}
	return dir, nil
}

// compactUninstallPaths normalizes the list, drops empty and duplicate
// entries, removes paths already covered by a parent in the list, and refuses
// relative paths, filesystem roots, the home folder, and any ancestor of home.
func compactUninstallPaths(paths []string) ([]string, error) {
	home, _ := os.UserHomeDir()
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		if err := checkUninstallPath(p, home); err != nil {
			return nil, err
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) < len(out[j]) })
	compact := out[:0]
	for _, p := range out {
		nested := false
		for _, parent := range compact {
			if p == parent || withinDir(p, parent) {
				nested = true
				break
			}
		}
		if !nested {
			compact = append(compact, p)
		}
	}
	if len(compact) == 0 {
		return nil, errors.New("nothing to remove")
	}
	return compact, nil
}

// checkUninstallPath keeps a bug in the platform path rules from turning the
// uninstall button into a home-folder delete. It allows absolute paths outside
// home because a macOS bundle may live in /Applications.
func checkUninstallPath(p, home string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("refusing to remove non-absolute path %q", p)
	}
	if filepath.Dir(p) == p {
		return fmt.Errorf("refusing to remove filesystem root %q", p)
	}
	if home != "" && (p == home || withinDir(home, p)) {
		return fmt.Errorf("refusing to remove %q because it contains the home folder", p)
	}
	if filepath.VolumeName(p) != "" && filepath.Dir(p) == filepath.VolumeName(p)+string(filepath.Separator) {
		return fmt.Errorf("refusing to remove volume root %q", p)
	}
	return nil
}
