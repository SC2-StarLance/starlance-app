package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture handle constants observed by decoding a real ladder replay with
// Blizzard's s2protocol. The checked-in fixture is synthesized from them (see
// fixture_test.go), so the probe still reproduces that reference output without
// shipping a real player's replay.
const (
	fixtureFirstHandle  = "73326d610000434e6de41503baccd05656360b6f027db88169fa1989bb6357b1b215a2547939f5fb"
	fixtureSecondHandle = "73326d610000434e421c8aa0f3619b652d23a2735dfee812ab644228235e7a797edecfe8b67da30e"
)

// TestReplayHandlesRealReplay pins the MPQ reader against the handle set of a
// real 5.0.15 LotV 1v1 (map plus six Blizzard base/balance dependencies, 7
// handles total), reproduced by the synthetic fixture. Every non-StarLance game
// looks like this, which is exactly why a "has mod handles" heuristic would be
// useless and only exact hash matching works.
func TestReplayHandlesRealReplay(t *testing.T) {
	h, err := replayHandles(fixtureReplayPath)
	if err != nil {
		t.Fatalf("replayHandles: %v", err)
	}
	if len(h) != 7 {
		t.Fatalf("expected 7 handles from the real replay, got %d: %v", len(h), h)
	}
	if h[0] != fixtureFirstHandle || h[1] != fixtureSecondHandle {
		t.Errorf("handles differ from the s2protocol-decoded reference:\n got %v\nwant [%s %s ...]", h, fixtureFirstHandle, fixtureSecondHandle)
	}
	for _, x := range h {
		if len(x) != handleLength*2 || strings.ToLower(x) != x {
			t.Errorf("handle %q is not 80-char lowercase hex", x)
		}
	}
}

// TestReplayHandlesGarbage: anything that is not an MPQ replay must be an
// error, never a partial or empty-but-successful result, so callers fall
// back to uploading.
func TestReplayHandlesGarbage(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"empty.sc2replay":      "",
		"tiny.sc2replay":       "hi",
		"notmpq.sc2replay":     "brand-new-replay-content-12345",
		"wrongmagic.sc2replay": "MPQ\x01garbagegarbagegarbagegarbagegarbage",
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := replayHandles(p); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestScanHandles: the schema-free blob scan must find framed handles
// regardless of the bloat bytes around them, deduplicate repeats, and ignore
// near-miss magic (shorter than a full handle at the end of the data).
func TestScanHandles(t *testing.T) {
	h1 := make([]byte, handleLength)
	copy(h1, handleMagic)
	for i := 4; i < handleLength; i++ {
		h1[i] = byte(i)
	}
	h2 := make([]byte, handleLength)
	copy(h2, handleMagic)
	for i := 4; i < handleLength; i++ {
		h2[i] = byte(200 - i)
	}
	data := []byte("\x02\x28framing")
	data = append(data, h1...)
	data = append(data, []byte("\x02\x28more-framing")...)
	data = append(data, h1...) // duplicate: deduplicated
	data = append(data, h2...)
	data = append(data, []byte("s2ma-truncated")...) // magic without a full handle

	got := scanHandles(data)
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct handles, got %d: %v", len(got), got)
	}
	want1 := hexHandle(h1)
	if got[0] != want1 {
		t.Errorf("first handle mismatch:\n got %s\nwant %s", got[0], want1)
	}
}

// TestScanHandlesEmpty: details without any handle yield an empty (not
// erroneous) result; the caller then treats the replay as unknown and
// uploads as before.
func TestScanHandlesEmpty(t *testing.T) {
	if got := scanHandles([]byte("no handles here at all")); len(got) != 0 {
		t.Errorf("expected no handles, got %v", got)
	}
}

// hexHandle is the lowercase hex encoding used for handle comparison.
func hexHandle(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0xF]
	}
	return string(out)
}

// --- Synthetic MPQ fixtures --------------------------------------------------

