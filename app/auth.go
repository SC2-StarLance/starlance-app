package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// pollInterval and loginTimeout govern the device-flow poll loop: 2s between
// polls, giving up after 10 minutes of waiting for the browser approval. They
// are vars so tests can shorten them.
var (
	pollInterval = 2 * time.Second
	loginTimeout = 10 * time.Minute
)

// tokenFile is the persisted, user-only credential store. It is deliberately
// separate from the config JSON and never logged or displayed.
type tokenFile struct {
	APIKey   string `json:"api_key"`
	PlayerID string `json:"player_id"`
}

// AuthState is the uploader's mutex-guarded in-memory auth state, backed by
// the 0600 token file so login survives restarts.
type AuthState struct {
	mu       sync.RWMutex
	path     string
	apiKey   string
	playerID string
	login    atomic.Bool // guards against concurrent device flows (tray double-click)
}

// BeginLogin atomically claims the single in-flight login slot. It returns
// false when a device flow is already running, so a second tray click (or any
// other re-entry) cannot spawn a competing poll loop racing auth.Set.
func (a *AuthState) BeginLogin() bool { return a.login.CompareAndSwap(false, true) }

// EndLogin releases the in-flight login slot.
func (a *AuthState) EndLogin() { a.login.Store(false) }

// TokenPath returns the 0600 token file location inside the per-user app dir:
// %APPDATA%\StarLance\uploader\auth.json on Windows,
// ~/.config/starlance-uploader/auth.json elsewhere (see appDir).
func TokenPath() (string, error) {
	dir, err := appDir()
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}
	return filepath.Join(dir, AuthFilename), nil
}

// LoadAuth reads the token file at path into a fresh AuthState. A missing file
// yields a logged-out state; a corrupt file is treated the same way (re-login
// is always safe, unlike the dedup records) with a warning logged.
func LoadAuth(path string, logger *log.Logger) *AuthState {
	a := &AuthState{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Printf("auth: cannot read token file %s: %v (treating as logged out)", path, err)
		}
		return a
	}
	var t tokenFile
	if err := json.Unmarshal(b, &t); err != nil {
		logger.Printf("auth: token file %s is corrupt (%v); log in again", path, err)
		return a
	}
	if t.APIKey == "" {
		logger.Printf("auth: token file %s has no API key; log in again", path)
		return a
	}
	a.apiKey = t.APIKey
	a.playerID = t.PlayerID
	logger.Printf("auth: loaded existing token from %s", path)
	return a
}

// LoggedIn reports whether a token is currently held in memory.
func (a *AuthState) LoggedIn() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.apiKey != ""
}

// Credentials returns the current API key and player ID (empty when logged out).
func (a *AuthState) Credentials() (apiKey, playerID string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.apiKey, a.playerID
}

// Set stores the credentials in memory and persists them to the token file
// with user-only permissions. The token is never written to the config JSON.
func (a *AuthState) Set(apiKey, playerID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.apiKey = apiKey
	a.playerID = playerID
	if err := a.saveLocked(); err != nil {
		return err
	}
	resetSessionExpiredNotice()
	return nil
}

// Clear wipes the in-memory token and removes the token file (log out).
func (a *AuthState) Clear() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.apiKey = ""
	a.playerID = ""
	if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// saveLocked persists the token atomically (writeFileAtomic) with 0600 perms.
// On Windows the file ACL inherits from the user-profile APPDATA dir, which is
// already user-only.
func (a *AuthState) saveLocked() error {
	b, err := json.MarshalIndent(tokenFile{APIKey: a.apiKey, PlayerID: a.playerID}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.path, append(b, '\n'), 0o600)
}

