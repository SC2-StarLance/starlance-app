package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var windowAssets embed.FS

// WindowService is the Go side of the window. It reads the same config and
// token the uploader uses and calls the ladder API directly.
type WindowService struct {
	cfg       *Config
	auth      *AuthState
	logger    *log.Logger
	client    *http.Client
	deps      TrayDeps
	stop      context.CancelFunc
	restartCh chan struct{}

	mu        sync.Mutex
	accountID string
}

// Bootstrap is the first screen's data: who is logged in and what they can
// queue for.
type Bootstrap struct {
	LoggedIn        bool                `json:"loggedIn"`
	Name            string              `json:"name"`
	Regions         []string            `json:"regions"`
	Folders         []string            `json:"folders"`
	StartWithSystem bool                `json:"startWithSystem"`
	SetupDone       bool                `json:"setupDone"`
	Version         string              `json:"version"`
	InstallPath     string              `json:"installPath"`
	SiteURL         string              `json:"siteURL"`
	Ranks           map[string]string   `json:"ranks"`
	Stats           map[string]RaceStat `json:"stats"`
	Ratings         []RatingSeries      `json:"ratings"`
	// PlayedRegions are the regions the account already has a confirmed match
	// on, so the window only reminds a player to join the in-game channel on a
	// server they have never played. Best effort: a failed call leaves it empty.
	PlayedRegions []string `json:"playedRegions"`
}

// RaceStat is one race's current-season ladder line, the numbers the window
// paints on the race tiles and the idle queue bar.
// RatingSeries is one race's rating line for the active season, trimmed to
// what the account sparkline draws.
type RatingSeries struct {
	Race   string        `json:"race"`
	Points []RatingPoint `json:"points"`
}

type RatingPoint struct {
	Rating int    `json:"rating"`
	Result string `json:"result"`
	T      string `json:"t"`
}

type RaceStat struct {
	League       string `json:"league"`
	Rating       int    `json:"rating"`
	Wins         int    `json:"wins"`
	Losses       int    `json:"losses"`
	RankInLeague *int   `json:"rankInLeague,omitempty"`
}

func runWindow(cfg *Config, auth *AuthState, logger *log.Logger, deps TrayDeps, stop context.CancelFunc, restartCh chan struct{}) error {
	sub, err := fs.Sub(windowAssets, "frontend/dist")
	if err != nil {
		return fmt.Errorf("window assets: %w", err)
	}
	svc := &WindowService{
		cfg:       cfg,
		auth:      auth,
		logger:    logger,
		client:    &http.Client{Timeout: 10 * time.Second},
		deps:      deps,
		stop:      stop,
		restartCh: restartCh,
	}
	// The window starts hidden: the first update check decides whether the
	// player sees the main window or the small update card. The tray toggle
	// tracks whichever one is on screen.
	windowVisible.Store(false)
	// The update may swap the binary at any time, but the restart waits for an
	// idle session. This is the only component that can read /queue/status.
	setSessionGate(svc.canRestartNow)
	go svc.watchHiddenMatches()
	return wails.Run(&options.App{
		Title:             windowTitle(),
		Width:             mainWindowWidth,
		Height:            mainWindowHeight,
		MinWidth:          380,
		MinHeight:         160,
		DisableResize:     false,
		StartHidden:       true,
		BackgroundColour:  &options.RGBA{R: 6, G: 11, B: 20, A: 255},
		AssetServer:       &assetserver.Options{Assets: sub},
		Linux:             &linux.Options{Icon: encodePNG(256), ProgramName: linuxAppID},
		Bind:              []interface{}{svc},
		HideWindowOnClose: true,
		OnStartup: func(ctx context.Context) {
			setWindowContext(ctx)
			runtime.WindowCenter(ctx)
			go svc.startUpdateFlow()
		},
	})
}

const (
	mainWindowWidth  = 1120
	mainWindowHeight = 820
	updateCardWidth  = 420
	updateCardHeight = 190
)

var (
	mainWindowShown atomic.Bool
	updateCardShown atomic.Bool
)

