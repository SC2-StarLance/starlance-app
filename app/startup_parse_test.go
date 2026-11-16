package main

// startup_parse_test.go - the small parsers that read back the login
// registration, kept platform-independent so every host tests them. Getting
// one of these wrong makes the checkbox rewrite (or drop) a correct entry.

import (
	"strconv"
	"testing"
)

func TestFirstCommandArg(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`"C:\Program Files\StarLance App\app.exe" "-profile" "alt"`, `C:\Program Files\StarLance App\app.exe`, true},
		{`C:\Tools\app.exe -profile alt`, `C:\Tools\app.exe`, true},
		{`"C:\dir\\" "-x"`, `C:\dir\`, true},
		{`"C:\we\"ird"`, `C:\we"ird`, true},
		{`""`, "", true},
		{``, "", false},
		{`"unterminated`, "", false},
	}
	for _, c := range cases {
		got, ok := firstCommandArg(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("firstCommandArg(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFirstExecArg(t *testing.T) {
	for _, path := range []string{
		"/home/player/StarLance App/app",
		"/home/we ird/qu\"ote/app",
		`/home/back\slash/app`,
	} {
		line := strconv.Quote(path) + ` "-profile" "alt"`
		got, ok := firstExecArg(line)
		if !ok || got != path {
			t.Errorf("firstExecArg(%q) = (%q, %v), want %q", line, got, ok, path)
		}
	}
	if got, ok := firstExecArg(`/usr/local/bin/app -x`); !ok || got != "/usr/local/bin/app" {
		t.Errorf("unquoted exec = (%q, %v)", got, ok)
	}
	if _, ok := firstExecArg(`"unterminated`); ok {
		t.Error("unterminated Exec parsed as valid")
	}
}

func TestPlistFirstProgramArg(t *testing.T) {
	exe := "/Users/star&lance/StarLance App.app/Contents/MacOS/app"
	body := launchAgentPlist(exe, "alt", "/games/prefix")
	got, ok := plistFirstProgramArg(body)
	if !ok || got != exe {
		t.Errorf("plistFirstProgramArg = (%q, %v), want %q", got, ok, exe)
	}
	if got, ok := plistFirstProgramArg("<plist></plist>"); ok {
		t.Errorf("plist without ProgramArguments parsed as %q", got)
	}
}

func TestXMLUnescapeRoundTrip(t *testing.T) {
	for _, s := range []string{"/a/b", `C:\Games`, `a&b<c>"d'e`, "&amp;lt;"} {
		if got := xmlUnescape(xmlEscape(s)); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
}

func TestSameExecutablePath(t *testing.T) {
	if !sameExecutablePath("/a/b/../c", "/a/c") {
		t.Error("cleaned paths do not match")
	}
	if sameExecutablePath("/a/c", "/a/d") {
		t.Error("different paths match")
	}
}
