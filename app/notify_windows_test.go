//go:build windows

package main

import (
	"strings"
	"testing"
)

// TestToastScriptEscaping pins the toast payload construction: XML text is
// escaped (&, <, >, apostrophe) so no game outcome or map name can break the
// toast document or the PowerShell single-quoted string, and the script
// references the registered PowerShell AUMID.
func TestToastScriptEscaping(t *testing.T) {
	script := toastScript("Victory & a 'quoted' <win>", "it's fine")
	for _, want := range []string{
		"Victory &amp; a &apos;quoted&apos; &lt;win&gt;",
		"it&apos;s fine",
		"ToastGeneric",
		toastAUMID,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("toast script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, `'quoted'`) {
		t.Error("raw apostrophes must be escaped in the payload")
	}
}

// TestEncodeUTF16LE is a known-answer check for the -EncodedCommand format.
func TestToastUTF16LE(t *testing.T) {
	got := utf16LE("hi")
	want := []byte{0x68, 0x00, 0x69, 0x00}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("utf16LE(%q) = %v, want %v", "hi", got, want)
	}
}
