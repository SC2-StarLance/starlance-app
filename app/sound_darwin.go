//go:build darwin

package main

import "os/exec"

func playSoundFile(path string) {
	_ = exec.Command("afplay", path).Start()
}
