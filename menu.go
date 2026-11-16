package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// dashboardURL is opened by the "Open dashboard" menu item on both platforms.
const dashboardURL = "https://starlance.club"

// MenuID identifies a tray menu item. The Windows (systray) and Linux
// (StatusNotifierItem) trays share the same routing table in
// handleMenuAction, so a menu action behaves identically everywhere.
type MenuID string

const (
	MenuStatus          MenuID = "status"
	MenuOpenWindow      MenuID = "open-window"
	MenuLogin           MenuID = "login"
	MenuLogout          MenuID = "logout"
	MenuDashboard       MenuID = "dashboard"
	MenuReplays         MenuID = "replays"      // disabled info row: which folders are watched
	MenuOpenReplays     MenuID = "open-replays" // open the watched folder in the file manager
	MenuPickFolder      MenuID = "pick-folder"  // add an extra watched folder (applies live)
	MenuWhatsWrong      MenuID = "whats-wrong"  // attention fix row (hidden unless attention)
	MenuRecentActivity  MenuID = "recent-activity"
	MenuStartWithSystem MenuID = "start-with-system"
	MenuAbout           MenuID = "about"        // submenu: version + update check
	MenuVersion         MenuID = "version"      // disabled info row: current version
	MenuCheckUpdate     MenuID = "check-update" // one on-demand update check
	MenuQuit            MenuID = "quit"
)

// TrayDeps bundles what the tray menu needs beyond the logger. It is shared
// by every platform's tray rendering. Cfg and State may be nil in tests.
type TrayDeps struct {
	Ctx    context.Context // overall app context; cancels an in-flight login on Quit
	Cfg    *Config
	Auth   *AuthState
	Status *Status
	State  *State // dedup records; supplies the "uploaded today" counter
	// Rebind adds dir to the watched set so a pick applies immediately
	// instead of after a restart. Nil in tests.
	Rebind func(dir string) error
	// WatchedDirs returns the folders currently being watched, primary
	// first; it feeds the replays info row and the open-folder target. Nil in
	// tests (falls back to Cfg.ReplayPath).
	WatchedDirs func() []string
	// CheckUpdateNow runs one on-demand update check and toasts the outcome.
	// Nil in tests.
	CheckUpdateNow func()
	// RemoveFolder drops dir from the watched set. Nil in tests.
	RemoveFolder func(dir string) error
}

// statusLine returns the tray's headline status label and a fuller tooltip
// derived from the live status (attention reasons are appended to the tip).
// While watching with uploads on the record, the label answers "is this
// working?" at a glance: "Watching · 4 uploaded today".
func statusLine(deps TrayDeps) (label, tip string) {
	kind, reason := deps.Status.Get()
	label = kind.String()
	tip = label
	if reason != "" {
		tip = label + ": " + reason
		return label, tip
	}
	if kind == StatusWatching && deps.State != nil {
		if n := deps.State.UploadsToday(); n > 0 {
			label = fmt.Sprintf("%s · %d uploaded today", label, n)
			tip = label
		}
	}
	return label, tip
}

// --- Shared tray menu model ------------------------------------------------

// trayKind distinguishes the row shapes in the tray menu.
type trayKind int

const (
	trayStatus trayKind = iota // disabled headline row
	trayAction                 // normal clickable item
	traySeparator
	traySubmenu // opens children when hovered
	trayToggle  // checkbox item; checked reflects the persisted state
)

// trayItem is one row of the tray menu. The menu is rebuilt from the app
// state on every refresh; each platform renderer consumes this one model, so
// menu contents, labels, and visibility are defined exactly once.
type trayItem struct {
	id       MenuID
	kind     trayKind
	label    string
	tip      string
	hidden   bool
	disabled bool       // rendered inert (info rows); clicks never fire
	checked  bool       // checkbox state (trayToggle rows)
	children []trayItem // submenu contents, one level deep
}

