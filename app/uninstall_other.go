//go:build !linux && !darwin && !windows

package main

// The three shipped platforms have a native uninstall path. Other builds say
// so honestly instead of leaving a half-removed app behind.

import (
	"errors"
	"log"
)

var errUninstallUnsupported = errors.New("uninstall is not supported on this platform")

func removeUninstallRegistrations(logger *log.Logger) error { return errUninstallUnsupported }

func uninstallPlatformPaths() []string { return nil }

func startUninstallCleanup(paths []string) error { return errUninstallUnsupported }
