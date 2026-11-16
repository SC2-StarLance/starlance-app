//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunAtStartToggle covers the Linux autostart registration end to end:
// enabling writes the .desktop entry pointing at the binary, the state
// query agrees, and disabling removes the entry again.
func TestRunAtStartToggle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	exe, _ := resolvedExecutable()
	if err := SetRunAtStart(true); err != nil {
		t.Fatalf("SetRunAtStart(true): %v", err)
	}
	entry := filepath.Join(home, ".config", "autostart", autostartFilename)
	b, err := os.ReadFile(entry)
	if err != nil {
		t.Fatalf("autostart entry not written: %v", err)
	}
	if !strings.Contains(string(b), `Exec="`+exe+`"`) {
		t.Errorf("entry does not point at the binary:\n%s", b)
	}
	if !RunAtStartEnabled() {
		t.Error("RunAtStartEnabled = false after enabling")
	}

	if err := SetRunAtStart(false); err != nil {
		t.Fatalf("SetRunAtStart(false): %v", err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Errorf("autostart entry still present after disabling: %v", err)
	}
	if RunAtStartEnabled() {
		t.Error("RunAtStartEnabled = true after disabling")
	}

	// Disabling when never enabled is a clean no-op.
	if err := SetRunAtStart(false); err != nil {
		t.Errorf("SetRunAtStart(false) when absent: %v", err)
	}
}

// useLoginArgs points the registration at a named profile with a replay
// prefix, then restores the globals for the rest of the package's tests.
func useLoginArgs(t *testing.T, profile, prefix string) {
	t.Helper()
	oldProfile, oldPrefix := profileName, prefixName
	profileName, prefixName = profile, prefix
	t.Cleanup(func() { profileName, prefixName = oldProfile, oldPrefix })
}

// TestRunAtStartKeepsProfile: a profile instance registers its own entry and
// replays its -profile/-prefix flags, so the login launch resumes that account
// instead of a bare default-profile app.
func TestRunAtStartKeepsProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	useLoginArgs(t, "alt", "/games/prefix two")

	exe, _ := resolvedExecutable()
	if err := SetRunAtStart(true); err != nil {
		t.Fatalf("SetRunAtStart(true): %v", err)
	}
	entry := filepath.Join(home, ".config", "autostart", autostartFilenameFor("alt"))
	b, err := os.ReadFile(entry)
	if err != nil {
		t.Fatalf("profile autostart entry not written: %v", err)
	}
	want := `Exec="` + exe + `" "-profile" "alt" "-prefix" "/games/prefix two"`
	if !strings.Contains(string(b), want) {
		t.Errorf("entry does not replay the profile flags:\n%s", b)
	}
	if !RunAtStartEnabled() {
		t.Error("RunAtStartEnabled = false for the entry it just wrote")
	}

	// The default profile is a different registration: the alt entry must
	// not light up its checkbox.
	profileName = ""
	if RunAtStartEnabled() {
		t.Error("default profile claims the alt profile's entry")
	}
}

// TestRunAtStartIgnoresForeignEntry: another binary's autostart entry must
// not read as ours (the checkbox would lie about launch-at-login).
func TestRunAtStartIgnoresForeignEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	useLoginArgs(t, "", "")

	path := filepath.Join(home, ".config", "autostart", autostartFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[Desktop Entry]\nType=Application\nExec=\"/opt/other/app\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if RunAtStartEnabled() {
		t.Error("RunAtStartEnabled = true for another binary's entry")
	}
}
