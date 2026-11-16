package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Settle parameters: a file is considered finished once it has had no writes
// for settleIdle AND its size has been unchanged across two consecutive
// samples settlePoll apart. settlePoll and maxStatErrors are vars so tests
// can shorten them.
const (
	settleIdle        = 2500 * time.Millisecond
	sizeStableSamples = 2
)

// settleIdleSamples and settleMaxAttempts are computed here (not in the const
// block) so they use the initial settlePoll value; settlePoll and maxStatErrors
// are vars so tests can shorten them.
var (
	settlePoll        = 500 * time.Millisecond
	settleIdleSamples = int(settleIdle / settlePoll)
	settleMaxAttempts = settleIdleSamples + 20

	// maxStatErrors is how many consecutive os.Stat failures monitorFile
	// tolerates before giving up: a transient failure (AV scan, a
	// create-pending race on Windows) must not drop the replay, only a file
	// that stays unreachable that long is gone for good.
	maxStatErrors = 10
)

// Watcher watches one or more replay directories for new .SC2Replay files
// and pushes settled, size-stable file paths onto fileCh for the uploader to
// drain. Multiple roots cover every SC2 account on the machine: each
// Battle.net account/toon writes replays under its own Accounts subtree, and
// watching only one would silently lose games played on an alt.
type Watcher struct {
	dirs    []string
	mu      sync.Mutex // guards dirs (read by Dirs from other goroutines)
	logger  *log.Logger
	fileCh  chan string
	done    chan struct{}
	runDone chan struct{}
	redir   chan redirRequest // live re-point of the watched directory set
	once    sync.Once
	wg      sync.WaitGroup // tracks in-flight monitorFile goroutines
}

// redirRequest is one SetDirs request: the new directory set and the channel
// to reply with the outcome on.
type redirRequest struct {
	dirs []string
	done chan error
}

// NewWatcher builds a Watcher for dirs, emitting settled paths to fileCh.
func NewWatcher(dirs []string, logger *log.Logger, fileCh chan string) *Watcher {
	return &Watcher{
		dirs:    normalizeDirs(dirs),
		logger:  logger,
		fileCh:  fileCh,
		done:    make(chan struct{}),
		runDone: make(chan struct{}),
		redir:   make(chan redirRequest),
	}
}

// SetDirs re-points the watcher at dirs while it is running, so a folder
// picked in the tray applies immediately instead of after a restart. It
// blocks until the event loop has switched (or failed and kept the old set).
// Returns an error when the watcher already stopped or a new directory could
// not be watched (the old set stays active in that case; removals are
// best-effort).
func (w *Watcher) SetDirs(dirs []string) error {
	req := redirRequest{dirs: normalizeDirs(dirs), done: make(chan error, 1)}
	select {
	case w.redir <- req:
	case <-w.done:
		return os.ErrClosed
	}
	select {
	case err := <-req.done:
		return err
	case <-time.After(3 * time.Second):
		return os.ErrDeadlineExceeded
	}
}

// Stop signals the watcher to stop queuing new files and releasing settled
// ones. It is safe to call multiple times and from any goroutine.
func (w *Watcher) Stop() {
	w.once.Do(func() { close(w.done) })
}

// WaitRunDone blocks until the event loop in Run has fully exited, meaning no
// further wg.Add(1) can happen. It must be awaited before wg.Wait() so that a
// concurrent Add (still possible while the loop is live processing an event)
// never races Wait.
func (w *Watcher) WaitRunDone() {
	<-w.runDone
}

// Dirs returns a copy of the directory set currently being watched.
func (w *Watcher) Dirs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.dirs...)
}

// Run watches the directory set for live replay events only. Replays already
// sitting in a folder are never uploaded (the never-backfill policy: StarLance
// counts games played while logged in; historical folders are the player's own
// archive). A root that cannot be watched is logged and skipped - one stale
// account folder must not stop the others - but if NO root can be watched an
// error is returned (the player needs to know nothing is being watched).
// It returns when the fsnotify watcher stops. Whatever the return path,
// runDone is closed only after the event loop has stopped taking new events,
// so no goroutine can call wg.Add afterwards.
func (w *Watcher) Run() error {
	defer close(w.runDone)

	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fs.Close()

	dirs := w.Dirs()
	added := 0
	for _, dir := range dirs {
		if err := fs.Add(dir); err != nil {
			w.logger.Printf("watcher: cannot watch %s: %v", dir, err)
			continue
		}
		added++
	}
	if added == 0 {
		return fmt.Errorf("watcher: no replay directory could be watched (%s)", strings.Join(dirs, ", "))
	}
	w.logger.Printf("watcher: watching %s", strings.Join(dirs, ", "))

	for {
		// Only start a monitor goroutine for events outstanding before done is
		// closed; never Add after shutdown begins, so wg.Add can not overlap
		// the later wg.Wait in the shutdown path.
		select {
		case <-w.done:
			return nil
		default:
		}

		select {
		case <-w.done:
			return nil
		case req := <-w.redir:
			req.done <- w.setDirs(fs, req.dirs)
			continue
		case ev, ok := <-fs.Events:
			if !ok {
				return nil
			}
			// Treat Create and Rename (into the directory) as new-file triggers.
			if ev.Op&(fsnotify.Create|fsnotify.Rename) != 0 && isReplayName(ev.Name) {
				select {
				case <-w.done:
					// done closed while handling this event: skip so we never
					// Add after shutdown has begun.
				default:
					w.wg.Add(1)
					go w.monitorFile(ev.Name)
				}
			}
		case err, ok := <-fs.Errors:
			if !ok {
				return nil
			}
			w.logger.Printf("watcher: error: %v", err)
		}
	}
}

