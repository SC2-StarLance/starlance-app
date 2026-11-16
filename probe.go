package main

// probe.go — build-independent StarLance replay pre-detection.
//
// A StarLance game must carry the StarLance mod, and every mod (and the map)
// appears in replay.details as a 40-byte CASC content handle whose first four
// bytes are "s2ma" (m_cacheHandles). The backend accepts a replay only when
// one of those handles matches an accepted mod hash, so their presence is a
// faithful client-side witness of "this could be a StarLance game".
//
// Deliberately NO SC2-versioned decoding happens here (see AGENTS.md: only
// Blizzard's s2protocol parses game data, and community decoders lag new
// builds). This file only decompresses replay.details out of the MPQ
// container — a format unchanged since SC2 launched — and scans for the
// fixed-size "s2ma" blobs, so there is nothing to drift per patch. Any
// decode trouble is an error and the caller uploads as before: the probe can
// only save a roundtrip, never block a legitimate upload.

import (
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// MPQ block flags (subset; see the MPQ format spec / mpyq).
const (
	mpqFileCompress   = 0x00000200
	mpqFileEncrypted  = 0x00010000
	mpqFileSingleUnit = 0x01000000
	mpqFileSectorCRC  = 0x04000000
	mpqFileExists     = 0x80000000
)

// handleMagic is the first four bytes of a CASC content handle inside
// replay.details ("s2ma" = StarCraft II map/archive digest). Handles are
// exactly 40 bytes: the magic plus a 36-byte digest.
const (
	handleMagic  = "s2ma"
	handleLength = 40
)

// replayHandles returns the lowercase hex of every distinct 40-byte "s2ma"
// handle found in the replay's details record, in file order. An error means
// the file could not be read as an SC2 replay MPQ (or a format the minimal
// reader does not support); callers must treat an error as "unknown" and
// upload as before.
func replayHandles(path string) ([]string, error) {
	if err := checkReplaySize(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	details, err := mpqReadFile(data, "replay.details")
	if err != nil {
		return nil, err
	}
	return scanHandles(details), nil
}

// maxReplayBytes bounds the in-memory read of one replay. Real ladder replays
// are well under a megabyte, so anything past this is not a replay and must not
// be pulled into RAM by the probe or the upload form.
const maxReplayBytes = 64 << 20

// checkReplaySize rejects a file too large to be a replay. Callers treat the
// error as "do not touch this file".
func checkReplaySize(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > maxReplayBytes {
		return fmt.Errorf("%d bytes is larger than any replay (%d byte limit)", info.Size(), maxReplayBytes)
	}
	return nil
}

// scanHandles extracts distinct 40-byte "s2ma" blobs from raw (decompressed)
// details bytes. It is schema-free: the bloat framing around each blob (type
// tag, length prefix) is irrelevant, and a stray "s2ma" inside some other
// string can only ever add a bogus handle — which can only cause "maybe
// StarLance" (upload anyway), never a false skip.
func scanHandles(data []byte) []string {
	var out []string
	seen := map[string]struct{}{}
	for at := 0; ; {
		idx := bytes.Index(data[at:], []byte(handleMagic))
		if idx < 0 {
			break
		}
		at += idx
		if at+handleLength > len(data) {
			break
		}
		blob := data[at : at+handleLength]
		h := hex.EncodeToString(blob)
		if _, ok := seen[h]; !ok {
			seen[h] = struct{}{}
			out = append(out, h)
		}
		at += handleLength
	}
	return out
}

// --- Minimal MPQ reader -----------------------------------------------------
//
// A faithful Go port of the replay-only path of mpyq (Blizzard MPQ archives):
// locate the archive header (replays start with a user-data header), decrypt
// the hash/block tables with MPQ's crypt algorithm, find the requested file,
// and reassemble + decompress its sectors (zlib or bzip2, as flagged).

var cryptTable [0x500]uint32

func init() {
	seed := uint32(0x00100001)
	for i := 0; i < 256; i++ {
		for j := 0; j < 5; j++ {
			seed = (seed*125 + 3) % 0x2AAAAB
			temp1 := (seed & 0xFFFF) << 0x10
			seed = (seed*125 + 3) % 0x2AAAAB
			// The classic MPQ crypt table is laid out column-major: the
			// j-th pass of entry i lands at i + j*0x100.
			cryptTable[i+0x100*j] = temp1 | (seed & 0xFFFF)
		}
	}
}

// mpqHash is MPQ's string hash (mpyq _hash). hashType: 0 table offset,
// 1 hash-a, 2 hash-b, 3 table key.
func mpqHash(s string, hashType uint32) uint32 {
	seed1 := uint32(0x7FED7FED)
	seed2 := uint32(0xEEEEEEEE)
	for _, ch := range []byte(strings.ToUpper(s)) {
		value := cryptTable[(hashType<<8)+uint32(ch)]
		seed1 = value ^ (seed1 + seed2)
		seed2 = uint32(ch) + seed1 + seed2 + (seed2 << 5) + 3
	}
	return seed1
}

// mpqDecrypt decrypts hash/block tables and encrypted sectors in place
// (mpyq _decrypt).
func mpqDecrypt(data []byte, key uint32) {
	seed1 := key
	seed2 := uint32(0xEEEEEEEE)
	for i := 0; i+4 <= len(data); i += 4 {
		seed2 += cryptTable[0x400+uint32(seed1&0xFF)]
		value := binary.LittleEndian.Uint32(data[i:]) ^ (seed1 + seed2)
		seed1 = (^seed1 << 0x15) + 0x11111111 | (seed1 >> 0x0B)
		seed2 = value + seed2 + (seed2 << 5) + 3
		binary.LittleEndian.PutUint32(data[i:], value)
	}
}

// mpqReadFile locates and reassembles one file from an in-memory MPQ archive
// (the .SC2Replay container). It supports the shapes SC2 replays actually
// use: multi-sector files compressed with zlib or bzip2. Anything else
// (encrypted or single-unit blocks, unknown compression) is an error — the
// caller uploads as before.
func mpqReadFile(data []byte, name string) ([]byte, error) {
	if len(data) < 32 {
		return nil, errors.New("probe: file too small for an MPQ archive")
	}

	headerOffset := 0
	switch {
	case string(data[:4]) == "MPQ\x1a":
		// Bare archive header at offset 0.
	case string(data[:4]) == "MPQ\x1b":
		// User-data header wraps the archive header (SC2 replays).
		if len(data) < 16 {
			return nil, errors.New("probe: truncated user-data header")
		}
		headerOffset = int(binary.LittleEndian.Uint32(data[8:]))
		if headerOffset <= 0 || headerOffset+32 > len(data) {
			return nil, errors.New("probe: user-data header offset out of range")
		}
	default:
		return nil, errors.New("probe: not an MPQ archive")
	}
	h := data[headerOffset:]
	if len(h) < 32 || string(h[:4]) != "MPQ\x1a" {
		return nil, errors.New("probe: missing MPQ archive header")
	}
	// <4s 2I 2H 4I>: magic, headerSize, archiveSize, formatVersion,
	// sectorSizeShift, hashTableOffset, blockTableOffset, hashEntries, blockEntries.
	hashTableOffset := int(binary.LittleEndian.Uint32(h[0x10:]))
	blockTableOffset := int(binary.LittleEndian.Uint32(h[0x14:]))
	hashEntries := int(binary.LittleEndian.Uint32(h[0x18:]))
	blockEntries := int(binary.LittleEndian.Uint32(h[0x1C:]))
	// sectorSizeShift is the SECOND uint16 (0x0E), next to formatVersion
	// (0x0C): mpyq's MPQFileHeader '<4s2I2H4I'. Reading 0x0C would use the
	// format version as the shift, which only ever worked by luck (an
	// assumed-too-small sector size degrades harmlessly thanks to the
	// left>0 guard in the sector loop).
	sectorSizeShift := int(binary.LittleEndian.Uint16(h[0x0E:]))
	if hashEntries <= 0 || blockEntries <= 0 ||
		hashEntries > 1<<20 || blockEntries > 1<<20 {
		return nil, errors.New("probe: implausible MPQ table size")
	}

	readTable := func(offset, entries int, key string) ([]byte, error) {
		start := headerOffset + offset
		end := start + entries*16
		if start < 0 || end > len(data) || end < start {
			return nil, errors.New("probe: MPQ table out of range")
		}
		t := make([]byte, entries*16)
		copy(t, data[start:end])
		mpqDecrypt(t, mpqHash(key, 3))
		return t, nil
	}
	hashTable, err := readTable(hashTableOffset, hashEntries, "(hash table)")
	if err != nil {
		return nil, err
	}
	blockTable, err := readTable(blockTableOffset, blockEntries, "(block table)")
	if err != nil {
		return nil, err
	}

	// Find the hash entry for name (hash-a/hash-b match, mpyq style).
	hashA := mpqHash(name, 1)
	hashB := mpqHash(name, 2)
	var blockIndex = -1
	for i := 0; i < hashEntries; i++ {
		e := hashTable[i*16:]
		if binary.LittleEndian.Uint32(e[0:]) == hashA &&
			binary.LittleEndian.Uint32(e[4:]) == hashB {
			blockIndex = int(binary.LittleEndian.Uint32(e[12:]))
			break
		}
	}
	if blockIndex < 0 || blockIndex >= blockEntries {
		return nil, errors.New("probe: file not present in MPQ hash table")
	}

	b := blockTable[blockIndex*16:]
	offset := int(binary.LittleEndian.Uint32(b[0:]))
	archivedSize := int(binary.LittleEndian.Uint32(b[4:]))
	size := int(binary.LittleEndian.Uint32(b[8:]))
	flags := binary.LittleEndian.Uint32(b[12:])
	if flags&mpqFileExists == 0 || archivedSize == 0 {
		return nil, errors.New("probe: file block not archived")
	}
	if flags&mpqFileEncrypted != 0 {
		return nil, errors.New("probe: encrypted MPQ blocks unsupported")
	}
	// The block offset is relative to the archive header, so the range check
	// must account for headerOffset: a truncated or hostile file whose block
	// table points near the end of the file must yield an error, never a
	// slice-bounds panic.
	if headerOffset+offset+archivedSize > len(data) {
		return nil, errors.New("probe: file block out of range")
	}
	raw := data[headerOffset+offset : headerOffset+offset+archivedSize]

	if flags&mpqFileSingleUnit != 0 {
		// Single-unit blocks are only decompressed when compression actually
		// gained bytes (like mpyq: size > archivedSize).
		if flags&mpqFileCompress != 0 && size > archivedSize {
			return mpqDecompressBlock(raw, true)
		}
		return raw, nil
	}

	// Multi-sector file: sector table first, then each sector.
	sectorSize := 512 << sectorSizeShift
	sectors := size / sectorSize
	if size%sectorSize != 0 {
		sectors++
	}
	tableLen := sectors + 1
	if flags&mpqFileSectorCRC != 0 {
		tableLen++
	}
	if tableLen*4 > len(raw) {
		return nil, errors.New("probe: sector table out of range")
	}
	positions := make([]int, tableLen)
	for i := range positions {
		positions[i] = int(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	var out []byte
	left := size
	for i := 0; i < len(positions)-1 && left > 0; i++ {
		start, end := positions[i], positions[i+1]
		if start < 0 || end < start || end > len(raw) {
			return nil, errors.New("probe: sector offsets out of range")
		}
		sector := raw[start:end]
		// Like mpyq: a sector flagged compressed but as long as its plain
		// size is stored uncompressed.
		if flags&mpqFileCompress != 0 && left > len(sector) {
			sector, err = mpqDecompressBlock(sector, true)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, sector...)
		left -= len(sector)
	}
	return out, nil
}

// mpqDecompressBlock decompresses one MPQ compressed block: a leading
// compression-type byte (0 = stored, 2 = zlib, 16 = bzip2), then the payload.
func mpqDecompressBlock(data []byte, compressed bool) ([]byte, error) {
	if !compressed || len(data) == 0 || data[0] == 0 {
		return data, nil
	}
	payload := data[1:]
	var r io.Reader
	switch data[0] {
	case 2:
		zr, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	case 16:
		r = bzip2.NewReader(bytes.NewReader(payload))
	default:
		return nil, errors.New("probe: unsupported MPQ compression type")
	}
	out, err := io.ReadAll(io.LimitReader(r, 1<<24))
	if err != nil {
		return nil, err
	}
	return out, nil
}
