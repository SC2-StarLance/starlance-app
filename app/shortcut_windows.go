//go:build windows

package main

// Start Menu shortcut, written with WScript.Shell over in-process COM (the
// same shell API Explorer uses; no PowerShell, no console window). The
// downloaded .exe lives in Downloads and may be deleted there, so the
// shortcut points at the installed copy from relocate_windows.go and keeps
// the app findable in the Start Menu after the first launch.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

const shortcutDescription = "Queue StarLance games and upload your replays automatically"

// installStartMenuShortcut keeps the shortcut current without churning the
// Start Menu every boot: it is written when missing and when the binary is
// newer (a self-update). A shortcut the player deleted comes back on the next
// launch, like the desktop entry on Linux.
func installStartMenuShortcut(logger *log.Logger) {
	if portableMode || isDevBuild() {
		return
	}
	exe, err := currentExecutable()
	if err != nil {
		logger.Printf("shortcut: %v", err)
		return
	}
	path, err := startMenuShortcutPath(profileName)
	if err != nil {
		logger.Printf("shortcut: %v", err)
		return
	}
	if info, err := os.Stat(path); err == nil {
		if exeInfo, err := os.Stat(exe); err == nil && !info.ModTime().Before(exeInfo.ModTime()) {
			return
		}
	}
	if err := writeShortcut(path, exe, shortcutArgsFor(profileName, prefixName)); err != nil {
		logger.Printf("shortcut: %v", err)
		return
	}
	logger.Printf("shortcut: wrote %s", path)
}

// writeShortcut creates or overwrites the .lnk at path. COM apartments are
// per-thread and a goroutine may migrate, so the thread is pinned for the
// whole call.
func writeShortcut(path, exe, args string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	initErr := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	if initErr != nil {
		// S_FALSE means this thread already had an apartment; that call is
		// still balanced by CoUninitialize. Anything else means COM is not
		// usable here.
		var oleErr *ole.OleError
		if !errors.As(initErr, &oleErr) || oleErr.Code() != 1 {
			return fmt.Errorf("COM init: %w", initErr)
		}
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("WScript.Shell: %w", err)
	}
	defer unknown.Release()
	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("WScript.Shell dispatch: %w", err)
	}
	defer shell.Release()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	v, err := oleutil.CallMethod(shell, "CreateShortcut", path)
	if err != nil {
		return fmt.Errorf("create shortcut: %w", err)
	}
	defer v.Clear()
	link := v.ToIDispatch()
	if link == nil {
		return errors.New("create shortcut: no shortcut object")
	}

	for _, p := range []struct{ name, value string }{
		{"TargetPath", exe},
		{"Arguments", args},
		{"WorkingDirectory", filepath.Dir(exe)},
		{"Description", shortcutDescription},
		{"IconLocation", exe + ",0"},
	} {
		if _, err := oleutil.PutProperty(link, p.name, p.value); err != nil {
			return fmt.Errorf("set %s: %w", p.name, err)
		}
	}
	// 1 = normal window: the app decides whether to show the window or the
	// update card, the shortcut must not force either.
	if _, err := oleutil.PutProperty(link, "WindowStyle", 1); err != nil {
		return fmt.Errorf("set WindowStyle: %w", err)
	}
	if _, err := oleutil.CallMethod(link, "Save"); err != nil {
		return fmt.Errorf("save shortcut: %w", err)
	}
	return nil
}
