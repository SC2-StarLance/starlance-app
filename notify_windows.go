//go:build windows

package main

import (
	"encoding/base64"
	"log"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Notify is the Windows side of the severity model:
//   - problems (a replay did not count, session expired, login failed) raise
//     a topmost warning message box on its own goroutine - rare, and exactly
//     the events a player must not miss. The box never takes keyboard focus
//     (MB_NOFOCUS): an upload result can land right after a game ends, while
//     the player has already re-queued, and stealing focus mid-game is
//     hostile. The box stays visible on top until clicked away.
//   - infos (match outcomes, saved settings) surface as Action Center toasts
//     via Windows.UI.Notifications, driven through PowerShell (the -H
//     windowsgui build has no console and cgo is off, so the WinRT COM API is
//     not reachable directly; PowerShell is preinstalled and its AUMID is a
//     registered toast source, which is what makes unregistered-app toasts
//     display reliably). A toast that cannot be shown falls back to the log
//     only - never a modal box per game.
func Notify(logger *log.Logger, level NoticeLevel, title, message string) {
	if level == NoticeProblem {
		go notifyProblemOnWindows(title, message)
		return
	}
	go notifyWindowsToast(title, message)
	if logger != nil {
		logger.Printf("notification: %s: %s", title, message)
	}
}

// notifyProblemOnWindows raises a topmost warning box that does NOT steal
// focus: it appears above other windows but never yanks the keyboard away
// from a running game. The player deals with it the moment they look up.
func notifyProblemOnWindows(title, message string) {
	const (
		mbOK       = 0
		mbIconWarn = 0x30
		mbTopmost  = 0x40000
		mbNoFocus  = 0x08000000
	)
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	msgPtr, _ := syscall.UTF16PtrFromString(message)
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(
		0,
		uintptr(unsafe.Pointer(msgPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		mbOK|mbIconWarn|mbTopmost|mbNoFocus,
	)
}

// notifyOnWindows surfaces a blocking warning message box that also takes
// focus. It is only used for fatal startup errors, where the process is on
// its way out and being missed is worse than a stolen focus.
func notifyOnWindows(title, message string) {
	const (
		mbOK         = 0
		mbIconWarn   = 0x30
		mbTopmost    = 0x40000
		mbSetForegrd = 0x10000
	)
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	msgPtr, _ := syscall.UTF16PtrFromString(message)
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(
		0,
		uintptr(unsafe.Pointer(msgPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		mbOK|mbIconWarn|mbTopmost|mbSetForegrd,
	)
}

// toastAUMID is the AppUserModelID the toast is shown under. An unregistered
// custom AUMID is silently dropped by some Windows 10 builds, so the toast
// is issued under PowerShell's registered AUMID (the same approach the
// widely used go-toast library takes). Attribution reads "Windows
// PowerShell"; a reliably visible toast beats a pretty-but-dropped one.
const toastAUMID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// notifyWindowsToast shows one Action Center toast (async, never modal).
// Failure to show (missing PowerShell, hard-blocked notifications) is quiet:
// infos are confirmations, not alarms.
func notifyWindowsToast(title, message string) {
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(utf16LE(toastScript(title, message))))
	// The app itself has no console (-H windowsgui); keep the child from
	// flashing a window over the game too.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if err := cmd.Run(); err != nil {
		// Toast could not be shown; the notification still went to the log.
		return
	}
}

// toastScript builds the PowerShell snippet that raises one toast. The toast
// XML is assembled in Go with XML-escaped text (apostrophes become &apos;,
// which is also what keeps the payload safe inside the PowerShell
// single-quoted string), and the whole script is passed via -EncodedCommand
// so no shell quoting can break or inject.
func toastScript(title, message string) string {
	xml := `<toast><visual><binding template="ToastGeneric">` +
		`<text>` + xmlEscape(title) + `</text>` +
		`<text>` + xmlEscape(message) + `</text>` +
		`</binding></visual></toast>`
	return "[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null; " +
		"[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null; " +
		"$x = New-Object Windows.Data.Xml.Dom.XmlDocument; " +
		"$x.LoadXml('" + xml + "'); " +
		"$n = New-Object Windows.UI.Notifications.ToastNotification $x; " +
		"[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('" + toastAUMID + "').Show($n)"
}

// utf16LE encodes s as UTF-16LE bytes, the format -EncodedCommand expects.
func utf16LE(s string) []byte {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return nil
	}
	b := make([]byte, 0, len(u)*2)
	for _, r := range u {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}
