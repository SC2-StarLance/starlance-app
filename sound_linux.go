//go:build linux

package main

import "os/exec"

// playSoundFile hands the cue to the first player present on the desktop. The
// commands are the PipeWire/Pulse/ALSA defaults on the distributions StarLance
// targets; a missing player means the cue is skipped, never an error.
func playSoundFile(path string) {
	for _, cmd := range [][]string{
		{"pw-play", path},
		{"paplay", path},
		{"aplay", "-q", path},
	} {
		if _, err := exec.LookPath(cmd[0]); err != nil {
			continue
		}
		if err := exec.Command(cmd[0], cmd[1:]...).Start(); err == nil {
			return
		}
	}
}
