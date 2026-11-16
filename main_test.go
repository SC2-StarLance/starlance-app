package main

import (
	"bytes"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigGeneratesThenLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SettingsFilename)

	// First call should create a default config and return it usable (no
	// quit-and-relaunch step: the app runs on the very first launch).
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("first LoadConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("first LoadConfig returned nil config")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected config file to be written: %v", statErr)
	}

	// Second call should load it without signalling creation.
	cfg2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("second LoadConfig: %v", err)
	}
	if cfg2.ReplayPath != cfg.ReplayPath {
		t.Errorf("round-trip mismatch: got %q want %q", cfg2.ReplayPath, cfg.ReplayPath)
	}
}

func TestLoadConfigValidatesBadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for malformed config")
	}
}

func TestIsReplayNameCaseInsensitive(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"match.SC2Replay", true},
		{"match.sc2replay", true},
		{"match.Sc2RePlay", true},
		{"noext", false},
		{"match.txt", false},
		{".SC2Replay", false}, // no base name
	}
	for _, c := range cases {
		if got := isReplayName(c.name); got != c.want {
			t.Errorf("isReplayName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSHA256Helper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.SC2Replay")
	payload := []byte("replay-content")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := sha256File(path)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	if len(sum) != 64 {
		t.Fatalf("expected 64-hex-char sha256, got %q (%d)", sum, len(sum))
	}
	if !strings.HasPrefix(sum, "42168cd2095ae97a8027f40c9bedc76c6fa0cb2bd4aac4f84b8ade8e5585c1fe") {
		t.Errorf("unexpected digest %q", sum)
	}
}

func TestStateDedupAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RecordsFilename)
	s := LoadState(path, testLogger())

	if s.HasEntry("a.SC2Replay", "abc") {
		t.Fatal("empty state should not report entries")
	}
	if err := s.AddEntry(newRecord("a.SC2Replay", "abc")); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	if !s.HasEntry("a.SC2Replay", "abc") {
		t.Fatal("state should report added entry by name")
	}
	if !s.HasEntry("other.SC2Replay", "abc") {
		t.Fatal("state should report added entry by sha")
	}
	// Reload from disk to confirm persistence.
	s2 := LoadState(path, testLogger())
	if !s2.HasEntry("a.SC2Replay", "abc") {
		t.Fatal("reloaded state lost an entry")
	}
}

// TestStateSameNameDifferentSHA verifies a replay reusing an already-recorded
// filename but with different content is NOT treated as duplicated: it must be
// matched by name only when the sha256 also matches.
func TestStateSameNameDifferentSHA(t *testing.T) {
	s := LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger())

	if err := s.AddEntry(newRecord("x.SC2Replay", "aaa")); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	if !s.HasEntry("x.SC2Replay", "aaa") {
		t.Fatal("state should report an entry whose name and sha both match")
	}
	if s.HasEntry("x.SC2Replay", "bbb") {
		t.Fatal("a different replay reusing the same name with a different sha must not be skipped")
	}
}

// testLogger returns a logger that discards output, for tests that only need a
// non-nil logger (e.g. LoadState's corrupt-file warning path).
func testLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func TestIsSettled(t *testing.T) {
	// Reached the required idle+stable sampling threshold but not yet the cap.
	if !isSettled(settleIdleSamples, sizeStableSamples, settleIdleSamples) {
		t.Fatal("isSettled should release once idle threshold and stable-pair threshold are both met")
	}
	if !isSettled(settleIdleSamples+5, sizeStableSamples, settleIdleSamples) {
		t.Fatal("isSettled should remain true once thresholds are exceeded")
	}
	// Not idle enough yet: must not settle just because stable pairs reached.
	if isSettled(settleIdleSamples-1, sizeStableSamples, settleIdleSamples) {
		t.Fatal("isSettled must not release before the idle threshold is met")
	}
	// Attempts cap releases an endlessly growing file even when not idle.
	if !isSettled(0, 0, settleMaxAttempts) {
		t.Fatal("isSettled must release on the attempts cap regardless of idle/stable state")
	}
	if isSettled(0, 0, settleMaxAttempts-1) {
		t.Fatal("isSettled must not release before the attempts cap or the idle threshold")
	}
}

