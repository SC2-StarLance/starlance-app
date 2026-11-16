package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestAppDirXDGResolution verifies appDir() honors XDG_CONFIG_HOME and falls
// back to $HOME/.config when it is unset. Skipped on Windows, where the
// per-user dir resolves from %APPDATA% instead.
func TestAppDirXDGResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("appDir() uses %APPDATA% on Windows, not XDG_CONFIG_HOME")
	}
	old := appDirOverride
	appDirOverride = ""
	t.Cleanup(func() { appDirOverride = old })

	t.Run("xdg_config_home_set", func(t *testing.T) {
		base := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", base)
		dir, err := appDir()
		if err != nil {
			t.Fatalf("appDir: %v", err)
		}
		if want := filepath.Join(base, "starlance-uploader"); dir != want {
			t.Errorf("appDir = %q, want %q", dir, want)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("appDir must create the directory: stat %s: %v", dir, err)
		}
	})

	t.Run("home_fallback", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", home)
		dir, err := appDir()
		if err != nil {
			t.Fatalf("appDir: %v", err)
		}
		if want := filepath.Join(home, ".config", "starlance-uploader"); dir != want {
			t.Errorf("appDir = %q, want %q", dir, want)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("appDir must create the directory: stat %s: %v", dir, err)
		}
	})
}

// TestTokenPathUsesAppDir verifies the token file resolves inside the per-user
// app dir (overridden to a t.TempDir()).
func TestTokenPathUsesAppDir(t *testing.T) {
	dir := t.TempDir()
	old := appDirOverride
	appDirOverride = dir
	t.Cleanup(func() { appDirOverride = old })

	p, err := TokenPath()
	if err != nil {
		t.Fatalf("TokenPath: %v", err)
	}
	if want := filepath.Join(dir, AuthFilename); p != want {
		t.Errorf("TokenPath = %q, want %q", p, want)
	}
}
