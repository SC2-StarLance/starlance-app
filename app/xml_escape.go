package main

import "strings"

// xmlEscape escapes the five XML metacharacters. It serves the Windows toast
// XML (where the apostrophe is also the PowerShell single-quote delimiter)
// and the macOS LaunchAgent plist, so one copy keeps both documents valid.
func xmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}
