//go:build !windows

package main

// windowsDocumentsDir is only implemented on Windows; every other platform
// resolves Documents from the home directory directly.
func windowsDocumentsDir() string { return "" }
