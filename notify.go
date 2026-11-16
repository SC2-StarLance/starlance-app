package main

import (
	"log"
	"sync"
)

// NoticeLevel says how much attention a notification demands. It is part of
// the shared Notifier contract so every platform renders both levels with
// its own, appropriately loud mechanism.
type NoticeLevel int

const (
	// NoticeInfo is a confirmation or outcome the player wants to see but
	// that never interrupts: a match result, a saved setting. On Linux it is
	// a normal toast with a timeout; on Windows it is an Action Center toast
	// raised through PowerShell with a registered AUMID (never modal).
	NoticeInfo NoticeLevel = iota

	// NoticeProblem demands attention: a replay did not count, the session
	// expired, login failed. On Linux it is a critical (persistent) toast;
	// on Windows it is a topmost warning message box.
	NoticeProblem
)

// Notifier delivers a user-facing notification. title is the bold first line,
// message the detail; both are written for players, not developers.
type Notifier func(logger *log.Logger, level NoticeLevel, title, message string)

// Session expiry is reported by two independent loops (uploads and the
// heartbeat); the player needs it once, not once per loop.
var (
	sessionExpiredMu      sync.Mutex
	sessionExpiredNoticed bool
)

func notifySessionExpired(logger *log.Logger, notify Notifier) {
	sessionExpiredMu.Lock()
	if sessionExpiredNoticed {
		sessionExpiredMu.Unlock()
		return
	}
	sessionExpiredNoticed = true
	sessionExpiredMu.Unlock()
	if notify != nil {
		notify(logger, NoticeProblem, "Login expired", "Log in again to keep queueing for matches.")
	}
}

// resetSessionExpiredNotice arms the expiry notice again after a fresh login.
func resetSessionExpiredNotice() {
	sessionExpiredMu.Lock()
	sessionExpiredNoticed = false
	sessionExpiredMu.Unlock()
}
