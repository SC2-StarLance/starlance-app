//go:build !linux && !windows && !darwin

package main

// No stable install location is defined for this platform: run from where
// the binary is.

// installLocation always reports "" off the three shipped platforms: their
// installers own the binary location.
func installLocation() string { return "" }

func installStableCopy(exe string) (string, bool, error) {
	return exe, false, nil
}
