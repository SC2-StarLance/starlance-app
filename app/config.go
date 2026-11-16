package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Config holds the user-configurable settings for the uploader. It is
// persisted as settings.json inside the per-user app dir (see paths.go).
//
// The API key is NOT stored here: it is issued by the device-flow login and
// persisted only in the 0600 auth token file (see auth.go).
type Config struct {
	BackendURL string `json:"backend_url"`
	ReplayPath string `json:"replay_path,omitempty"`
	// HeartbeatSeconds is the /uploader/heartbeat period (seconds). Empty/0
	// means the built-in default of 30s (see heartbeat.go); values below the
	// 5s minimum are clamped.
	HeartbeatSeconds int `json:"heartbeat_seconds,omitempty"`
	// StartWithSystem launches the uploader when the player logs in (Windows
	// Run key / XDG autostart). Toggled from the tray menu; persisted like
	// every other setting. See startup.go.
	StartWithSystem bool `json:"start_with_system,omitempty"`
	// SetupDone hides the first-run three-step guide once the player has read
	// it. Only the window writes it; a fresh install shows the guide.
	SetupDone bool `json:"setup_done,omitempty"`
}

// defaultBackendURL is the production upload endpoint; auth and liveness
// endpoints are derived from its scheme+host (see Config.BaseURL). Dev builds
// talk to the local backend instead, so a binary built without a release
// version never hits production by accident.
const (
	defaultBackendURL = "https://starlance.club/api/replays/upload"
	devBackendURL     = "http://localhost:8080/api/replays/upload"
)

// isDevBuild reports whether this binary is a local build. Release builds
// stamp main.Version through -ldflags (see the Makefile and release.yml); a
// `dev` prefix (dev, dev-local, dev-<branch>) always means a local build, so a
// hand-built binary never points its window at the production site.
func isDevBuild() bool {
	return Version == "" || strings.HasPrefix(Version, "dev")
}

// DefaultConfig returns the backend URL for this build; the replay path is
// filled by DetectReplayDir at first run (or left for the user to set).
func DefaultConfig() *Config {
	if isDevBuild() {
		return &Config{BackendURL: devBackendURL}
	}
	return &Config{BackendURL: defaultBackendURL}
}

// LoadConfig reads the config JSON at path. If the file does not exist it
// writes out a default config and returns it, so a first launch goes straight
// into the tray - the "Not logged in" startup toast and the folder picker
// (when detection fails) carry the onboarding instead of a
// quit-and-relaunch step. The replay folder set is NOT persisted here:
// account-folder detection is cheap and re-runs at every startup, so folders
// that appear later (a new alt account) are picked up without a config edit.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		var cfg Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		// A dev build never talks to production. A settings file written by a
		// released build carries the production default, not a player choice,
		// so point it at the local backend.
		if isDevBuild() && cfg.BackendURL == defaultBackendURL {
			cfg.BackendURL = devBackendURL
		}
		return &cfg, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := DefaultConfig()
	if err := SaveConfig(path, cfg); err != nil {
		return nil, fmt.Errorf("write config %s: %w", path, err)
	}
	return cfg, nil
}

// SaveConfig persists cfg as JSON at path with 0600 perms, atomically
// (writeFileAtomic: temp file + rename) so a partial write never corrupts the
// settings. The API key is never part of Config, so it can never end up in
// this file.
func SaveConfig(path string, cfg *Config) error {
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'), 0o600)
}

// validateAndWarn performs startup sanity checks that log warnings but do not
// prevent the program from running. It warns when an http:// backend is used
// alongside a stored API key, since the key would be sent in plaintext.
func validateAndWarn(cfg *Config, auth *AuthState, logger *log.Logger) {
	if cfg == nil || cfg.BackendURL == "" {
		return
	}
	parsed, err := url.Parse(cfg.BackendURL)
	if err != nil {
		return
	}
	if parsed.Scheme != "http" {
		return
	}
	if auth != nil && auth.LoggedIn() {
		logger.Println("WARNING: backend_url is http (not https); the API key will be sent in plaintext.")
	}
}

