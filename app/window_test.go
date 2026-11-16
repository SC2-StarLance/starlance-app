package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A revoked token must not wedge the window: Bootstrap drops the local
// session and reports LoggedIn=false so the login panel can take over.
func TestBootstrapDropsRevokedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"invalid api key"}`))
	}))
	defer srv.Close()

	svc := testWindowService(t, srv)
	status := NewStatus(StatusWatching)
	svc.deps = TrayDeps{Auth: svc.auth, Status: status}

	got, err := svc.Bootstrap()
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if got.LoggedIn {
		t.Fatal("Bootstrap kept a revoked session")
	}
	if svc.auth.LoggedIn() {
		t.Fatal("revoked token still in memory")
	}
	if kind, _ := status.Get(); kind != StatusLoggedOut {
		t.Fatalf("status = %v, want logged out", kind)
	}
}
