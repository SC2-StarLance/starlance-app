package main

// startup.go — "Start with system" support, toggled from the tray menu.
//
// The single most common way a player loses StarLance games is forgetting
// the uploader: they queued without it, played, and the replay never
// uploaded. A tray toggle that registers the app at login removes that
// whole failure class with zero configuration. The registered command is
// the resolved binary path (symlinks evaluated), never relative.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// SetRunAtStart enables or disables launch-at-login for the current user.
// It only flips the platform registration; persisting the Config field is
// the caller's job (so a failed registration never desyncs the checkbox).
func SetRunAtStart(enable bool) error {
	exe, err := resolvedExecutable()
	if err != nil {
		return err
	}
	return setRunAtStart(exe, enable)
}

// resolvedExecutable is the binary path every platform registers at login:
// symlinks evaluated, so a package-manager shim never wins over the real
// binary. Inside a macOS .app bundle os.Executable already points at
// Contents/MacOS/<binary>, which is exactly what launchd must start.
func resolvedExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	if resolved, lerr := filepath.EvalSymlinks(exe); lerr == nil {
		exe = resolved
	}
	return exe, nil
}

// RunAtStartEnabled reports the current platform registration state.
func RunAtStartEnabled() bool {
	return runAtStartEnabled()
}

// loginArgs are the flags the registration must replay so the login launch
// resumes this instance instead of a bare default-profile app: without them
// a second account's entry starts the first account (and watches its
// replays). The macOS plist renders the same pair from its own argument
// list; Linux and Windows quote these with the host's command-line rules.
func loginArgs(profile, prefix string) []string {
	var out []string
	if profile != "" {
		out = append(out, "-profile", profile)
	}
	if prefix != "" {
		out = append(out, "-prefix", prefix)
	}
	return out
}

// reconcileRunAtStart keeps the stored StartWithSystem preference honest at
// every launch: the OS registration is the truth. An entry that launches an
// older install location (the binary moved, or the player updated a copy)
// is re-pointed at this binary. An entry the player removed through the OS is
// honored by turning the setting off, never by re-adding it underneath them.
func reconcileRunAtStart(cfg *Config, logger *log.Logger) {
	if cfg == nil || !cfg.StartWithSystem {
		return
	}
	exe, err := resolvedExecutable()
	if err != nil {
		logger.Printf("startup: cannot check launch-at-login: %v", err)
		return
	}
	registered, found := runAtStartPath()
	if !found {
		cfg.StartWithSystem = false
		if err := SaveConfig(settingsPath, cfg); err != nil {
			logger.Printf("startup: could not turn launch-at-login off: %v", err)
		}
		logger.Println("startup: launch-at-login entry is gone; turned the setting off")
		return
	}
	if registered != "" && sameExecutablePath(registered, exe) {
		return
	}
	if err := SetRunAtStart(true); err != nil {
		cfg.StartWithSystem = false
		if serr := SaveConfig(settingsPath, cfg); serr != nil {
			logger.Printf("startup: could not turn launch-at-login off: %v", serr)
		}
		logger.Printf("startup: cannot point launch-at-login at %s: %v", exe, err)
		return
	}
	logger.Printf("startup: launch-at-login re-pointed from %q to %q", registered, exe)
}

// sameExecutablePath compares two binary paths the way the host filesystem
// does: exact everywhere except Windows, where the same file can be spelled
// with different case.
func sameExecutablePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	return runtime.GOOS == "windows" && strings.EqualFold(a, b)
}

// firstCommandArg returns the first argv word of a Windows command line as it
// is launched: either a quoted path (what the registration writes) or a bare
// one (a hand-edited value).
func firstCommandArg(cmd string) (string, bool) {
	rest := strings.TrimSpace(cmd)
	if rest == "" {
		return "", false
	}
	if rest[0] != '"' {
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			return rest[:i], true
		}
		return rest, true
	}
	// Walk to the closing quote with the Windows rule: backslashes are literal
	// unless they precede a quote, where pairs of them become one backslash.
	var b strings.Builder
	slashes := 0
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		if c == '\\' {
			slashes++
			continue
		}
		if c == '"' {
			b.WriteString(strings.Repeat(`\`, slashes/2))
			if slashes%2 == 1 {
				b.WriteByte('"')
				slashes = 0
				continue
			}
			return b.String(), true
		}
		b.WriteString(strings.Repeat(`\`, slashes))
		slashes = 0
		b.WriteByte(c)
	}
	return "", false
}

// firstExecArg returns the program of a Desktop Entry Exec= value. The
// registration writes Go-quoted arguments (%q), so the first token is decoded
// with strconv.Unquote; an unquoted hand-written value is taken up to the
// first space.
func firstExecArg(execLine string) (string, bool) {
	rest := strings.TrimSpace(execLine)
	if rest == "" {
		return "", false
	}
	if rest[0] != '"' {
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			return rest[:i], true
		}
		return rest, true
	}
	end := -1
	for i := 1; i < len(rest); i++ {
		if rest[i] == '\\' {
			i++
			continue
		}
		if rest[i] == '"' {
			end = i
			break
		}
	}
	if end < 0 {
		return "", false
	}
	arg, err := strconv.Unquote(rest[:end+1])
	if err != nil {
		return "", false
	}
	return arg, true
}

// plistFirstProgramArg returns the first ProgramArguments entry of a
// LaunchAgent property list, so the startup check can tell whether the
// registration still launches this binary.
func plistFirstProgramArg(body string) (string, bool) {
	const key = "<key>ProgramArguments</key>"
	i := strings.Index(body, key)
	if i < 0 {
		return "", false
	}
	rest := body[i+len(key):]
	open := strings.Index(rest, "<string>")
	if open < 0 {
		return "", false
	}
	rest = rest[open+len("<string>"):]
	close := strings.Index(rest, "</string>")
	if close < 0 {
		return "", false
	}
	return xmlUnescape(rest[:close]), true
}

// xmlUnescape reverses xmlEscape (used only to read back our own plist).
func xmlUnescape(s string) string {
	return strings.NewReplacer(
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&apos;", "'",
		"&amp;", "&",
	).Replace(s)
}
