//go:build linux

package main

// relocate_linux_test.go - the Linux target rules: a downloaded binary is
// copied into the XDG data dir once, a binary already there is left alone,
// and an existing install is never overwritten by a second download.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallStableCopyLinux(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	downloads := t.TempDir()
	exe := filepath.Join(downloads, "starlance-app")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	target, moved, err := installStableCopy(exe)
	if err != nil {
		t.Fatalf("installStableCopy: %v", err)
	}
	want := filepath.Join(data, "starlance-app", "starlance-app")
	if !moved || target != want {
		t.Fatalf("installStableCopy = (%q, %v), want (%q, true)", target, moved, want)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "v1" {
		t.Fatalf("installed copy = %q, %v", b, err)
	}

	// The installed copy is stable: no second copy, no handover loop.
	if got, moved, err := installStableCopy(target); err != nil || moved || got != want {
		t.Errorf("installed binary: (%q, %v, %v), want (%q, false, nil)", got, moved, err, want)
	}

	// A second download must not overwrite an install (it may be running).
	if err := os.WriteFile(exe, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, moved, err := installStableCopy(exe); err != nil || !moved || got != want {
		t.Errorf("second download: (%q, %v, %v), want (%q, true, nil)", got, moved, err, want)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "v1" {
		t.Errorf("existing install was overwritten: %q, %v", b, err)
	}
}

func TestStableInstallPathRequiresDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	if _, err := stableInstallPath(); err == nil {
		t.Error("stableInstallPath succeeded without a home or data dir")
	}
}