// startUpdateFlow wires the update phases to the window and runs the update
// loop. The window is still hidden here: "downloading" turns it into the
// small update card, "uptodate" (or a failed check) opens the real window.
// A dev build never updates, so it opens the main window immediately.
func (s *WindowService) startUpdateFlow() {
	if !selfUpdateEnabled() {
		s.showMainWindow()
		return
	}
	setUpdatePhaseSink(s.onUpdatePhase)
	runAutoUpdate(windowContext(), s.stop, s.logger, Notify, s.restartCh)
}

func (s *WindowService) onUpdatePhase(p UpdatePhase) {
	ctx := windowContext()
	s.logger.Printf("window: update phase %q ctx=%v", p.Phase, ctx != nil)
	if ctx == nil {
		return
	}
	switch p.Phase {
	case "checking":
		// Nothing to show: the window stays hidden until a download starts.
	case "downloading":
		// The card belongs to the startup path. A mid-session check leaves the
		// full window alone instead of shrinking it under the player.
		if !mainWindowShown.Load() {
			if !updateCardShown.Swap(true) {
				runtime.WindowSetSize(ctx, updateCardWidth, updateCardHeight)
				runtime.WindowCenter(ctx)
				runtime.WindowShow(ctx)
				windowVisible.Store(true)
			}
			runtime.EventsEmit(ctx, "update-phase", p)
		}
	case "installed":
		if !mainWindowShown.Load() {
			runtime.EventsEmit(ctx, "update-phase", p)
		}
	case "ready":
		// The new binary is installed but the player is queued or in a match.
		// Give them the app now; the restart waits for idle.
		s.showMainWindow()
	case "restarting":
		runtime.EventsEmit(ctx, "update-phase", p)
	case "uptodate":
		s.showMainWindow()
	case "failed":
		if updateCardShown.Load() {
			// A mid-session update failed after the card was up: put the
			// full window back and let the page clear the card.
			runtime.WindowSetSize(ctx, mainWindowWidth, mainWindowHeight)
			runtime.WindowCenter(ctx)
			updateCardShown.Store(false)
			runtime.EventsEmit(ctx, "update-phase", p)
			return
		}
		s.showMainWindow()
	}
}

// showMainWindow reveals the normal app window, once.
func (s *WindowService) showMainWindow() {
	ctx := windowContext()
	if ctx == nil || !mainWindowShown.CompareAndSwap(false, true) {
		return
	}
	runtime.WindowCenter(ctx)
	runtime.WindowShow(ctx)
	windowVisible.Store(true)
	runtime.EventsEmit(ctx, "update-phase", UpdatePhase{Phase: "idle"})
}

// windowTitle names the profile in the title bar so two side-by-side windows
// are never confused.
func windowTitle() string {
	if profileName == "" {
		return "StarLance App"
	}
	return "StarLance App (" + profileName + ")"
}

