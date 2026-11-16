//go:build !windows

package main

import "errors"

// shellExecuteOpen is only implemented for Windows; on other platforms
// openBrowser uses xdg-open directly.
func shellExecuteOpen(string) error { return errors.New("shellExecuteOpen requires windows") }