// testAuth builds an AuthState backed by a temp token file. An empty apiKey
// leaves it logged out.
func testAuth(t *testing.T, apiKey, playerID string) *AuthState {
	t.Helper()
	a := LoadAuth(filepath.Join(t.TempDir(), "auth.json"), testLogger())
	if apiKey != "" {
		if err := a.Set(apiKey, playerID); err != nil {
			t.Fatalf("auth.Set: %v", err)
		}
	}
	return a
}

// newUploaderForTests builds an Uploader ready for uploadWithRetry tests against
// the given backend URL, with retry delays overridden to be near-instant.
func newUploaderForTests(t *testing.T, backendURL string) *Uploader {
	t.Helper()
	// Save and shorten the retry delays so the failure test does not sleep for
	// the product-retry durations (2s+4s+8s).
	oldDelays := retryDelays
	retryDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() { retryDelays = oldDelays })

	cfg := &Config{BackendURL: backendURL}
	auth := testAuth(t, "test-key", "player-1")
	return NewUploader(cfg, auth, LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger()), NewStatus(StatusWatching), nil, nil, testLogger(), make(chan string))
}

func writeTestReplay(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.SC2Replay")
	if err := os.WriteFile(path, []byte("replay-bytes-for-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUploadWithRetry_RetriesThenFails(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	u := newUploaderForTests(t, srv.URL)
	if _, _, err := u.uploadWithRetry(writeTestReplay(t)); err == nil {
		t.Fatal("expected error after all attempts failed")
	}
	if attempts != maxUploadAttempts {
		t.Errorf("expected %d attempts, got %d", maxUploadAttempts, attempts)
	}
}

func TestUploadWithRetry_Succeeds(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u := newUploaderForTests(t, srv.URL)
	if _, _, err := u.uploadWithRetry(writeTestReplay(t)); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt on success, got %d", attempts)
	}
}

func TestUploadWithRetry_401NotRetriedAndWarnedOnce(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	var notes []string
	u := newUploaderForTests(t, srv.URL)
	u.notify = func(_ *log.Logger, _ NoticeLevel, _, message string) { notes = append(notes, message) }

	// Two files both hit the 401; the session-expired warning must fire once.
	resetSessionExpiredNotice()
	u.handleFile(writeTestReplay(t))
	u.handleFile(writeTestReplay(t))
	if attempts != 2 {
		t.Errorf("a 401 must not be retried; got %d attempts across 2 files", attempts)
	}
	if len(notes) != 1 {
		t.Errorf("expected exactly 1 session-expired notification, got %d (%v)", len(notes), notes)
	}
	if kind, reason := u.status.Get(); kind != StatusAttention || !strings.Contains(reason, "log in again") {
		t.Errorf("status = (%v, %q), want (StatusAttention, \"...log in again...\")", kind, reason)
	}
}

func TestUploadWithRetry_400NotRetried(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	// retryDelays are zeroed by newUploaderForTests so a retry (if the logic
	// wrongly retried) would be immediate, making this test fast and precise.
	u := newUploaderForTests(t, srv.URL)
	_, used, err := u.uploadWithRetry(writeTestReplay(t))
	if err == nil {
		t.Fatal("expected error after non-retryable 400 response")
	}
	if used != 1 {
		t.Errorf("uploadWithRetry must report the attempt it stopped at (1), got %d", used)
	}
	if !strings.Contains(err.Error(), "backend returned 400 Bad Request") {
		t.Errorf("error %q must identify the HTTP status", err)
	}
	if attempts != 1 {
		t.Errorf("expected exactly 1 attempt for a 400 (no retries), got %d", attempts)
	}
}

func TestBuildMultipart(t *testing.T) {
	// Succeed with player_id configured and an api key set.
	dir := t.TempDir()
	replay := filepath.Join(dir, "race.SC2Replay")
	if err := os.WriteFile(replay, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := NewUploader(&Config{BackendURL: "https://example.com"}, testAuth(t, "secret", "player-9"), nil, nil, nil, nil, testLogger(), make(chan string))
	body, contentType, err := u.buildMultipart(replay)
	if err != nil {
		t.Fatalf("buildMultipart: %v", err)
	}
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart/form-data prefix", contentType)
	}

	boundary := contentType[strings.Index(contentType, "boundary=")+len("boundary="):]
	mr := multipart.NewReader(bytes.NewReader(body), boundary)

	var sawFile, sawPlayer bool
	var fileData []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch part.FormName() {
		case "file":
			sawFile = true
			b, rerr := io.ReadAll(part)
			if rerr != nil {
				t.Fatal(rerr)
			}
			fileData = b
		case "player_id":
			sawPlayer = true
			b, rerr := io.ReadAll(part)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(b) != "player-9" {
				t.Errorf("player_id = %q, want %q", b, "player-9")
			}
		}
	}
	if !sawFile {
		t.Error("multipart body must contain a 'file' field")
	}
	if !sawPlayer {
		t.Error("multipart body must contain a 'player_id' field when configured")
	}
	if string(fileData) != "payload" {
		t.Errorf("file content = %q, want %q", fileData, "payload")
	}

	// player_id omitted when the auth token has none.
	u2 := &Uploader{cfg: &Config{}, auth: testAuth(t, "key", ""), client: &http.Client{}, logger: testLogger(), fileCh: make(chan string), status: NewStatus(StatusWatching)}
	body2, contentType2, err := u2.buildMultipart(replay)
	if err != nil {
		t.Fatalf("buildMultipart (no player): %v", err)
	}
	if !strings.HasPrefix(contentType2, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart/form-data prefix", contentType2)
	}
	boundary2 := contentType2[strings.Index(contentType2, "boundary=")+len("boundary="):]
	mr2 := multipart.NewReader(bytes.NewReader(body2), boundary2)
	for {
		part, err := mr2.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() == "player_id" {
			t.Error("player_id must be omitted when not configured")
		}
	}
}

