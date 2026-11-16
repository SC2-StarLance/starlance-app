package main

import (
	"strings"
	"testing"
)

func TestLaunchAgentPlistEscapesAndCarriesProfile(t *testing.T) {
	body := launchAgentPlist("/Users/a&b/StarLance <dev>/sc2-replay-uploader", "alt", "/Users/a&b/prefix")
	for _, want := range []string{
		"<string>/Users/a&amp;b/StarLance &lt;dev&gt;/sc2-replay-uploader</string>",
		"<string>-profile</string><string>alt</string>",
		"<string>club.starlance.app.alt</string>",
		"<string>-prefix</string><string>/Users/a&amp;b/prefix</string>",
		"<key>RunAtLoad</key>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "a&b") {
		t.Error("plist contains an unescaped ampersand")
	}
}

func TestLaunchAgentLabelAndService(t *testing.T) {
	if got := launchAgentLabelFor(""); got != "club.starlance.app" {
		t.Errorf("default label = %q", got)
	}
	if got := launchAgentService(501, "alt"); got != "gui/501/club.starlance.app.alt" {
		t.Errorf("service = %q", got)
	}
}

func TestAppleScriptLiteralEscapesQuotesAndBackslashes(t *testing.T) {
	if got := appleScriptLiteral(`C:\games "replays"`); got != `"C:\\games \"replays\""` {
		t.Errorf("appleScriptLiteral = %s", got)
	}
}