// trayMenu builds the tray menu for the current app state. This is the
// single definition of the tray's contents: RunTray (tray.go) renders it via
// fyne.io/systray on every platform, and tests assert on it directly.
//
// From the player's perspective the menu answers four questions and nothing
// else: am I connected (status + Log in/out), did my games count (status
// counter + match toasts), is it watching the right folder (replays rows),
// and what happened recently (activity).
func trayMenu(deps TrayDeps) []trayItem {
	status, tip := statusLine(deps)
	loggedIn := deps.Auth.LoggedIn()
	items := []trayItem{
		{id: MenuStatus, kind: trayStatus, label: status, tip: tip},
		{kind: traySeparator},
	}
	// Attention states get a first-class fix row right under the status so
	// the player never has to guess what broke or where to look. The row is
	// ALWAYS in the model and hidden otherwise: the tray renderer builds the
	// menu once and later only flips the hidden flag of existing rows, so a
	// conditionally appended row could never appear mid-session (and its ID
	// must be distinct - a shared ID would collide in the renderer's handle
	// map).
	kind, _ := deps.Status.Get()
	items = append(items, trayItem{
		id:     MenuWhatsWrong,
		kind:   trayAction,
		label:  "Recent activity",
		tip:    "Show recent activity",
		hidden: !(kind == StatusAttention && loggedIn),
	})
	items = append(items,
		trayItem{id: MenuOpenWindow, kind: trayAction, label: "Open the StarLance App", tip: "Queue for a ladder match"},
		trayItem{kind: traySeparator},
		trayItem{id: MenuQuit, kind: trayAction, label: "Quit", tip: "Stop watching and exit"},
	)
	return items
}

// replayDirsForMenu returns the folders to present in the replays rows: the
// live watched set when the tray is wired in main, falling back to the
// configured folder for tests and early startup.
func replayDirsForMenu(deps TrayDeps) []string {
	if deps.WatchedDirs != nil {
		if dirs := deps.WatchedDirs(); len(dirs) > 0 {
			return dirs
		}
	}
	if deps.Cfg != nil && deps.Cfg.ReplayPath != "" {
		return []string{deps.Cfg.ReplayPath}
	}
	return nil
}

// replaysLabel is the disabled info row: which folders are watched. One
// folder shows its name; several show the count (the tooltip lists them).
func replaysLabel(dirs []string) string {
	switch {
	case len(dirs) == 0:
		return "No replay folder"
	case len(dirs) == 1:
		return "Replay folder: " + filepath.Base(dirs[0])
	default:
		return fmt.Sprintf("%d replay folders", len(dirs))
	}
}

// replaysTip lists every watched folder for the info row tooltip.
func replaysTip(dirs []string) string {
	if len(dirs) == 0 {
		return "No folder is watched yet"
	}
	return "Replay folders: " + strings.Join(dirs, ", ")
}

// handleMenuAction routes a tray "clicked" event to the shared handler. stop
// cancels the overall app context when Quit is pressed.
func handleMenuAction(logger *log.Logger, deps TrayDeps, stop context.CancelFunc, id MenuID) {
	switch id {
	case MenuLogin:
		doLogin(logger, deps)
	case MenuLogout:
		doLogout(logger, deps)
	case MenuDashboard:
		if err := openBrowserFn(dashboardURL); err != nil {
			logger.Printf("tray: could not open dashboard: %v", err)
		}
	case MenuOpenReplays:
		dirs := replayDirsForMenu(deps)
		if len(dirs) == 0 {
			break
		}
		if err := openFolder(dirs[0]); err != nil {
			logger.Printf("tray: could not open replays folder: %v", err)
		}
	case MenuOpenWindow:
		showWindow()
	case MenuPickFolder:
		doPickFolder(deps, logger)
	case MenuStartWithSystem:
		doToggleStartWithSystem(logger, deps)
	case MenuWhatsWrong, MenuRecentActivity:
		doShowActivity(logger, deps)
	case MenuCheckUpdate:
		if deps.CheckUpdateNow != nil {
			deps.CheckUpdateNow()
		}
	case MenuQuit:
		logger.Println("tray: Quit clicked")
		stop() // cancels the context, triggering graceful shutdown
	}
}

// doLogin runs the device flow in the background and updates status. The
// token is stored by DeviceLogin and never displayed. An atomic in-flight
// guard (claimed synchronously, before the goroutine spawns) collapses rapid
// double-clicks into a single flow.
func doLogin(logger *log.Logger, deps TrayDeps) {
	if deps.Auth.LoggedIn() {
		return
	}
	if !deps.Auth.BeginLogin() {
		logger.Println("tray: login already in progress; ignoring extra click")
		return
	}
	go func() {
		defer deps.Auth.EndLogin()
		deps.Status.Set(StatusWatching, "") // optimistic; login overwrites on failure below
		if err := DeviceLogin(deps.Ctx, deps.Cfg, deps.Auth, logger); err != nil {
			if deps.Ctx.Err() != nil { // app shutting down mid-login: stay quiet
				logger.Println("tray: login aborted (shutting down)")
				return
			}
			logger.Printf("tray: login failed: %v", err)
			deps.Status.Set(StatusAttention, "Login failed")
			Notify(logger, NoticeProblem, "Login failed", "Try again from the StarLance App window.")
			return
		}
		deps.Status.Set(StatusWatching, "")
	}()
}