// BaseURL derives the API base (scheme + host, plus the /api path prefix when
// the configured upload endpoint lives under one) from BackendURL. The auth
// and liveness endpoints are addressed relative to it, keeping the upload
// endpoint's semantics unchanged.
func (c *Config) BaseURL() string {
	parsed, err := url.Parse(c.BackendURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	base := parsed.Scheme + "://" + parsed.Host
	if strings.HasPrefix(strings.TrimPrefix(parsed.Path, "/"), "api/") || strings.TrimPrefix(parsed.Path, "/") == "api" {
		base += "/api"
	}
	return base
}

// DetectReplayDirs locates every StarCraft II multiplayer replays folder on
// the machine, most recently modified first. SC2 gives each Battle.net
// account/toon its own Accounts subtree, so watching only the newest folder
// would silently lose games played on an alt - every candidate is returned.
// The result is empty when no location can be determined; the caller must
// then ask the user (folder picker) instead of guessing.
//
// On Windows it looks under the known Documents folder (SHGetKnownFolderPath,
// with the literal 'Documents'/'My Documents' home subdirectories as
// fallback) at Documents\StarCraft II\Accounts\*\Replays\Multiplayer. On
// Linux it probes common Wine/Proton prefixes plus the native XDG Documents
// locations. On macOS the Blizzard support folder and ~/Documents are probed.
func DetectReplayDirs() []string {
	switch runtime.GOOS {
	case "windows":
		return detectWindowsReplayDirs()
	case "darwin":
		return detectDarwinReplayDirs()
	}
	return detectLinuxReplayDirs()
}

func detectWindowsReplayDirs() []string {
	var dirs []string
	// Preferred: the real Documents folder even when relocated.
	if docs := windowsDocumentsDir(); docs != "" {
		dirs = append(dirs, replayDirsUnder(filepath.Join(docs, "StarCraft II", "Accounts"))...)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return normalizeDirs(dirs)
	}
	// Conservative fallback: only 'Documents' and 'My Documents' are checked so
	// anything else falls through to the folder picker.
	for _, docs := range []string{"Documents", "My Documents"} {
		dirs = append(dirs, replayDirsUnder(filepath.Join(home, docs, "StarCraft II", "Accounts"))...)
	}
	return normalizeDirs(dirs)
}

// detectDarwinReplayDirs finds the macOS install's replays: Blizzard keeps
// them under ~/Library/Application Support/Blizzard/StarCraft II/Accounts
// (same Accounts/<account>/<toon>/Replays/Multiplayer depth as Windows); the
// Documents location covers manual/copied installs.
func detectDarwinReplayDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var dirs []string
	for _, base := range []string{
		filepath.Join(home, "Library", "Application Support", "Blizzard", "StarCraft II", "Accounts"),
		filepath.Join(home, "Documents", "StarCraft II", "Accounts"),
	} {
		dirs = append(dirs, replayDirsUnder(base)...)
	}
	// Mac players increasingly run the Windows client through Whisky or
	// CrossOver; both keep a Wine prefix with the same Documents layout.
	for _, pattern := range []string{
		filepath.Join(home, "Library", "Containers", "com.isaacmarovitz.Whisky", "Data",
			"Library", "Application Support", "Whisky", "Bottles", "*"),
		filepath.Join(home, "Library", "Application Support", "CrossOver", "Bottles", "*"),
	} {
		prefixes, _ := filepath.Glob(pattern)
		for _, prefix := range prefixes {
			dirs = append(dirs, replayDirsInWinePrefix(prefix)...)
		}
	}
	return normalizeDirs(dirs)
}

// replayDirsUnder returns every Replays/Multiplayer directory under an
// Accounts base, most recently modified first. SC2 nests it one or two
// levels deep: Accounts/<account>/Replays/Multiplayer (legacy) or, on every
// current install, Accounts/<account>/<toon like 2-S2-1-3430393>/Replays/
// Multiplayer - the toon level was originally missed, so detection never
// matched a real install.
func replayDirsUnder(accountsBase string) []string {
	candidates, _ := filepath.Glob(filepath.Join(accountsBase, "*", "Replays", "Multiplayer"))
	toons, _ := filepath.Glob(filepath.Join(accountsBase, "*", "*", "Replays", "Multiplayer"))
	candidates = append(candidates, toons...)

	type entry struct {
		dir string
		mod int64
	}
	var found []entry
	for _, multi := range candidates {
		info, err := os.Stat(multi)
		if err != nil || !info.IsDir() {
			continue
		}
		found = append(found, entry{dir: multi, mod: info.ModTime().UnixNano()})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod > found[j].mod })
	dirs := make([]string, 0, len(found))
	for _, e := range found {
		dirs = append(dirs, e.dir)
	}
	return dirs
}

