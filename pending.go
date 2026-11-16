package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Pending-match poll tuning: a 202-accepted upload (the player's replay is
// in, the match is not resolved yet) is followed to resolution so the player
// still gets the "did my game count?" toast when the opponent's replay or
// the grace timer confirms the match. The interval is a quiet background
// poll of a single match, and the cap comfortably covers the confirm window
// plus backend processing. Both are vars so tests can shorten them.
var (
	pendingPollInterval = 20 * time.Second
	pendingPollMax      = 30 * time.Minute
)

// startPendingPoll follows the just-uploaded pending match to resolution and
// toasts the outcome. A player has at most one pending match, so only one
// poller runs at a time; a second 202 while one is active is covered by the
// running poller.
func (u *Uploader) startPendingPoll() {
	u.pollMu.Lock()
	defer u.pollMu.Unlock()
	if u.polling {
		return
	}
	u.polling = true
	// Snapshot the tuning so the poller goroutine never re-reads the vars
	// (tests swap them; a mid-run read would be a data race).
	interval, max := pendingPollInterval, pendingPollMax
	go func() {
		defer func() {
			u.pollMu.Lock()
			u.polling = false
			u.pollMu.Unlock()
		}()
		u.followPendingMatch(interval, max)
	}()
}

// followPendingMatch discovers the pending match id via GET /queue/status
// (the 202 upload response carries no id), then polls the public
// GET /matches/{id} until the match leaves "pending" and toasts the outcome
// through the same matchNotice path as a resolved upload. Everything here is
// fail-soft: on any error it keeps polling until the cap, then goes quiet
// and leaves the outcome to the site's own notifications.
func (u *Uploader) followPendingMatch(interval, max time.Duration) {
	base := u.cfg.BaseURL()
	if base == "" {
		return
	}
	deadline := time.Now().Add(max)

	// Discovery: up to three queue-status reads to find the pending match.
	// If the match is already gone from the queue status (resolved and
	// cleared between upload and first poll), there is no id to follow and
	// no way to learn the outcome here - the site handles that case.
	var matchID string
	for attempt := 0; attempt < 3; attempt++ {
		m := u.fetchQueuePendingMatch(base)
		if m != nil {
			matchID = m.ID
			if u.toastIfResolved(m) {
				return
			}
			break
		}
		if attempt < 2 {
			time.Sleep(interval)
		}
	}
	if matchID == "" {
		u.logger.Printf("pending: no pending match visible after upload; leaving the outcome to site notifications")
		return
	}

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		status, body := u.getMatch(base, matchID)
		if status == http.StatusNotFound {
			u.logger.Printf("pending: match %s no longer exists", matchID)
			return
		}
		if status < 200 || status >= 300 {
			continue // transient error: keep polling until the cap
		}
		if u.toastIfResolved(parseMatchResult(body)) {
			return
		}
	}
	u.logger.Printf("pending: match %s did not resolve within %s; leaving the outcome to site notifications", matchID, max)
}

// toastIfResolved notifies the outcome of m when it has one. Returns true
// when the match is resolved (the poller stops), false while it is still
// pending (or carries no readable outcome yet).
func (u *Uploader) toastIfResolved(m *matchResult) bool {
	var myID string
	if u.auth != nil {
		_, myID = u.auth.Credentials()
	}
	title, body := matchNotice(m, myID)
	if title == "" {
		return false
	}
	if u.notify != nil {
		u.notify(u.logger, NoticeInfo, title, body)
	}
	u.logger.Printf("pending: match resolved: %s", title)
	return true
}

// queueStatusBody is the minimal GET /queue/status shape the poller needs.
type queueStatusBody struct {
	PendingMatch *matchResult `json:"pending_match"`
}

// fetchQueuePendingMatch reads the queue status with the device-flow API
// key and returns the pending match, or nil when absent/unreadable.
func (u *Uploader) fetchQueuePendingMatch(base string) *matchResult {
	apiKey, _ := u.auth.Credentials()
	req, err := http.NewRequest(http.MethodGet, base+"/queue/status", nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var qs queueStatusBody
	if err := json.Unmarshal(bytes.TrimSpace(body), &qs); err != nil {
		return nil
	}
	return qs.PendingMatch
}

// getMatch fetches the public match detail and returns the status code plus
// body (nil body on error; the caller treats non-2xx as transient).
func (u *Uploader) getMatch(base, matchID string) (int, []byte) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/matches/%s", base, matchID), nil)
	if err != nil {
		return 0, nil
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, body
}