// DeviceLogin runs the zero-paste device flow: request a device code, open the
// authorize URL in the default browser, then poll until the backend issues the
// token (428 = keep waiting) or the 10-minute window expires. ctx cancellation
// (app quit) aborts an in-flight login so no auth.Set/notify can happen after
// shutdown. On success the token is stored via auth.Set and never displayed
// or logged.
func DeviceLogin(ctx context.Context, cfg *Config, auth *AuthState, logger *log.Logger) error {
	code, err := StartDeviceLogin(ctx, cfg, logger, openBrowserFn)
	if err != nil {
		return err
	}
	return WaitDeviceLogin(ctx, cfg, auth, logger, code)
}

// StartDeviceLogin requests a device code and opens the authorize URL with
// open. It returns the code WaitDeviceLogin polls with. It is split from the
// poll so the window can report a start failure immediately instead of
// leaving the player looking at "Waiting for browser" forever.
func StartDeviceLogin(ctx context.Context, cfg *Config, logger *log.Logger, open func(string) error) (string, error) {
	base := cfg.BaseURL()
	if base == "" {
		return "", errors.New("auth: backend URL is not configured")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	var start struct {
		DeviceCode   string `json:"device_code"`
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := postJSON(ctx, client, base+"/uploader/authorize/start", nil, &start); err != nil {
		return "", fmt.Errorf("auth: authorize/start: %w", err)
	}
	if start.DeviceCode == "" || start.AuthorizeURL == "" {
		return "", errors.New("auth: authorize/start returned an incomplete response")
	}
	if err := open(start.AuthorizeURL); err != nil {
		logger.Printf("auth: could not open browser (%v); open %s manually", err, start.AuthorizeURL)
	}
	return start.DeviceCode, nil
}

// WaitDeviceLogin polls until the backend issues the token or the login
// window expires.
func WaitDeviceLogin(ctx context.Context, cfg *Config, auth *AuthState, logger *log.Logger, deviceCode string) error {
	base := cfg.BaseURL()
	client := &http.Client{Timeout: 15 * time.Second}
	deadline := time.Now().Add(loginTimeout)
	for time.Now().Before(deadline) {
		// Cancellable poll sleep: ctx.Done() wins over the interval timer.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
		var tok struct {
			APIKey   string `json:"api_key"`
			PlayerID string `json:"player_id"`
		}
		pending, err := pollToken(ctx, client, base+"/uploader/authorize/poll", deviceCode, &tok)
		if err != nil {
			return fmt.Errorf("auth: authorize/poll: %w", err)
		}
		if pending {
			continue // 428: not yet approved, keep polling
		}
		if tok.APIKey == "" {
			// A 200 without a token is a malformed backend response; fail fast
			// instead of silently polling to the 10-minute cap.
			return errors.New("auth: authorize/poll returned 200 with an empty token")
		}
		if err := auth.Set(tok.APIKey, tok.PlayerID); err != nil {
			return fmt.Errorf("auth: persist token: %w", err)
		}
		logger.Printf("auth: login complete (token stored in %s)", auth.Path())
		return nil
	}
	return errors.New("auth: login timed out after 10 minutes waiting for browser approval")
}

// Path returns the token file location (for log messages only; the token
// itself is never included).
func (a *AuthState) Path() string { return a.path }

// pollToken POSTs one device-code poll. A 428 response reports pending=true
// (keep polling); any other non-2xx is an error.
func pollToken(ctx context.Context, client *http.Client, url, deviceCode string, tok any) (pending bool, err error) {
	body, err := json.Marshal(map[string]string{"device_code": deviceCode})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusPreconditionRequired { // 428: keep polling
		return true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return false, json.Unmarshal(b, tok)
}

// postJSON POSTs body (nil → empty) as JSON and decodes the response into out.
func postJSON(ctx context.Context, client *http.Client, url string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// LivenessProbe pings the backend via the public /mod-versions endpoint (the
// backend has no dedicated health route). It reports whether the backend is
// reachable so startup can silently proceed with an existing token.
func LivenessProbe(cfg *Config, client *http.Client) bool {
	base := cfg.BaseURL()
	if base == "" {
		return false
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Get(base + "/mod-versions")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}
