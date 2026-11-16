//go:build !windows

package main

// relaunch_other.go - spawning the updated binary and waiting for the old
// one to exit, non-Windows edition. On these platforms a rename over the
// running binary is legal, so no .old file is ever produced; the old process
// just keeps running its in-memory copy until it exits.

import (
	"log"
	"os/exec"
	"syscall"
	"time"
)

// startDetached launches the updated binary in its own session so it
// survives this process exiting immediately after.
func startDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// waitForParent blocks until the process that spawned this instance (the old
// binary, mid self-update) has exited (signal 0 probe: ESRCH = gone). EPERM
// means it exists under another uid, so keep waiting. Bounded to 10s: a
// wedged parent must not wedge the update with it.
func waitForParent(pid int, logger *log.Logger) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := syscall.Kill(pid, syscall.Signal(0))
		if err == syscall.ESRCH {
			logger.Printf("update: previous instance (pid %d) exited", pid)
			return
		}
		if err != nil && err != syscall.EPERM {
			// Unexpected errno (not "alive", not "gone"): do not hang on it.
			logger.Printf("update: cannot probe previous instance (pid %d): %v", pid, err)
			return
		}
		if !time.Now().Before(deadline) {
			logger.Printf("update: previous instance (pid %d) still running after 10s - taking over anyway", pid)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