// doLogout clears the token from memory and disk.
func doLogout(logger *log.Logger, deps TrayDeps) {
	if err := deps.Auth.Clear(); err != nil {
		logger.Printf("tray: logout failed: %v", err)
		return
	}
	logger.Println("tray: logged out")
	deps.Status.Set(StatusLoggedOut, "")
}

// doToggleStartWithSystem flips launch-at-login. The platform registration
// is attempted first and only a success is persisted, so the checkbox can
// never claim a state the system does not honor. The menu state itself is
// the feedback; a toast for a toggle would be noise. Cfg may be nil in tests.
func doToggleStartWithSystem(logger *log.Logger, deps TrayDeps) {
	if deps.Cfg == nil {
		return
	}
	next := !deps.Cfg.StartWithSystem
	if err := setStartWithSystem(deps, next, logger); err != nil {
		Notify(logger, NoticeProblem, "Could not update startup",
			"Nothing was changed.")
		return
	}
	logger.Printf("tray: launch at login = %v", next)
}

// setStartWithSystem applies launch-at-login for the window and the tray.
// The platform registration is attempted first and only a success is
// persisted, so no surface can claim a state the system does not honor.
func setStartWithSystem(deps TrayDeps, want bool, logger *log.Logger) error {
	if deps.Cfg == nil {
		return fmt.Errorf("settings are unavailable")
	}
	if deps.Cfg.StartWithSystem == want {
		return nil
	}
	if err := SetRunAtStart(want); err != nil {
		logger.Printf("tray: could not update launch-at-login: %v", err)
		return err
	}
	deps.Cfg.StartWithSystem = want
	if err := SaveConfig(settingsPath, deps.Cfg); err != nil {
		logger.Printf("tray: could not save start_with_system: %v", err)
	}
	return nil
}

// addReplayFolder adds dir to the watched set live, then persists it. The
// rebind runs first so settings never claim a folder the watcher rejected.
func addReplayFolder(deps TrayDeps, dir string, logger *log.Logger) error {
	if deps.Cfg == nil {
		return fmt.Errorf("settings are unavailable")
	}
	if dir == "" || dir == deps.Cfg.ReplayPath {
		return nil
	}
	if deps.Rebind != nil {
		if err := deps.Rebind(dir); err != nil {
			logger.Printf("tray: could not switch to %s: %v", dir, err)
			return err
		}
	}
	deps.Cfg.ReplayPath = dir
	if err := SaveConfig(settingsPath, deps.Cfg); err != nil {
		logger.Printf("tray: could not save config: %v", err)
		return err
	}
	return nil
}

// doPickFolder shows the native folder dialog, adds the folder to the watched
// set, and persists the choice - no restart, no waiting. The watched set is
// the union of every auto-detected SC2 account folder plus this explicit
// pick, so nothing already watched is lost. The live rebind is attempted
// first and only a success is persisted, so the saved settings never claim a
// folder the watcher is not actually watching (the same order the
// start-with-system toggle uses). A failure to re-point keeps the old set
// watching and says so.
func doPickFolder(deps TrayDeps, logger *log.Logger) {
	dir := PickFolder(logger, "Choose a StarCraft II multiplayer replays folder to watch")
	if dir == "" {
		return
	}
	if err := addReplayFolder(deps, dir, logger); err != nil {
		return
	}
	if dirs := replayDirsForMenu(deps); len(dirs) > 1 {
		logger.Printf("tray: watching %d replay folders", len(dirs))
		return
	}
	logger.Println("tray: replay folder saved")
}

// openFolder opens dir in the platform file manager.
func openFolder(dir string) error {
	return openExternal(dir)
}

