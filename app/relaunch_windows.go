//go:build windows

package main

// relaunch_windows.go - spawning the updated binary and waiting for the old
// one to exit, Windows edition.

import (
	"log"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// startDetached launches the updated binary fully detached: its own process
// group, no console inheritance (both builds run as -H windowsgui anyway).
// The new instance survives this process exiting immediately after.
func startDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
	return cmd.Start()
}

// waitForParent blocks until the process that spawned this instance (the old
// binary, mid self-update) has fully exited, so the two never run at once -
// no double tray icon, no double watcher or heartbeat. Bounded to 10s: a
// wedged parent must not wedge the update with it.
func waitForParent(pid int, logger *log.Logger) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		logger.Printf("update: cannot wait for previous instance (pid %d): %v", pid, err)
		return
	}
	defer windows.CloseHandle(h)
	const timeout = 10 * time.Second
	start := time.Now()
	for {
		remaining := uint32(timeout - time.Since(start))
		if remaining == 0 {
			logger.Printf("update: previous instance (pid %d) still running after %s - taking over anyway", pid, timeout)
			return
		}
		event, err := windows.WaitForSingleObject(h, remaining)
		if err != nil {
			logger.Printf("update: wait for previous instance (pid %d) failed: %v", pid, err)
			return
		}
		if event == windows.WAIT_OBJECT_0 {
			logger.Printf("update: previous instance (pid %d) exited", pid)
			return
		}
		if event == windows.WAIT_FAILED {
			logger.Printf("update: wait for previous instance (pid %d) failed: WAIT_FAILED", pid)
			return
		}
		// WAIT_TIMEOUT with remaining == 0 is handled by the next loop pass.
	}
}
