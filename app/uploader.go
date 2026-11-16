package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxUploadAttempts is the number of upload attempts before giving up and
// logging the failure (the file is not marked uploaded, so a later run/retry
// can pick it up).
const maxUploadAttempts = 4

// The blizzard-game-started signal is best-effort: at most maxSignalAttempts
// sends with signalRetryDelay of base backoff, then give up quietly
// (signalRetryDelay is a var so tests can shorten it).
const maxSignalAttempts = 2

var signalRetryDelay = 2 * time.Second

// retryDelays are the base sleeps between attempts; jitter is added on top.
var retryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// errNotRetryable marks an upload failure that will never succeed with more
// attempts (e.g. 4xx client errors and bad request construction), so
// uploadWithRetry can avoid futile retries and immediate queue blocking.
type errNotRetryable struct {
	err error
}

func (e *errNotRetryable) Error() string { return e.err.Error() }
func (e *errNotRetryable) Unwrap() error { return e.err }

// errBuildFailure marks an upload failure that occurred while constructing the
// upload request (e.g. the replay file could not be read when building the
// multipart body), before any HTTP POST was attempted. It lets callers avoid
// reporting a misleading "after N attempts" count when no upload happened.
type errBuildFailure struct {
	err error
}

func (e *errBuildFailure) Error() string { return e.err.Error() }
func (e *errBuildFailure) Unwrap() error { return e.err }

// Uploader posts replay files to a backend, deduping against State.
type Uploader struct {
	cfg    *Config
	auth   *AuthState
	state  *State
	status *Status
	notify Notifier // nil-safe for tests
	mods   *ModHashCache
	client *http.Client
	logger *log.Logger
	fileCh chan string

	pollMu  sync.Mutex // guards polling
	polling bool       // a pending-match poller is running
}

// NewUploader builds an Uploader reading from fileCh. auth supplies the
// X-API-Key and player_id from the device-flow token; status and notify are
// shared with the tray (nil-safe for tests). mods may be nil (no
// pre-detection); it only ever saves a roundtrip, never gates an upload.
func NewUploader(cfg *Config, auth *AuthState, state *State, status *Status, notify Notifier, mods *ModHashCache, logger *log.Logger, fileCh chan string) *Uploader {
	if notify == nil {
		notify = func(l *log.Logger, _ NoticeLevel, _, _ string) {}
	}
	if status == nil {
		status = NewStatus(StatusWatching)
	}
	return &Uploader{
		cfg:    cfg,
		auth:   auth,
		state:  state,
		status: status,
		notify: notify,
		mods:   mods,
		client: &http.Client{Timeout: 60 * time.Second},
		logger: logger,
		fileCh: fileCh,
	}
}

// Run consumes file paths from the channel until it is closed.
func (u *Uploader) Run() {
	for path := range u.fileCh {
		u.handleFile(path)
	}
}

