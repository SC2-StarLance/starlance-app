// tools/fetchassets downloads the StarCraft II artwork the app displays from
// the public artwork repository and verifies every file against assets.lock.
//
// The artwork is not stored in this repository (it is Blizzard Entertainment
// property). Builds, tests and releases run this first; a plain `go build`
// fails on the artwork embed guard until it has run.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type entry struct {
	sum  string
	path string
}

type lock struct {
	repo   string
	commit string
	files  []entry
}

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "verify the local files without downloading")
	flag.Parse()

	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "fetchassets:", err)
		os.Exit(1)
	}
}

func run(root string, checkOnly bool) error {
	l, err := readLock(filepath.Join(root, "assets.lock"))
	if err != nil {
		return err
	}
	if !checkOnly && (l.repo == "" || l.commit == "") {
		return errors.New("assets.lock is missing repo or commit")
	}

	client := &http.Client{Timeout: 60 * time.Second}
	fetched := 0
	for _, f := range l.files {
		if err := safeRelativePath(f.path); err != nil {
			return fmt.Errorf("%s: %w", f.path, err)
		}
		target := filepath.Join(root, "app", "frontend", "dist", filepath.FromSlash(f.path))
		if current, err := fileHash(target); err == nil && current == f.sum {
			continue
		}
		if checkOnly {
			return fmt.Errorf("%s is missing or does not match assets.lock (run make assets)", f.path)
		}
		url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", l.repo, l.commit, f.path)
		if err := download(client, url, target, f.sum); err != nil {
			return fmt.Errorf("%s: %w", f.path, err)
		}
		fetched++
		fmt.Println("fetched", f.path)
	}
	if fetched > 0 {
		fmt.Printf("%d artwork files fetched\n", fetched)
	} else {
		fmt.Printf("artwork is current (%d files)\n", len(l.files))
	}
	return nil
}

func readLock(name string) (lock, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return lock{}, err
	}
	var l lock
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "repo "):
			l.repo = strings.TrimSpace(strings.TrimPrefix(line, "repo "))
		case strings.HasPrefix(line, "commit "):
			l.commit = strings.TrimSpace(strings.TrimPrefix(line, "commit "))
		default:
			fields := strings.Fields(line)
			if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
				return lock{}, fmt.Errorf("assets.lock:%d: expected '<sha256>  <path>'", i+1)
			}
			if _, err := hex.DecodeString(fields[0]); err != nil {
				return lock{}, fmt.Errorf("assets.lock:%d: bad sha256: %w", i+1, err)
			}
			l.files = append(l.files, entry{sum: fields[0], path: fields[1]})
		}
	}
	if len(l.files) == 0 {
		return lock{}, errors.New("assets.lock lists no files")
	}
	return l, nil
}

func safeRelativePath(p string) error {
	if p == "" || path.IsAbs(p) || strings.Contains(p, "\\") {
		return errors.New("not a relative slash path")
	}
	clean := path.Clean(p)
	if clean != p || clean == "." || strings.HasPrefix(clean, "..") {
		return errors.New("path must be clean and stay inside app/frontend/dist")
	}
	return nil
}

func fileHash(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func download(client *http.Client, url, target, want string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch: expected %s, got %x", want, got)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".fetch-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, target)
}
