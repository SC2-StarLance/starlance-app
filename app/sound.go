package main

import (
	"embed"
	"os"
	"path/filepath"
	"sync"
)

// The match cue is a CC0 chime (OpenGameArt "16-bit success sound"), shipped as
// PCM WAV so every platform's native player can take it. Playback lives in the
// app rather than the page because WebKit refuses media that no user gesture
// started, which made in-page audio useless for a match that lands while the
// player is looking elsewhere.
//
//go:embed assets/sounds/*.wav
var soundAssets embed.FS

// soundPaths maps a cue name to its embedded file. The file is deliberately not
// named after the cue so purging older audio from history cannot collide with a
// live asset.
var soundPaths = map[string]string{
	"match-found": "assets/sounds/match-cue.wav",
}

var soundOnce sync.Once
var soundDir string

// soundFile returns a filesystem path for one of the embedded cues, writing it
// into the user cache on first use. Players never see these files.
func soundFile(name string) string {
	soundOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil {
			return
		}
		soundDir = filepath.Join(base, "starlance", "sounds")
		_ = os.MkdirAll(soundDir, 0o755)
	})
	if soundDir == "" {
		return ""
	}
	// Always rewritten: the cue comes from the build, so a cached copy must
	// never pin an older asset after an update. One small write per match.
	dst := filepath.Join(soundDir, name+".wav")
	src, ok := soundPaths[name]
	if !ok {
		return ""
	}
	data, err := soundAssets.ReadFile(src)
	if err != nil {
		return ""
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return ""
	}
	return dst
}

// playSound starts a cue and returns immediately; sounds never block a queue
// action or a match notification.
func playSound(name string) {
	path := soundFile(name)
	if path == "" {
		return
	}
	playSoundFile(path)
}
