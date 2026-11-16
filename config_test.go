package main

import (
	"path/filepath"
	"testing"
)

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

func TestDefaultConfigUsesLocalBackendInDev(t *testing.T) {
	withVersion(t, "dev")
	if got := DefaultConfig().BackendURL; got != devBackendURL {
		t.Fatalf("dev default backend = %q, want %q", got, devBackendURL)
	}
}

func TestDefaultConfigUsesProductionInRelease(t *testing.T) {
	withVersion(t, "1.2.3")
	if got := DefaultConfig().BackendURL; got != defaultBackendURL {
		t.Fatalf("release default backend = %q, want %q", got, defaultBackendURL)
	}
}

// A hand-built binary stamps a dev-prefixed version; it must not send the
// window's map previews (site-relative image paths) to the production site.
func TestDevPrefixedVersionPointsTheWindowAtTheLocalSite(t *testing.T) {
	withVersion(t, "dev-local")
	if !isDevBuild() {
		t.Fatal("dev-local must count as a dev build")
	}
	if got := siteBaseURL(); got != "http://localhost:4321" {
		t.Fatalf("site base = %q, want the local frontend", got)
	}
	withVersion(t, "1.2.3")
	if got := siteBaseURL(); got != "https://starlance.club" {
		t.Fatalf("release site base = %q, want production", got)
	}
}

func TestLoadConfigDevRewritesProductionDefault(t *testing.T) {
	withVersion(t, "dev")
	path := filepath.Join(t.TempDir(), SettingsFilename)
	if err := SaveConfig(path, &Config{BackendURL: defaultBackendURL}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BackendURL != devBackendURL {
		t.Fatalf("dev load backend = %q, want %q", cfg.BackendURL, devBackendURL)
	}
}

func TestLoadConfigReleaseKeepsStoredBackend(t *testing.T) {
	withVersion(t, "1.2.3")
	path := filepath.Join(t.TempDir(), SettingsFilename)
	custom := "https://example.test/api/replays/upload"
	if err := SaveConfig(path, &Config{BackendURL: custom}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BackendURL != custom {
		t.Fatalf("release load backend = %q, want %q", cfg.BackendURL, custom)
	}
}
