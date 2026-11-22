package main

// update_test.go - tests for the self-update glue around
// github.com/creativeprojects/go-selfupdate. The library itself owns
// detection, checksum validation, archive decompression, and the binary
// swap (all covered by its own test suite); what is tested here is the glue:
// the version gate (dev builds never update), the "newer only" rule, the
// restart plumbing pieces that are pure functions, and the fail-open error
// paths against a fake release source. applyUpdateWith's success path would
// replace the running test binary, so it is only exercised up to (and
// excluding) the swap.

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

func TestSelfUpdateEnabled(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	for _, v := range []string{"dev", "", "banana"} {
		Version = v
		if selfUpdateEnabled() {
			t.Errorf("%q build must not self-update", v)
		}
	}
	for _, v := range []string{"1.2.3", "v1.2.3"} {
		Version = v
		if !selfUpdateEnabled() {
			t.Errorf("%q release build must self-update", v)
		}
	}
}

func TestCurrentExecutable(t *testing.T) {
	exe, err := currentExecutable()
	if err != nil {
		t.Fatalf("currentExecutable: %v", err)
	}
	if !filepath.IsAbs(exe) {
		t.Errorf("currentExecutable must be absolute, got %s", exe)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Errorf("resolved binary does not exist: %v", err)
	}
}

func TestRemoveOldBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "starlance-app")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := log.New(os.Stderr, "", 0)

	// No leftover: quiet no-op.
	removeOldBinary(exe, logger)

	// The library's Windows leftover name: ".<exe>.old" next to the binary.
	old := filepath.Join(dir, ".starlance-app.old")
	if err := os.WriteFile(old, []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	removeOldBinary(exe, logger)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("leftover %s must be removed", old)
	}

	// Empty exe path: quiet no-op.
	removeOldBinary("", logger)
}

// fakeSource implements selfupdate.Source against in-memory releases.
type fakeSource struct {
	releases []selfupdate.SourceRelease
}

func (s *fakeSource) ListReleases(ctx context.Context, repo selfupdate.Repository) ([]selfupdate.SourceRelease, error) {
	return s.releases, nil
}

func (s *fakeSource) DownloadReleaseAsset(ctx context.Context, rel *selfupdate.Release, assetID int64) (io.ReadCloser, error) {
	return nil, errors.New("not implemented by fakeSource")
}

type fakeRelease struct {
	tag    string
	assets []selfupdate.SourceAsset
}

func (r fakeRelease) GetID() int64              { return 1 }
func (r fakeRelease) GetTagName() string        { return r.tag }
func (r fakeRelease) GetDraft() bool            { return false }
func (r fakeRelease) GetPrerelease() bool       { return false }
func (r fakeRelease) GetPublishedAt() time.Time { return time.Now() }
func (r fakeRelease) GetReleaseNotes() string   { return "" }
func (r fakeRelease) GetName() string           { return r.tag }
func (r fakeRelease) GetURL() string            { return "https://example.com/" + r.tag }
func (r fakeRelease) GetAssets() []selfupdate.SourceAsset {
	return r.assets
}

type fakeAsset struct {
	name string
	id   int64
}

func (a fakeAsset) GetID() int64                  { return a.id }
func (a fakeAsset) GetName() string               { return a.name }
func (a fakeAsset) GetSize() int                  { return 1 }
func (a fakeAsset) GetBrowserDownloadURL() string { return "https://example.com/dl/" + a.name }

// testAssets builds the asset set a real release must carry for this
// platform: the platform zip, its .sha256 sidecar (required by
// SHAValidator), and noise that must be ignored.
func testAssets() []selfupdate.SourceAsset {
	zipName := "starlance-app-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip"
	return []selfupdate.SourceAsset{
		fakeAsset{name: zipName + ".sha256", id: 3},
		fakeAsset{name: zipName, id: 2},
		fakeAsset{name: "starlance-app-linux-amd64.zip", id: 4},
		fakeAsset{name: "checksums.txt", id: 5},
	}
}

