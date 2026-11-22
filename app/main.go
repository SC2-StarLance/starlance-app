package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// settingsPath is the resolved settings file location, shared with the Windows
// tray (Settings → Set backend URL opens it in Notepad).
var settingsPath string

func main() {
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fPath := flags.String("path", "", "override the SC2 replay directory to watch")
	fConfig := flags.String("config", "", "path to the settings file (default: settings.json in the per-user app dir)")
	fTakeover := flags.Int("takeover-from", 0, "internal: wait for the previous instance to exit (self-update)")
	fProfile := flags.String("profile", "", "run as a named profile with its own login, settings and log (for testing two accounts side by side)")
	fPrefix := flags.String("prefix", "", "only watch replay folders under this Wine/Steam prefix (keeps a two-account test from cross-uploading)")
	fPortable := flags.Bool("portable", false, "run from wherever the binary is; do not install a copy in the per-user app folder")
	flags.Parse(os.Args[1:])
	portableMode = *fPortable

	// Preserved verbatim across a self-update relaunch so -path/-config
	// overrides survive the restart.
	userArgs := os.Args[1:]

	appDirPath, err := appDir()
	if err != nil {
		logFatal(fmt.Sprintf("cannot resolve per-user app dir: %v", err))
	}

	// A named profile moves every persisted file (auth token, settings, dedup
	// records, log) into appDir/profiles/<name>, so a second instance can hold a
	// different Battle.net login without touching the first one's state.
	if *fProfile != "" {
		for _, r := range *fProfile {
			ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				logFatal(fmt.Sprintf("profile name %q may only use letters, digits, dash or underscore", *fProfile))
			}
		}
		appDirOverride = filepath.Join(appDirPath, "profiles", *fProfile)
		if appDirPath, err = appDir(); err != nil {
			logFatal(fmt.Sprintf("cannot resolve profile dir: %v", err))
		}
		profileName = *fProfile
	}
	prefixName = *fPrefix

	logger := newLogger(appDirPath)

	// Self-update takeover: this instance was started by the previous one
	// mid-update. Wait for it to fully exit (no double tray/watcher), then
	// clean up the hidden .old binary the library left behind.
	if *fTakeover > 0 {
		waitForParent(*fTakeover, logger)
	}
	if exe, err := currentExecutable(); err == nil {
		launchedExe = exe
		removeOldBinary(exe, logger)
	}

	// A first launch from a download installs the app in its stable per-user
	// location and hands over to that copy, so the desktop entry, login
	// registration and updater never point into a folder the player or the
	// browser may delete. The handed-over copy starts with the same flags and
	// waits for this process to exit (no double tray, watcher or heartbeat).
	if relocateIfNeeded(logger, userArgs) {
		return
	}

	settingsPath = *fConfig
	if settingsPath == "" {
		settingsPath = filepath.Join(appDirPath, SettingsFilename)
	}

	// Wayland has no per-window icon, so the taskbar icon comes from a desktop
	// entry whose id matches the window's app_id. Keep that entry and its icon
	// current on every start (it also follows the binary if it moved).
	installDesktopIntegration(logger)
	installStartMenuShortcut(logger)

	cfg, err := LoadConfig(settingsPath)
	if err != nil {
		logFatal(fmt.Sprintf("load config: %v", err))
	}
	reconcileRunAtStart(cfg, logger)

	// Device-flow token: 0600 file in the per-user app dir, loaded into
	// memory. Never in the config JSON, never logged.
	tokenPath, err := TokenPath()
	if err != nil {
		logFatal(fmt.Sprintf("resolve token path: %v", err))
	}
	auth := LoadAuth(tokenPath, logger)

	validateAndWarn(cfg, auth, logger)

	// Liveness: with a stored token, ping the public /mod-versions endpoint
	// (the backend has no health route). Reachable → silently ready.
	if auth.LoggedIn() {
		if LivenessProbe(cfg, nil) {
			logger.Println("main: stored token present; backend reachable — ready")
		} else {
			logger.Println("main: stored token present; backend unreachable — will retry on upload")
		}
	}

	replayDirs := resolveReplayDirs(cfg, *fPath, *fPrefix, logger)
	if len(replayDirs) == 0 {
		// The window owns the fix (Settings -> Add folder), so a missing folder
		// must never stop the app from opening: say it once and keep going.
		logger.Println("main: no replay folder yet; the window asks for one")
	}

	kind := StatusLoggedOut
	if auth.LoggedIn() {
		kind = StatusWatching
		if len(replayDirs) == 0 {
			kind = StatusAttention
		}
	}
	status := NewStatus(kind)

	state := LoadState(filepath.Join(appDirPath, RecordsFilename), logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Accepted StarLance mod hash witness cache (public /mod-versions). It
	// only ever saves an upload roundtrip on non-StarLance replays; an empty
	// or stale cache simply disables that skip.
	var modWG sync.WaitGroup
	mods := NewModHashCache(filepath.Join(appDirPath, ModHashesFilename), cfg.BaseURL(), logger)
	modWG.Add(1)
	go func() {
		defer modWG.Done()
		mods.Run(ctx)
	}()

	fileCh := make(chan string, 128)
	up := NewUploader(cfg, auth, state, status, Notify, mods, logger, fileCh)

	var uploadWG sync.WaitGroup
	uploadWG.Add(1)
	go func() {
		defer uploadWG.Done()
		up.Run()
	}()

	// Continuous liveness heartbeat (config: heartbeat_seconds, default 30s).
	// It feeds the backend's queue-entry systems check — no heartbeat, no
	// queue entry — and resumes on its own after connectivity or login issues.
	hb := NewHeartbeater(cfg, auth, status, Notify, logger)

	w := NewWatcher(replayDirs, logger, fileCh)
	// With no folder there is nothing to upload, so the app must not report
	// itself online for queue entry. The tray/window picker flips this back on
	// as soon as a folder is watched.
	hb.Ready = func() bool { return len(w.Dirs()) > 0 }
	var heartbeatWG sync.WaitGroup
	heartbeatWG.Add(1)
	go func() {
		defer heartbeatWG.Done()
		hb.Run(ctx)
	}()

	watcherDone := make(chan error, 1)
	go func() { watcherDone <- w.Run() }()

	// restartCh must exist before the tray: the manual "Check for update"
	// action shares the same restart path as the automatic loop.
	restartCh := make(chan struct{}, 1)

	// The tray runs on its own so a non-Windows build degrades to a blocking
	// no-op that still shuts down cleanly with the rest of the process.
	deps := TrayDeps{
		Ctx:    ctx,
		Cfg:    cfg,
		Auth:   auth,
		Status: status,
		State:  state,
		// A picked folder is added to the watched set, never replacing it:
		// every auto-detected SC2 account folder stays watched so an alt
		// account's games cannot be silently lost.
		Rebind: func(dir string) error {
			return w.SetDirs(normalizeDirs(append([]string{dir}, DetectReplayDirs()...)))
		},
		WatchedDirs:    func() []string { return w.Dirs() },
		CheckUpdateNow: func() { go checkUpdateNow(ctx, stop, logger, Notify, restartCh) },
		RemoveFolder: func(dir string) error {
			keep := make([]string, 0, len(w.Dirs()))
			for _, d := range w.Dirs() {
				if d != dir {
					keep = append(keep, d)
				}
			}
			if len(keep) == len(w.Dirs()) {
				return nil
			}
			if len(keep) == 0 {
				return fmt.Errorf("cannot remove the only replays folder")
			}
			return w.SetDirs(keep)
		},
	}
	go RunTray(ctx, stop, logger, deps)

	// Fully automatic self-update (GitHub Releases): checks shortly after
	// startup and every 6 hours; on a successful swap it signals restartCh,
	// everything below shuts down cleanly, and the new binary is started with
	// -takeover-from so it waits for this process to exit first.
	// The update loop starts from the window's startup flow (window.go):
	// the window is hidden until the first check decides between the main
	// window and the small update card.

	logger.Printf("main: started; version=%s backend=%s replayDirs=%s heartbeatSeconds=%d", Version, cfg.BackendURL, strings.Join(replayDirs, ", "), heartbeatInterval(cfg)/time.Second)

	// The window runs on the main goroutine (Wails owns it there) and the
	// shutdown decisions run alongside it. There is one app: the window is
	// the app, the tray is the same process's icon. Closing the window hides
	// it; Quit from the window or tray ends the process.
	relaunchCh := make(chan bool, 1)
	go func() {
		relaunchAfterExit := false
		for {
			select {
			case <-ctx.Done():
				logger.Println("main: shutdown requested")
				quitWindow()
				relaunchCh <- relaunchAfterExit
				return
			case <-restartCh:
				// Update installed: shut down (same path as Quit) and hand over.
				logger.Println("main: update installed - shutting down for restart")
				relaunchAfterExit = true
				stop()
				quitWindow()
				relaunchCh <- relaunchAfterExit
				return
			case err := <-watcherDone:
				if err != nil {
					// A stale replay_path (a folder that no longer exists) must
					// not end the app: the window still offers the picker, and
					// a pending update still needs this loop to restart it.
					logger.Printf("main: watcher stopped: %v", err)
					Notify(logger, NoticeProblem, "Replays folder unavailable",
						"Pick another folder from the tray menu.")
				}
			}
		}
	}()

	if err := runWindow(cfg, auth, logger, deps, stop, restartCh); err != nil {
		// A window that cannot start (a build without GTK/WebKit on Linux)
		// must not orphan the tray: log it, say so, and keep the tray alive
		// until the player quits.
		logger.Printf("main: window unavailable: %v", err)
		Notify(logger, NoticeProblem, "Window could not open",
			"Use the tray menu, or restart the app.")
		<-ctx.Done()
	}
	stop()
	relaunchAfterExit := <-relaunchCh

	// Stop the watcher so no new files are queued. First wait until Run's event
	// loop has fully exited (only then is no more wg.Add possible), so the wait
	// on monitor goroutines below never races a concurrent Add. Only after both
	// steps is it safe to close fileCh: monitor goroutines never send after
	// done closes, so a send can never race a close.
	w.Stop()
	w.WaitRunDone()
	w.wg.Wait()

	close(fileCh)
	uploadWG.Wait()
	modWG.Wait()
	heartbeatWG.Wait()

	if relaunchAfterExit {
		if err := relaunchSelf(logger, userArgs); err != nil {
			logger.Printf("main: relaunch after update failed: %v", err)
			Notify(logger, NoticeProblem, "Update installed",
				"Restart the app to finish updating. You stay logged in.")
		}
	}
	logger.Println("main: exiting")
}