func TestUploadSendsHeadersAndMultipart(t *testing.T) {
	// Verify the actual request carries X-API-Key and the multipart 'file' field.
	var gotHeader string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-API-Key")
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	u := newUploaderForTests(t, srv.URL)
	replay := writeTestReplay(t)
	if _, _, err := u.uploadWithRetry(replay); err != nil {
		t.Fatalf("uploadWithRetry: %v", err)
	}
	if gotHeader != "test-key" {
		t.Errorf("X-API-Key = %q, want %q", gotHeader, "test-key")
	}
	if !bytes.Contains(gotBody, []byte(`name="file"`)) {
		t.Error("request body must contain the multipart 'file' field")
	}
}

func TestValidateWarnTLS(t *testing.T) {
	capture := func(cfg *Config, auth *AuthState) string {
		var buf bytes.Buffer
		validateAndWarn(cfg, auth, log.New(&buf, "", 0))
		return buf.String()
	}
	apiKeyMsg := "the API key will be sent in plaintext"

	// http:// backend with a stored token -> warning.
	if out := capture(&Config{BackendURL: "http://example.com/upload"}, testAuth(t, "secret", "")); !strings.Contains(out, apiKeyMsg) {
		t.Errorf("http + api key must warn, got %q", out)
	}
	// https:// backend with a stored token -> no warning.
	if out := capture(&Config{BackendURL: "https://example.com/upload"}, testAuth(t, "secret", "")); out != "" {
		t.Errorf("https + api key must not warn, got %q", out)
	}
	// http:// backend while logged out -> no api-key-specific warning.
	if out := capture(&Config{BackendURL: "http://example.com/upload"}, testAuth(t, "", "")); strings.Contains(out, apiKeyMsg) {
		t.Errorf("http without api key must not warn about plaintext key, got %q", out)
	}
	// Missing/empty backend -> no warning.
	if out := capture(&Config{BackendURL: ""}, testAuth(t, "secret", "")); out != "" {
		t.Errorf("empty backend must not warn, got %q", out)
	}
}

func TestBaseURLDerivation(t *testing.T) {
	cases := []struct{ backend, want string }{
		{"https://starlance.club/api/replays/upload", "https://starlance.club/api"},
		{"http://localhost:8080/replays/upload", "http://localhost:8080"},
		{"https://starlance.club/api", "https://starlance.club/api"},
		{"", ""},
		{"not a url", ""},
	}
	for _, c := range cases {
		if got := (&Config{BackendURL: c.backend}).BaseURL(); got != c.want {
			t.Errorf("BaseURL(%q) = %q, want %q", c.backend, got, c.want)
		}
	}
}

