package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// fixtureReplayPath points at a synthetic replay built at test start. The
// fixture is generated instead of checked in so the repository carries no
// ladder replay recorded by real players.
var fixtureReplayPath string

// syntheticReplayHandles are the content handles the fixture's replay.details
// carries: the map plus six Blizzard base/balance dependencies, the shape of
// every non-StarLance ladder replay. The first three match the values the
// original s2protocol-decoded reference produced (the third is the bound map
// marker the fail-open tests use); the rest are synthesized with the same
// 4-byte magic and 36-byte content tail.
var syntheticReplayHandles = func() []string {
	real := []string{
		"73326d610000434e6de41503baccd05656360b6f027db88169fa1989bb6357b1b215a2547939f5fb",
		"73326d610000434e421c8aa0f3619b652d23a2735dfee812ab644228235e7a797edecfe8b67da30e",
		"73326d610000434e214fff52a60a3a36de01bce6a246a59509cb400bb6e8e95e831725e955c57429",
	}
	out := append([]string{}, real...)
	for i := len(real); i < 7; i++ {
		blob := make([]byte, handleLength)
		copy(blob, handleMagic)
		for j := 4; j < handleLength; j++ {
			blob[j] = byte(i*37 + j*11)
		}
		out = append(out, hex.EncodeToString(blob))
	}
	return out
}()

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "starlance-replay-fixture")
	if err != nil {
		panic(err)
	}
	content := make([]byte, 0, len(syntheticReplayHandles)*handleLength)
	for _, h := range syntheticReplayHandles {
		blob, err := hex.DecodeString(h)
		if err != nil || len(blob) != handleLength {
			panic("bad synthetic handle " + h)
		}
		content = append(content, blob...)
	}
	data, err := buildMPQ(1, 3, content)
	if err != nil {
		panic(err)
	}
	fixtureReplayPath = filepath.Join(dir, "synthetic-ladder.SC2Replay")
	if err := os.WriteFile(fixtureReplayPath, data, 0o600); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// TestSyntheticFixtureMatchesReference guards the fixture itself: the probe
// must read exactly the seven handles listed above, in order.
func TestSyntheticFixtureMatchesReference(t *testing.T) {
	h, err := replayHandles(fixtureReplayPath)
	if err != nil {
		t.Fatalf("replayHandles: %v", err)
	}
	if len(h) != len(syntheticReplayHandles) {
		t.Fatalf("expected %d handles, got %d: %v", len(syntheticReplayHandles), len(h), h)
	}
	for i := range h {
		if h[i] != syntheticReplayHandles[i] {
			t.Fatalf("handle %d = %s, want %s", i, h[i], syntheticReplayHandles[i])
		}
	}
}
