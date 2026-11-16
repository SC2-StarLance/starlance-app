package main

// shortcut_test.go - the Start Menu shortcut's name and argument string; the
// .lnk write itself is Windows-only and lives in shortcut_windows.go.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestShortcutNameFor(t *testing.T) {
	if got := shortcutNameFor(""); got != "StarLance App" {
		t.Errorf("default shortcut name = %q", got)
	}
	if got := shortcutNameFor("alt"); got != "StarLance App (alt)" {
		t.Errorf("profile shortcut name = %q", got)
	}
}

func TestShortcutArgsFor(t *testing.T) {
	if got := shortcutArgsFor("", ""); got != "" {
		t.Errorf("default args = %q, want empty", got)
	}
	got := shortcutArgsFor("alt", `C:\Games\battlenet2`)
	want := `"-profile" "alt" "-prefix" "C:\Games\battlenet2"`
	if got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

func TestStartMenuShortcutPath(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("HOME", base)
	path, err := startMenuShortcutPath("alt")
	if err != nil {
		t.Fatalf("startMenuShortcutPath: %v", err)
	}
	if filepath.Base(path) != "StarLance App (alt).lnk" {
		t.Errorf("shortcut file = %q", path)
	}
	if !strings.HasSuffix(path, filepath.Join("Microsoft", "Windows", "Start Menu", "Programs", "StarLance App (alt).lnk")) {
		t.Errorf("shortcut is not under the Start Menu Programs dir: %q", path)
	}
}
