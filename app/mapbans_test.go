package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testWindowService(t *testing.T, srv *httptest.Server) *WindowService {
	t.Helper()
	return &WindowService{
		cfg:    &Config{BackendURL: srv.URL},
		auth:   testAuth(t, "test-key", "player-1"),
		client: srv.Client(),
		logger: testLogger(),
		// Queue tests exercise region/body logic, so pretend a folder is
		// watched; the no-folder refusal has its own test.
		deps: TrayDeps{WatchedDirs: func() []string { return []string{"/tmp/replays"} }},
	}
}

// TestMapBansLoadsActiveSeason: the editor must offer the active season's
// pool and its allowance, and must read the account's saved ban list.
// Older seasons in the list are skipped even when they carry pools.
func TestMapBansLoadsActiveSeason(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ladder/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"draft":{"bans_per_player":3}}`))
	})
	mux.HandleFunc("/seasons", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[
			{"id":"s0","status":"archived","map_pool":[{"id":"m0","name":"Old Map"}]},
			{"id":"s1","status":"active","map_pool":[{"id":"m1","name":"Hardwire LE","image_url":"/maps/previews/legacy_of_the_void_Hardwire_LE.webp"},{"id":"m2","name":"Acid Plant LE","image_url":null}]}
		]`))
	})
	mux.HandleFunc("/me/map-bans", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"map_ids":["m2","m1"]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	view, err := testWindowService(t, srv).MapBans()
	if err != nil {
		t.Fatalf("MapBans: %v", err)
	}
	if view.Allowance != 3 {
		t.Errorf("allowance = %d, want 3", view.Allowance)
	}
	if len(view.Pool) != 2 || view.Pool[0].ID != "m1" || view.Pool[1].Name != "Acid Plant LE" {
		t.Errorf("pool = %+v, want the active season's two maps", view.Pool)
	}
	if view.Pool[0].ImageURL != "/maps/previews/legacy_of_the_void_Hardwire_LE.webp" {
		t.Errorf("image = %q, want the preview path", view.Pool[0].ImageURL)
	}
	if view.Pool[1].ImageURL != "" {
		t.Errorf("image = %q, want empty when the API has no preview", view.Pool[1].ImageURL)
	}
	if strings.Join(view.MapIDs, ",") != "m2,m1" {
		t.Errorf("map ids = %v, want the saved pool in stored order", view.MapIDs)
	}
}

// TestMapBansDegrades: a broken config keeps the default allowance and a
// season list without an active pool falls back to the full map list, but a
// failed ban-pool call is an error: an empty editor would lie about the
// account.
func TestMapBansDegrades(t *testing.T) {
	prefsStatus := http.StatusOK
	mux := http.NewServeMux()
	mux.HandleFunc("/ladder/config", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/seasons", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/maps", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"id":"m9","name":"Lightshade LE"}]`))
	})
	mux.HandleFunc("/me/map-bans", func(w http.ResponseWriter, _ *http.Request) {
		if prefsStatus != http.StatusOK {
			w.WriteHeader(prefsStatus)
			return
		}
		w.Write([]byte(`{"map_ids":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	view, err := testWindowService(t, srv).MapBans()
	if err != nil {
		t.Fatalf("MapBans: %v", err)
	}
	if view.Allowance != 2 {
		t.Errorf("allowance = %d, want the 2 default", view.Allowance)
	}
	if len(view.Pool) != 1 || view.Pool[0].ID != "m9" {
		t.Errorf("pool = %+v, want the /maps fallback", view.Pool)
	}

	prefsStatus = http.StatusUnauthorized
	if _, err := testWindowService(t, srv).MapBans(); err == nil {
		t.Error("a failed ban-pool call must be an error")
	}
}

// TestSaveMapBansWritesEditorState: the PUT body is `{map_ids}` in editor
// order, and a backend rejection keeps its message for the player.
func TestSaveMapBansWritesEditorState(t *testing.T) {
	var method, got string
	fail := false
	mux := http.NewServeMux()
	mux.HandleFunc("/me/map-bans", func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"a player can ban at most 2 maps"}`))
			return
		}
		method = r.Method
		var body struct {
			MapIDs []string `json:"map_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("body: %v", err)
		}
		raw, _ := json.Marshal(body)
		got = string(raw)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := testWindowService(t, srv).SaveMapBans([]string{"m2", "m1"})
	if err != nil {
		t.Fatalf("SaveMapBans: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %s, want PUT", method)
	}
	want := `{"map_ids":["m2","m1"]}`
	if got != want {
		t.Errorf("body = %s, want %s", got, want)
	}

	fail = true
	err = testWindowService(t, srv).SaveMapBans([]string{"m1", "m2", "m3"})
	if err == nil || err.Error() != "a player can ban at most 2 maps" {
		t.Errorf("error = %v, want the backend message", err)
	}
}
