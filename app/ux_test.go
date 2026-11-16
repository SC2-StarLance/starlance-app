package main

import (
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMatchNotice(t *testing.T) {
	win := &matchResult{Status: "confirmed"}
	win.Winner = &struct {
		UserID string `json:"user_id"`
		Name   string `json:"name"`
	}{"player-1", "Maru"}
	loss := &matchResult{Status: "confirmed"}
	loss.Winner = &struct {
		UserID string `json:"user_id"`
		Name   string `json:"name"`
	}{"player-2", "Maru"}
	withMap := &matchResult{Status: "confirmed"}
	withMap.Winner = win.Winner
	withMap.Map = &struct {
		Name string `json:"name"`
	}{"Acolyte LE"}

	cases := map[string]struct {
		m         *matchResult
		wantTitle string
		wantBody  string
	}{
		"202 pending":                 {nil, "", ""},
		"win":                         {win, "Victory", "vs Maru"},
		"loss":                        {loss, "Defeat", "vs Maru"},
		"win with map":                {withMap, "Victory", "vs Maru · Acolyte LE"},
		"held":                        {&matchResult{Status: "held"}, "Match under review", "The two uploads differ - an admin will check it"},
		"voided":                      {&matchResult{Status: "voided"}, "Match not counted", "The match was voided"},
		"draw":                        {&matchResult{Status: "confirmed", Draw: true}, "Draw", "Match recorded"},
		"no winner":                   {&matchResult{Status: "confirmed"}, "", ""},
		"pending winner stays quiet":  {&matchResult{Status: "pending", Winner: win.Winner}, "", ""},
		"reviewed winner stays quiet": {&matchResult{Status: "held", Winner: win.Winner}, "Match under review", "The two uploads differ - an admin will check it"},
	}
	for name, tc := range cases {
		title, body := matchNotice(tc.m, "player-1")
		if title != tc.wantTitle || body != tc.wantBody {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", name, title, body, tc.wantTitle, tc.wantBody)
		}
	}
}

func TestParseMatchResult(t *testing.T) {
	m := parseMatchResult([]byte(`{"id":"m1","status":"confirmed","draw":false,"map":{"name":"Lightshade LE"},"winner":{"user_id":"u1","name":"Serral"}}`))
	if m.Status != "confirmed" || m.Map == nil || m.Map.Name != "Lightshade LE" || m.Winner == nil || m.Winner.Name != "Serral" {
		t.Fatalf("parsed match = %+v", m)
	}
	// A non-JSON body still yields a zero match, not nil.
	if m := parseMatchResult([]byte("ok")); m == nil || m.Status != "" {
		t.Fatalf("body fallback = %+v", m)
	}
}

func TestUploadResolvedMatchNotifiesWithResult(t *testing.T) {
	var got [][2]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"m1","status":"confirmed","map":{"name":"Acolyte LE"},"winner":{"user_id":"player-1","name":"Maru"}}`))
	}))
	defer srv.Close()

	cfg := &Config{BackendURL: srv.URL}
	auth := testAuth(t, "test-key", "player-1")
	up := NewUploader(cfg, auth, LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger()), NewStatus(StatusWatching),
		func(_ *log.Logger, _ NoticeLevel, title, body string) { got = append(got, [2]string{title, body}) }, nil, testLogger(), make(chan string))
	up.handleFile(writeTestReplay(t))
	if len(got) != 1 || got[0][0] != "Victory" || got[0][1] != "vs Maru · Acolyte LE" {
		t.Fatalf("notice = %v, want [Victory vs Maru · Acolyte LE]", got)
	}
}

// TestUploadsTodayCounter covers the per-day counter shown in the status line.
func TestUploadsTodayCounter(t *testing.T) {
	st := LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger())
	if n := st.UploadsToday(); n != 0 {
		t.Fatalf("fresh state uploads today = %d, want 0", n)
	}
	if err := st.AddEntry(newRecord("a.SC2Replay", "aa")); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	if err := st.AddEntry(newRecord("b.SC2Replay", "bb")); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	// A record from "yesterday" must not count toward today.
	old := newRecord("c.SC2Replay", "cc")
	old.UploadedAt = time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)
	if err := st.AddEntry(old); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	if n := st.UploadsToday(); n != 2 {
		t.Fatalf("uploads today = %d, want 2", n)
	}
}

func TestStatusLineShowsCounter(t *testing.T) {
	old := appDirOverride
	appDirOverride = t.TempDir()
	t.Cleanup(func() { appDirOverride = old })

	st := LoadState(filepath.Join(t.TempDir(), RecordsFilename), testLogger())
	_ = st.AddEntry(newRecord("a.SC2Replay", "aa"))
	deps := TrayDeps{Auth: &AuthState{}, Status: NewStatus(StatusWatching), State: st}
	label, tip := statusLine(deps)
	if label != "Watching · 1 uploaded today" {
		t.Errorf("label = %q", label)
	}
	if tip != label {
		t.Errorf("tip = %q, want it to mirror the label", tip)
	}
	// Attention reasons always win over the counter.
	deps.Status.Set(StatusAttention, "Session expired - log in again")
	label, tip = statusLine(deps)
	if label != "Needs attention" || tip != "Needs attention: Session expired - log in again" {
		t.Errorf("attention label/tip = %q / %q", label, tip)
	}
}

func TestActivityFeed(t *testing.T) {
	tail := `2026/09/07 23:21:59 main: started
2026/09/07 23:22:05 upload: skip Acolyte LE.SC2Replay (not logged in; use the tray menu to log in)
2026/09/07 23:23:01 upload: uploaded Lightshade LE.SC2Replay (sha256=abc)
2026/09/07 23:24:10 upload: failed OutdatedMod.SC2Replay after 4 attempts: backend rejected the upload (409): mod_hash_rejected
2026/09/07 23:25:44 upload: failed Bad Map.SC2Replay after 4 attempts: backend returned 409 Conflict
2026/09/07 23:25:59 upload: failed Whatever.SC2Replay after 4 attempts: backend returned 503 Service Unavailable
2026/09/07 23:26:10 upload: skip Other.SC2Replay (already uploaded)
2026/09/07 23:27:00 heartbeat: ok`
	want := "23:22  · Acolyte LE.SC2Replay (logged out, not uploaded)\n" +
		"23:23  ✓ Lightshade LE.SC2Replay\n" +
		"23:24  ✗ OutdatedMod.SC2Replay - StarLance mod is outdated\n" +
		"23:25  ✗ Bad Map.SC2Replay\n" +
		"23:25  ✗ Whatever.SC2Replay - StarLance unreachable"
	if got := activityFeed(tail, 15); got != want {
		t.Errorf("feed =\n%s\nwant\n%s", got, want)
	}
	if got := activityFeed("2026/09/07 23:27:00 heartbeat: ok", 15); got != "" {
		t.Errorf("feed with no upload events = %q, want empty", got)
	}
}

// TestRejectionShort covers the one-phrase reason used in the tray tooltip
// and activity lines: every known reason must map to player words, and an
// unknown one must stay identifiable.
func TestRejectionShort(t *testing.T) {
	cases := map[string]string{
		"mod_hash_rejected":      "StarLance mod is outdated",
		"map_mismatch":           "wrong map for the match",
		"toons_not_both_present": "lobby did not match the match",
		"no_single_winner":       "no clear winner in the replay",
		"no_pending_match":       "not a queued StarLance match",
		"not_a_replay":           "no readable replay",
		"replay_build":           "unsupported game version",
		"map_unverifiable":       "map not readable",
		"":                       "rejected by StarLance",
		"brand_new_reason":       "rejected: brand_new_reason",
	}
	for reason, want := range cases {
		if got := rejectionShort(reason); got != want {
			t.Errorf("rejectionShort(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestRejectionMessagesAreActionable(t *testing.T) {
	for _, reason := range []string{
		"mod_hash_rejected", "map_mismatch", "toons_not_both_present",
		"no_pending_match", "no_single_winner", "not_a_replay", "replay_build",
		"replay_too_large", "map_unverifiable", "", "something_new",
	} {
		msg := rejectionMessage(reason)
		if msg == "" {
			t.Errorf("reason %q has no message", reason)
			continue
		}
		// The outcome ("Replay not uploaded") is already the toast title, so
		// each message explains the cause and the next action as a complete
		// sentence instead of repeating "so the game will not count" every
		// time.
		if !strings.HasSuffix(msg, ".") {
			t.Errorf("reason %q message is not a complete sentence: %q", reason, msg)
		}
		if strings.Contains(msg, "so the game will not count") || strings.Contains(msg, "so it will not count") {
			t.Errorf("reason %q message repeats the outcome instead of the cause: %q", reason, msg)
		}
	}
}
