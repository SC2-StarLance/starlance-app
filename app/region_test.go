package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStartQueueSendsOnlySelectedStar: the queue body carries the one preferred
// region when it is part of the selected set (spec 2.31.0). A stale preference
// for a region the player is not queueing must be dropped, never sent as a
// preference the backend would silently ignore.
func TestStartQueueSendsOnlySelectedStar(t *testing.T) {
	var queued map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"battlenet_accounts":[{"id":"acct-1"}]}`))
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("queue method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&queued); err != nil {
			t.Errorf("decode queue body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := testWindowService(t, srv)
	if err := s.StartQueue("terran", []string{"us", "eu"}, "EU", ""); err != nil {
		t.Fatalf("StartQueue: %v", err)
	}

	regions, _ := queued["regions"].([]any)
	if len(regions) != 2 {
		t.Fatalf("regions = %v, want both selected", queued["regions"])
	}
	if preferred, _ := queued["preferred_region"].(string); preferred != "eu" {
		t.Fatalf("preferred_region = %v, want eu", queued["preferred_region"])
	}
}

// TestStartQueueDropsAStalePreference: a region the player deselected must not
// ride along in the body, even when localStorage still holds it.
func TestStartQueueDropsAStalePreference(t *testing.T) {
	var queued map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"battlenet_accounts":[{"id":"acct-1"}]}`))
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&queued); err != nil {
			t.Errorf("decode queue body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := testWindowService(t, srv)
	if err := s.StartQueue("zerg", []string{"us"}, "kr", ""); err != nil {
		t.Fatalf("StartQueue: %v", err)
	}
	if preferred, _ := queued["preferred_region"].(string); preferred != "" {
		t.Fatalf("preferred_region = %v, want empty", queued["preferred_region"])
	}
}

func TestStartQueueSendsQueuePreference(t *testing.T) {
	var queued map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"battlenet_accounts":[{"id":"acct-1"}]}`))
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&queued); err != nil {
			t.Errorf("decode queue body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := testWindowService(t, srv)
	if err := s.StartQueue("protoss", []string{"eu"}, "", "unranked_only"); err != nil {
		t.Fatalf("StartQueue: %v", err)
	}
	if got, _ := queued["queue_preference"].(string); got != "unranked_only" {
		t.Fatalf("queue_preference = %v, want unranked_only", queued["queue_preference"])
	}
}
