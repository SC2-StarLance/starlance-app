//go:build !windows && !darwin

package main

import (
	"log"
	"os"
	"os/exec"
)

// Native dialog helpers used by the tray's "Recent activity" menu item. They
// reuse the zenity → kdialog pattern of the folder picker: no new
// dependencies, and the app degrades to the text-editor fallback when
// neither is installed.

// showTextInfo opens a scrollable text window with the given body (the tail
// of uploader.log for "Recent activity"). Returns false when no dialog tool
// is available.
func showTextInfo(logger *log.Logger, title, body string) bool {
	if zn := lookPath("zenity"); zn != "" {
		if launchTextDialog(zn, []string{"--text-info", "--title", title, "--width", "640", "--height", "440"}, body, logger) {
			return true
		}
	}
	if kd := lookPath("kdialog"); kd != "" {
		if launchTextDialog(kd, []string{"--title", title, "--textbox"}, body, logger) {
			return true
		}
	}
	return false
}

// launchTextDialog writes body to a temp file and opens it with the dialog
// tool and the given arguments. Reports whether the tool was launched. The
// temp file is removed only after the dialog exits: the tool reads it
// asynchronously, so deleting it immediately could leave the window empty.
// The child is waited on so it never lingers as a zombie.
func launchTextDialog(tool string, args []string, body string, logger *log.Logger) bool {
	tmp, err := os.CreateTemp("", "starlance-activity-*.txt")
	if err != nil {
		logger.Printf("dialog: create temp activity file: %v", err)
		return false
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(name)
		logger.Printf("dialog: write activity file: %v", err)
		return false
	}
	tmp.Close()
	cmd := append(append([]string{}, args...), "--filename", name)
	if tool == "kdialog" {
		// kdialog takes the file as the value of --textbox.
		cmd = append(append([]string{}, args...), name)
	}
	c := exec.Command(tool, cmd...)
	if err := c.Start(); err != nil {
		os.Remove(name)
		logger.Printf("dialog: %s failed: %v", tool, err)
		return false
	}
	go func() {
		_ = c.Wait() // reap: never leave a zombie behind
		os.Remove(name)
	}()
	return true
}

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}
