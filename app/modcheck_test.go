package main

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// joinQuoted renders hashes as a JSON string array body.
func joinQuoted(hashes []string) string {
	out := ""
	for i, h := range hashes {
		if i > 0 {
			out += ", "
		}
		out += `"` + h + `"`
	}
	return out
}

// TestModHashCacheRefreshAndDegrade covers the fetch/persist/degrade cycle:
// a successful refresh grows the union cache and persists it; a dead
// backend keeps the last known hashes (never failure).
func TestModHashCacheRefreshAndDegrade(t *testing.T) {
	var served []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"accepted_hashes": [` + joinQuoted(served) + `]}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), ModHashesFilename)
	c := NewModHashCache(path, srv.URL, testLogger())
	if c.Active() {
		t.Fatal("empty cache must be inactive")
	}

	served = []string{"aaaaaaaa", "bbbbbbbb"}
	c.refresh(context.Background())
	if !c.Active() || c.ContainsAny([]string{"aaaaaaaa"}) != true {
		t.Fatalf("cache did not learn served hashes")
	}
	if c.ContainsAny([]string{"zzzzzzzz"}) {
		t.Error("unknown hash must not match")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("cache not persisted: %v", err)
	}

	// Backend dies: the cache survives (degrade to cache, never to failure).
	srv.Close()
	c.refresh(context.Background())
	if !c.Active() || !c.ContainsAny([]string{"bbbbbbbb"}) {
		t.Error("refresh failure must keep the last known hashes")
	}
}

// TestModHashCacheUnionPersistsAcrossRestart: the cache is a union of
// everything ever accepted, so an outdated StarLance mod (hash rotated out
// of accepted_hashes) still matches after a restart and keeps uploading —
// the backend then gives the player the precise "mod outdated" guidance.
func TestModHashCacheUnionPersistsAcrossRestart(t *testing.T) {
	var served []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"accepted_hashes": [` + joinQuoted(served) + `]}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), ModHashesFilename)
	served = []string{"old-hash"}
	NewModHashCache(path, srv.URL, testLogger()).refresh(context.Background())

	// The mod rotates; a fresh process loads the persisted union and then
	// refreshes, learning the new hash without forgetting the old one.
	served = []string{"new-hash"}
	c2 := NewModHashCache(path, srv.URL, testLogger())
	c2.refresh(context.Background())
	if !c2.ContainsAny([]string{"old-hash"}) {
		t.Error("union cache must remember rotated-out StarLance hashes")
	}
	if !c2.ContainsAny([]string{"new-hash"}) {
		t.Error("union cache must accept the new hash")
	}
}