// TestDetectLinuxReplayDirLutrisPrefix pins the real-world Linux layout this
// app must auto-detect: Lutris installs Battle.net games as
// ~/Games/<game>/drive_c with the Wine user dir 'steamuser' (observed live:
// SC2 at ~/Games/battlenet). The old probe list only tried ~/Games itself
// and only the 'user'/home-basename Wine users, finding nothing.
func TestDetectLinuxReplayDirLutrisPrefix(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only detection paths")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	multi := filepath.Join(home, "Games", "battlenet", "drive_c", "users", "steamuser",
		"Documents", "StarCraft II", "Accounts", "132104812", "2-S2-1-3430393", "Replays", "Multiplayer")
	if err := os.MkdirAll(multi, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectReplayDirs(); !slicesEqual(got, []string{multi}) {
		t.Errorf("DetectReplayDirs() = %q, want [%q]", got, multi)
	}
}

// mkReplayDir creates a fake Wine prefix replay folder and returns its path.
func mkReplayDir(t *testing.T, prefix, wineUser string) string {
	t.Helper()
	multi := filepath.Join(prefix, "drive_c", "users", wineUser, "Documents",
		"StarCraft II", "Accounts", "42", "2-S2-1-1", "Replays", "Multiplayer")
	if err := os.MkdirAll(multi, 0o755); err != nil {
		t.Fatal(err)
	}
	return multi
}

// TestDetectLinuxReplayDirsCoversPrefixSources pins the four layouts players
// actually run SC2 in on Linux besides a plain ~/.wine: the Flatpak Steam
// compat prefix, a Proton prefix in a Steam library listed only in
// libraryfolders.vdf, and a Bottles prefix whose Wine user is not one of the
// guessed names.
func TestDetectLinuxReplayDirsCoversPrefixSources(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only detection paths")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	flatpak := mkReplayDir(t, filepath.Join(home, ".var", "app", "com.valvesoftware.Steam",
		".local", "share", "Steam", "steamapps", "compatdata", "123", "pfx"), "steamuser")

	library := filepath.Join(t.TempDir(), "SteamLibrary")
	vdfDir := filepath.Join(home, ".local", "share", "Steam", "steamapps")
	if err := os.MkdirAll(vdfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vdf := "\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\"" + library + "\"\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(vdfDir, "libraryfolders.vdf"), []byte(vdf), 0o644); err != nil {
		t.Fatal(err)
	}
	secondLibrary := mkReplayDir(t, filepath.Join(library, "steamapps", "compatdata", "9", "pfx"), "user")

	bottles := mkReplayDir(t, filepath.Join(home, ".local", "share", "bottles", "bottles", "game"), "crossover")

	got := DetectReplayDirs()
	for _, want := range []string{flatpak, secondLibrary, bottles} {
		found := false
		for _, dir := range got {
			if dir == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DetectReplayDirs() missing %q; got %q", want, got)
		}
	}
}

// TestDetectDarwinReplayDirsCoversWhisky pins the macOS-emulated install:
// Whisky keeps its prefix inside the app container.
func TestDetectDarwinReplayDirsCoversWhisky(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	whisky := mkReplayDir(t, filepath.Join(home, "Library", "Containers",
		"com.isaacmarovitz.Whisky", "Data", "Library", "Application Support",
		"Whisky", "Bottles", "bottle1"), "crossover")

	got := detectDarwinReplayDirs()
	if !slicesEqual(got, []string{whisky}) {
		t.Errorf("detectDarwinReplayDirs() = %q, want [%q]", got, whisky)
	}
}

// slicesEqual compares two string slices without pulling in a reflect import
// for one call.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newUploaderWithMux builds an Uploader whose uploads go to srv.URL (path
// "/") — the mux routes that POST plus any /uploader/ auxiliary endpoints.
func newUploaderWithMux(t *testing.T, srv *httptest.Server) *Uploader {
	t.Helper()
	oldDelays := retryDelays
	retryDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() { retryDelays = oldDelays })

	auth := testAuth(t, "test-key", "player-1")
	return NewUploader(&Config{BackendURL: srv.URL}, auth, LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger()), NewStatus(StatusWatching), nil, nil, testLogger(), make(chan string))
}

