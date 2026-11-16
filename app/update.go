package main

// update.go - fully automatic self-update from GitHub Releases, powered by
// github.com/creativeprojects/go-selfupdate (the maintained continuation of
// rhysd/go-github-selfupdate).
//
// Players install the uploader once (a browser download from
// github.com/SC2-StarLance/starlance-app/releases) and never think
// about versions again. Shortly after startup, and then every 6 hours, the
// running binary asks GitHub for the repo's latest release; when a newer one
// exists the library downloads the platform zip, verifies it against the
// release's .minisig signature, and swaps the binary on disk
// (Windows: .new/.old rename dance, with the .old hidden while the process
// still runs - removeOldBinary cleans it up on the next start). Then this
// file's own glue takes over: notify the player, shut everything down
// cleanly, and relaunch - the new instance (started with
// -takeover-from=<pid>, see main.go) waits for the old process to fully exit
// before taking over, so the two never run at once.
//
// Asset contract with .github/workflows/release.yml, as the library detects
// it: assets named sc2-replay-uploader-<GOOS>-<GOARCH>.zip (suffix
// -<os>-<arch>.zip), the archive containing exactly the binary named
// sc2-replay-uploader(.exe), plus a <asset>.zip.minisig signature made with
// this project's minisign key (tools/signrelease, public key in minisign.pub).
// GitHub's release transport is public and unauthenticated, so the signature
// is what stops a swapped asset from replacing the player's binary. A plain
// .sha256 sidecar is still published for manual checks.
//
// Releases also carry a bare sc2-replay-uploader-windows-amd64.exe for the
// website's one-click download. The updater pins its filter to the zip: the
// library's suffix matching treats a bare -<os>-<arch>.exe as eligible too,
// so without the filter asset order would decide which one it downloads.
//
// Failure discipline mirrors the replay probe (fail-open, never a gate): any
// error - the GitHub repo not published yet, network down, checksum
// mismatch, disk trouble - logs one quiet line and retries on the next
// cycle. Update checks never block uploads, never raise the attention
// state, never show a dialog, and never interrupt the tray. A "dev" build
// (go build / go test) never self-updates: a developer's working binary
// must not be replaced.

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/jedisct1/go-minisign"
)

// updateRepoSlug is the GitHub repository that publishes releases. The repo
// does not exist yet (until then every check fails quietly and retries).
var updateRepoSlug = "SC2-StarLance/starlance-app"

const (
	// The window starts hidden and waits for the first check, so the first
	// check runs quickly: it decides between the main window and the small
	// update card.
	updateCheckDelay    = 700 * time.Millisecond
	updateCheckInterval = 6 * time.Hour

	// restartDelay lets the card paint "Restarting" before the process swap
	// tears the window down; without it the last state is an unseen frame.
	restartDelay = 900 * time.Millisecond
)

// UpdatePhase is one step of a check-and-swap cycle, reported to the window
// so the small update card can say what is happening.
type UpdatePhase struct {
	Phase   string `json:"phase"`
	Version string `json:"version"`
}

var (
	updatePhaseMu   sync.Mutex
	updatePhaseSink func(UpdatePhase)
)

// setUpdatePhaseSink installs the receiver for update phases; nil clears it.
func setUpdatePhaseSink(fn func(UpdatePhase)) {
	updatePhaseMu.Lock()
	updatePhaseSink = fn
	updatePhaseMu.Unlock()
}

func reportUpdatePhase(phase, version string) {
	updatePhaseMu.Lock()
	sink := updatePhaseSink
	updatePhaseMu.Unlock()
	if sink != nil {
		sink(UpdatePhase{Phase: phase, Version: version})
	}
}

// Version is the uploader's own version, injected at build time via
// -ldflags "-X main.Version=1.2.3". Anything other than a semver (the
// default "dev") disables self-update.
var Version = "dev"

