//go:build windows

package main

// Windows launch-at-login via the per-user Run key
// (HKCU\Software\Microsoft\Windows\CurrentVersion\Run). HKCU needs no admin
// rights, and the tray app is per-user by design (state lives under
// %APPDATA%).

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// runValueName is the Run-key value; stable so toggling overwrites instead
// of accumulating duplicates.
const runValueName = "StarLanceReplayUploader"

// runValueNameFor suffixes the value for -profile instances, so two accounts
// keep their own login item instead of overwriting one shared value.
func runValueNameFor(profile string) string {
	if profile == "" {
		return runValueName
	}
	return runValueName + "-" + profile
}

// runCommand is the registered command line: quoted binary plus the flags
// that pick this instance's profile and replay prefix, so the login launch
// resumes this account instead of a bare default-profile app.
func runCommand(exe string) string {
	parts := []string{`"` + exe + `"`}
	for _, a := range loginArgs(profileName, prefixName) {
		parts = append(parts, `"`+a+`"`)
	}
	return strings.Join(parts, " ")
}

func setRunAtStart(exe string, enable bool) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if !enable {
		if err := key.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	// Quoted path: Run-key values with spaces need quoting to be parsed
	// correctly at login.
	return key.SetStringValue(runValueNameFor(profileName), runCommand(exe))
}

func runAtStartEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	v, _, err := key.GetStringValue(runValueNameFor(profileName))
	if err != nil {
		return false
	}
	// Registered only when the value replays this exact binary and profile,
	// so a moved binary or another account's entry never counts as ours.
	self, err := resolvedExecutable()
	if err != nil {
		return false
	}
	return v == runCommand(self)
}

// runAtStartPath reports the binary the current profile's Run value launches
// and whether a value exists at all. An unparsable value still counts as
// found, so startup rewrites it instead of assuming the player removed the
// registration.
func runAtStartPath() (string, bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer key.Close()
	v, _, err := key.GetStringValue(runValueNameFor(profileName))
	if err != nil {
		return "", false
	}
	arg, ok := firstCommandArg(v)
	if !ok {
		return "", true
	}
	return arg, true
}
