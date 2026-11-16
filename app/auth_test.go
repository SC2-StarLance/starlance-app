package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortenLogin makes the device-flow poll loop near-instant for tests.
func shortenLogin(t *testing.T) {
	t.Helper()
	oldInterval, oldTimeout := pollInterval, loginTimeout
	pollInterval = time.Millisecond
	loginTimeout = 5 * time.Second
	t.Cleanup(func() { pollInterval, loginTimeout = oldInterval, oldTimeout })
}

func stubBrowser(t *testing.T, opened *string) {
	t.Helper()
	old := openBrowserFn
	openBrowserFn = func(url string) error {
		if opened != nil {
			*opened = url
		}
		return nil
	}
	t.Cleanup(func() { openBrowserFn = old })
}

// TestDeviceLoginAndUpload is the end-to-end stub integration: tray login
// (start → browser approval → poll 428×2 → token) → token persisted with
// user-only perms → replay upload carrying X-API-Key + player_id.
func TestDeviceLoginAndUpload(t *testing.T) {
	shortenLogin(t)
	stubBrowser(t, nil)

	const (
		wantKey    = "issued-token-1"
		wantPlayer = "player-42"
	)
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/uploader/authorize/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("start method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"device_code":"dev-1","authorize_url":"https://starlance.club/settings/uploader/authorize?code=dev-1"}`)
	})
	mux.HandleFunc("/api/uploader/authorize/poll", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeviceCode string `json:"device_code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode poll body: %v", err)
		}
		if body.DeviceCode != "dev-1" {
			t.Errorf("device_code = %q, want dev-1", body.DeviceCode)
		}
		polls++
		if polls <= 2 {
			w.WriteHeader(http.StatusPreconditionRequired) // 428: keep waiting
			io.WriteString(w, `{"status":"pending"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"api_key":"`+wantKey+`","player_id":"`+wantPlayer+`"}`)
	})

	// The upload stub asserts the exact contract: multipart file + player_id
	// form field + X-API-Key header.
	var gotKey, gotPlayerID, gotFile string
	mux.HandleFunc("/api/replays/upload", func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotPlayerID = r.FormValue("player_id")
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		gotFile = string(b)
		w.WriteHeader(http.StatusAccepted)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &Config{BackendURL: srv.URL + "/api/replays/upload"}
	if got := cfg.BaseURL(); got != srv.URL+"/api" {
		t.Fatalf("BaseURL = %q, want %q", got, srv.URL+"/api")
	}

	tokenPath := filepath.Join(t.TempDir(), "auth.json")
	auth := LoadAuth(tokenPath, testLogger())
	oldDelays := retryDelays
	retryDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() { retryDelays = oldDelays })

	if err := DeviceLogin(context.Background(), cfg, auth, testLogger()); err != nil {
		t.Fatalf("DeviceLogin: %v", err)
	}
	if polls != 3 {
		t.Errorf("expected 3 polls (2 pending + 1 success), got %d", polls)
	}
	if key, player := auth.Credentials(); key != wantKey || player != wantPlayer {
		t.Fatalf("credentials = (%q, %q), want (%q, %q)", key, player, wantKey, wantPlayer)
	}

	// Token persisted, user-only perms, and never inside the config JSON.
	b, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("token file missing: %v", err)
	}
	if !strings.Contains(string(b), wantKey) {
		t.Error("token file must contain the issued token")
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeIsUnix() && info.Mode().Perm() != 0o600 {
		t.Errorf("token file perms = %v, want 0600", info.Mode().Perm())
	}
	// Reload from disk: login survives a restart.
	if key, _ := LoadAuth(tokenPath, testLogger()).Credentials(); key != wantKey {
		t.Error("reloaded auth lost the token")
	}

	// Upload with the persisted credentials.
	up := NewUploader(cfg, auth, LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger()), NewStatus(StatusWatching), nil, nil, testLogger(), make(chan string))
	replay := writeTestReplay(t)
	if _, _, err := up.uploadWithRetry(replay); err != nil {
		t.Fatalf("uploadWithRetry: %v", err)
	}
	if gotKey != wantKey {
		t.Errorf("X-API-Key = %q, want %q", gotKey, wantKey)
	}
	if gotPlayerID != wantPlayer {
		t.Errorf("player_id = %q, want %q", gotPlayerID, wantPlayer)
	}
	if gotFile != "replay-bytes-for-test" {
		t.Errorf("file field = %q", gotFile)
	}
}

func TestDeviceLoginTimeout(t *testing.T) {
	shortenLogin(t)
	stubBrowser(t, nil)

	oldTimeout := loginTimeout
	loginTimeout = 30 * time.Millisecond
	t.Cleanup(func() { loginTimeout = oldTimeout })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/uploader/authorize/start") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"device_code":"dev-1","authorize_url":"https://starlance.club/x"}`)
			return
		}
		w.WriteHeader(http.StatusPreconditionRequired) // never approved
	}))
	defer srv.Close()

	auth := testAuth(t, "", "")
	if err := DeviceLogin(context.Background(), &Config{BackendURL: srv.URL + "/api/replays/upload"}, auth, testLogger()); err == nil {
		t.Fatal("expected timeout error when approval never arrives")
	}
	if auth.LoggedIn() {
		t.Error("auth must remain logged out after a failed login")
	}
}