func (s *WindowService) Bootstrap() (Bootstrap, error) {
	out := Bootstrap{
		LoggedIn:        s.auth.LoggedIn(),
		Folders:         s.folders(),
		StartWithSystem: s.cfg.StartWithSystem,
		SetupDone:       s.cfg.SetupDone,
		Version:         Version,
		InstallPath:     installLocation(),
		SiteURL:         siteBaseURL(),
		Ranks:           map[string]string{},
		Stats:           map[string]RaceStat{},
		Ratings:         []RatingSeries{},
	}
	if !out.LoggedIn {
		return out, nil
	}
	var me struct {
		ID              string   `json:"id"`
		Name            string   `json:"name"`
		VerifiedRegions []string `json:"verified_regions"`
		Books           []struct {
			Race   string  `json:"race"`
			League *string `json:"league"`
			Rating int     `json:"rating"`
			Wins   int     `json:"wins"`
			Losses int     `json:"losses"`
		} `json:"books"`
		BattlenetAccounts []struct {
			ID string `json:"id"`
		} `json:"battlenet_accounts"`
	}
	if err := s.call(http.MethodGet, "/me", nil, &me); err != nil {
		// A revoked token must not leave the window stuck behind a broken
		// session: drop it so the login panel can take over.
		var unauth *errUnauthorized
		if errors.As(err, &unauth) {
			doLogout(s.logger, s.deps)
			out.LoggedIn = false
			return out, nil
		}
		return out, err
	}
	out.Name = me.Name
	out.Regions = orderRegions(me.VerifiedRegions)
	for _, b := range me.Books {
		if b.League != nil && *b.League != "" {
			out.Ranks[b.Race] = *b.League
			out.Stats[b.Race] = RaceStat{
				League: *b.League,
				Rating: b.Rating,
				Wins:   b.Wins,
				Losses: b.Losses,
			}
		}
	}
	// rank_in_league lives on the public profile, not on /me. Best effort:
	// the queue and settings still work when this call fails.
	var profile struct {
		Books []struct {
			Race         string `json:"race"`
			RankInLeague *int   `json:"rank_in_league"`
		} `json:"books"`
	}
	if err := s.call(http.MethodGet, "/players/name/"+url.PathEscape(out.Name), nil, &profile); err == nil {
		for _, b := range profile.Books {
			if st, ok := out.Stats[b.Race]; ok {
				st.RankInLeague = b.RankInLeague
				out.Stats[b.Race] = st
			}
		}
	}
	if me.ID != "" {
		var hist struct {
			Series []struct {
				Race   string `json:"race"`
				Points []struct {
					Rating int    `json:"rating"`
					Result string `json:"result"`
					T      string `json:"t"`
				} `json:"points"`
			} `json:"series"`
		}
		if err := s.call(http.MethodGet, "/players/"+url.PathEscape(me.ID)+"/ratings", nil, &hist); err == nil {
			for _, se := range hist.Series {
				series := RatingSeries{Race: se.Race}
				for _, pt := range se.Points {
					series.Points = append(series.Points, RatingPoint{Rating: pt.Rating, Result: pt.Result, T: pt.T})
				}
				out.Ratings = append(out.Ratings, series)
			}
		}
	}
	if me.ID != "" {
		// Three pages of confirmed matches cover the "has this account ever
		// played on region X" question for every realistic history without
		// walking a long list; the per-region reminder only mis-fires for an
		// account whose only game on a region sits beyond 150 matches back.
		seen := map[string]bool{}
		for page := 1; page <= 3; page++ {
			var history struct {
				Matches []struct {
					Region string `json:"region"`
				} `json:"matches"`
			}
			path := "/players/" + url.PathEscape(me.ID) + "/matches?status=confirmed&page=" + strconv.Itoa(page)
			if err := s.call(http.MethodGet, path, nil, &history); err != nil {
				break
			}
			for _, m := range history.Matches {
				if m.Region == "" || seen[m.Region] {
					continue
				}
				seen[m.Region] = true
				out.PlayedRegions = append(out.PlayedRegions, m.Region)
			}
			if len(history.Matches) < 50 {
				break
			}
		}
	}
	// Dev switch: a test account that already played looks like a fresh one,
	// so the in-game channel reminder can be exercised without deleting real
	// match history.
	if isDevBuild() && os.Getenv("STARLANCE_DEV_ASSUME_NEW_PLAYER") == "1" {
		if len(out.PlayedRegions) > 0 {
			s.logger.Printf("dev: assuming a new player; hiding %d played region(s): %v", len(out.PlayedRegions), out.PlayedRegions)
		}
		out.PlayedRegions = nil
	}
	if len(me.BattlenetAccounts) > 0 {
		s.mu.Lock()
		s.accountID = me.BattlenetAccounts[0].ID
		s.mu.Unlock()
	}
	return out, nil
}