// resolveReplayDirs assembles the replay folder set to watch: the --path
// override (exactly one folder), otherwise every auto-detected SC2 account
// folder plus the configured extra folder picked from the tray. Detection
// re-runs at every startup (it is cheap), so an account folder that appears
// later is picked up without a config edit. It never guesses: when the set
// would still be empty it asks via the folder picker, and returns nil only
// when every mechanism failed.
func resolveReplayDirs(cfg *Config, override, prefixFilter string, logger *log.Logger) []string {
	if override != "" {
		return []string{override}
	}
	dirs := DetectReplayDirs()
	if prefixFilter != "" {
		abs, err := filepath.Abs(prefixFilter)
		if err != nil {
			logger.Printf("main: bad prefix filter %q: %v", prefixFilter, err)
		} else {
			kept := dirs[:0]
			for _, d := range dirs {
				if strings.HasPrefix(d, abs+string(filepath.Separator)) {
					kept = append(kept, d)
				}
			}
			dirs = kept
			logger.Printf("main: prefix %s keeps %d replay folder(s)", abs, len(dirs))
		}
	}
	if len(dirs) > 0 {
		logger.Printf("main: auto-detected %d replay folder(s): %s", len(dirs), strings.Join(dirs, ", "))
	}
	if cfg.ReplayPath != "" {
		logger.Printf("main: adding configured replay folder %s", cfg.ReplayPath)
		dirs = append([]string{cfg.ReplayPath}, dirs...)
	}
	dirs = normalizeDirs(dirs)
	if len(dirs) > 0 {
		return dirs
	}
	// No first-run dialog: a modal folder picker out of nowhere reads as a
	// broken app. The window shows the "pick your replays folder" card
	// instead, queueing stays blocked, and Settings owns the picker.
	logger.Println("main: no replay folder found; waiting for the player to add one in Settings")
	return nil
}

// logFatal reports a fatal startup error and exits. On the -H windowsgui
// build there is no console, so the error is also surfaced via a blocking
// message box before the process exits (a no-op elsewhere, where stderr
// already reaches the user).
func logFatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	notifyOnWindows("StarLance App", msg)
	os.Exit(1)
}
