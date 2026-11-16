//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

const (
	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndFilename  = 0x00020000
)

var winmm = syscall.NewLazyDLL("winmm.dll")
var procPlaySoundW = winmm.NewProc("PlaySoundW")

// playSoundFile plays through winmm so there is no PowerShell (or any other
// process) startup cost on the hot path of a button click.
func playSoundFile(path string) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	procPlaySoundW.Call(uintptr(unsafe.Pointer(p)), 0, sndAsync|sndNoDefault|sndFilename)
}
