//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// shellExecuteOpen opens a URL or file with its default handler via
// shell32!ShellExecuteW ("open" verb).
func shellExecuteOpen(target string) error {
	verb, _ := syscall.UTF16PtrFromString("open")
	arg, _ := syscall.UTF16PtrFromString(target)
	ret, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW").Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(arg)),
		0,
		0,
		1, // SW_SHOWNORMAL
	)
	if ret > 31 { // >31 means the handler launched
		return nil
	}
	return syscall.EINVAL
}
