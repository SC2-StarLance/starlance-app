package main

// AppleScript string literals, shared by the macOS notification and picker
// scripts. Kept outside the darwin-tagged files so the escaping is testable
// anywhere.

import "strings"

// appleScriptLiteral renders s as a double-quoted AppleScript string literal.
// The pickers' titles are our own fixed copy, but escaping keeps a translated
// or player-supplied string from breaking the script.
func appleScriptLiteral(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
