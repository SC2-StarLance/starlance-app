//go:build !linux && !windows && !darwin

package main

// launch-at-login is not implemented for this platform; the toggle reports
// the failure honestly instead of pretending to work.

import "errors"

var errUnsupportedPlatform = errors.New("start with system is not supported on this platform")

func setRunAtStart(exe string, enable bool) error { return errUnsupportedPlatform }

func runAtStartEnabled() bool { return false }

func runAtStartPath() (string, bool) { return "", false }
