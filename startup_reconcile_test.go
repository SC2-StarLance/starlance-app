//go:build linux

package main

// startup_reconcile_test.go - the launch-time check that the checkbox never
// claims a launch-at-login the OS will not honor: a stale entry is re-pointed
// at the current binary, a removed entry turns the setting off, and a correct
// entry is left untouched.

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func useSettingsPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), SettingsFilename)
	old := settingsPath
	settingsPath = path
	t.Cleanup(func() { settingsPath = old })
	return path
}

func TestReconcileRunAtStartRepointsStaleEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	useLoginArgs(t, "", "")
	useSettingsPath(t)

	path := filepath.Join(home, ".config", "autostart", autostartFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Desktop Entry]\nType=Application\nExec=\"/opt/old/app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{StartWithSystem: true}
	reconcileRunAtStart(cfg, log.New(io.Discard, "", 0))

	if !cfg.StartWithSystem {
		t.Error("setting was turned off instead of re-pointing the entry")
	}
	if !RunAtStartEnabled() {
		t.Error("entry still points at the old binary after reconcile")
	}
}

func TestReconcileRunAtStartHonorsRemoval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	useLoginArgs(t, "", "")
	settings := useSettingsPath(t)

	cfg := &Config{StartWithSystem: true}
	reconcileRunAtStart(cfg, log.New(io.Discard, "", 0))

	if cfg.StartWithSystem {
		t.Error("missing registration still claims launch-at-login is on")
	}
	reloaded, err := LoadConfig(settings)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if reloaded.StartWithSystem {
		t.Error("turned-off setting was not persisted")
	}
	if _, err := os.Stat(autostartEntryAt(home)); !os.IsNotExist(err) {
		t.Error("reconcile re-added the entry the player removed")
	}
}

func autostartEntryAt(home string) string {
	return filepath.Join(home, ".config", "autostart", autostartFilename)
}

func TestReconcileRunAtStartLeavesCorrectEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	useLoginArgs(t, "", "")
	useSettingsPath(t)

	if err := SetRunAtStart(true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(autostartEntryAt(home))
	if err != nil {
		t.Fatal(err)
	}

	cfg := &Config{StartWithSystem: true}
	reconcileRunAtStart(cfg, log.New(io.Discard, "", 0))

	if !cfg.StartWithSystem {
		t.Error("correct registration turned the setting off")
	}
	after, err := os.ReadFile(autostartEntryAt(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("correct entry was rewritten:\nbefore %s\nafter %s", before, after)
	}
}

func TestReconcileRunAtStartSkipsWhenOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	useLoginArgs(t, "", "")
	settings := useSettingsPath(t)

	cfg := &Config{StartWithSystem: false}
	reconcileRunAtStart(cfg, log.New(io.Discard, "", 0))

	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Error("settings were touched for a disabled toggle")
	}
	if _, err := os.Stat(autostartEntryAt(home)); !os.IsNotExist(err) {
		t.Error("an entry was created for a disabled toggle")
	}
}