func (u *Uploader) handleFile(path string) {
	name := filepath.Base(path)

	// Credentials come from the device-flow token, not the config file.
	var apiKey string
	if u.auth != nil {
		apiKey, _ = u.auth.Credentials()
	}
	if apiKey == "" {
		u.logger.Printf("upload: skip %s (not logged in; use the tray menu to log in)", name)
		u.status.Set(StatusLoggedOut, "")
		return
	}

	if err := checkReplaySize(path); err != nil {
		u.logger.Printf("upload: skip %s: %v", path, err)
		return
	}

	// Only upload files that still exist (may have been cleaned up).
	fh, err := os.Open(path)
	if err != nil {
		u.logger.Printf("upload: cannot open %s: %v", path, err)
		return
	}
	sum, err := sha256FileReader(fh)
	fh.Close()
	if err != nil {
		u.logger.Printf("upload: compute sha256 for %s: %v", path, err)
		return
	}

	if u.state.HasEntry(name, sum) {
		u.logger.Printf("upload: skip %s (already uploaded)", name)
		return
	}

	// Pre-detection (fail-open): a replay that certainly carries no accepted
	// StarLance mod hash is not a StarLance game (ladder, AI, custom), so
	// skip the roundtrip and its rejection noise. Unknown cases (no cache,
	// probe error, empty handles, outdated-but-known mod) upload as before
	// and the backend stays the only source of truth. The local classification
	// is the same evidence of a non-StarLance game the backend's mod/map
	// rejections used to provide, so the double-queue gate still applies:
	// only when no StarLance match is pending does the signal fire.
	if classifyStarLance(u.mods, path) {
		u.logger.Printf("upload: skip %s (not a StarLance game)", name)
		if !u.hasPendingStarLanceMatch() {
			u.reportBlizzardGameStarted()
		}
		return
	}

	match, attempts, err := u.uploadWithRetry(path)
	if err != nil {
		var be *errBuildFailure
		if errors.As(err, &be) {
			u.logger.Printf("upload: failed to build upload request for %s: %v (no upload attempted)", name, be.err)
			return
		}
		u.logger.Printf("upload: failed %s after %d attempt(s): %v", name, attempts, err)
		// A 401 means the token is revoked/expired: stop pretending we are
		// still logged in and tell the user to log in again. The status is
		// re-set per file (uploadWithRetry's defer resets it to Watching), but
		// the notification fires only once per session.
		if isUnauthorizedErr(err) {
			u.handleSessionExpired()
			return
		}
		// Surface backend rejection reasons as loud, actionable notifications,
		// never silent failures: the player must know the game will not count
		// and what to do about it.
		var rej *errRejected
		if errors.As(err, &rej) {
			// A StarLance-mod game played outside the queue (custom, AI) is
			// expected behavior, not a problem: informational toast, no
			// attention state (there is nothing to fix, and on Windows a
			// problem notice is a modal warning, far too loud for this).
			if rej.reason == "no_pending_match" {
				// Custom/AI games on the mod are expected and stay quiet.
				u.logger.Printf("upload: %s has no queued match; not a ladder game", name)
			} else {
				u.status.Set(StatusAttention, rejectionShort(rej.reason))
				if u.notify != nil {
					u.notify(u.logger, NoticeProblem, "Replay not uploaded",
						strings.TrimSuffix(name, ".SC2Replay")+": "+rejectionMessage(rej.reason))
				}
			}
			// Double-queue gate — report only when BOTH hold:
			// (a) the fresh replay failed StarLance validation with a
			//     non-StarLance-game reason (map/mod/identity mismatch — not
			//     the normal idle no_pending_match rejection), and
			// (b) the backend confirms no pending match (a rejection while a
			//     match IS pending is a wrong-lobby attempt the pending-match
			//     lifecycle handles — never an automatic queue exit; a failing
			//     status call reports a pending match, skipping the signal).
			if shouldSignalBlizzardGame(rej.reason, u.hasPendingStarLanceMatch()) {
				u.reportBlizzardGameStarted()
			}
			return
		}
		// The failure was retryable (network error / 5xx, e.g. a parser or
		// backend redeploy outlasting the retry budget): the replay is not
		// marked uploaded and will never be retried (never-backfill), so the
		// player must know the game may not have counted and where the manual
		// escape hatch is.
		u.status.Set(StatusAttention, "StarLance unreachable - replay not uploaded")
		if u.notify != nil {
			u.notify(u.logger, NoticeProblem, "Replay not uploaded",
				strings.TrimSuffix(name, ".SC2Replay")+": StarLance could not be reached. If this was a StarLance match, upload the replay from its match page on starlance.club.")
		}
		return
	}

	if err := u.state.AddEntry(newRecord(name, sum)); err != nil {
		u.logger.Printf("upload: recorded %s but could not persist state: %v", name, err)
	} else {
		u.logger.Printf("upload: uploaded %s (sha256=%s)", name, sum)
	}
	// Per-game closure: every resolved upload tells the player what happened
	// to their game. Pending resolutions stay quiet here - instead the
	// pending-match poller follows the match and toasts when it resolves.
	if u.notify != nil {
		var myID string
		if u.auth != nil {
			_, myID = u.auth.Credentials()
		}
		if title, body := matchNotice(match, myID); title != "" {
			u.notify(u.logger, NoticeInfo, title, body)
		}
	}
	if match == nil {
		u.startPendingPoll()
	}
}

// handleSessionExpired marks the attention state and notifies the user once
// per session that the token was expired or revoked and the re-login device
// flow must be run from the tray menu.
func (u *Uploader) handleSessionExpired() {
	u.status.Set(StatusAttention, "Session expired - log in again")
	notifySessionExpired(u.logger, u.notify)
}

// shouldSignalBlizzardGame is the double-queue gate: the rejection reason
// must identify a genuine non-StarLance game (wrong map/mod or missing queued
// identities, or an unscoreable outcome) and no StarLance match may be
// pending. Idle no_pending_match rejections (e.g. AI/custom-game replays)
// and wrong-lobby attempts against a pending match must never trigger the
// signal.
func shouldSignalBlizzardGame(rejectionReason string, hasPendingMatch bool) bool {
	switch rejectionReason {
	case "mod_hash_rejected", "map_mismatch", "toons_not_both_present", "no_single_winner":
		return !hasPendingMatch
	default:
		// Unknown or empty reasons cannot be confirmed as non-StarLance-game
		// evidence; stay quiet and let the next replay re-evaluate.
		return false
	}
}

