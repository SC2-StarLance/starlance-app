//go:build linux

package main

// The Linux uninstall removes every StarLance autostart file, the app-menu
// entry and its icon before the cleaner runs. This test pins that sweep,
// including the old names, so a rename cannot leave a login item behind.

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveUninstallRegistrationsLinux(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	config := filepath.Join(base, "config")
	data := filepath.Join(base, "data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_DATA_HOME", data)

	autostart := filepath.Join(config, "autostart")
	apps := filepath.Join(data, "applications")
	icons := filepath.Join(data, "icons", "hicolor", "256x256", "apps")
	for _, dir := range []string{autostart, apps, icons} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ours := []string{
		filepath.Join(autostart, autostartFilename),
		filepath.Join(autostart, autostartFilenameFor("alt")),
		filepath.Join(apps, linuxAppID+".desktop"),
		filepath.Join(icons, linuxAppID+".png"),
		filepath.Join(apps, "StarLance.desktop"),
		filepath.Join(icons, desktopIconOldName),
	}
	for _, path := range ours {
		write(path, "[Desktop Entry]\nIcon="+linuxAppID+"\n")
	}
	foreign := filepath.Join(autostart, "other-app.desktop")
	write(foreign, "[Desktop Entry]\nExec=/opt/other\n")

	if err := removeUninstallRegistrations(log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("removeUninstallRegistrations: %v", err)
	}

	for _, path := range ours {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still present after uninstall: %v", path, err)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign autostart entry was removed: %v", err)
	}
}

func TestUninstallPlatformPathsIncludesWebKitData(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))

	var found bool
	for _, path := range uninstallPlatformPaths() {
		if strings.HasSuffix(path, filepath.Join("StarLance")) {
			found = true
		}
	}
	if !found {
		t.Errorf("uninstallPlatformPaths = %v, want the StarLance WebKit data folder", uninstallPlatformPaths())
	}
}