// mpqEncrypt is the inverse of mpqDecrypt, so synthetic archives can carry a
// plaintext table and still exercise the real decrypt path.
func mpqEncrypt(data []byte, key uint32) {
	seed1, seed2 := key, uint32(0xEEEEEEEE)
	for i := 0; i+4 <= len(data); i += 4 {
		seed2 += cryptTable[0x400+uint32(seed1&0xFF)]
		plain := binary.LittleEndian.Uint32(data[i:])
		val := plain ^ (seed1 + seed2)
		seed1 = (^seed1 << 0x15) + 0x11111111 | (seed1 >> 0x0B)
		seed2 = plain + seed2 + (seed2 << 5) + 3
		binary.LittleEndian.PutUint32(data[i:], val)
	}
}

// buildTestMPQ assembles a minimal user-data-wrapped MPQ archive holding one
// multi-sector, zlib-compressed "replay.details" of the given content.
// formatVersion and sectorShift are written independently so tests can pin
// which header field the reader honors (the shift lives at 0x0E, next to the
// version at 0x0C: mpyq's MPQFileHeader '<4s2I2H4I').
func buildTestMPQ(t *testing.T, formatVersion, sectorShift uint16, content []byte) []byte {
	t.Helper()
	data, err := buildMPQ(formatVersion, sectorShift, content)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func buildMPQ(formatVersion, sectorShift uint16, content []byte) ([]byte, error) {
	const headerOffset = 64
	sectorSize := 512 << sectorShift

	var raw bytes.Buffer
	var positions []uint32
	// Sector offsets are relative to the start of the archived file data,
	// which begins with the sector table itself (mpyq unpacks positions from
	// file_data and slices the same buffer).
	sectors := (len(content) + sectorSize - 1) / sectorSize
	tableBytes := uint32(4 * (sectors + 1))
	for off := 0; off < len(content); off += sectorSize {
		end := off + sectorSize
		if end > len(content) {
			end = len(content)
		}
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write(content[off:end]); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		positions = append(positions, tableBytes+uint32(raw.Len()))
		raw.WriteByte(2) // MPQ compression marker: zlib
		raw.Write(z.Bytes())
	}
	positions = append(positions, tableBytes+uint32(raw.Len()))

	fileData := make([]byte, 0, len(positions)*4+raw.Len())
	for _, p := range positions {
		fileData = binary.LittleEndian.AppendUint32(fileData, p)
	}
	fileData = append(fileData, raw.Bytes()...)

	// Layout: user-data header @0, archive header @64, hash table @96,
	// block table @112, file data @128. Table and block offsets in the
	// header are relative to the archive header.
	data := make([]byte, 128+len(fileData))
	copy(data[0:4], "MPQ\x1b")
	binary.LittleEndian.PutUint32(data[8:12], headerOffset)
	h := data[headerOffset:]
	copy(h[0:4], "MPQ\x1a")
	binary.LittleEndian.PutUint16(h[0x0C:], formatVersion)
	binary.LittleEndian.PutUint16(h[0x0E:], sectorShift)
	binary.LittleEndian.PutUint32(h[0x10:], 96-headerOffset)
	binary.LittleEndian.PutUint32(h[0x14:], 112-headerOffset)
	binary.LittleEndian.PutUint32(h[0x18:], 1)
	binary.LittleEndian.PutUint32(h[0x1C:], 1)

	hash := make([]byte, 16)
	binary.LittleEndian.PutUint32(hash[0:], mpqHash("replay.details", 1))
	binary.LittleEndian.PutUint32(hash[4:], mpqHash("replay.details", 2))
	binary.LittleEndian.PutUint32(hash[12:], 0) // block index 0
	mpqEncrypt(hash, mpqHash("(hash table)", 3))
	copy(data[96:], hash)

	block := make([]byte, 16)
	binary.LittleEndian.PutUint32(block[0:], 128-headerOffset) // relative to archive header
	binary.LittleEndian.PutUint32(block[4:], uint32(len(fileData)))
	binary.LittleEndian.PutUint32(block[8:], uint32(len(content)))
	binary.LittleEndian.PutUint32(block[12:], mpqFileExists|mpqFileCompress)
	mpqEncrypt(block, mpqHash("(block table)", 3))
	copy(data[112:], block)

	copy(data[128:], fileData)
	return data, nil
}

// TestReplayReadHonorsSectorShiftFieldNotFormatVersion pins the header field
// the reader uses as the sector-size shift. Reading 0x0C (formatVersion, here
// 4) instead of 0x0E (shift, here 3 -> 4096) would assume oversized sectors,
// under-count the sector table, and silently truncate the details at 8192
// bytes - losing every handle stored in the final sector.
func TestReplayReadHonorsSectorShiftFieldNotFormatVersion(t *testing.T) {
	content := make([]byte, 10240)
	handle := make([]byte, handleLength)
	copy(handle, handleMagic)
	copy(content[8192:], handle) // only in the third 4096-byte sector

	data := buildTestMPQ(t, 4, 3, content)
	p := filepath.Join(t.TempDir(), "synthetic.SC2Replay")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := replayHandles(p)
	if err != nil {
		t.Fatalf("replayHandles: %v", err)
	}
	if len(h) != 1 || h[0] != hexHandle(handle) {
		t.Fatalf("handles = %v, want exactly the synthetic handle", h)
	}
}

// TestReplayHandlesBlockOutOfRangeIsErrorNotPanic: the file-block range check
// must be measured from the archive header (headerOffset + offset + size),
// not from the start of the file. A block table that fits the file measured
// from offset 0 but overruns the archive is corrupt input: the reader must
// return an error, never a slice-bounds panic (when the overrun passes the
// buffer's capacity) and never a silent read of zero bytes past EOF (when it
// lands inside the cap slack os.ReadFile leaves beyond len).
func TestReplayHandlesBlockOutOfRangeIsErrorNotPanic(t *testing.T) {
	const headerOffset = 64
	// 600 bytes: os.ReadFile caps the buffer at size+1, so a block slice
	// reaching past 601 panics rather than quietly reading zeros.
	data := make([]byte, 600)
	copy(data[0:4], "MPQ\x1b")
	binary.LittleEndian.PutUint32(data[8:12], headerOffset)
	h := data[headerOffset:]
	copy(h[0:4], "MPQ\x1a")
	binary.LittleEndian.PutUint16(h[0x0E:], 3)
	binary.LittleEndian.PutUint32(h[0x10:], 96-headerOffset)
	binary.LittleEndian.PutUint32(h[0x14:], 112-headerOffset)
	binary.LittleEndian.PutUint32(h[0x18:], 1)
	binary.LittleEndian.PutUint32(h[0x1C:], 1)

	hash := make([]byte, 16)
	binary.LittleEndian.PutUint32(hash[0:], mpqHash("replay.details", 1))
	binary.LittleEndian.PutUint32(hash[4:], mpqHash("replay.details", 2))
	mpqEncrypt(hash, mpqHash("(hash table)", 3))
	copy(data[96:], hash)

	// Offset 560 + size 40 fits the 600-byte file measured from offset 0,
	// but not from the archive header at 64 (64+560+40 = 664 > 601).
	block := make([]byte, 16)
	binary.LittleEndian.PutUint32(block[0:], 560)
	binary.LittleEndian.PutUint32(block[4:], 40)
	binary.LittleEndian.PutUint32(block[8:], 40)
	binary.LittleEndian.PutUint32(block[12:], mpqFileExists|mpqFileSingleUnit)
	mpqEncrypt(block, mpqHash("(block table)", 3))
	copy(data[112:], block)

	p := filepath.Join(t.TempDir(), "truncated.SC2Replay")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := replayHandles(p); err == nil {
		t.Fatal("an out-of-range file block must be an error, never a decoded result")
	}
}
