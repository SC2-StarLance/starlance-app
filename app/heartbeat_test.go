package main

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeartbeatIntervalConfig(t *testing.T) {
	cases := []struct {
		seconds int
		want    time.Duration
	}{
		{0, 30 * time.Second},  // unset → default
		{-3, 30 * time.Second}, // nonsense → default
		{1, 5 * time.Second},   // clamped to the minimum
		{5, 5 * time.Second},   // exact minimum
		{30, 30 * time.Second}, // default value
		{90, 90 * time.Second}, // arbitrary config honored
	}
	for _, c := range cases {
		if got := heartbeatInterval(&Config{HeartbeatSeconds: c.seconds}); got != c.want {
			t.Errorf("heartbeatInterval(%d) = %v, want %v", c.seconds, got, c.want)
		}
	}
	if got := heartbeatInterval(nil); got != 30*time.Second {
		t.Errorf("heartbeatInterval(nil) = %v, want default 30s", got)
	}
}

func TestHeartbeatSendSendsAPIKey(t *testing.T) {
	var gotKey, gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	hb := NewHeartbeater(&Config{BackendURL: srv.URL}, testAuth(t, "hb-key", "player-1"), NewStatus(StatusWatching), nil, testLogger())
	if err := hb.send(context.Background()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/uploader/heartbeat" {
		t.Errorf("path = %q, want /uploader/heartbeat", gotPath)
	}
	if gotKey != "hb-key" {
		t.Errorf("X-API-Key = %q, want %q", gotKey, "hb-key")
	}
}

// TestHeartbeatPausedWithoutReplayFolder: a running app with no watched
// folder cannot upload, so it must not keep reporting itself online for queue
// entry. Adding a folder resumes the heartbeat.
func TestHeartbeatPausedWithoutReplayFolder(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	canUpload := false
	hb := NewHeartbeater(&Config{BackendURL: srv.URL}, testAuth(t, "hb-key", "player-1"), NewStatus(StatusWatching), nil, testLogger())
	hb.Ready = func() bool { return canUpload }

	hb.tick(context.Background())
	hb.tick(context.Background())
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("heartbeat sent %d time(s) with no replay folder, want 0", got)
	}

	canUpload = true
	hb.tick(context.Background())
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("heartbeat sent %d time(s) after a folder appeared, want 1", got)
	}
}

func TestHeartbeat401SurfacesReloginOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	var notes []string
	status := NewStatus(StatusWatching)
	hb := NewHeartbeater(&Config{BackendURL: srv.URL}, testAuth(t, "hb-key", "player-1"), status,
		func(_ *log.Logger, _ NoticeLevel, _, message string) { notes = append(notes, message) }, testLogger())

	// The re-login notice is one per process across both the heartbeat and the
	// upload path; arm it here since other tests may have fired it already.
	resetSessionExpiredNotice()
	// Two consecutive ticks hit the 401 but the re-login notification must
	// fire exactly once, matching the upload path's once-per-session noise rule.
	hb.tick(context.Background())
	hb.tick(context.Background())
	if len(notes) != 1 {
		t.Errorf("expected exactly 1 session-expired notification, got %d (%v)", len(notes), notes)
	}
	if kind, reason := status.Get(); kind != StatusAttention || reason == "" {
		t.Errorf("status = (%v, %q), want (StatusAttention, non-empty reason)", kind, reason)
	}
}

func TestHeartbeatOfflineFailuresStayQuiet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	var notes []string
	hb := NewHeartbeater(&Config{BackendURL: srv.URL}, testAuth(t, "hb-key", "player-1"), NewStatus(StatusWatching),
		func(_ *log.Logger, _ NoticeLevel, _, message string) { notes = append(notes, message) }, testLogger())

	// Backend up: successful beat clears (or never sets) the offline state.
	hb.tick(context.Background())
	if hb.offline {
		t.Error("successful beat must not set the offline state")
	}
	// Outage: the server goes away; repeated ticks must stay quiet.
	srv.Close()
	hb.tick(context.Background())
	hb.tick(context.Background())
	if len(notes) != 0 {
		t.Errorf("offline heartbeats must never notify; got %v", notes)
	}
	if !hb.offline {
		t.Error("failed beat should set the offline state for transition-only logging")
	}
}

func TestHeartbeatRunFirstBeatImmediately(t *testing.T) {
	beats := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		beats <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	hb := NewHeartbeater(&Config{BackendURL: srv.URL, HeartbeatSeconds: 3600}, testAuth(t, "hb-key", "player-1"), NewStatus(StatusWatching), nil, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hb.Run(ctx)

	select {
	case <-beats:
		// The immediate first beat arrived: the owner can queue right after startup.
	case <-time.After(2 * time.Second):
		t.Fatal("Run should send a heartbeat immediately on startup")
	}
}

func TestHeartbeatSkippedWhenLoggedOut(t *testing.T) {
	var beats int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		beats++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	hb := NewHeartbeater(&Config{BackendURL: srv.URL}, testAuth(t, "", ""), NewStatus(StatusWatching), nil, testLogger())
	hb.tick(context.Background())
	hb.tick(context.Background())
	if beats != 0 {
		t.Errorf("logged-out heartbeats must not hit the backend; got %d", beats)
	}
}