// monitorFile waits until path is settled, then hands it to the uploader. The
// file's size is sampled every settlePoll: any size change resets both the
// idle count and the stable-pair count. The file is released once it has been
// idle for the settle window AND its size has been unchanged across at least
// two consecutive samples. A hard attempt cap avoids blocking forever on an
// endlessly growing file.
func (w *Watcher) monitorFile(path string) {
	defer w.wg.Done()
	ticker := time.NewTicker(settlePoll)
	defer ticker.Stop()

	var prevSize, curSize int64
	var idleSamples int
	var stablePairs int
	var attempts int
	var statErrors int

	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			attempts++
			info, err := os.Stat(path)
			if err != nil {
				// Transient unreachability must not drop the replay (AV scan,
				// create-pending race); only give up once it persists.
				statErrors++
				if statErrors >= maxStatErrors {
					return // unreachable for ~settlePoll*maxStatErrors; gone
				}
				continue
			}
			statErrors = 0

			prevSize = curSize
			curSize = info.Size()

			if prevSize != 0 && curSize != prevSize {
				// Still being written: restart both counters.
				idleSamples = 0
				stablePairs = 0
				continue
			}

			if prevSize == curSize && prevSize != 0 {
				stablePairs++
			}
			idleSamples++

			if isSettled(idleSamples, stablePairs, attempts) {
				w.release(path)
				return
			}
		}
	}
}

// isSettled is the pure release decision for monitorFile: a file is handed to
// the uploader once it has been idle across at least settleIdleSamples samples
// with size unchanged across at least sizeStableSamples pairs, OR once
// settleMaxAttempts polls have elapsed (so an endlessly growing file is still
// released).
func isSettled(idleSamples, stablePairs, attempts int) bool {
	if idleSamples >= settleIdleSamples && stablePairs >= sizeStableSamples {
		return true
	}
	return attempts >= settleMaxAttempts
}

// release hands a settled file to the uploader. It blocks so no replay is ever
// dropped; the uploader pumps the buffer continuously. On shutdown it returns
// immediately without sending, so a send can never race a close on fileCh.
func (w *Watcher) release(path string) {
	select {
	case <-w.done:
		return
	case w.fileCh <- path:
	}
}

// Replays already in the folder are never re-examined (never-backfill
// policy, see Run). Live events flow through release().

// normalizeDirs cleans a directory list, drops empty entries and duplicates,
// and preserves order - so detection plus an explicit settings path that name
// the same folder never double-watch it (and Windows casing variants of the
// same path fold together).
func normalizeDirs(dirs []string) []string {
	seen := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		d = filepath.Clean(d)
		key := d
		if runtime.GOOS == "windows" {
			key = strings.ToLower(d)
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, d)
	}
	return out
}

// setDirs moves the fsnotify watches to exactly dirs: new roots are added,
// roots no longer in the set are dropped, and everything already watched
// stays put. A root that cannot be added keeps whatever watch was there
// before and the first add error is returned (the tray reports it);
// removals are best-effort.
func (w *Watcher) setDirs(fs *fsnotify.Watcher, dirs []string) error {
	current := w.Dirs()

	currentSet := make(map[string]struct{}, len(current))
	for _, d := range current {
		currentSet[d] = struct{}{}
	}
	nextSet := make(map[string]struct{}, len(dirs))
	for _, d := range dirs {
		nextSet[d] = struct{}{}
	}

	var firstErr error
	for _, d := range dirs {
		if _, watched := currentSet[d]; watched {
			continue
		}
		if err := fs.Add(d); err != nil {
			w.logger.Printf("watcher: cannot watch %s: %v", d, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	for _, d := range current {
		if _, kept := nextSet[d]; kept {
			continue
		}
		_ = fs.Remove(d)
	}

	w.mu.Lock()
	w.dirs = dirs
	w.mu.Unlock()
	w.logger.Printf("watcher: now watching %s", strings.Join(dirs, ", "))
	return firstErr
}
