package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Record is one entry in the persistence file, marking a replay as uploaded.
type Record struct {
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
	UploadedAt string `json:"uploadedAt"`
}

// State is a concurrent-safe in-memory index of already-uploaded replays,
// persisted to a JSON file. Load on start gives restart-dedup before any new
// upload is attempted. It also maintains the "uploaded today" counter shown
// in the tray status line, derived from the same records (no extra file).
type State struct {
	mu     sync.RWMutex
	path   string
	bySHA  map[string]Record
	byName map[string]string // filename -> sha256
	today  string            // UTC day the counter was computed for
	count  int               // uploads recorded on that day
}

// RecordsFilename ("records.json") lives in paths.go alongside the per-user
// app dir resolution.

// LoadState reads records from path into a fresh State, logging to logger. A
// missing file yields an empty State (writes will recreate it). A corrupt or
// otherwise unreadable records file is not silently ignored: it is backed up
// aside and a loud warning is logged, because starting empty would re-upload
// every replay.
func LoadState(path string, logger *log.Logger) *State {
	s := &State{
		path:   path,
		bySHA:  make(map[string]Record),
		byName: make(map[string]string),
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Printf("state: cannot read records %s: %v; dedup state reset (will re-upload previously uploaded replays)", path, err)
			return s
		}
		return s
	}
	var list []Record
	if err := json.Unmarshal(b, &list); err != nil {
		// Corrupt records file: back it up so the user can recover it, and log
		// loudly that dedup state was reset, rather than silently starting fresh.
		backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		if rerr := os.Rename(path, backup); rerr != nil {
			logger.Printf("state: corrupt records file %s could not be backed up: %v", path, rerr)
		} else {
			logger.Printf("state: corrupt records file backed up to %s", backup)
		}
		logger.Printf("state: dedup state reset (will re-upload previously uploaded replays): %v", err)
		return s
	}
	for _, r := range list {
		s.bySHA[r.SHA256] = r
		s.byName[r.Filename] = r.SHA256
	}
	s.recountTodayLocked(time.Now().UTC())
	return s
}

// recountTodayLocked recomputes the day counter from the stored records.
func (s *State) recountTodayLocked(now time.Time) {
	s.today = now.Format("2006-01-02")
	s.count = 0
	for _, r := range s.bySHA {
		if strings.HasPrefix(r.UploadedAt, s.today) {
			s.count++
		}
	}
}

// HasEntry reports whether the file (by name) or the content (by sha256) has
// already been recorded as uploaded. A name match alone is not treated as a
// duplicate: a later replay reusing the same filename with different content
// must not be skipped, so the by-name check is gated on the exact sha.
func (s *State) HasEntry(filename, sha256 string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.byName[filename] == sha256 {
		return true
	}
	if _, ok := s.bySHA[sha256]; ok {
		return true
	}
	return false
}

// AddEntry records a successful upload and immediately persists it, so a crash
// between uploads does not lose the state.
func (s *State) AddEntry(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bySHA[r.SHA256] = r
	s.byName[r.Filename] = r.SHA256
	s.recountTodayLocked(time.Now().UTC())
	return s.saveLocked()
}

// UploadsToday returns how many uploads were recorded so far today (UTC).
func (s *State) UploadsToday() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.today != time.Now().UTC().Format("2006-01-02") {
		return 0
	}
	return s.count
}

// saveLocked writes the state atomically (writeFileAtomic: temp file + rename)
// so a partial write never corrupts the records.
func (s *State) saveLocked() error {
	list := make([]Record, 0, len(s.bySHA))
	for _, r := range s.bySHA {
		list = append(list, r)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, append(b, '\n'), 0o600)
}

func newRecord(filename, sha256 string) Record {
	return Record{
		Filename:   filename,
		SHA256:     sha256,
		UploadedAt: time.Now().UTC().Format(time.RFC3339),
	}
}