// steamLibraryPathRe matches a "path" value inside libraryfolders.vdf, the
// Valve text format Steam writes (quoted key, quoted value, any indentation).
var steamLibraryPathRe = regexp.MustCompile(`"path"\s*"([^"]+)"`)

// candidatePrefixDirs enumerates likely Wine/Proton prefix roots on Linux:
// a plain ~/.wine, ~/Games itself, each Lutris game prefix (Lutris lays
// games out as ~/Games/<game>/drive_c, e.g. ~/Games/battlenet), Heroic and
// Bottles' default layouts, native Steam compatdata, the Flatpak Steam
// compatdata, and every additional Steam library listed in
// libraryfolders.vdf (games on a second drive are the common case).
func candidatePrefixDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dirs := []string{
		filepath.Join(home, ".wine"),
		filepath.Join(home, "Games"),
	}
	for _, pattern := range []string{
		filepath.Join(home, "Games", "*"),
		filepath.Join(home, "Games", "Heroic", "Prefixes", "*"),
		filepath.Join(home, "Games", "Heroic", "Bottles", "*"),
		filepath.Join(home, ".local", "share", "bottles", "bottles", "*"),
		filepath.Join(home, ".local", "share", "Steam", "steamapps", "compatdata", "*", "pfx"),
		filepath.Join(home, ".steam", "steam", "steamapps", "compatdata", "*", "pfx"),
		filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share",
			"Steam", "steamapps", "compatdata", "*", "pfx"),
	} {
		matches, _ := filepath.Glob(pattern)
		dirs = append(dirs, matches...)
	}
	for _, lib := range steamLibraryRoots(home) {
		matches, _ := filepath.Glob(filepath.Join(lib, "steamapps", "compatdata", "*", "pfx"))
		dirs = append(dirs, matches...)
	}
	return dirs
}

// steamLibraryRoots returns the Steam install roots to search for compatdata:
// the native installs, the Flatpak install, and every library path listed in
// a libraryfolders.vdf (a game installed on /mnt/games lives there, not under
// ~/.local/share/Steam).
func steamLibraryRoots(home string) []string {
	roots := []string{
		filepath.Join(home, ".local", "share", "Steam"),
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
	}
	for _, root := range append([]string(nil), roots...) {
		body, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(body), "\n") {
			m := steamLibraryPathRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if path := m[1]; path != "" {
				roots = append(roots, path)
			}
		}
	}
	return roots
}

// replayDirsInWinePrefix returns the replay folders under every Wine user dir
// of a prefix. The user dir name depends on how the prefix was created
// (user, steamuser, crossover, the host user name), so the whole
// drive_c/users list is scanned instead of guessing names.
func replayDirsInWinePrefix(prefix string) []string {
	users, _ := filepath.Glob(filepath.Join(prefix, "drive_c", "users", "*"))
	dirs := make([]string, 0, len(users))
	for _, user := range users {
		dirs = append(dirs, replayDirsUnder(filepath.Join(user, "Documents", "StarCraft II", "Accounts"))...)
	}
	return dirs
}

func detectLinuxReplayDirs() []string {
	var dirs []string
	for _, prefix := range candidatePrefixDirs() {
		dirs = append(dirs, replayDirsInWinePrefix(prefix)...)
	}
	// Native/XDG Documents locations (e.g. replays synced outside a Wine prefix).
	home, err := os.UserHomeDir()
	if err == nil {
		for _, docs := range []string{"Documents", "My Documents"} {
			dirs = append(dirs, replayDirsUnder(filepath.Join(home, docs, "StarCraft II", "Accounts"))...)
		}
	}
	return normalizeDirs(dirs)
}