func TestHandleFile_CustomGameWhileQueuedTriggersBlizzardSignal(t *testing.T) {
	// A replay failing StarLance validation with a wrong-game reason (here a
	// mod hash mismatch, as a custom game produces) while the backend reports
	// the queue searching with no pending match is the double-queue case: the
	// signal must be sent exactly once, with the same X-API-Key.
	var signalKeys []string
	var signalCount int
	var statusKeys []string
	mux := http.NewServeMux()
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, r *http.Request) {
		signalCount++
		signalKeys = append(signalKeys, r.Header.Get("X-API-Key"))
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, r *http.Request) {
		statusKeys = append(statusKeys, r.Header.Get("X-API-Key"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "searching", "elapsed_seconds": 42, "counters": {"in_queue": 1, "live_games": 0, "online": 0}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"reason": "mod_hash_rejected"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := newUploaderWithMux(t, srv)
	u.handleFile(writeTestReplay(t))
	if signalCount != 1 {
		t.Errorf("expected exactly 1 blizzard-game-started signal, got %d", signalCount)
	}
	if len(signalKeys) > 0 && signalKeys[0] != "test-key" {
		t.Errorf("signal X-API-Key = %q, want %q", signalKeys[0], "test-key")
	}
	if len(statusKeys) > 0 && statusKeys[0] != "test-key" {
		t.Errorf("queue status X-API-Key = %q, want %q", statusKeys[0], "test-key")
	}
}

func TestHandleFile_RejectionWhileMatchPendingSendsNoBlizzardSignal(t *testing.T) {
	// A wrong-game rejection while the backend reports a pending match is a
	// wrong-lobby attempt — the pending-match lifecycle handles it, never an
	// automatic queue exit.
	signalCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		signalCount++
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "matched", "counters": {"in_queue": 1, "live_games": 0, "online": 0}, "pending_match": {"id": "m-1", "state": "pending"}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"reason": "map_mismatch"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := newUploaderWithMux(t, srv)
	u.handleFile(writeTestReplay(t))
	if signalCount != 0 {
		t.Errorf("wrong-lobby rejection against a pending match must not trigger the signal; got %d", signalCount)
	}
}

func TestHandleFile_QueueStatusFailureSendsNoBlizzardSignal(t *testing.T) {
	// If the queue status query fails (here: unhandled route → 404 with an
	// unparseable body) the signal must be skipped conservatively.
	signalCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		signalCount++
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"reason": "mod_hash_rejected"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := newUploaderWithMux(t, srv)
	u.handleFile(writeTestReplay(t))
	if signalCount != 0 {
		t.Errorf("a failed queue status query must skip the signal; got %d", signalCount)
	}
}

func TestHandleFile_SuccessSendsNoBlizzardSignal(t *testing.T) {
	// An accepted replay belongs to a pending StarLance match — no signal.
	signalCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		signalCount++
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := newUploaderWithMux(t, srv)
	replay := writeTestReplay(t)
	u.handleFile(replay)
	if signalCount != 0 {
		t.Errorf("accepted replay must not trigger the blizzard signal; got %d", signalCount)
	}
	sum, err := sha256File(replay)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	if !u.state.HasEntry(filepath.Base(replay), sum) {
		t.Error("accepted replay should have been recorded for dedup")
	}
}

func TestHandleFile_DuplicateSkipsUploadAndSignal(t *testing.T) {
	// A replay already in the dedup records (e.g. re-emitted by the
	// initial scan after restart) must cause neither an upload nor a signal.
	totalRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		totalRequests++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	u := newUploaderWithMux(t, srv)
	replay := writeTestReplay(t)
	sum, err := sha256File(replay)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	if err := u.state.AddEntry(newRecord("test.SC2Replay", sum)); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	u.handleFile(replay)
	if totalRequests != 0 {
		t.Errorf("duplicate replay must not hit the backend; got %d requests", totalRequests)
	}
}

func TestReportBlizzardGameStarted_ServerErrorFailsSoftly(t *testing.T) {
	// A broken backend must not surface anything to the user; the signal is
	// retried maxSignalAttempts times then abandoned quietly.
	oldDelay := signalRetryDelay
	signalRetryDelay = 0
	t.Cleanup(func() { signalRetryDelay = oldDelay })

	signalAttempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		signalAttempts++
		w.WriteHeader(http.StatusBadGateway)
	})
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "idle", "counters": {"in_queue": 0, "live_games": 0, "online": 0}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"reason": "mod_hash_rejected"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var notes []string
	u := newUploaderWithMux(t, srv)
	u.notify = func(_ *log.Logger, _ NoticeLevel, _, message string) { notes = append(notes, message) }

	u.handleFile(writeTestReplay(t))
	if signalAttempts != maxSignalAttempts {
		t.Errorf("expected %d signal attempts before giving up, got %d", maxSignalAttempts, signalAttempts)
	}
	// The upload 409 itself legitimately notifies; a signal outage must not
	// add anything (no session-expired/re-login noise from the soft failure).
	for _, n := range notes {
		if strings.Contains(n, "login expired") || strings.Contains(strings.ToLower(n), "signal") {
			t.Errorf("signal failures must stay quiet; got notification %q", n)
		}
	}
}