// doShowActivity opens a viewer with recent uploader feedback: the current
// status plus the tail of uploader.log rendered as a player-readable feed
// (one line per upload outcome). Without a native dialog tool it falls back
// to opening the log file in the platform editor.
func doShowActivity(logger *log.Logger, deps TrayDeps) {
	appDirPath, err := appDir()
	if err != nil {
		logger.Printf("tray: cannot resolve app dir for activity: %v", err)
		return
	}
	logPath := filepath.Join(appDirPath, LogFilename)

	kind, reason := deps.Status.Get()
	body := "Status: " + kind.String()
	if reason != "" {
		body += " (" + reason + ")"
	}

	tail, _ := tailFile(logPath, 200)
	switch feed := activityFeed(tail, 15); {
	case feed != "":
		body += "\n\n== recent uploads ==\n" + feed
	case tail != "":
		body += "\n\n== recent log ==\n" + tail
	default:
		body += "\n\n(no log entries yet)"
	}

	if showTextInfo(logger, "StarLance App", body) {
		return
	}
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "windows":
		cmd = exec.Command("notepad.exe", logPath)
	case runtime.GOOS == "darwin":
		// TextEdit is the macOS equivalent of Notepad and needs no config.
		cmd = exec.Command("open", "-e", logPath)
	case os.Getenv("EDITOR") != "":
		cmd = exec.Command(os.Getenv("EDITOR"), logPath)
	default:
		cmd = exec.Command("xdg-open", logPath)
	}
	if err := cmd.Start(); err != nil {
		logger.Printf("tray: could not open activity log: %v", err)
		return
	}
	go func() { _ = cmd.Wait() }() // reap: never leave a zombie behind
}

// activityFeed renders the uploader log tail as a short, player-readable
// list ("23:45  ✓ Acolyte LE.SC2Replay"), newest last. Lines that are not
// upload outcomes (heartbeats, startup noise, re-skip spam) are dropped.
// Returns "" when the tail holds no recognizable upload events.
func activityFeed(tail string, max int) string {
	var out []string
	for _, ln := range strings.Split(tail, "\n") {
		if len(ln) < 21 || ln[4] != '/' || ln[7] != '/' || ln[10] != ' ' {
			continue // not a "2006/01/02 15:04:05 msg" line
		}
		clock, msg := ln[11:16], strings.TrimSpace(ln[20:])
		var entry string
		switch {
		case strings.HasPrefix(msg, "upload: uploaded "):
			name, _, _ := strings.Cut(strings.TrimPrefix(msg, "upload: uploaded "), " (sha256=")
			entry = clock + "  ✓ " + name
		case strings.HasPrefix(msg, "upload: failed "):
			name := strings.TrimPrefix(msg, "upload: failed ")
			if i := strings.Index(name, " after "); i >= 0 {
				name = name[:i]
			}
			entry = clock + "  ✗ " + name + failReasonSuffix(msg)
		case strings.HasPrefix(msg, "upload: skip ") && strings.Contains(msg, "not logged in"):
			name := strings.TrimPrefix(msg, "upload: skip ")
			if i := strings.Index(name, " ("); i >= 0 {
				name = name[:i]
			}
			entry = clock + "  · " + name + " (logged out, not uploaded)"
		case strings.HasPrefix(msg, "upload: skip ") && strings.Contains(msg, "not a StarLance game"):
			name := strings.TrimPrefix(msg, "upload: skip ")
			if i := strings.Index(name, " ("); i >= 0 {
				name = name[:i]
			}
			entry = clock + "  · " + name + " (not a StarLance game)"
		}
		if entry != "" {
			out = append(out, entry)
		}
	}
	if len(out) == 0 {
		return ""
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return strings.Join(out, "\n")
}

// failReasonSuffix extracts a short human explanation from a failed-upload
// log line so the activity feed answers "why did it fail?" at a glance: the
// 409 rejection reason (mapped to the player phrase) or the unreachable
// backend. Other failures (network blips, 4xx oddities) stay bare - there is
// no action the player can take from a line of text anyway.
func failReasonSuffix(msg string) string {
	if i := strings.Index(msg, "(409): "); i >= 0 {
		return " - " + rejectionShort(strings.TrimSpace(msg[i+len("(409): "):]))
	}
	if strings.Contains(msg, "backend returned 5") {
		return " - StarLance unreachable"
	}
	return ""
}

// tailFile returns the last n lines of path, or "" on any error.
func tailFile(path string, n int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}
