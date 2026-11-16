//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopEntryNamesTheAppAndQuotesExec(t *testing.T) {
	entry := desktopEntry("/home/player/Games/Star Lance/uploader")
	if !strings.Contains(entry, "Name=StarLance App\n") {
		t.Errorf("entry must name the app in full:\n%s", entry)
	}
	if !strings.Contains(entry, "Icon="+linuxAppID+"\n") {
		t.Errorf("entry icon key must match the installed icon name:\n%s", entry)
	}
	if !strings.Contains(entry, "StartupWMClass="+linuxAppID+"\n") {
		t.Errorf("entry must declare the window class:\n%s", entry)
	}
	if !strings.Contains(entry, `Exec="/home/player/Games/Star Lance/uploader"`) {
		t.Errorf("exec path with a space must stay one quoted argument:\n%s", entry)
	}
}

func TestDesktopExecArgEscapesReservedCharacters(t *testing.T) {
	if got, want := desktopExecArg("/tmp/a b"), `"/tmp/a b"`; got != want {
		t.Errorf("desktopExecArg = %s, want %s", got, want)
	}
	escaped := desktopExecArg(`a"b$c\d` + "`e`")
	for _, want := range []string{`\"`, `\$`, `\\`, "\\`"} {
		if !strings.Contains(escaped, want) {
			t.Errorf("desktopExecArg must escape %s: %s", want, escaped)
		}
	}
}

func TestWriteIfChangedWritesOnceThenSkips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "entry.desktop")
	changed, err := writeIfChanged(path, []byte("one"), 0o644)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	changed, err = writeIfChanged(path, []byte("one"), 0o644)
	if err != nil || changed {
		t.Fatalf("identical content must not rewrite: changed=%v err=%v", changed, err)
	}
	changed, err = writeIfChanged(path, []byte("two"), 0o644)
	if err != nil || !changed {
		t.Fatalf("changed content must rewrite: changed=%v err=%v", changed, err)
	}
}

func TestInstallDesktopIntegrationWritesEntryAndIcon(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	installDesktopIntegration(testLogger())

	entryPath := filepath.Join(data, "applications", linuxAppID+".desktop")
	b, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("desktop entry not written: %v", err)
	}
	if !strings.Contains(string(b), "Icon="+linuxAppID+"\n") {
		t.Errorf("entry must reference the installed icon:\n%s", b)
	}

	iconPath := filepath.Join(data, "icons", "hicolor", "256x256", "apps", linuxAppID+".png")
	icon, err := os.ReadFile(iconPath)
	if err != nil {
		t.Fatalf("icon not written: %v", err)
	}
	if len(icon) < 8 || string(icon[1:4]) != "PNG" {
		t.Errorf("icon is not a PNG")
	}
}

func TestStaleEntryIsOnlyRemovedWhenItIsOurs(t *testing.T) {
	apps := t.TempDir()
	ours := filepath.Join(apps, "StarLance.desktop")
	if err := os.WriteFile(ours, []byte("[Desktop Entry]\nName=StarLance\nIcon=starlance-uploader\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(apps, "other.desktop")
	if err := os.WriteFile(theirs, []byte("[Desktop Entry]\nName=Other\nIcon=some-other-icon\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	removeStaleDesktopEntry(ours, testLogger())
	removeStaleDesktopEntry(theirs, testLogger())

	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Errorf("our stale entry must be removed, stat err=%v", err)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("another app's entry must be left alone: %v", err)
	}
}