// runAutoUpdate drives the update loop for the life of the process. On a
// successful swap it notifies the player in plain words, signals restartCh
// (main shuts everything down cleanly and relaunches), and returns.
func runAutoUpdate(ctx context.Context, stop context.CancelFunc, logger *log.Logger, notify Notifier, restartCh chan<- struct{}) {
	if !selfUpdateEnabled() {
		logger.Printf("update: %q build - self-update disabled", Version)
		return
	}
	selfupdate.SetLogger(logger)
	timer := time.NewTimer(updateCheckDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		v, err := applyUpdate(ctx)
		if err != nil {
			// Fail-open: one quiet line, retry on the next cycle. This
			// covers "repo not published yet" for as long as it takes.
			logger.Printf("update: check failed (will retry): %v", err)
		} else if v != "" {
			logger.Printf("update: replaced binary with version %s, restarting", v)
			reportUpdatePhase("restarting", v)
			time.Sleep(restartDelay)
			notify(logger, NoticeInfo, "StarLance App",
				"Updated itself to "+v+" and is restarting.")
			restartCh <- struct{}{}
			stop()
			return
		}
		timer.Reset(updateCheckInterval)
	}
}

// checkUpdateNow is the tray's "Check for update now" action: one on-demand
// check-and-swap cycle. Unlike the automatic loop, the player just clicked,
// so every outcome gets a quiet info toast - "nothing happened" would read
// as a dead button. A successful swap follows the exact same restart path
// as runAutoUpdate.
func checkUpdateNow(ctx context.Context, stop context.CancelFunc, logger *log.Logger, notify Notifier, restartCh chan<- struct{}) {
	if !selfUpdateEnabled() {
		notify(logger, NoticeInfo, "Updates",
			"This is a dev build (version \""+Version+"\"); release builds update themselves automatically.")
		return
	}
	v, err := applyUpdate(ctx)
	switch {
	case err != nil:
		// Fail-open, like the loop: say what happened, keep auto-retrying.
		logger.Printf("update: manual check failed: %v", err)
		notify(logger, NoticeInfo, "Could not check for updates",
			"The check failed right now ("+err.Error()+"). The uploader keeps checking automatically.")
	case v != "":
		logger.Printf("update: manual check installed version %s, restarting", v)
		reportUpdatePhase("restarting", v)
		time.Sleep(restartDelay)
		notify(logger, NoticeInfo, "StarLance App",
			"Updated itself to "+v+" and is restarting.")
		restartCh <- struct{}{}
		stop()
	default:
		notify(logger, NoticeInfo, "You're up to date",
			"The StarLance App is on the latest version ("+Version+").")
	}
}

// selfUpdateEnabled reports whether this binary may replace itself: only
// release builds stamped with a semver version qualify.
func selfUpdateEnabled() bool {
	_, err := semver.NewVersion(Version)
	return err == nil
}

// pinnedAssetPattern is the updater's asset filter: exactly the release zip
// for this platform, never the raw exe published beside it.
func pinnedAssetPattern(goos, goarch string) string {
	return fmt.Sprintf(`(-|_)%s(-|_)%s\.zip$`,
		regexp.QuoteMeta(goos), regexp.QuoteMeta(goarch))
}

// applyUpdate performs one full check-and-swap cycle. It returns the new
// version string when the binary on disk was replaced (the caller must
// restart), "" when no update was needed, and an error otherwise (caller
// retries later; the binary is untouched).
func applyUpdate(ctx context.Context) (string, error) {
	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		// Verify the downloaded archive against the release's
		// <asset>.zip.minisig before swapping the binary.
		Validator: minisignValidator{publicKey: minisignPublicKey},
		// Only the zip is updater payload; the raw Windows exe published
		// for the website must never be picked instead.
		Filters: []string{pinnedAssetPattern(runtime.GOOS, runtime.GOARCH)},
	})
	if err != nil {
		return "", err
	}
	repo := selfupdate.ParseSlug(updateRepoSlug)
	return applyUpdateWith(ctx, updater, repo)
}

//go:embed minisign.pub
var minisignPublicKey string