// Login starts the same device flow the tray uses and returns immediately;
// the window polls Bootstrap until LoggedIn flips.
func (s *WindowService) Login() error {
	if s.auth.LoggedIn() {
		return nil
	}
	if !s.auth.BeginLogin() {
		return errors.New("login already in progress")
	}
	ctx := windowContext()
	if ctx == nil {
		ctx = context.Background()
	}
	code, err := StartDeviceLogin(ctx, s.cfg, s.logger, func(url string) error {
		runtime.BrowserOpenURL(ctx, url)
		return nil
	})
	if err != nil {
		s.auth.EndLogin()
		return err
	}
	go func() {
		defer s.auth.EndLogin()
		if err := WaitDeviceLogin(ctx, s.cfg, s.auth, s.logger, code); err != nil {
			s.logger.Printf("window: login failed: %v", err)
		}
	}()
	return nil
}

// Logout drops the local token. The window returns to the login panel.
func (s *WindowService) Logout() error {
	doLogout(s.logger, s.deps)
	return nil
}

// VerifyRegions re-reads the account's Battle.net toon lists, so a region the
// player has since created a profile in unlocks without a re-login.
func (s *WindowService) VerifyRegions() ([]string, error) {
	var out struct {
		VerifiedRegions []string `json:"verified_regions"`
	}
	if err := s.call(http.MethodPost, "/me/verify-regions", nil, &out); err != nil {
		return nil, err
	}
	return orderRegions(out.VerifiedRegions), nil
}

// AddFolder opens the folder picker and adds the chosen folder to the watched
// set, applying live through the same rebind the tray uses.
func (s *WindowService) AddFolder() ([]string, error) {
	ctx := windowContext()
	if ctx == nil {
		return nil, errors.New("window is not ready")
	}
	dir, err := runtime.OpenDirectoryDialog(ctx, runtime.OpenDialogOptions{
		Title: "Choose a StarCraft II multiplayer replays folder",
	})
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return s.folders(), nil
	}
	if err := addReplayFolder(s.deps, dir, s.logger); err != nil {
		return nil, err
	}
	return s.folders(), nil
}

// RemoveFolder drops a watched folder. The last folder cannot be removed:
// the watcher needs somewhere to look.
func (s *WindowService) RemoveFolder(dir string) ([]string, error) {
	if s.deps.RemoveFolder == nil {
		return s.folders(), errors.New("removing folders is unavailable")
	}
	if err := s.deps.RemoveFolder(dir); err != nil {
		return s.folders(), err
	}
	if s.cfg.ReplayPath == dir {
		s.cfg.ReplayPath = ""
		_ = SaveConfig(settingsPath, s.cfg)
	}
	return s.folders(), nil
}

// CompleteSetup retires the first-run guide. Persisted so a restart does not
// show the three steps again.
func (s *WindowService) CompleteSetup() error {
	s.cfg.SetupDone = true
	return SaveConfig(settingsPath, s.cfg)
}

// SetStartWithSystem flips launch-at-login and persists it.
func (s *WindowService) SetStartWithSystem(on bool) error {
	return setStartWithSystem(s.deps, on, s.logger)
}

// OpenDashboard opens the player's dashboard in the default browser.
func (s *WindowService) OpenDashboard() {
	ctx := windowContext()
	if ctx != nil {
		runtime.BrowserOpenURL(ctx, dashboardURL)
	}
}

// OpenReplays opens the first watched folder in the file manager.
func (s *WindowService) OpenReplays() error {
	dirs := s.folders()
	if len(dirs) == 0 {
		return errors.New("no replays folder is being watched yet")
	}
	return openFolder(dirs[0])
}

// OpenFolder opens one of the watched replay folders in the file manager.
func (s *WindowService) OpenFolder(dir string) error {
	for _, d := range s.folders() {
		if d == dir {
			return openFolder(dir)
		}
	}
	return errors.New("that folder is not being watched")
}

// CheckUpdate asks the updater to look for a new release now.
func (s *WindowService) CheckUpdate() {
	if s.deps.CheckUpdateNow != nil {
		s.deps.CheckUpdateNow()
	}
}

// Quit ends the app from the window, the same path as the tray Quit.
func (s *WindowService) Quit() {
	if s.stop != nil {
		s.stop()
	}
	quitWindow()
}