func newTestUpdater(t *testing.T, src selfupdate.Source) *selfupdate.Updater {
	t.Helper()
	up, err := selfupdate.NewUpdater(selfupdate.Config{
		Source:    src,
		Validator: &selfupdate.SHAValidator{},
	})
	if err != nil {
		t.Fatalf("NewUpdater: %v", err)
	}
	return up
}

func TestApplyUpdateDetectsAndGates(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "1.0.0"

	repo := selfupdate.ParseSlug("starlance/starlance-app")

	src := &fakeSource{releases: []selfupdate.SourceRelease{
		fakeRelease{tag: "v1.0.0", assets: testAssets()},
	}}
	up := newTestUpdater(t, src)

	// Same version: no-op.
	v, err := applyUpdateWith(context.Background(), up, repo)
	if err != nil || v != "" {
		t.Errorf("same version must be a no-op, got %q, %v", v, err)
	}

	// Newer version: proceeds to the download, which the fake source
	// deliberately fails - proving detection, version comparison, and the
	// checksum-sidecar requirement all passed before any binary is touched.
	src.releases = []selfupdate.SourceRelease{
		fakeRelease{tag: "v1.2.0", assets: testAssets()},
	}
	v, err = applyUpdateWith(context.Background(), up, repo)
	if err == nil || v != "" {
		t.Errorf("failed download must error without swap, got %q, %v", v, err)
	}

	// Older release: never downgrade, even though it is the latest.
	Version = "2.0.0"
	v, err = applyUpdateWith(context.Background(), up, repo)
	if err != nil || v != "" {
		t.Errorf("older latest must be a no-op, got %q, %v", v, err)
	}
}

func TestApplyUpdateReportsPhases(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "1.0.0"

	var seen []string
	setUpdatePhaseSink(func(p UpdatePhase) { seen = append(seen, p.Phase) })
	defer setUpdatePhaseSink(nil)

	repo := selfupdate.ParseSlug("starlance/starlance-app")

	// Nothing published: the check fails and the window must learn to open.
	src := &fakeSource{}
	up := newTestUpdater(t, src)
	if _, err := applyUpdateWith(context.Background(), up, repo); err == nil {
		t.Fatal("no release must error")
	}
	if got := seen; len(got) != 2 || got[0] != "checking" || got[1] != "failed" {
		t.Fatalf("no-release phases = %v, want [checking failed]", got)
	}

	// Same version: checking then uptodate, no download.
	seen = nil
	src.releases = []selfupdate.SourceRelease{fakeRelease{tag: "v1.0.0", assets: testAssets()}}
	if _, err := applyUpdateWith(context.Background(), up, repo); err != nil {
		t.Fatalf("same version: %v", err)
	}
	if got := seen; len(got) != 2 || got[0] != "checking" || got[1] != "uptodate" {
		t.Fatalf("uptodate phases = %v, want [checking uptodate]", got)
	}

	// Newer version, failing download: the card must see the download start
	// and the failure that puts the main window back.
	seen = nil
	src.releases = []selfupdate.SourceRelease{fakeRelease{tag: "v1.2.0", assets: testAssets()}}
	if _, err := applyUpdateWith(context.Background(), up, repo); err == nil {
		t.Fatal("failing download must error")
	}
	if got := seen; len(got) != 3 || got[0] != "checking" || got[1] != "downloading" || got[2] != "failed" {
		t.Fatalf("download phases = %v, want [checking downloading failed]", got)
	}
}

func TestApplyUpdateNeedsChecksumSidecar(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "1.0.0"

	repo := selfupdate.ParseSlug("starlance/starlance-app")

	// Release without the .sha256 sidecar: SHAValidator must fail detection.
	src := &fakeSource{releases: []selfupdate.SourceRelease{
		fakeRelease{tag: "v1.2.0", assets: []selfupdate.SourceAsset{
			fakeAsset{name: "starlance-app-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip", id: 2},
		}},
	}}
	up := newTestUpdater(t, src)
	if _, err := applyUpdateWith(context.Background(), up, repo); err == nil {
		t.Error("a release without a .sha256 sidecar must fail detection")
	}
}