// TestShouldSignalBlizzardGame is the table-driven double-queue gate: the
// signal fires only for genuine wrong-game rejection reasons combined with a
// confirmed no-pending-match status.
func TestShouldSignalBlizzardGame(t *testing.T) {
	cases := []struct {
		name            string
		rejectionReason string
		hasPendingMatch bool
		want            bool
	}{
		{"custom game while queued (mod mismatch, searching)", "mod_hash_rejected", false, true},
		{"custom game while queued (map mismatch, searching)", "map_mismatch", false, true},
		{"identity mismatch while queued", "toons_not_both_present", false, true},
		{"unscoreable replay while queued", "no_single_winner", false, true},
		{"wrong-lobby attempt with pending match", "mod_hash_rejected", true, false},
		{"wrong-lobby map mismatch with pending match", "map_mismatch", true, false},
		{"idle no_pending_match rejection", "no_pending_match", false, false},
		{"idle no_pending_match rejection while pending", "no_pending_match", true, false},
		{"empty reason must not signal", "", false, false},
		{"unknown reason must not signal", "parser_failure", false, false},
		{"unknown reason while pending must not signal", "parser_failure", true, false},
	}
	for _, c := range cases {
		if got := shouldSignalBlizzardGame(c.rejectionReason, c.hasPendingMatch); got != c.want {
			t.Errorf("%s: shouldSignalBlizzardGame(%q, %v) = %v, want %v", c.name, c.rejectionReason, c.hasPendingMatch, got, c.want)
		}
	}
}

func TestReportBlizzardGameStarted_401SharesSessionExpiredWarning(t *testing.T) {
	// A revoked token surfaces the same log-in-again warning as an upload 401,
	// and only once per session.
	mux := http.NewServeMux()
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"reason": "no_pending_match"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var notes []string
	u := newUploaderWithMux(t, srv)
	u.notify = func(_ *log.Logger, _ NoticeLevel, _, message string) { notes = append(notes, message) }

	resetSessionExpiredNotice()
	u.reportBlizzardGameStarted()
	u.reportBlizzardGameStarted()
	if len(notes) != 1 {
		t.Errorf("expected exactly 1 session-expired notification, got %d (%v)", len(notes), notes)
	}
	if kind, reason := u.status.Get(); kind != StatusAttention || !strings.Contains(reason, "log in again") {
		t.Errorf("status = (%v, %q), want (StatusAttention, \"...log in again...\")", kind, reason)
	}
}

func TestHandleFile_RetryExhaustionNotifiesAndFlagsAttention(t *testing.T) {
	// A retryable failure (5xx, e.g. a backend/parser redeploy outlasting the
	// retry budget) must tell the player the game may not have counted and
	// point at the manual upload escape hatch — never fail silently behind a
	// log line (the replay is not marked uploaded and is never retried).
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var levels []NoticeLevel
	var bodies []string
	u := newUploaderWithMux(t, srv)
	u.notify = func(_ *log.Logger, level NoticeLevel, _, body string) {
		levels = append(levels, level)
		bodies = append(bodies, body)
	}

	u.handleFile(writeTestReplay(t))
	if attempts != maxUploadAttempts {
		t.Errorf("expected %d attempts, got %d", maxUploadAttempts, attempts)
	}
	if len(levels) != 1 || levels[0] != NoticeProblem {
		t.Fatalf("notice levels = %v, want exactly one NoticeProblem", levels)
	}
	if !strings.Contains(bodies[0], "could not be reached") || !strings.Contains(bodies[0], "match page") {
		t.Errorf("notice body must point at the manual upload escape hatch: %q", bodies[0])
	}
	if kind, reason := u.status.Get(); kind != StatusAttention || !strings.Contains(reason, "unreachable") {
		t.Errorf("status = (%v, %q), want (StatusAttention, \"...unreachable...\")", kind, reason)
	}
}

