package main

// Tests for the path rules behind the Settings uninstall. The platform
// removal itself is covered per OS; these tests pin the refusals, because a
// bug here would delete a folder the player did not choose.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckUninstallPathRefusesDanger(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"state folder under home", filepath.Join(home, "state"), false},
		{"app bundle on macOS", filepath.Join(base, "Applications", "StarLance App.app"), false},
		{"home folder", home, true},
		{"ancestor of home", base, true},
		{"relative path", filepath.Join("starlance-uploader"), true},
		{"filesystem root", string(filepath.Separator), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkUninstallPath(tc.path, home)
			if tc.wantErr && err == nil {
				t.Errorf("checkUninstallPath(%q) = nil, want an error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("checkUninstallPath(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// TestCompactUninstallPathsDedupesAndDropsNested: the state root plus a
// profile folder is one removal, and duplicates must not make the cleaner
// walk the same tree twice.
func TestCompactUninstallPathsDedupesAndDropsNested(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	nested := filepath.Join(state, "profiles", "main")

	got, err := compactUninstallPaths([]string{state, nested, state, ""})
	if err != nil {
		t.Fatalf("compactUninstallPaths: %v", err)
	}
	if len(got) != 1 || got[0] != state {
		t.Errorf("compactUninstallPaths = %v, want [%s]", got, state)
	}
}

func TestCompactUninstallPathsRefusesEmpty(t *testing.T) {
	if _, err := compactUninstallPaths([]string{"", ""}); err == nil {
		t.Error("compactUninstallPaths([]) = nil error, want a refusal")
	}
}

// TestAppStateRootWalksOutOfProfiles: a named profile's appDir is
// <root>/profiles/<name>, so uninstall must remove the root and every other
// account's login with it.
func TestAppStateRootWalksOutOfProfiles(t *testing.T) {
	root := t.TempDir()
	old := appDirOverride
	t.Cleanup(func() { appDirOverride = old })

	appDirOverride = filepath.Join(root, "profiles", "main")
	got, err := appStateRoot()
	if err != nil {
		t.Fatalf("appStateRoot: %v", err)
	}
	if got != root {
		t.Errorf("appStateRoot profile = %q, want %q", got, root)
	}

	appDirOverride = filepath.Join(root, "plain")
	got, err = appStateRoot()
	if err != nil {
		t.Fatalf("appStateRoot plain: %v", err)
	}
	if want := filepath.Join(root, "plain"); got != want {
		t.Errorf("appStateRoot plain = %q, want %q", got, want)
	}
}

// TestUninstallPlatformPathsAreAbsolute: whatever the OS rules return must be
// an absolute path, because the cleaner passes each one to rm -rf or
// Remove-Item.
func TestUninstallPlatformPathsAreAbsolute(t *testing.T) {
	for _, path := range uninstallPlatformPaths() {
		if !filepath.IsAbs(path) {
			t.Errorf("uninstallPlatformPaths returned non-absolute path %q", path)
		}
		if runtime.GOOS != "windows" && filepath.Clean(path) == "/" {
			t.Errorf("uninstallPlatformPaths returned the filesystem root")
		}
	}
}
