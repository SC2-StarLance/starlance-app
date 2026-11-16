//go:build linux

package main

// Linux desktop integration. Wayland has no per-window icon, so the
// compositor resolves a window's taskbar/dock icon by matching its app_id
// (linux.Options.ProgramName) to a .desktop entry and reading that entry's
// Icon key. Release builds ship a bare binary, so nothing installs those
// files; the app writes its own entry and icon into the user's XDG data dir
// on every start and rewrites them only when the content changed (the entry
// also self-heals its Exec after the binary moves or updates). Without this
// KDE paints the generic Wayland-logo fallback: the white W on a yellow
// circle. This is why the fix is not a window icon: linux.Options.Icon only
// reaches X11.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	desktopIconName      = linuxAppID
	desktopIconSize      = 256
	desktopIconOldName   = "starlance-uploader.png"
	desktopEntryOldNames = "StarLance.desktop,starlance-uploader.desktop"
)

// installDesktopIntegration writes the desktop entry and icon that give the
// window its taskbar identity, then removes stale entries left by older
// builds. Every failure is logged and skipped: a missing icon is cosmetic and
// must never block the uploader.
func installDesktopIntegration(logger *log.Logger) {
	exe, err := currentExecutable()
	if err != nil {
		logger.Printf("desktop: %v", err)
		return
	}
	data := xdgDataHome()
	if data == "" {
		logger.Printf("desktop: cannot resolve the XDG data dir")
		return
	}
	appsDir := filepath.Join(data, "applications")
	iconDir := filepath.Join(data, "icons", "hicolor", fmt.Sprintf("%dx%d", desktopIconSize, desktopIconSize), "apps")

	iconChanged, err := writeIfChanged(filepath.Join(iconDir, desktopIconName+".png"), encodePNG(desktopIconSize), 0o644)
	if err != nil {
		logger.Printf("desktop: write icon: %v", err)
	}

	entryPath := filepath.Join(appsDir, linuxAppID+".desktop")
	if _, err := writeIfChanged(entryPath, []byte(desktopEntry(exe)), 0o644); err != nil {
		logger.Printf("desktop: write entry: %v", err)
	}

	if iconChanged {
		refreshIconCache(filepath.Join(data, "icons", "hicolor"), logger)
	}

	for _, name := range strings.Split(desktopEntryOldNames, ",") {
		removeStaleDesktopEntry(filepath.Join(appsDir, name), logger)
	}
	// The old icon name is now unreferenced; drop it so no stale artwork can
	// resurface if an old entry survives somewhere else.
	removeStaleDesktopIcon(filepath.Join(iconDir, desktopIconOldName), logger)
}

// desktopEntry renders the entry for this binary. Exec is always quoted per
// the Desktop Entry spec so a path with spaces still launches.
func desktopEntry(exe string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=StarLance App\n" +
		"Comment=Uploads StarLance ladder replays automatically\n" +
		"Exec=" + desktopExecArg(exe) + "\n" +
		"Icon=" + desktopIconName + "\n" +
		"Terminal=false\n" +
		"Categories=Game;Network;\n" +
		"StartupWMClass=" + linuxAppID + "\n"
}

// desktopExecArg quotes one Exec argument. Inside double quotes the spec
// reserves backslash, double quote, backtick and dollar, so each is escaped.
func desktopExecArg(arg string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"`", "\\`",
		`$`, `\$`,
	)
	return `"` + r.Replace(arg) + `"`
}

// xdgDataHome returns $XDG_DATA_HOME, defaulting to ~/.local/share.
func xdgDataHome() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// writeIfChanged writes b only when the file does not already hold exactly b,
// so a normal start never rewrites (and never restarts the compositor's
// watchers for) unchanged files. Reports whether the file changed.
func writeIfChanged(path string, b []byte, perm os.FileMode) (bool, error) {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, b) {
		return false, nil
	}
	if err := writeFileAtomic(path, b, perm); err != nil {
		return false, err
	}
	return true, nil
}

// removeStaleDesktopEntry deletes an entry left by an older build, but only
// when its icon key proves it is ours; another app's entry is left untouched.
func removeStaleDesktopEntry(path string, logger *log.Logger) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	text := string(b)
	if !strings.Contains(text, "Icon="+desktopIconName) && !strings.Contains(text, "Icon=starlance-uploader") {
		return
	}
	removeStaleFile(path, "entry", logger)
}

// removeStaleDesktopIcon deletes the icon of the retired entry name.
func removeStaleDesktopIcon(path string, logger *log.Logger) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// Only the icon of the retired entry is deleted, and only when it differs
	// from the current artwork (a byte-identical file is already harmless).
	if bytes.Equal(b, encodePNG(desktopIconSize)) {
		return
	}
	removeStaleFile(path, "icon", logger)
}

func removeStaleFile(path, what string, logger *log.Logger) {
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			logger.Printf("desktop: remove stale %s %s: %v", what, path, err)
		}
		return
	}
	logger.Printf("desktop: removed stale %s %s", what, path)
}

// refreshIconCache rebuilds the hicolor cache so icon lookups see the newly
// written file immediately. Best effort: the cache is an optimization and
// tooling may be absent.
func refreshIconCache(themeDir string, logger *log.Logger) {
	bin, err := exec.LookPath("gtk-update-icon-cache")
	if err != nil {
		return
	}
	if out, err := exec.Command(bin, "-q", "-t", themeDir).CombinedOutput(); err != nil {
		logger.Printf("desktop: icon cache refresh: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}