func TestHandleFile_NoPendingMatchStaysQuiet(t *testing.T) {
	// A StarLance-mod game played outside the queue (custom, AI) is expected
	// behavior: log it, never toast it and never raise attention.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"reason": "no_pending_match"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var levels []NoticeLevel
	u := newUploaderWithMux(t, srv)
	u.notify = func(_ *log.Logger, level NoticeLevel, _, _ string) {
		levels = append(levels, level)
	}

	u.handleFile(writeTestReplay(t))
	if len(levels) != 0 {
		t.Fatalf("notice levels = %v, want none for an expected idle rejection", levels)
	}
	if kind, _ := u.status.Get(); kind != StatusWatching {
		t.Errorf("status = %v, want StatusWatching (no attention for an expected idle rejection)", kind)
	}
}

func TestMonitorFile_SurvivesTransientStatErrors(t *testing.T) {
	// A replay whose os.Stat fails for the first polls (create-pending race,
	// AV scan window) must still be released once it appears and settles —
	// not dropped.
	oldPoll := settlePoll
	settlePoll = 10 * time.Millisecond
	t.Cleanup(func() { settlePoll = oldPoll })

	dir := t.TempDir()
	path := filepath.Join(dir, "late.SC2Replay")
	fileCh := make(chan string, 1)
	w := NewWatcher([]string{dir}, testLogger(), fileCh)

	go func() {
		// Several failed stat samples happen before the file exists.
		time.Sleep(80 * time.Millisecond)
		if err := os.WriteFile(path, []byte("replay-bytes-for-test"), 0o600); err != nil {
			t.Errorf("write late replay: %v", err)
		}
	}()
	w.wg.Add(1) // monitorFile defers wg.Done; match Run's accounting.
	go w.monitorFile(path)

	select {
	case got := <-fileCh:
		if got != path {
			t.Errorf("released %q, want %q", got, path)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("monitorFile dropped a replay that appeared after transient stat errors")
	}
}

func TestMonitorFile_GivesUpOnPersistentlyUnreachableFile(t *testing.T) {
	oldPoll, oldMax := settlePoll, maxStatErrors
	settlePoll = 10 * time.Millisecond
	maxStatErrors = 3
	t.Cleanup(func() { settlePoll, maxStatErrors = oldPoll, oldMax })

	w := NewWatcher([]string{t.TempDir()}, testLogger(), make(chan string, 1))
	done := make(chan struct{})
	w.wg.Add(1) // monitorFile defers wg.Done; match Run's accounting.
	go func() {
		w.monitorFile(filepath.Join(t.TempDir(), "gone.SC2Replay"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("monitorFile must give up after maxStatErrors consecutive stat failures")
	}
}

// TestNormalizeDirsDedupesAndCleans pins the watcher's input hygiene: empty
// entries dropped, paths cleaned, duplicates (detection + an explicit pick
// naming the same folder) folded into one watch.
func TestNormalizeDirsDedupesAndCleans(t *testing.T) {
	got := normalizeDirs([]string{"", "/a/b/", "/a/b", "/c"})
	if !slicesEqual(got, []string{"/a/b", "/c"}) {
		t.Errorf("normalizeDirs = %q, want [/a/b /c]", got)
	}
}

// TestWatcherDirsReturnsCurrentSet pins the Dirs getter used by the tray's
// replays info row: primary first, exactly what the watcher holds.
func TestWatcherDirsReturnsCurrentSet(t *testing.T) {
	w := NewWatcher([]string{"/a", "", "/a"}, testLogger(), make(chan string, 1))
	if got := w.Dirs(); !slicesEqual(got, []string{"/a"}) {
		t.Errorf("Dirs() = %q, want [/a]", got)
	}
}

// TestWatcherSetDirsSwitchesLiveAndKeepsOthers pins the multi-root hot
// switch: a live SetDirs adds the new root, keeps untouched roots flowing,
// and stops events from removed roots - all without restarting the watcher.
func TestWatcherSetDirsSwitchesLiveAndKeepsOthers(t *testing.T) {
	oldPoll := settlePoll
	settlePoll = 10 * time.Millisecond
	t.Cleanup(func() { settlePoll = oldPoll })

	dirA, dirB, dirC := t.TempDir(), t.TempDir(), t.TempDir()
	fileCh := make(chan string, 3)
	w := NewWatcher([]string{dirA, dirB}, testLogger(), fileCh)
	done := make(chan error, 1)
	go func() { done <- w.Run() }()
	defer func() {
		w.Stop()
		<-done
	}()

	// Hot-switch: B stays watched, C is added, A is dropped.
	if err := w.SetDirs([]string{dirB, dirC}); err != nil {
		t.Fatalf("SetDirs: %v", err)
	}

	write := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("replay"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(want string) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case got := <-fileCh:
				if got == want {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %s", want)
			}
		}
	}

	write(dirB, "kept.SC2Replay")
	waitFor(filepath.Join(dirB, "kept.SC2Replay"))

	write(dirC, "added.SC2Replay")
	waitFor(filepath.Join(dirC, "added.SC2Replay"))

	// The removed root must no longer produce releases.
	write(dirA, "dropped.SC2Replay")
	select {
	case got := <-fileCh:
		t.Errorf("removed root still released a replay: %s", got)
	case <-time.After(2 * time.Second):
	}
}

// TestHandleFileRejectionSetsHumanAttentionReason covers the attention-state
// reason: the tray tooltip shows a player phrase, not the machine code.
func TestHandleFileRejectionSetsHumanAttentionReason(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"reason": "mod_hash_rejected"}`))
	})
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "searching", "counters": {"in_queue": 1, "live_games": 0, "online": 0}}`))
	})
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := newUploaderWithMux(t, srv)
	u.handleFile(writeTestReplay(t))
	kind, reason := u.status.Get()
	if kind != StatusAttention || reason != "StarLance mod is outdated" {
		t.Fatalf("status = (%v, %q), want (attention, human phrase)", kind, reason)
	}
}

