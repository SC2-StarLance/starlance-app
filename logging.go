package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

// newLogger returns a logger that writes to uploader.log in the per-user app
// dir and, unless we are the Windows GUI subsystem, also echoes to stderr so
// output is visible in a terminal when running interactively.
func newLogger(appDirPath string) *log.Logger {
	logPath := filepath.Join(appDirPath, LogFilename)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		f = nil // fall back to stderr only
	}

	var writers []io.Writer
	if !isWindowsGUI() {
		writers = append(writers, os.Stderr)
	}
	if f != nil {
		writers = append(writers, f)
	}
	return log.New(io.MultiWriter(writers...), "", log.LstdFlags)
}

// isWindowsGUI reports whether we should suppress stderr echoes (the -H
// windowsgui build hides console output anyway).
func isWindowsGUI() bool {
	return runtime.GOOS == "windows"
}
