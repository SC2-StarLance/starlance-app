package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// File names inside the per-user app dir. Everything the uploader persists
// (auth token, settings, dedup records, log) lives in one per-user directory
// so all state survives reboots and reinstalls regardless of where the binary
// is installed.
const (
	AuthFilename      = "auth.json"
	SettingsFilename  = "settings.json"
	RecordsFilename   = "records.json"
	LogFilename       = "uploader.log"
	ModHashesFilename = "mod-hashes.json"
)

// appDirOverride, when non-empty, redirects appDir() (used by tests to point
// persistence at a t.TempDir()).
var appDirOverride string

// profileName is the -profile value, empty for the default profile. It only
// distinguishes windows and logs; all persistence separation comes from
// appDirOverride.
var profileName string

// prefixName is the -prefix filter this instance was started with (empty when
// unwatched). It rides along in the login registration so a two-account test
// keeps watching the same Wine/Steam prefix after a reboot instead of
// cross-uploading the other account's replays.
var prefixName string

// writeFileAtomic writes b to path with the given perms atomically: the data
// goes to a .tmp sibling first and is renamed into place, so a partial write
// never corrupts the target. The parent directory is created with 0700 when
// missing. This is the single write path for all persisted state files
// (settings.json, records.json, auth.json).
func writeFileAtomic(path string, b []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// appDir returns the per-user app dir that holds all persisted state:
// %APPDATA%\StarLance\uploader on Windows, ~/Library/Application Support/
// StarLance/uploader on macOS, $XDG_CONFIG_HOME/starlance-uploader or
// ~/.config/starlance-uploader on Linux. The directory is created with 0700
// perms when missing; it is the same directory that holds auth.json.
func appDir() (string, error) {
	if appDirOverride != "" {
		return appDirOverride, nil
	}
	var dir string
	switch runtime.GOOS {
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return "", errors.New("paths: APPDATA is not set")
		}
		dir = filepath.Join(appdata, "StarLance", "uploader")
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("paths: cannot resolve home: %w", err)
		}
		dir = filepath.Join(home, "Library", "Application Support", "StarLance", "uploader")
	default:
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("paths: cannot resolve home: %w", err)
			}
			base = filepath.Join(home, ".config")
		}
		dir = filepath.Join(base, "starlance-uploader")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("paths: create app dir %s: %w", dir, err)
	}
	return dir, nil
}