// TestDeviceLoginCtxCancel verifies a cancelled context aborts an in-flight
// login promptly (no polling to the timeout cap, no token stored).
func TestDeviceLoginCtxCancel(t *testing.T) {
	shortenLogin(t)
	stubBrowser(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/uploader/authorize/start") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"device_code":"dev-1","authorize_url":"https://starlance.club/x"}`)
			return
		}
		cancel() // quit mid-poll
		w.WriteHeader(http.StatusPreconditionRequired)
	}))
	defer srv.Close()

	auth := testAuth(t, "", "")
	if err := DeviceLogin(ctx, &Config{BackendURL: srv.URL + "/api/replays/upload"}, auth, testLogger()); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeviceLogin error = %v, want context.Canceled", err)
	}
	if auth.LoggedIn() {
		t.Error("auth must remain logged out after a cancelled login")
	}
}

// TestDeviceLoginMalformedPoll200 verifies a 200 poll response with an empty
// token fails fast instead of polling until the timeout cap.
func TestDeviceLoginMalformedPoll200(t *testing.T) {
	shortenLogin(t)
	stubBrowser(t, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/uploader/authorize/start") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"device_code":"dev-1","authorize_url":"https://starlance.club/x"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`) // 200 but no api_key
	}))
	defer srv.Close()

	auth := testAuth(t, "", "")
	err := DeviceLogin(context.Background(), &Config{BackendURL: srv.URL + "/api/replays/upload"}, auth, testLogger())
	if err == nil || !strings.Contains(err.Error(), "empty token") {
		t.Fatalf("DeviceLogin error = %v, want a fast empty-token failure", err)
	}
	if auth.LoggedIn() {
		t.Error("auth must remain logged out after a malformed poll response")
	}
}

// TestRejectionReasonParsing covers the 409 body shapes (plain text and JSON)
// and the user-facing mod_hash_rejected mapping.
func TestRejectionReasonParsing(t *testing.T) {
	if got := parseRejectionReason([]byte("mod_hash_rejected")); got != "mod_hash_rejected" {
		t.Errorf("plain-text reason = %q", got)
	}
	if got := parseRejectionReason([]byte(`{"reason":"map_mismatch"}`)); got != "map_mismatch" {
		t.Errorf("JSON reason = %q", got)
	}
	if got := parseRejectionReason([]byte("")); got != "" {
		t.Errorf("empty body = %q", got)
	}
	if got := parseRejectionReason([]byte("{\"multi\nline}\n")); got != "" {
		t.Errorf("non-reason body = %q, want empty", got)
	}

	if msg := rejectionMessage("mod_hash_rejected"); !strings.Contains(msg, "mod is outdated") || !strings.Contains(msg, "starlance.club/mod-versions") {
		t.Errorf("mod_hash_rejected message = %q", msg)
	}
	if msg := rejectionMessage("weird_reason"); strings.Contains(msg, "weird_reason") || !strings.Contains(msg, "Discord") {
		t.Errorf("unknown reason must stay player-facing and point at support, got %q", msg)
	}
}

// TestUpload409SurfacesReason verifies a 409 upload becomes an errRejected
// with the parsed reason and is not retried.
func TestUpload409SurfacesReason(t *testing.T) {
	var attempts int
	mux := http.NewServeMux()
	// The upload 409 conditionally triggers the best-effort blizzard-game-started
	// signal (double-queue gate); route it separately so the counter tracks
	// uploads only, and stub the gate's queue status query as idle/no pending.
	mux.HandleFunc("/uploader/blizzard-game-started", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/queue/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state": "idle", "counters": {"in_queue": 0, "live_games": 0, "online": 0}}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, "mod_hash_rejected")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldDelays := retryDelays
	retryDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() { retryDelays = oldDelays })

	u := newUploaderForTests(t, srv.URL)
	u.handleFile(writeTestReplay(t))
	if attempts != 1 {
		t.Errorf("a 409 must not be retried; got %d attempts", attempts)
	}
	// The attention reason is the player phrase, not the machine code.
	if kind, reason := u.status.Get(); kind != StatusAttention || reason != "StarLance mod is outdated" {
		t.Errorf("status = (%v, %q), want (StatusAttention, human phrase)", kind, reason)
	}
	// A rejected upload must not create a dedup record for the file.
	if u.state.HasEntry("test.SC2Replay", "no-such-sha") {
		t.Error("rejected upload must not create a dedup record")
	}
}

// runtimeIsUnix reports whether perms checks are meaningful (Windows ACLs are
// not reflected in Mode().Perm()).
func runtimeIsUnix() bool { return os.PathSeparator == '/' }
