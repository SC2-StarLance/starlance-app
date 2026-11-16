package main

// modcheck.go — client-side witness cache for "could this be a StarLance
// game?".
//
// The backend accepts a replay when any of its replay.details dependency
// handles is an accepted StarLance mod hash (MatchService.validate, which
// also depot-verifies and auto-adopts unknown mod hashes mid-validation).
// The public GET /mod-versions endpoint returns exactly those hashes
// (accepted_hashes) plus the bound map content hashes (map_hashes). This
// file keeps the union of every hash ever observed in BOTH lists, persisted
// in the per-user app dir, so the uploader can recognize replays that
// certainly are not StarLance games (regular ladder, AI, custom games) and
// skip the upload roundtrip and its rejection noise.
//
// The two marker fields cover each other's blind spot: map markers pass the
// first game after a mod publish through to the backend's auto-adoption,
// mod markers pass the first game on a newly bound map (decision
// 2026-09-08-depot-verified-mod-auto-adoption).
//
// Hash identity is the 64-hex content TAIL: the last 32 bytes of the
// 40-byte s2ma handle (the depot content key). The scan yields full
// 80-char handle hexes; comparison normalizes to tails because the
// region-coded 8-byte handle prefix differs across regions.
//
// Design rules (AGENTS.md):
//   - Fail open. An empty, stale, or unfetchable cache disables the skip
//     entirely: everything is uploaded and the backend stays the only
//     source of truth. The probe can save a roundtrip; it can never block
//     a legitimate upload.
//   - Degrade to cache, never to failure (mod-hash decision record): a
//     refresh error keeps the last known hashes and retries later.
//   - The cache is a union, never a replace: a hash that rotated out of
//     accepted_hashes still identifies the StarLance mod (just an outdated
//     version), and those replays must keep uploading so the player gets
//     the precise "your mod is outdated" guidance instead of silence.

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// modHashRefresh is how often the accepted-hash list is re-fetched. Only a
// per-game concern, so a generous period is fine; a brand-new mod version
// promoted mid-session is picked up at most one period later (and the union
// cache means the stale window self-heals).
const modHashRefresh = 30 * time.Minute

// ModHashCache is the concurrent-safe ever-seen set of StarLance marker
// hashes: accepted mod hashes plus bound map hashes, all 64-hex content tails.
type ModHashCache struct {
	mu     sync.Mutex
	path   string // mod-hashes.json in the per-user app dir
	seen   map[string]struct{}
	client *http.Client
	base   string // API base URL (cfg.BaseURL())
	logger *log.Logger
}

// modHashFile is the persisted shape of the cache (tails; older files with
// full 80-char handle hexes are normalized on load).
type modHashFile struct {
	Seen      []string `json:"seen"`
	FetchedAt string   `json:"fetched_at,omitempty"`
}

// ModHashesFilename lives in paths.go alongside the other app-dir files.

// hashTail normalizes a handle hex (or hash) to its 64-hex content tail:
// the last 32 bytes of the 40-byte s2ma handle, the depot content key.
func hashTail(h string) string {
	if len(h) > 64 {
		return h[len(h)-64:]
	}
	return h
}

// NewModHashCache loads the persisted cache (a missing or corrupt file just
// starts empty and is rewritten on the next successful refresh). Entries are
// normalized to tails so pre-tail caches (full 80-char handle hexes) keep
// working unchanged.
func NewModHashCache(path, baseURL string, logger *log.Logger) *ModHashCache {
	c := &ModHashCache{
		path:   path,
		seen:   map[string]struct{}{},
		client: &http.Client{Timeout: 10 * time.Second},
		base:   baseURL,
		logger: logger,
	}
	if b, err := os.ReadFile(path); err == nil {
		var f modHashFile
		if json.Unmarshal(b, &f) == nil {
			for _, h := range f.Seen {
				if t := hashTail(h); t != "" {
					c.seen[t] = struct{}{}
				}
			}
		} else {
			logger.Printf("modcheck: corrupt %s; starting empty (non-StarLance replays will upload once)", path)
		}
	}
	if len(c.seen) > 0 {
		logger.Printf("modcheck: %d known StarLance mod hash(es) loaded", len(c.seen))
	}
	return c
}

// Active reports whether the cache knows any hashes at all. When false, the
// pre-detection skip is disabled and every replay uploads as before.
func (c *ModHashCache) Active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen) > 0
}

// ContainsAny reports whether one of the given (lowercase hex) handles is a
// known StarLance marker - a mod hash or a bound map hash. Handles compare
// by 64-hex content tail: the scan yields full 80-char handle hexes, the
// cache holds tails, and the region-coded prefix never matches anyway.
func (c *ModHashCache) ContainsAny(handles []string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, h := range handles {
		if _, ok := c.seen[hashTail(h)]; ok {
			return true
		}
	}
	return false
}

// Run refreshes the cache immediately and then once per modHashRefresh
// until ctx is done. Failures are soft: the last cache stays live.
func (c *ModHashCache) Run(ctx context.Context) {
	c.refresh(ctx)
	ticker := time.NewTicker(modHashRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refresh(ctx)
		}
	}
}

// refresh fetches GET /mod-versions (public endpoint) and merges
// accepted_hashes into the union set, persisting on any growth.
func (c *ModHashCache) refresh(ctx context.Context) {
	if c.base == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/mod-versions", nil)
	if err != nil {
		return
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Printf("modcheck: refresh failed (%v); keeping last known hashes", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.logger.Printf("modcheck: refresh failed (%s); keeping last known hashes", resp.Status)
		return
	}
	var payload struct {
		AcceptedHashes []string `json:"accepted_hashes"`
		MapHashes      []string `json:"map_hashes"`
	}
	if err := json.Unmarshal(body, &payload); err != nil ||
		(len(payload.AcceptedHashes) == 0 && len(payload.MapHashes) == 0) {
		c.logger.Printf("modcheck: unexpected /mod-versions body; keeping last known hashes")
		return
	}

	c.mu.Lock()
	grew := false
	for _, h := range append(payload.AcceptedHashes, payload.MapHashes...) {
		t := hashTail(h)
		if t == "" {
			continue
		}
		if _, ok := c.seen[t]; !ok {
			c.seen[t] = struct{}{}
			grew = true
		}
	}
	seen := make([]string, 0, len(c.seen))
	for h := range c.seen {
		seen = append(seen, h)
	}
	c.mu.Unlock()

	if !grew {
		return
	}
	f, _ := json.Marshal(modHashFile{Seen: seen, FetchedAt: time.Now().UTC().Format(time.RFC3339)})
	if err := writeFileAtomic(c.path, f, 0o600); err != nil {
		c.logger.Printf("modcheck: could not persist hash cache: %v", err)
	}
	c.logger.Printf("modcheck: now %d known StarLance marker hash(es) (mod + map)", len(seen))
}

// classifyStarLance is the fail-open decision the uploader applies before
// uploading: it returns true only when the cache is live, the probe decoded
// handles, and none of them is a known StarLance marker (mod or bound map
// hash) — i.e. the replay is certainly not a StarLance game. Everything
// else ("unknown") returns false so the backend decides.
func classifyStarLance(cache *ModHashCache, path string) bool {
	if cache == nil || !cache.Active() {
		return false
	}
	handles, err := replayHandles(path)
	if err != nil {
		// Unreadable/corrupt/unsupported archive: unknown, upload as before.
		return false
	}
	if len(handles) == 0 {
		// Details carried no content handles at all: unknown, upload as before.
		return false
	}
	return !cache.ContainsAny(handles)
}
