//go:build windows

package main

// Windows side of the Settings uninstall: delete every StarLance Run-key
// value and Start Menu shortcut now, then hand the running executable's own
// folder and the state folder to a detached PowerShell that waits for this
// process to exit before removing them. Windows will not delete a running
// image, and a helper written as an encoded command needs no quoting to break
// on a player name containing '&' or a quote.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

func removeUninstallRegistrations(logger *log.Logger) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	switch {
	case err == nil:
		names, _ := key.ReadValueNames(-1)
		for _, name := range names {
			if name == runValueName || strings.HasPrefix(name, runValueName+"-") {
				_ = key.DeleteValue(name)
			}
		}
		key.Close()
	case !errors.Is(err, registry.ErrNotExist):
		return fmt.Errorf("open startup registration: %w", err)
	}

	cfg, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve app data folder: %w", err)
	}
	entries, _ := filepath.Glob(filepath.Join(cfg, "Microsoft", "Windows", "Start Menu", "Programs", shortcutNameFor("")+"*.lnk"))
	var firstErr error
	for _, path := range entries {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("remove Start Menu shortcut: %w", firstErr)
	}
	return nil
}

// uninstallPlatformPaths adds the installed copy's folder and the WebView2
// data folder. A copy launched from Downloads is left where the player put it.
func uninstallPlatformPaths() []string {
	var out []string
	if installed := installLocation(); installed != "" {
		out = append(out, filepath.Dir(installed))
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		if exe, err := resolvedExecutable(); err == nil {
			out = append(out, filepath.Join(cfg, filepath.Base(exe)))
		}
	}
	return out
}

func startUninstallCleanup(paths []string) error {
	script := "Wait-Process -Id " + strconv.Itoa(os.Getpid()) + " -ErrorAction SilentlyContinue;"
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, psSingleQuote(path))
	}
	// WebView2 helper processes can outlive the app by a moment and keep a
	// file locked, so retry for 30 seconds instead of leaving a partial
	// removal behind.
	script += "$paths = @(" + strings.Join(quoted, ",") + ");" +
		"for ($i = 0; $i -lt 60; $i++) {" +
		" $left = @($paths | Where-Object { Test-Path -LiteralPath $_ });" +
		" if ($left.Count -eq 0) { break };" +
		" foreach ($p in $left) { Remove-Item -LiteralPath $p -Recurse -Force -ErrorAction SilentlyContinue };" +
		" Start-Sleep -Milliseconds 500" +
		"}"
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(utf16LE(script)))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
	return cmd.Start()
}

// psSingleQuote renders a PowerShell single-quoted string. Doubling every
// quote is the only escape that language applies inside these literals.
func psSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
