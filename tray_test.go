package main

import (
	"testing"
)

func newMenuTestDeps(t *testing.T, loggedIn bool) TrayDeps {
	t.Helper()
	old := appDirOverride
	appDirOverride = t.TempDir()
	t.Cleanup(func() { appDirOverride = old })

	status := NewStatus(StatusLoggedOut)
	if loggedIn {
		status = NewStatus(StatusWatching)
	}
	auth := &AuthState{}
	if loggedIn {
		tokenPath, err := TokenPath()
		if err != nil {
			t.Fatalf("TokenPath: %v", err)
		}
		auth = LoadAuth(tokenPath, testLogger())
		if err := auth.Set("key", "player"); err != nil {
			t.Fatalf("auth.Set: %v", err)
		}
	}
	return TrayDeps{Auth: auth, Status: status}
}

func TestTrayMenuModel(t *testing.T) {
	cases := map[string]struct {
		loggedIn  bool
		statusRow string
	}{
		"logged out": {false, "Not logged in"},
		"logged in":  {true, "Watching"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			items := trayMenu(newMenuTestDeps(t, tc.loggedIn))
			if len(items) != 6 {
				t.Fatalf("items = %d, want 6 (status, separator, fix, open, separator, quit)", len(items))
			}
			if items[0].kind != trayStatus || items[0].label != tc.statusRow {
				t.Errorf("status row = %+v, want label %q", items[0], tc.statusRow)
			}
			if items[1].kind != traySeparator || items[4].kind != traySeparator {
				t.Errorf("separators = %+v / %+v", items[1], items[4])
			}
			if fix := items[2]; fix.id != MenuWhatsWrong || !fix.hidden || fix.kind != trayAction {
				t.Errorf("fix row = %+v, want hidden action", fix)
			}
			if win := items[3]; win.id != MenuOpenWindow || win.hidden {
				t.Errorf("window row = %+v, want visible action", win)
			}
			if quit := items[5]; quit.id != MenuQuit || quit.hidden {
				t.Errorf("quit row = %+v, want visible action", quit)
			}
		})
	}
}

func TestTrayMenuAttentionFixRow(t *testing.T) {
	deps := newMenuTestDeps(t, true)
	deps.Status = NewStatus(StatusAttention)
	deps.Status.Set(StatusAttention, "mod_hash_rejected")

	items := trayMenu(deps)
	if len(items) != 6 {
		t.Fatalf("items = %d, want 6", len(items))
	}
	if fix := items[2]; fix.id != MenuWhatsWrong || fix.hidden {
		t.Errorf("fix row = %+v, want visible", fix)
	}
	// Logged out + attention: the fix row stays hidden (login lives in the
	// window), but remains in the model so it can reappear after a login.
	deps2 := newMenuTestDeps(t, false)
	deps2.Status = NewStatus(StatusAttention)
	deps2.Status.Set(StatusAttention, "Login failed")
	if fix := trayMenu(deps2)[2]; fix.id != MenuWhatsWrong || !fix.hidden {
		t.Errorf("logged-out fix row = %+v, want hidden", fix)
	}
	deps.Status.Set(StatusWatching, "")
	for _, it := range trayMenu(deps) {
		if it.id == MenuWhatsWrong && !it.hidden {
			t.Error("fix row must hide again once the attention state clears")
		}
	}
}

// TestReplaysLabel covers the folder wording that moved from the tray menu
// into the window's Settings tab. One folder shows its name; several show the
// count with the full list in the tooltip, so an alt account is visibly
// covered instead of wondering which single folder won.
func TestReplaysLabel(t *testing.T) {
	if got := replaysLabel(nil); got != "No replay folder" {
		t.Errorf("empty label = %q", got)
	}
	one := []string{"/games/sc2/Replays/Multiplayer"}
	if got := replaysLabel(one); got != "Replay folder: Multiplayer" {
		t.Errorf("one label = %q", got)
	}
	if got := replaysTip(one); got != "Replay folders: /games/sc2/Replays/Multiplayer" {
		t.Errorf("one tip = %q", got)
	}
	two := []string{
		"/games/sc2/Replays/Multiplayer",
		"/games/sc2/Accounts/132104812/2-S2-1-999999/Replays/Multiplayer",
	}
	if got := replaysLabel(two); got != "2 replay folders" {
		t.Errorf("two label = %q", got)
	}
	wantTip := "Replay folders: /games/sc2/Replays/Multiplayer, /games/sc2/Accounts/132104812/2-S2-1-999999/Replays/Multiplayer"
	if got := replaysTip(two); got != wantTip {
		t.Errorf("two tip = %q, want %q", got, wantTip)
	}
}
