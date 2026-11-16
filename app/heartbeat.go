package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Heartbeat tuning: the queue-entry systems check uses this signal as the
// uploader liveness gate (no heartbeat, no queue entry — see
// docs/design/matchmaking.md). The period is config-driven
// (heartbeat_seconds) and defaults to 30s.
const (
	defaultHeartbeatSeconds = 30
	minHeartbeatSeconds     = 5 // clamp: a mistyped 0/1 must not hammer the backend
)

// heartbeatInterval returns the configured heartbeat period: the
// heartbeat_seconds setting, defaulting to 30s when unset and clamped up to
// minHeartbeatSeconds.
func heartbeatInterval(cfg *Config) time.Duration {
	seconds := defaultHeartbeatSeconds
	if cfg != nil && cfg.HeartbeatSeconds > 0 {
		seconds = cfg.HeartbeatSeconds
	}
	if seconds < minHeartbeatSeconds {
		seconds = minHeartbeatSeconds
	}
	return time.Duration(seconds) * time.Second
}

// Heartbeater periodically POSTs /uploader/heartbeat with the device-flow
// API key while the app runs, so the backend counts the owner as online for
// queue entry. Failures are soft: the loop keeps running and reconnects on
// its own; only a 401 (expired/revoked token) surfaces the re-login flow.
type Heartbeater struct {
	cfg     *Config
	auth    *AuthState
	status  *Status
	notify  Notifier
	client  *http.Client
	logger  *log.Logger
	offline bool // last send failed; log state transitions, not every miss
}

// NewHeartbeater builds a Heartbeater; status and notify are shared with the
// tray (nil-safe for tests).
func NewHeartbeater(cfg *Config, auth *AuthState, status *Status, notify Notifier, logger *log.Logger) *Heartbeater {
	if notify == nil {
		notify = func(l *log.Logger, _ NoticeLevel, _, _ string) {}
	}
	if status == nil {
		status = NewStatus(StatusWatching)
	}
	return &Heartbeater{
		cfg:    cfg,
		auth:   auth,
		status: status,
		notify: notify,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

// Run sends one heartbeat immediately (so the owner can queue right after
// startup) and then one per configured interval until ctx is done. A pending
// in-flight request is aborted by ctx cancellation.
func (h *Heartbeater) Run(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval(h.cfg))
	defer ticker.Stop()
	h.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.tick(ctx)
		}
	}
}

// tick sends a single heartbeat, logging and escalating as needed. Logged
// out: skip silently (the loop keeps ticking so a later tray login resumes
// the heartbeat without restarting the app).
func (h *Heartbeater) tick(ctx context.Context) {
	if h.auth == nil || !h.auth.LoggedIn() {
		return
	}
	err := h.send(ctx)
	switch {
	case err == nil:
		if h.offline {
			h.offline = false
			h.logger.Printf("heartbeat: backend reachable again")
		}
	case isUnauthorizedErr(err):
		// Token expired/revoked: surface the re-login device flow. The
		// notification fires only once per session to avoid noise, matching
		// the upload path.
		h.status.Set(StatusAttention, "Session expired - log in again")
		notifySessionExpired(h.logger, h.notify)
	default:
		// Soft failure (offline/network): log the transition once and resume.
		if !h.offline {
			h.offline = true
			h.logger.Printf("heartbeat: send failed (%v); retrying quietly", err)
		}
	}
}

// send POSTs one liveness heartbeat with the X-API-Key header.
func (h *Heartbeater) send(ctx context.Context) error {
	base := h.cfg.BaseURL()
	if base == "" {
		return errors.New("heartbeat: backend URL is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/uploader/heartbeat", nil)
	if err != nil {
		return err
	}
	if apiKey, _ := h.auth.Credentials(); apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := h.client.Do(req)
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
	return fmt.Errorf("backend returned %s", resp.Status)
}

// isUnauthorizedErr reports whether err is the 401 token-rejected marker.
func isUnauthorizedErr(err error) bool {
	var unauth *errUnauthorized
	return errors.As(err, &unauth)
}