func (s *WindowService) folders() []string {
	if s.deps.WatchedDirs != nil {
		if dirs := s.deps.WatchedDirs(); len(dirs) > 0 {
			return dirs
		}
	}
	if s.cfg.ReplayPath != "" {
		return []string{s.cfg.ReplayPath}
	}
	return nil
}

func (s *WindowService) StartQueue(race string, regions []string, preferred string) error {
	// No watched folder means no uploaded result, so a queue entry would be a
	// promise the app cannot keep. Block it and send the player to Settings.
	if len(s.folders()) == 0 {
		return errors.New("add your StarCraft II replays folder in Settings before queueing")
	}
	switch race {
	case "terran", "protoss", "zerg", "random":
	default:
		return errors.New("pick a race first")
	}
	regions = orderRegions(regions)
	if len(regions) == 0 {
		return errors.New("pick at least one region")
	}
	// A preference outside the selected set is stale localStorage, not a
	// refused join: the backend drops it too (spec 2.31.0).
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	if !slices.Contains(regions, preferred) {
		preferred = ""
	}
	id, err := s.ensureAccountID()
	if err != nil {
		return err
	}
	body := map[string]any{
		"race":             race,
		"bnet_account_id":  id,
		"regions":          regions,
		"preferred_region": preferred,
	}
	if err := s.call(http.MethodPost, "/queue", body, nil); err != nil {
		return err
	}
	startTraySpin()
	return nil
}

// AcceptMatch confirms presence in a found match. Both players have 60
// seconds to accept; a player who accepts and is left waiting goes back into
// the queue, while the silent player is dropped with no penalty.
func (s *WindowService) AcceptMatch(matchID string) error {
	if matchID == "" {
		return errors.New("no match to accept")
	}
	return s.call(http.MethodPost, "/matches/"+matchID+"/draft/confirm", map[string]any{}, nil)
}

// DeclineMatch answers a found match with "no". The match is voided for both
// players; an opponent who already accepted goes back in the queue.
func (s *WindowService) DeclineMatch(matchID string) error {
	if matchID == "" {
		return errors.New("no match to decline")
	}
	return s.call(http.MethodPost, "/matches/"+matchID+"/draft/decline", map[string]any{}, nil)
}

