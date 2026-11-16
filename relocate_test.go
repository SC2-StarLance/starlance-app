package main

// relocate_test.go - the platform-independent pieces of the install-on-first-
// launch step: handing over the command line, the stable-dir check, the copy
// primitives. The per-OS target rules are tested in relocate_linux_test.go.

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestTakeoverArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"keeps user flags", []string{"-profile", "alt", "-prefix", "/games/p"}, []string{"-profile", "alt", "-prefix", "/games/p"}},
		{"drops inline takeover", []string{"-takeover-from=12", "-path", "/r"}, []string{"-path", "/r"}},
		{"drops separate takeover", []string{"-takeover-from", "12", "-path", "/r"}, []string{"-path", "/r"}},
		{"drops double dash form", []string{"--takeover-from=12"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := takeoverArgs(c.in)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if got[0] != "-takeover-from="+strconv.Itoa(os.Getpid()) {
				t.Fatalf("first arg = %q, want the current pid handover", got[0])
			}
			if want := append([]string{got[0]}, c.want...); !reflect.DeepEqual(got, want) {
				t.Errorf("takeoverArgs(%q) = %q, want %q", c.in, got, want)
			}
		})
	}
}

func TestWithinDir(t *testing.T) {
	base := string(filepath.Separator) + filepath.Join("home", "player", "apps")
	cases := []struct {
		path string
		want bool
	}{
		{base, true},
		{filepath.Join(base, "app.exe"), true},
		{filepath.Join(base, "a", "b"), true},
		{filepath.Join(base, "..", "other"), false},
		{filepath.Join(filepath.Dir(base), "apps2"), false},
	}
	for _, c := range cases {
		if got := withinDir(c.path, base); got != c.want {
			t.Errorf("withinDir(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestCopyExecutable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "download", "app")
	dst := filepath.Join(dir, "stable", "app")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("binary contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("installed copy unreadable: %v", err)
	}
	if string(b) != "binary contents" {
		t.Errorf("installed copy = %q", b)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed copy is not executable: %v", info.Mode())
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

func TestCopyTree(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "StarLance App.app")
	if err := os.MkdirAll(filepath.Join(src, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Contents", "Info.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Contents", "MacOS", "app"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("MacOS/app", filepath.Join(src, "Contents", "link")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "Applications", "StarLance App.app")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	for _, rel := range []string{"Contents/Info.plist", "Contents/MacOS/app"} {
		b, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if len(b) == 0 {
			t.Errorf("%s is empty", rel)
		}
	}
	link, err := os.Readlink(filepath.Join(dst, "Contents", "link"))
	if err != nil {
		t.Fatalf("symlink not preserved: %v", err)
	}
	if link != "MacOS/app" {
		t.Errorf("symlink target = %q", link)
	}
	info, err := os.Stat(filepath.Join(dst, "Contents", "MacOS", "app"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("bundle executable lost its mode: %v", info.Mode())
	}
}

func TestCopyExecutableMissingSource(t *testing.T) {
	err := copyExecutable(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "dst"))
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing source: err = %v", err)
	}
}
