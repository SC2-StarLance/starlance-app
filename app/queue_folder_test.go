package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStartQueueRefusesWithoutReplayFolder: queueing with nothing watched
// would promise an upload the app cannot make, so the window must refuse
// before it calls the backend and point at Settings.
func TestStartQueueRefusesWithoutReplayFolder(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path == "/me" {
			w.Write([]byte(`{"battlenet_accounts":[{"id":"acct-1"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := testWindowService(t, srv)
	s.deps = TrayDeps{WatchedDirs: func() []string { return nil }}
	s.cfg.ReplayPath = ""

	err := s.StartQueue("terran", []string{"eu"}, "")
	if err == nil {
		t.Fatal("StartQueue with no replay folder must fail")
	}
	if !strings.Contains(err.Error(), "Settings") {
		t.Errorf("error = %q, want it to send the player to Settings", err)
	}
	if called {
		t.Errorf("the backend was called even though no folder is watched")
	}
}
