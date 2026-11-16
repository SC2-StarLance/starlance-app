package main

// The macOS launch-at-login registration is a property list, and a plist is
// just data: rendering it here (outside the darwin-only file) keeps the
// escaping and the label rules testable on any platform.

import "fmt"

// launchAgentLabel is the reverse-DNS id; it matches the app bundle id so the
// login item, notifications and Dock entry read as one application.
const launchAgentLabel = "club.starlance.app"

// launchAgentLabelFor suffixes the label for -profile instances, so two test
// accounts never fight over one login item.
func launchAgentLabelFor(profile string) string {
	if profile != "" {
		return launchAgentLabel + "." + profile
	}
	return launchAgentLabel
}

// launchAgentPlist renders the property list. Only the executable path and
// the optional -profile/-prefix pairs are dynamic; all are XML-escaped
// because a user's home directory may contain '&' or '<'.
func launchAgentPlist(exe, profile, prefix string) string {
	args := "<string>" + xmlEscape(exe) + "</string>"
	for _, a := range loginArgs(profile, prefix) {
		args += "<string>" + xmlEscape(a) + "</string>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchAgentLabelFor(profile) + `</string>
	<key>ProgramArguments</key>
	<array>
		` + args + `
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
}

// launchAgentDomain is the launchd GUI domain for a user (gui/<uid>).
func launchAgentDomain(uid int) string {
	return fmt.Sprintf("gui/%d", uid)
}

// launchAgentService is the bootout target (gui/<uid>/<label>).
func launchAgentService(uid int, profile string) string {
	return launchAgentDomain(uid) + "/" + launchAgentLabelFor(profile)
}