// TestPendingPollNotifiesOnResolution covers the 202 path: the upload is
// accepted but unresolved, the poller discovers the pending match on
// /queue/status, follows /matches/{id}, and toasts the outcome when it
// resolves - the "did my game count?" answer for the first uploader.
func TestPendingPollNotifiesOnResolution(t *testing.T) {
	oldInterval, oldMax := pendingPollInterval, pendingPollMax
	pendingPollInterval = 5 * time.Millisecond
	pendingPollMax = 5 * time.Second
	t.Cleanup(func() { pendingPollInterval, pendingPollMax = oldInterval, oldMax })

	notes := make(chan string, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "matched", "pending_match": {"id": "m-1", "status": "pending"}}`))
	})
	mux.HandleFunc("/matches/m-1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": "m-1", "status": "confirmed", "map": {"name": "Acolyte LE"}, "winner": {"user_id": "player-1", "name": "Maru"}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := newUploaderWithMux(t, srv)
	u.notify = func(_ *log.Logger, _ NoticeLevel, title, _ string) { notes <- title }

	u.handleFile(writeTestReplay(t))
	select {
	case title := <-notes:
		if title != "Victory" {
			t.Fatalf("notified %q, want Victory", title)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pending poll never notified the resolution")
	}
}

// TestPendingPollQuietWhenNoPendingMatch: when the queue status no longer
// carries a pending match, there is no id to follow - the poller gives up
// quietly and the site's notifications own the outcome.
func TestPendingPollQuietWhenNoPendingMatch(t *testing.T) {
	oldInterval, oldMax := pendingPollInterval, pendingPollMax
	pendingPollInterval = 2 * time.Millisecond
	pendingPollMax = 1 * time.Second
	t.Cleanup(func() { pendingPollInterval, pendingPollMax = oldInterval, oldMax })

	notes := make(chan string, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "idle", "counters": {"in_queue": 0, "live_games": 0, "online": 0}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := newUploaderWithMux(t, srv)
	u.notify = func(_ *log.Logger, _ NoticeLevel, title, _ string) { notes <- title }

	u.handleFile(writeTestReplay(t))
	time.Sleep(200 * time.Millisecond) // let discovery run out and exit
	select {
	case title := <-notes:
		t.Fatalf("unexpected notification %q", title)
	default:
	}
}
