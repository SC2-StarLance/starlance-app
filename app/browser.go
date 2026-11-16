package main

import (
	"os/exec"
	"runtime"
)

// openBrowserFn is indirected so tests can stub the browser launch.
var openBrowserFn = openBrowser

// openBrowser opens url in the default browser: ShellExecute "open" on
// Windows, `open` on macOS, xdg-open elsewhere.
func openBrowser(url string) error {
	return openExternal(url)
}

// openExternal hands a URL or a path to the desktop's default handler:
// ShellExecute on Windows, `open` on macOS, xdg-open on Linux/BSD.
func openExternal(target string) error {
	if runtime.GOOS == "windows" {
		return shellExecuteOpen(target)
	}
	tool := "xdg-open"
	if runtime.GOOS == "darwin" {
		tool = "open"
	}
	cmd := exec.Command(tool, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap: never leave a zombie behind
	return nil
}