// minisignValidator checks an update asset against the <asset>.minisig
// signature published beside it. A release without a signature is rejected
// (go-selfupdate treats a missing validation asset as a failed check), so an
// attacker who can edit the release cannot install anything.
type minisignValidator struct{ publicKey string }

func (minisignValidator) GetValidationAssetName(releaseFilename string) string {
	return releaseFilename + ".minisig"
}

func (v minisignValidator) Validate(_ string, release, asset []byte) error {
	if len(asset) == 0 {
		return errors.New("release has no .minisig signature")
	}
	sig, err := minisign.DecodeSignature(string(asset))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	pub, err := minisign.DecodePublicKey(v.publicKey)
	if err != nil {
		return fmt.Errorf("decode embedded public key: %w", err)
	}
	ok, err := pub.Verify(release, sig)
	if err != nil {
		return fmt.Errorf("verify signature: %w", err)
	}
	if !ok {
		return errors.New("signature does not match the release asset")
	}
	return nil
}

// applyUpdateWith is applyUpdate with the updater and repository injected,
// so tests can drive it against a fake release source.
func applyUpdateWith(ctx context.Context, updater *selfupdate.Updater, repo selfupdate.Repository) (string, error) {
	reportUpdatePhase("checking", "")
	// The window stays hidden until this check answers, so it must answer
	// even on a network that never replies.
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	rel, found, err := updater.DetectLatest(checkCtx, repo)
	cancel()
	if err != nil {
		reportUpdatePhase("failed", "")
		return "", fmt.Errorf("detect latest release: %w", err)
	}
	if !found {
		reportUpdatePhase("failed", "")
		return "", errors.New("no release found for this platform (repo not published yet?)")
	}
	// Newer only: a rollback on the server must never downgrade players.
	if !rel.GreaterThan(Version) {
		reportUpdatePhase("uptodate", Version)
		return "", nil
	}
	exe, err := currentExecutable()
	if err != nil {
		reportUpdatePhase("failed", "")
		return "", err
	}
	// UpdateTo downloads + checksum-verifies the asset, decompresses the
	// zip, and swaps the binary in place (the library's .new/.old rename).
	reportUpdatePhase("downloading", rel.Version())
	if err := updater.UpdateTo(ctx, rel, exe); err != nil {
		reportUpdatePhase("failed", rel.Version())
		return "", fmt.Errorf("install version %s: %w", rel.Version(), err)
	}
	reportUpdatePhase("installed", rel.Version())
	return rel.Version(), nil
}

// currentExecutable resolves the real (symlink-free) path of the running
// binary so the swap lands on the actual file.
func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve running binary: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real, nil
	}
	return exe, nil
}

// removeOldBinary deletes the hidden ".<exe>.old" leftover of a previous
// self-update. The library cannot delete it on Windows while the old process
// is still running (it hides the file instead), so the new instance cleans
// it up here - called at startup, after the old process has exited. On Linux
// the library already deleted it. A failure just logs; the next start (or
// the next update) retries.
func removeOldBinary(exe string, logger *log.Logger) {
	if exe == "" {
		return
	}
	old := filepath.Join(filepath.Dir(exe), "."+filepath.Base(exe)+".old")
	if _, err := os.Stat(old); err != nil {
		return
	}
	if err := os.Remove(old); err != nil {
		logger.Printf("update: could not remove stale %s (will retry next start): %v", old, err)
		return
	}
	logger.Printf("update: removed stale %s", old)
}

// relaunchSelf starts the freshly installed binary as a detached process
// with -takeover-from=<current pid> plus the user's original command-line
// arguments, so the new instance waits for this process to exit before
// taking over. The caller must be on its way out (main's cleanup is done).
func relaunchSelf(logger *log.Logger, userArgs []string) error {
	exe, err := currentExecutable()
	if err != nil {
		return err
	}
	args := takeoverArgs(userArgs)
	if err := startDetached(exe, args); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	logger.Printf("update: relaunched %s (args preserved: %d)", exe, len(userArgs))
	return nil
}