// LeaveMatch is the explicit exit from a ready match before the game starts.
// The opponent is requeued with their carried wait; the leaver waits out the
// server's leave lock before queueing again.
func (s *WindowService) LeaveMatch(matchID string) (map[string]any, error) {
	if matchID == "" {
		return nil, errors.New("no match to leave")
	}
	var out map[string]any
	if err := s.call(http.MethodPost, "/matches/"+matchID+"/leave", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *WindowService) StopQueue() error {
	if err := s.call(http.MethodDelete, "/queue", nil, nil); err != nil {
		return err
	}
	stopTraySpin()
	return nil
}

// HideToTray hides the window and leaves StarLance running in the tray.
// Uploads and queue polling continue; Open StarLance brings the window back.
func (s *WindowService) HideToTray() {
	windowVisible.Store(false)
	runtime.WindowHide(windowContext())
}

// MatchFound pulls the window to the front when a pairing lands. The player
// asked to be interrupted exactly here, so this is the one place the uploader
// raises its voice: window, toast, and the in-window StarCraft II match sound.
func (s *WindowService) MatchFound() {
	// The Go poll and the page both report the same pairing when the window
	// comes back; one cue per match is enough.
	now := time.Now().UnixMilli()
	if prev := lastMatchFound.Swap(now); now-prev < 5000 {
		return
	}
	stopTraySpin()
	s.logger.Printf("match found: raising window (visible=%v)", windowVisible.Load())
	raiseWindow(windowContext())
	playSound("match-found")
	Notify(s.logger, NoticeInfo, "Match found", "Open the StarLance App and accept the match.")
}

// CopyText puts a match card's details on the clipboard so the player can
// paste them into Battle.net chat or Discord while setting up the lobby.
func (s *WindowService) CopyText(text string) error {
	return runtime.ClipboardSetText(windowContext(), text)
}

// OpenURL opens a StarLance site page in the default browser.
func (s *WindowService) OpenURL(url string) error {
	base := siteBaseURL()
	if url != base && !strings.HasPrefix(url, base+"/") {
		return errors.New("refusing to open a non-StarLance URL")
	}
	runtime.BrowserOpenURL(windowContext(), url)
	return nil
}

func siteBaseURL() string {
	if isDevBuild() {
		return "http://localhost:4321"
	}
	return "https://starlance.club"
}

func (s *WindowService) Status() (map[string]any, error) {
	var out map[string]any
	if err := s.call(http.MethodGet, "/queue/status", nil, &out); err != nil {
		return nil, err
	}
	// The queue can end outside the window (the website, or a match landing
	// while the tray only watches). Stop the icon animation whenever the
	// search is no longer live, not only when this process stopped it.
	if state, _ := out["state"].(string); state != "searching" {
		stopTraySpin()
	}
	// Folders ride along with the status poll so a folder added from the tray
	// clears the Play tab's blocked state without reopening the window.
	out["folders"] = s.folders()
	return out, nil
}

// canRestartNow gates the self-update restart. The binary swap is safe at any
// time, but a restart during a queue search, the map draft, or a live match
// would drop the player out of the queue or lose the replay: the watcher only
// uploads files that appear while the app is running, so a replay written
// during the restart is never sent. A pending match covers the draft, the
// ready stage, and the game itself, and stays pending until both replays
// land; waiting through it is deliberate because a lost result is worse than
// a late update. Ambiguity refuses the restart: a network failure could hide a
// live game, while "not logged in" or an expired key means this app has no
// session to interrupt.
func (s *WindowService) canRestartNow() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out struct {
		State        string          `json:"state"`
		PendingMatch json.RawMessage `json:"pending_match"`
	}
	if err := s.callCtx(ctx, http.MethodGet, "/queue/status", nil, &out); err != nil {
		if errors.Is(err, errNotLoggedIn) {
			return true
		}
		var unauthorized *errUnauthorized
		return errors.As(err, &unauthorized)
	}
	if out.State == "searching" {
		return false
	}
	return len(out.PendingMatch) == 0 || string(out.PendingMatch) == "null"
}

func (s *WindowService) ensureAccountID() (string, error) {
	s.mu.Lock()
	id := s.accountID
	s.mu.Unlock()
	if id != "" {
		return id, nil
	}
	var me struct {
		BattlenetAccounts []struct {
			ID string `json:"id"`
		} `json:"battlenet_accounts"`
	}
	if err := s.call(http.MethodGet, "/me", nil, &me); err != nil {
		return "", err
	}
	if len(me.BattlenetAccounts) == 0 {
		return "", errors.New("link a Battle.net account first")
	}
	s.mu.Lock()
	s.accountID = me.BattlenetAccounts[0].ID
	s.mu.Unlock()
	return s.accountID, nil
}

func (s *WindowService) call(method, path string, body any, out any) error {
	return s.callCtx(context.Background(), method, path, body, out)
}

// errNotLoggedIn means there is no saved API key. It is a sentinel so callers
// can tell "nothing this app can be doing" from a network failure.
var errNotLoggedIn = errors.New("log in to StarLance first")