// TestClassifyStarLanceFailOpen: the probe can only save a roundtrip, never
// gate an upload. Unknown cases must always report "upload as before".
func TestClassifyStarLanceFailOpen(t *testing.T) {
	fixture := fixtureReplayPath

	cases := []struct {
		name  string
		cache *ModHashCache
		path  string
		want  bool // true = confidently not a StarLance game
	}{
		{"nil cache", nil, fixture, false},
		{"empty cache", NewModHashCache(filepath.Join(t.TempDir(), "m.json"), "", testLogger()), fixture, false},
		{"accepted mod present", cacheWith("73326d610000434e421c8aa0f3619b652d23a2735dfee812ab644228235e7a797edecfe8b67da30e"), fixture, false},
		{"outdated but known mod", cacheWith("73326d610000434e6de41503baccd05656360b6f027db88169fa1989bb6357b1b215a2547939f5fb"), fixture, false},
		// First game after a mod publish: the mod tail is unknown, but the
		// map marker (bound map hash from map_hashes) passes it through to
		// the backend's depot-verified auto-adoption (decision 2026-09-08).
		{"known map marker, unknown mod", cacheWith("73326d610000434e214fff52a60a3a36de01bce6a246a59509cb400bb6e8e95e831725e955c57429"), fixture, false},
		// Tail comparison: the cache holds the 64-hex tail, the scan yields
		// the full 80-char handle - the region-coded prefix never matches.
		{"map marker as bare tail", cacheWith("214fff52a60a3a36de01bce6a246a59509cb400bb6e8e95e831725e955c57429"), fixture, false},
		{"foreign hashes only", cacheWith("00000000000000000000000000000000000000000000000000000000000000000000000000000000"), fixture, true},
		{"garbage replay", cacheWith("aaaa"), fixtureReplayPath + ".missing", false},
	}
	for _, tc := range cases {
		if got := classifyStarLance(tc.cache, tc.path); got != tc.want {
			t.Errorf("%s: classifyStarLance = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func cacheWith(hashes ...string) *ModHashCache {
	c := &ModHashCache{
		seen:   map[string]struct{}{},
		logger: log.New(os.Stderr, "", 0),
		path:   filepath.Join(os.TempDir(), "unused.json"),
	}
	for _, h := range hashes {
		c.seen[hashTail(h)] = struct{}{}
	}
	return c
}

// TestHandleFile_NonStarLanceReplaySkipsUploadAndSignals is the e2e
// pre-detection behavior: a replay whose handles include no known StarLance
// mod hash never reaches the upload endpoint (no roundtrip, no rejection
// noise), but still feeds the double-queue gate — with the queue idle, the
// blizzard-game-started signal fires exactly once, replacing the backend
// rejection evidence the gate used to rely on.
func TestHandleFile_NonStarLanceReplaySkipsUploadAndSignals(t *testing.T) {
	uploads, signals := 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		signals++
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "searching", "counters": {"in_queue": 1, "live_games": 0, "online": 0}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		uploads++
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	auth := testAuth(t, "test-key", "player-1")
	mods := cacheWith("00000000000000000000000000000000000000000000000000000000000000000000000000000000")
	up := NewUploader(&Config{BackendURL: srv.URL}, auth, LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger()),
		NewStatus(StatusWatching), nil, mods, testLogger(), make(chan string))
	up.handleFile(fixtureReplayPath)

	if uploads != 0 {
		t.Errorf("non-StarLance replay must not be uploaded; got %d uploads", uploads)
	}
	if signals != 1 {
		t.Errorf("expected exactly 1 blizzard-game-started signal, got %d", signals)
	}
}

// TestHandleFile_KnownModReplayStillUploads: a replay carrying a known
// StarLance mod hash (current or outdated — the cache is a union) is never
// skipped; the backend stays the only source of truth.
func TestHandleFile_KnownModReplayStillUploads(t *testing.T) {
	uploads, signals := 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		signals++
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"state": "searching"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		uploads++
		w.WriteHeader(http.StatusAccepted) // 202: pending, stays silent
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	auth := testAuth(t, "test-key", "player-1")
	mods := cacheWith("73326d610000434e421c8aa0f3619b652d23a2735dfee812ab644228235e7a797edecfe8b67da30e")
	up := NewUploader(&Config{BackendURL: srv.URL}, auth, LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger()),
		NewStatus(StatusWatching), nil, mods, testLogger(), make(chan string))
	up.handleFile(fixtureReplayPath)

	if uploads != 1 {
		t.Errorf("replay with a known StarLance mod hash must upload; got %d uploads", uploads)
	}
	if signals != 0 {
		t.Errorf("uploaded replay must not trigger the blizzard signal; got %d", signals)
	}
}

// TestModHashCacheMapHashesUnion: GET /mod-versions also returns the bound
// map content hashes (map_hashes); they merge into the same union cache -
// map markers are what pass the first game after a mod publish through
// pre-detection to the backend's auto-adoption.
func TestModHashCacheMapHashesUnion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"accepted_hashes": [], "map_hashes": ["214fff52a60a3a36de01bce6a246a59509cb400bb6e8e95e831725e955c57429"]}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), ModHashesFilename)
	c := NewModHashCache(path, srv.URL, testLogger())
	if c.Active() {
		t.Fatal("empty cache must be inactive")
	}
	c.refresh(context.Background())
	if !c.Active() {
		t.Fatal("map_hashes alone must activate the cache (no mod versions published yet)")
	}
	// The fixture replay's map handle (last handle) matches the map marker.
	if !c.ContainsAny([]string{"73326d610000434e214fff52a60a3a36de01bce6a246a59509cb400bb6e8e95e831725e955c57429"}) {
		t.Error("map marker must match the scanned full handle by tail")
	}
}

// TestModHashCacheLegacyFileNormalized: a persisted cache written by an
// older build (full 80-char handle hexes) loads normalized to tails.
func TestModHashCacheLegacyFileNormalized(t *testing.T) {
	path := filepath.Join(t.TempDir(), ModHashesFilename)
	legacy := `{"seen": ["73326d610000434e421c8aa0f3619b652d23a2735dfee812ab644228235e7a797edecfe8b67da30e"], "fetched_at": "2026-09-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewModHashCache(path, "", testLogger())
	if !c.ContainsAny([]string{"73326d610000434e421c8aa0f3619b652d23a2735dfee812ab644228235e7a797edecfe8b67da30e"}) {
		t.Error("legacy 80-char entry must still match (compared by tail)")
	}
	if c.ContainsAny([]string{"73326d610000434e9999999999999999999999999999999999999999999999999999999999"}) {
		t.Error("no false match on a different content hash")
	}
}
