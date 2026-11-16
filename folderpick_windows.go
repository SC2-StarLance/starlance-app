//go:build windows

package main

import (
	"log"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// PickFolder asks the user to choose a directory using the native dialog.
// It returns "" when the user cancels — the caller must then tell the user
// how to configure the path manually instead of ever guessing.
func PickFolder(logger *log.Logger, title string) string {
	return windowsPickFolder(title)
}

// windowsPickFolder shows the native folder browser dialog
// (shell32!SHBrowseForFolderW) and returns the chosen path, or "" when
// cancelled.
func windowsPickFolder(title string) string {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	display := make([]uint16, 260) // MAX_PATH

	bi := &browseInfoW{
		hwndOwner:   0,
		displayName: &display[0],
		title:       titlePtr,
		flags:       0x0001, // BIF_RETURNONLYFSDIRS
	}
	pidl, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("SHBrowseForFolderW").Call(
		uintptr(unsafe.Pointer(bi)),
	)
	if pidl == 0 {
		return ""
	}
	defer syscall.NewLazyDLL("ole32.dll").NewProc("CoTaskMemFree").Call(pidl)

	path := make([]uint16, 260)
	ok, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("SHGetPathFromIDListW").Call(
		pidl,
		uintptr(unsafe.Pointer(&path[0])),
	)
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(path)
}

// browseInfoW mirrors shell32's BROWSEINFOW struct.
type browseInfoW struct {
	hwndOwner   uintptr
	root        uintptr
	displayName *uint16
	title       *uint16
	flags       uint32
	callback    uintptr
	lParam      uintptr
	image       int32
}

// PickReplayFile asks the user to choose a .SC2Replay file with the native
// open-file dialog (comdlg32!GetOpenFileNameW). It returns "" when the user
// cancels - the caller stays quiet instead of guessing.
func PickReplayFile(logger *log.Logger, title string) string {
	filter := utf16.Encode([]rune("StarCraft II replays (*.SC2Replay)\x00*.SC2Replay\x00All files (*.*)\x00*.*\x00\x00"))
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	file := make([]uint16, 32768)

	ofn := &openFilenameW{
		lpstrFilter: &filter[0],
		lpstrFile:   &file[0],
		nMaxFile:    uint32(len(file)),
		lpstrTitle:  titlePtr,
		flags:       ofnPathMustExist | ofnFileMustExist | ofnHideReadOnly | ofnNoChangeDir,
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(*ofn))

	ok, _, _ := syscall.NewLazyDLL("comdlg32.dll").NewProc("GetOpenFileNameW").Call(
		uintptr(unsafe.Pointer(ofn)),
	)
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(file)
}

// openFilenameW mirrors comdlg32's OPENFILENAMEW struct.
type openFilenameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

// GetOpenFileNameW flag constants (winuser.h / cderr.h).
const (
	ofnHideReadOnly  = 0x00000004
	ofnNoChangeDir   = 0x00000008
	ofnPathMustExist = 0x00000800
	ofnFileMustExist = 0x00001000
)

// windowsDocumentsDir returns the user's Documents folder via
// SHGetKnownFolderPath (FOLDERID_Documents), or "" when unavailable.
func windowsDocumentsDir() string {
	// FOLDERID_Documents GUID {FDD39AD0-238F-46AF-ADB4-6C85480369C7}
	guid := syscall.GUID{
		Data1: 0xFDD39AD0,
		Data2: 0x238F,
		Data3: 0x46AF,
		Data4: [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7},
	}
	var path uintptr
	r, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("SHGetKnownFolderPath").Call(
		uintptr(unsafe.Pointer(&guid)),
		0, // no special flags
		0,
		uintptr(unsafe.Pointer(&path)),
	)
	if r != 0 || path == 0 {
		return ""
	}
	defer syscall.NewLazyDLL("ole32.dll").NewProc("CoTaskMemFree").Call(path)
	return utf16PtrToStringPtr(path)
}

// utf16PtrToStringPtr converts a NUL-terminated UTF-16 string pointer to a
// Go string without needing the length.
func utf16PtrToStringPtr(p uintptr) string {
	var n int
	for ptr := (*uint16)(unsafe.Pointer(p)); ; ptr = (*uint16)(unsafe.Pointer(p + uintptr(2*n))) {
		if *ptr == 0 {
			break
		}
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(p)), n))
}
