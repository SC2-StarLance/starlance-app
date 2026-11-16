package main

// relocation.go - the first launch from a downloaded file installs the app
// in a stable per-user location and hands over to that copy. Players run the
// file straight out of Downloads (or a zip extracted to the Desktop), and
// browser/cleanup tools delete those folders; a desktop entry, login
// registration or updater left pointing there is how the app silently stops
// starting. After the first launch everything points at a path the player
// never has to touch again. A dev build and -portable never install a copy.

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// portableMode is the -portable switch: run from wherever the binary is,
// never install a copy. It is the way out for a player who manages the
// binary themselves (a package manager, a USB stick, a shared machine).
var portableMode bool

// relocateIfNeeded installs the running binary at its stable per-user path
// and starts that copy with the same command line, reporting whether this
// process handed over. Every failure falls back to running in place: the app
// must never refuse to start because it could not copy itself.
func relocateIfNeeded(logger *log.Logger, userArgs []string) bool {
	if portableMode || isDevBuild() {
		return false
	}
	exe, err := currentExecutable()
	if err != nil {
		logger.Printf("relocate: %v", err)
		return false
	}
	target, moved, err := installStableCopy(exe)
	if err != nil {
		logger.Printf("relocate: cannot install %s: %v", exe, err)
		return false
	}
	if !moved {
		return false
	}
	if err := startDetached(target, takeoverArgs(userArgs)); err != nil {
		logger.Printf("relocate: installed at %s but cannot start it: %v", target, err)
		return false
	}
	logger.Printf("relocate: running from %s from now on", target)
	return true
}

// takeoverArgs is the command line for the handed-over copy: the user's
// flags verbatim (so -profile/-prefix/-path/-portable survive) plus this
// process's pid, so the copy waits for this one to exit and the two never
// run at once.
func takeoverArgs(userArgs []string) []string {
	out := []string{fmt.Sprintf("-takeover-from=%d", os.Getpid())}
	for i := 0; i < len(userArgs); i++ {
		a := userArgs[i]
		if a == "-takeover-from" || a == "--takeover-from" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-takeover-from=") || strings.HasPrefix(a, "--takeover-from=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// withinDir reports whether path lives inside dir (or is dir itself).
func withinDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// copyExecutable copies src to dst through a temp file and a rename, so an
// interrupted copy never leaves a truncated binary at the install path. A
// rename that loses a race with another launch is a success: the target that
// won is the copy we wanted.
func copyExecutable(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		if info, statErr := os.Stat(dst); statErr == nil && info.Mode().IsRegular() {
			return nil
		}
		return err
	}
	return nil
}

// copyTree copies a directory tree (a macOS .app bundle) preserving file
// modes. Symlinks are recreated as symlinks, never followed, so a bundle
// cannot drag files from outside itself along.
func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(dst, info.Mode().Perm())
	case info.Mode().IsRegular():
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}
	return nil
}