func (s *WindowService) callCtx(ctx context.Context, method, path string, body any, out any) error {
	key, _ := s.auth.Credentials()
	if key == "" {
		return errNotLoggedIn
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.BaseURL()+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.client.Do(req)
	if err != nil {
		return errors.New("cannot reach StarLance")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return &errUnauthorized{}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		var apiErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Message != "" {
			return errors.New(apiErr.Message)
		}
		return fmt.Errorf("StarLance returned %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

var regionOrder = []string{"us", "eu", "kr"}

// orderRegions keeps a stable display/request order and drops anything the
// server does not know, so a stale saved preference cannot block the queue.
func orderRegions(in []string) []string {
	seen := map[string]bool{}
	for _, r := range in {
		seen[strings.ToLower(r)] = true
	}
	out := make([]string, 0, len(seen))
	for _, r := range regionOrder {
		if seen[r] {
			out = append(out, r)
		}
	}
	return out
}

var (
	windowMu     sync.Mutex
	windowCtx    context.Context
	quitOnWindow bool
)

func windowContext() context.Context {
	windowMu.Lock()
	defer windowMu.Unlock()
	return windowCtx
}

func setWindowContext(ctx context.Context) {
	windowMu.Lock()
	windowCtx = ctx
	quit := quitOnWindow
	windowMu.Unlock()
	if quit {
		runtime.Quit(ctx)
	}
}

// watchHiddenMatches notices a pairing while the window is minimized or in
// the tray. WebKit suspends the page's timers once the widget is not mapped,
// so the frontend poll stops there and cannot be the thing that wakes the
// player; this poll runs in Go, where nothing throttles it.
func (s *WindowService) watchHiddenMatches() {
	announced := ""
	for {
		time.Sleep(3 * time.Second)
		if windowVisible.Load() {
			continue
		}
		var out queueStatusBody
		if err := s.call(http.MethodGet, "/queue/status", nil, &out); err != nil {
			continue
		}
		m := out.PendingMatch
		if m == nil {
			announced = ""
			continue
		}
		if m.Status != "pending" || m.ID == announced {
			continue
		}
		announced = m.ID
		s.logger.Printf("match found while hidden: waking the window for %s", m.ID)
		s.MatchFound()
	}
}

// windowVisible tracks whether the window is mapped. Wails has no
// cross-platform visibility query, and its Linux WindowIsNormal ignores hidden
// windows, so the frontend reports the Page Visibility API state, which WebKit
// derives from the GTK widget's mapped state.
var windowVisible atomic.Bool

// lastMatchFound is the Unix-millisecond stamp of the last raise, so the Go
// poller and the page cannot sound the match cue twice for one pairing.
var lastMatchFound atomic.Int64

// SetWindowVisible is reported by the frontend on visibilitychange and by a
// slow poll that self-corrects after a native close-to-tray hide.
func (s *WindowService) SetWindowVisible(visible bool) {
	windowVisible.Store(visible)
}

// toggleWindow shows the window when it is hidden and hides it when the tray
// icon is clicked while the window is already open.
func toggleWindow() {
	ctx := windowContext()
	if ctx == nil {
		return
	}
	if windowVisible.Load() {
		windowVisible.Store(false)
		runtime.WindowHide(ctx)
		return
	}
	runtime.WindowShow(ctx)
	runtime.WindowUnminimise(ctx)
	windowVisible.Store(true)
}

// showWindow brings the window back from the tray. The window is part of this
// process; closing it only hides it.
func showWindow() {
	windowMu.Lock()
	ctx := windowCtx
	windowMu.Unlock()
	if ctx != nil {
		windowVisible.Store(true)
		raiseWindow(ctx)
	}
}

// raiseWindow brings the window back from the tray or from behind other
// windows and asks the compositor to focus it. Wayland compositors may keep
// the keyboard where it was (focus stealing prevention), so the window is
// also held above other windows for a moment: the interruption stays visible
// even on the sessions that refuse the focus request.
func raiseWindow(ctx context.Context) {
	if ctx == nil {
		return
	}
	runtime.WindowShow(ctx)
	runtime.WindowUnminimise(ctx)
	runtime.WindowSetAlwaysOnTop(ctx, true)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if c := windowContext(); c != nil {
			runtime.WindowSetAlwaysOnTop(c, false)
		}
	}()
}

// quitWindow ends the Wails loop, which is the app's main goroutine. If the
// window has not started yet, the request is honored on startup.
func quitWindow() {
	windowMu.Lock()
	ctx := windowCtx
	if ctx == nil {
		quitOnWindow = true
	}
	windowMu.Unlock()
	if ctx != nil {
		runtime.Quit(ctx)
	}
}