// hasPendingStarLanceMatch queries GET /queue/status (device-flow API key) to
// implement gate (b) of the double-queue signal: a rejected replay while a
// match is pending is a wrong-lobby attempt the pending-match lifecycle
// handles, so the signal must be suppressed. Fail-soft and pessimistic — on
// any error (network, non-2xx, unparseable body, unconfigured backend) it
// reports a pending match so the caller skips the signal.
func (u *Uploader) hasPendingStarLanceMatch() bool {
	base := u.cfg.BaseURL()
	if base == "" {
		u.logger.Printf("signal: cannot query queue status (backend URL not configured)")
		return true
	}
	req, err := http.NewRequest(http.MethodGet, base+"/queue/status", nil)
	if err != nil {
		return true
	}
	if u.auth != nil {
		if apiKey, _ := u.auth.Credentials(); apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
	}
	resp, err := u.client.Do(req)
	if err != nil {
		u.logger.Printf("signal: queue status query failed (skipping signal): %v", err)
		return true
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		u.logger.Printf("signal: queue status query failed (skipping signal): %s", resp.Status)
		return true
	}
	return parsePendingMatch(body)
}

// parsePendingMatch interprets a GET /queue/status body (QueueStatus schema:
// snake_case wire format, state enum idle/searching/matched, pending_match
// object-or-null): a pending Match object or a matched lifecycle state means
// a match is pending.
func parsePendingMatch(body []byte) bool {
	var status struct {
		State        string         `json:"state"`
		PendingMatch map[string]any `json:"pending_match"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &status); err != nil {
		return true // unparseable — conservative: assume pending
	}
	return status.PendingMatch != nil || status.State == "matched"
}

// reportBlizzardGameStarted POSTs /uploader/blizzard-game-started (the
// double-queue protection signal): the owner just played a non-StarLance SC2
// game, so the backend can auto-exit their queue if they were queueing.
// Best-effort and fail-soft — network failures cost one quiet retry and a log
// line, never a user-facing notification; the next replay resumes naturally.
func (u *Uploader) reportBlizzardGameStarted() {
	base := u.cfg.BaseURL()
	if base == "" {
		u.logger.Printf("signal: cannot report blizzard game started (backend URL not configured)")
		return
	}
	var lastErr error
	for attempt := 0; attempt < maxSignalAttempts; attempt++ {
		if attempt > 0 {
			sleepWithJitter(signalRetryDelay)
		}
		err := u.sendBlizzardSignal(base)
		if err == nil {
			u.logger.Printf("signal: blizzard game started reported")
			return
		}
		if isUnauthorizedErr(err) {
			// Same token expiry as an upload 401: surface the re-login flow.
			u.handleSessionExpired()
			return
		}
		var nre *errNotRetryable
		if errors.As(err, &nre) {
			u.logger.Printf("signal: blizzard game started rejected (%v); not retrying", err)
			return
		}
		lastErr = err
	}
	// Soft failure only: the backend being down also means it cannot act on
	// the signal right now, and the loop resumes with the next replay.
	u.logger.Printf("signal: blizzard game started report failed after %d attempts (ignored): %v", maxSignalAttempts, lastErr)
}

// sendBlizzardSignal performs one POST of /uploader/blizzard-game-started
// with the X-API-Key header. 2xx reports success; 401 yields errUnauthorized;
// other 4xx responses are marked not retryable.
func (u *Uploader) sendBlizzardSignal(base string) error {
	req, err := http.NewRequest(http.MethodPost, base+"/uploader/blizzard-game-started", nil)
	if err != nil {
		return err
	}
	if u.auth != nil {
		if apiKey, _ := u.auth.Credentials(); apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return &errUnauthorized{}
	}
	msg := fmt.Errorf("backend returned %s", resp.Status)
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return &errNotRetryable{err: msg}
	}
	return msg
}

func (u *Uploader) uploadWithRetry(path string) (*matchResult, int, error) {
	u.status.Set(StatusUploading, "")
	defer func() {
		if kind, _ := u.status.Get(); kind == StatusUploading {
			u.status.Set(StatusWatching, "")
		}
	}()
	body, contentType, err := u.buildMultipart(path)
	if err != nil {
		return nil, 0, &errBuildFailure{err: fmt.Errorf("build request: %w", err)}
	}
	var lastErr error
	var match *matchResult
	for attempt := 0; attempt < maxUploadAttempts; attempt++ {
		if attempt > 0 {
			sleepWithJitter(retryDelays[attempt-1])
		}
		match, lastErr = u.doUpload(path, body, contentType)
		if lastErr == nil {
			return match, attempt + 1, nil
		}
		var nre *errNotRetryable
		var rej *errRejected
		var unauth *errUnauthorized
		if errors.As(lastErr, &nre) || errors.As(lastErr, &rej) || errors.As(lastErr, &unauth) {
			// Permanent client-side error (including a 409 rejection): retrying
			// cannot succeed. Log once and bail so the serial upload queue is
			// not delayed.
			u.logger.Printf("upload: attempt %d/%d failed for %s: %v [not retrying]", attempt+1, maxUploadAttempts, filepath.Base(path), lastErr)
			return nil, attempt + 1, lastErr
		}
		u.logger.Printf("upload: attempt %d/%d failed for %s: %v", attempt+1, maxUploadAttempts, filepath.Base(path), lastErr)
	}
	return nil, maxUploadAttempts, lastErr
}

// matchResult is the subset of the Match schema (ladder-api.yaml) the uploader
// reads from a 200 upload response, which the backend sends when this upload
// resolved the match (both players uploaded, or a draw was confirmed).
type matchResult struct {
	ID     string `json:"id"`
	Status string `json:"status"` // pending | confirmed | held | voided
	Draw   bool   `json:"draw"`
	Map    *struct {
		Name string `json:"name"`
	} `json:"map"`
	Winner *struct {
		UserID string `json:"user_id"`
		Name   string `json:"name"`
	} `json:"winner"`
}

// parseMatchResult decodes a 200 response body. An empty or unparseable body
// yields a non-nil zero match: the upload still resolved something, it just
// carries no readable context.
func parseMatchResult(body []byte) *matchResult {
	m := &matchResult{}
	if err := json.Unmarshal(bytes.TrimSpace(body), m); err != nil {
		return m
	}
	return m
}

// matchNotice turns a resolved upload into the per-game notification the
// player sees - the post-game "did my game count?" answer, led by the
// outcome. Pending resolutions return empty strings: the player's own upload
// resolving is almost always followed within seconds by the match
// resolving, so a "waiting" toast would only ever be noise; the site
// notifies on resolve.
func matchNotice(m *matchResult, myPlayerID string) (title, body string) {
	if m == nil {
		return "", ""
	}
	where := ""
	if m.Map != nil && m.Map.Name != "" {
		where = " · " + m.Map.Name
	}
	switch {
	case m.Status == "held":
		return "Match under review", "The two uploads differ - an admin will check it" + where
	case m.Status == "voided":
		return "Match not counted", "The match was voided" + where
	case m.Status != "confirmed":
		// A winner or draw on an unresolved match is not final yet; the
		// result toast waits for the confirmed status (ratings applied).
		return "", ""
	case m.Draw:
		return "Draw", "Match recorded" + where
	case m.Winner == nil:
		return "", ""
	}
	vs := m.Winner.Name
	if vs == "" {
		vs = "your opponent"
	}
	vs = "vs " + vs + where
	if m.Winner.UserID != "" && m.Winner.UserID == myPlayerID {
		return "Victory", vs
	}
	return "Defeat", vs
}

func (u *Uploader) doUpload(path string, body []byte, contentType string) (*matchResult, error) {
	req, err := http.NewRequest(http.MethodPost, u.cfg.BackendURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if u.auth != nil {
		if apiKey, _ := u.auth.Credentials(); apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if resp.StatusCode == http.StatusAccepted {
			return nil, nil // 202: pending the other player / grace window
		}
		return parseMatchResult(respBody), nil
	}
	if resp.StatusCode == http.StatusConflict {
		reason := parseRejectionReason(respBody)
		return nil, &errRejected{reason: reason}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &errUnauthorized{}
	}
	msg := fmt.Errorf("backend returned %s", resp.Status)
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return nil, &errNotRetryable{err: fmt.Errorf("%w [non-retryable]", msg)}
	}
	return nil, msg
}

// errUnauthorized marks a 401 upload response: the API key was rejected
// (token expired or revoked). Retrying cannot succeed and the user must log
// in again from the tray menu.
type errUnauthorized struct{}

func (e *errUnauthorized) Error() string {
	return "backend rejected the API key (401): token expired or revoked"
}

// errRejected marks a 409 upload rejection; reason is the machine-readable
// body (e.g. mod_hash_rejected) surfaced as a distinct notification.
type errRejected struct {
	reason string
}

func (e *errRejected) Error() string {
	if e.reason == "" {
		return "backend rejected the upload (409)"
	}
	return fmt.Sprintf("backend rejected the upload (409): %s", e.reason)
}

// parseRejectionReason extracts the machine-readable reason from a 409 body.
// The backend may answer with JSON ({"reason": "..."}) or a plain-text reason.
func parseRejectionReason(body []byte) string {
	var jsonBody struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &jsonBody); err == nil && jsonBody.Reason != "" {
		return jsonBody.Reason
	}
	plain := strings.TrimSpace(string(body))
	if plain != "" && len(plain) <= 200 && !strings.ContainsAny(plain, "\n\r{") {
		return plain
	}
	return ""
}

// rejectionMessage maps a backend rejection reason to the player-facing
// explanation: what went wrong and what to do about it. The caller prefixes
// the replay name.
func rejectionMessage(reason string) string {
	switch reason {
	case "mod_hash_rejected":
		return "Your StarLance mod is outdated. Install the current version from starlance.club/mod-versions, then restart StarCraft II."
	case "map_mismatch":
		return "This replay is not from your assigned ladder map. Play the match in the lobby StarLance created."
	case "toons_not_both_present":
		return "The replay does not contain both queued players. Play the match in the exact lobby StarLance created."
	case "no_pending_match":
		return "No StarLance match was waiting for this replay. Only games played through the StarLance queue count."
	case "no_single_winner":
		return "The replay has no clear winner, so it cannot be scored. If this was a StarLance match, report it on the StarLance Discord."
	case "not_a_replay", "invalid_replay":
		return "StarCraft II did not write a readable replay for this game. Finish the game normally and let it save to the replay folder."
	case "replay_build":
		return "This replay is from a StarCraft II version StarLance does not support yet. Update StarCraft II before your next match."
	case "replay_too_large":
		return "This replay is too large for StarLance to read. Upload it from the match page on starlance.club if you think it should have counted."
	case "map_unverifiable":
		return "StarLance could not read the map from this replay. Play the match in the lobby StarLance created."
	case "":
		return "StarLance did not accept this replay."
	default:
		return "StarLance did not accept this replay. If this keeps happening, send the StarLance App log in the StarLance Discord."
	}
}

// rejectionShort is the one-phrase player version of a rejection reason for
// the places where only a line fits: the tray tooltip's attention reason and
// the activity feed's failure lines. The full explanation lives in
// rejectionMessage.
func rejectionShort(reason string) string {
	switch reason {
	case "mod_hash_rejected":
		return "StarLance mod is outdated"
	case "map_mismatch":
		return "wrong map for the match"
	case "toons_not_both_present":
		return "lobby did not match the match"
	case "no_single_winner":
		return "no clear winner in the replay"
	case "no_pending_match":
		return "not a queued StarLance match"
	case "not_a_replay", "invalid_replay":
		return "no readable replay"
	case "replay_build":
		return "unsupported game version"
	case "replay_too_large":
		return "replay too large"
	case "map_unverifiable":
		return "map not readable"
	case "":
		return "rejected by StarLance"
	default:
		return "rejected: " + reason
	}
}

// buildMultipart marshals the replay file plus player_id (from the auth
// token) into a multipart body and returns it along with the exact
// Content-Type header.
func (u *Uploader) buildMultipart(path string) ([]byte, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if u.auth != nil {
		if _, playerID := u.auth.Credentials(); playerID != "" {
			if err := mw.WriteField("player_id", playerID); err != nil {
				mw.Close()
				return nil, "", err
			}
		}
	}

	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		mw.Close()
		return nil, "", err
	}
	fh, err := os.Open(path)
	if err != nil {
		mw.Close()
		return nil, "", err
	}
	defer fh.Close()
	if _, err := io.Copy(fw, fh); err != nil {
		mw.Close()
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

func sha256FileReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha256File(fp string) (string, error) {
	fh, err := os.Open(fp)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	return sha256FileReader(fh)
}

// isReplayName reports whether name has a .SC2Replay suffix, case-insensitively.
func isReplayName(name string) bool {
	const suffix = ".SC2Replay"
	return len(name) > len(suffix) && strings.EqualFold(name[len(name)-len(suffix):], suffix)
}

// sleepWithJitter sleeps for d plus up to +500ms of random jitter.
func sleepWithJitter(d time.Duration) {
	const jitterMax = 500 * time.Millisecond
	sleep := d + time.Duration(rand.Int63n(int64(jitterMax/time.Millisecond)+1))*time.Millisecond
	time.Sleep(sleep)
}