func TestApplyUpdateNoReleaseFound(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "1.0.0"

	repo := selfupdate.ParseSlug("starlance/starlance-app")

	// The GitHub repo does not exist yet: the check must be an ordinary
	// error the loop retries on, never a silent update.
	src := &fakeSource{}
	up := newTestUpdater(t, src)
	if _, err := applyUpdateWith(context.Background(), up, repo); err == nil {
		t.Error("no release found must return an error")
	}
}

func TestPinnedAssetPatternSelectsOnlyTheZip(t *testing.T) {
	// The releases carry a raw Windows exe for the website download; the
	// updater must never pick it (or a checksum sidecar, or another OS).
	re := regexp.MustCompile(pinnedAssetPattern("windows", "amd64"))
	want := "starlance-app-windows-amd64.zip"
	if !re.MatchString(want) {
		t.Fatalf("pattern %q does not match %q", re, want)
	}
	for _, name := range []string{
		"starlance-app-windows-amd64.exe",
		"starlance-app-windows-amd64.zip.sha256",
		"starlance-app-darwin-arm64.zip",
		"starlance-app-macos-arm64.zip",
	} {
		if re.MatchString(name) {
			t.Errorf("pattern %q must not match %q", re, name)
		}
	}
	if _, err := selfupdate.NewUpdater(selfupdate.Config{
		Filters: []string{pinnedAssetPattern(runtime.GOOS, runtime.GOARCH)},
	}); err != nil {
		t.Fatalf("updater rejects the pinned filter: %v", err)
	}
}

func TestParseSlug(t *testing.T) {
	repo := selfupdate.ParseSlug(updateRepoSlug)
	if owner, name, err := repo.GetSlug(); err != nil || owner != "SC2-StarLance" || name != "starlance-app" {
		t.Errorf("slug %q must parse, got %s/%s, %v", updateRepoSlug, owner, name, err)
	}
	if _, _, err := selfupdate.ParseSlug("not a slug").GetSlug(); err == nil {
		t.Error("a malformed slug must fail")
	}
}

func TestAwaitIdle(t *testing.T) {
	// Free session: no wait at all.
	if !awaitIdle(context.Background(), func() bool { return true }, time.Hour) {
		t.Fatal("an idle session must be allowed to restart immediately")
	}

	// Busy session that never clears: cancellation ends the wait and reports
	// that the restart did not happen.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		done <- awaitIdle(ctx, func() bool { return false }, 5*time.Millisecond)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case allowed := <-done:
		if allowed {
			t.Fatal("a cancelled wait must not report an idle session")
		}
	case <-time.After(time.Second):
		t.Fatal("awaitIdle did not return after cancellation")
	}

	// Busy session that clears: the wait notices and returns true.
	release := make(chan struct{})
	busy := func() bool {
		select {
		case <-release:
			return true
		default:
			return false
		}
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan bool, 1)
	go func() {
		done2 <- awaitIdle(ctx2, busy, 5*time.Millisecond)
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case ok := <-done2:
		if !ok {
			t.Fatal("awaitIdle must report true once the session is idle")
		}
	case <-time.After(time.Second):
		t.Fatal("awaitIdle did not notice the idle session")
	}
}

func canRestartNowWith(t *testing.T, status int, body string) bool {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	return testWindowService(t, srv).canRestartNow()
}

func TestCanRestartNow(t *testing.T) {
	if canRestartNowWith(t, http.StatusOK, `{"state":"searching","pending_match":null}`) {
		t.Fatal("a searching session must block the restart")
	}
	if canRestartNowWith(t, http.StatusOK, `{"state":"idle","pending_match":{"id":"m1"}}`) {
		t.Fatal("a pending match must block the restart")
	}
	if !canRestartNowWith(t, http.StatusOK, `{"state":"idle","pending_match":null}`) {
		t.Fatal("an idle session must allow the restart")
	}
	if canRestartNowWith(t, http.StatusBadGateway, `{"message":"down"}`) {
		t.Fatal("an ambiguous status failure must refuse the restart")
	}

	// Logged out: this app has no session to interrupt, even when the
	// backend is unreachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	svc := testWindowService(t, srv)
	svc.auth = testAuth(t, "", "")
	if !svc.canRestartNow() {
		t.Fatal("a logged-out session must not block the restart")
	}
}
