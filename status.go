package main

import "sync"

// StatusKind is the lifecycle state shown as the first (disabled) tray menu
// entry: Watching, Uploading, Logged out, or Needs attention.
type StatusKind int

const (
	StatusLoggedOut StatusKind = iota
	StatusWatching
	StatusUploading
	StatusAttention
)

func (s StatusKind) String() string {
	switch s {
	case StatusWatching:
		return "Watching"
	case StatusUploading:
		return "Uploading..."
	case StatusAttention:
		return "Needs attention"
	default:
		return "Not logged in"
	}
}

// Status is the concurrency-safe holder the watcher/uploader/login write and
// the tray reads. On non-tray platforms it only feeds log lines.
type Status struct {
	mu     sync.RWMutex
	kind   StatusKind
	reason string // extra detail when StatusAttention
}

// NewStatus returns a Status in the given initial state.
func NewStatus(initial StatusKind) *Status {
	return &Status{kind: initial}
}

// Set transitions the status. Attention carries a short, user-facing reason.
func (s *Status) Set(kind StatusKind, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kind = kind
	s.reason = reason
}

// Get returns the current status and its attention reason (empty otherwise).
func (s *Status) Get() (StatusKind, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.kind, s.reason
}
