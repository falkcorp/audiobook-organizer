// file: internal/fingerprint/fpcalc.go
// version: 3.6.0
// guid: b1c2d3e4-f5a6-7b8c-9d0e-1f2a3b4c5d6e
// last-edited: 2026-09-19

// Package fingerprint generates AcoustID-compatible acoustic fingerprints for
// audio files. It supports two backends:
//
//   - fpcalc (preferred): the official Chromaprint CLI from the AcoustID project.
//     Produces standard AcoustID fingerprint strings. Install via
//     `apt install libchromaprint-tools` or `brew install chromaprint`.
//
//   - ffmpeg (fallback): when fpcalc is absent, uses the Chromaprint muxer built
//     into ffmpeg (`-f chromaprint -fp_format base64`). The ffmpeg on the
//     production server was compiled with --enable-chromaprint.
//
// The package generates 7 fingerprint segments per file:
//
//	[0] intro:    starting at offset 0
//	[1–5] body:  at offsets dur*1/6, *2/6, *3/6, *4/6, *5/6
//	[6] outro:   starting at max(0, dur–SegmentSeconds)
//
// Each segment covers SegmentSeconds (5 minutes) of audio. Together the 7
// segments provide enough coverage for confident content-based matching of
// long audiobooks that share the same narrator/production regardless of
// metadata changes, file moves, or container remux.
package fingerprint

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
)

// ErrNotAvailable is returned when neither fpcalc nor ffmpeg is on PATH.
var ErrNotAvailable = errors.New("fingerprint: neither fpcalc nor ffmpeg found — install chromaprint-tools or ffmpeg with chromaprint support")

// resolvedFpcalcPath is set by server init via SetResolvedFpcalcPath when a
// ToolRegistry provides the binary location. Empty = fall back to PATH lookup.
var resolvedFpcalcPath string

// SetResolvedFpcalcPath is called once during server startup with the path
// returned by ToolRegistry.Resolve("fpcalc"). An empty string resets to PATH lookup.
func SetResolvedFpcalcPath(path string) {
	resolvedFpcalcPath = path
}

// lookupFpcalc returns the path to fpcalc, preferring the ToolRegistry-injected
// path when set, then falling back to exec.LookPath. Returns ("", error) when
// fpcalc is not found by either means.
func lookupFpcalc() (string, error) {
	if resolvedFpcalcPath != "" {
		if _, err := os.Stat(resolvedFpcalcPath); err == nil {
			return resolvedFpcalcPath, nil
		}
	}
	return exec.LookPath("fpcalc")
}

// SegmentSeconds is the audio duration (in seconds) analysed per segment.
const SegmentSeconds = 300 // 5 minutes

// NumSegments is the number of fingerprint segments generated per file.
const NumSegments = 7

// FuzzyMinSimilarity is the default threshold for fuzzy Hamming matching.
// 0.80 means 80% of bits agree — tolerant of minor encoding differences
// while rejecting different recordings of the same title.
const FuzzyMinSimilarity = 0.80

// MinUsefulFingerprintFrames is the minimum number of decoded chromaprint
// frames (≈8 frames/sec) we accept before treating a fingerprint as real.
// fpcalc emits a header-only "AQAAAA" string when it's handed too little PCM
// to produce a hash (e.g. ffmpeg seeks past EOF because the container's
// reported duration is wrong). Storing those sentinels makes every other
// book with the same sentinel index-match at similarity 1.0. 80 frames ≈
// 10 seconds of real audio — well above any sentinel, well below any real
// 5-minute segment.
const MinUsefulFingerprintFrames = 80

// IsUsefulFingerprint reports whether fp decodes to at least
// MinUsefulFingerprintFrames real chromaprint frames. Empty / sentinel /
// truncated values return false.
func IsUsefulFingerprint(fp string) bool {
	if fp == "" {
		return false
	}
	ints, err := decodeAnyFingerprint(fp)
	if err != nil {
		return false
	}
	return len(ints) >= MinUsefulFingerprintFrames
}

// NormalizeForStorage canonicalizes fp for storage and drops degenerate
// outputs that would otherwise poison the AcoustID index. Use this at all
// write sites instead of plain NormalizeFingerprint.
func NormalizeForStorage(fp string) string {
	canonical := NormalizeFingerprint(fp)
	if !IsUsefulFingerprint(canonical) {
		return ""
	}
	return canonical
}

// Segments holds the 7 acoustic fingerprint strings for one audio file.
// [0]=intro, [1–5]=body at evenly-spaced offsets, [6]=outro.
// Unused segments are empty strings (e.g., when the file is too short).
type Segments [NumSegments]string

// Result holds the output of a single fingerprint run (backward compat).
type Result struct {
	// Duration is the audio duration in seconds.
	Duration float64 `json:"duration"`
	// Fingerprint is the AcoustID fingerprint string (first segment).
	Fingerprint string `json:"fingerprint"`
}

// Available reports whether any supported fingerprint backend is on PATH (or
// has been injected via SetResolvedFpcalcPath).
func Available() bool {
	if _, err := lookupFpcalc(); err == nil {
		return true
	}
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// File generates an AcoustID fingerprint for the first segment of an audio
// file (backward compatibility wrapper). Returns ErrNotAvailable if no
// backend is on PATH.
func File(path string) (*Result, error) {
	segs, err := FileSegments(path, 0)
	if err != nil {
		return nil, err
	}
	dur, err := probeDuration(path)
	if err != nil {
		dur = 0
	}
	return &Result{
		Duration:    dur,
		Fingerprint: segs[0],
	}, nil
}

// FileHeadSegment returns only segment [0] of FileSegments: SegmentSeconds
// from offset 0, via fpcalc reading the file directly when fpcalc is
// available, else the ffmpeg chromaprint muxer. It is byte-for-byte what
// FileSegments(path, n)[0] would be, without probing the duration or cutting
// the six later segments. Use it when only the head print is compared.
func FileHeadSegment(path string) (string, error) {
	if !Available() {
		return "", ErrNotAvailable
	}
	return fingerprintAt(path, 0)
}

// FileSegments generates all 7 acoustic fingerprint segments for the audio
// file at path. durationHint provides the file duration in seconds; pass 0
// to have it probed via ffprobe.
//
// Segment offsets:
//
//	[0]: 0
//	[1]: dur/6
//	[2]: dur*2/6
//	[3]: dur*3/6
//	[4]: dur*4/6
//	[5]: dur*5/6
//	[6]: max(0, dur-SegmentSeconds)
//
// Returns ErrNotAvailable if neither fpcalc nor ffmpeg is on PATH.
func FileSegments(path string, durationHint int) (*Segments, error) {
	if !Available() {
		return nil, ErrNotAvailable
	}

	dur := float64(durationHint)
	if dur <= 0 {
		probed, err := probeDuration(path)
		if err != nil || probed <= 0 {
			// Cannot determine duration — fingerprint offset 0 only.
			fp, ferr := fingerprintAt(path, 0)
			if ferr != nil {
				return nil, ferr
			}
			var segs Segments
			segs[0] = fp
			return &segs, nil
		}
		dur = probed
	}

	// Compute the 7 offsets.
	offsets := [NumSegments]float64{
		0,
		dur * 1 / 6,
		dur * 2 / 6,
		dur * 3 / 6,
		dur * 4 / 6,
		dur * 5 / 6,
		dur - SegmentSeconds,
	}
	if offsets[6] < 0 {
		offsets[6] = 0
	}

	var segs Segments
	for i, off := range offsets {
		fp, err := fingerprintAt(path, off)
		if err != nil {
			// Non-fatal: leave segment empty.
			continue
		}
		segs[i] = fp
	}
	return &segs, nil
}

// fingerprintAt generates a fingerprint string for SegmentSeconds of audio
// starting at the given offset (in seconds). It prefers fpcalc (resolved via
// ToolRegistry injection or PATH), falling back to ffmpeg -f chromaprint.
//
// With fpcalc, offset 0 is fpcalc reading the file directly (fpcalc's
// compressed base64 output, the same form every stored head print has), and
// offset > 0 is the window pipeline (ffmpeg PCM cut -> fpcalc -raw), returned
// as EncodeWholeFingerprint of the raw frames. The old fpcalcAt offset path
// was deleted on 2026-09-19: it gave headerless PCM to fpcalc with no
// -format/-rate/-channels, decoded `-raw -json`'s integer array into a string
// field (so every offset > 0 segment failed and was silently left empty), and
// discarded ffmpeg's exit status and stderr.
func fingerprintAt(path string, offset float64) (string, error) {
	fpcalc, err := lookupFpcalc()
	if err != nil {
		return ffmpegChromaprintAt(path, offset)
	}
	if offset <= 0 {
		return fpcalcHead(fpcalc, path)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("segment at %.2f needs ffmpeg: %w", offset, err)
	}
	return fpcalcSegmentAt(fpcalc, ffmpeg, path, offset)
}

// segmentTimeout bounds one legacy SegmentSeconds (300 s) segment.
const segmentTimeout = 2 * DefaultWindowTimeout

// fpcalcHead runs fpcalc on the file directly for the first SegmentSeconds.
func fpcalcHead(fpcalc, path string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(fpcalc, "-json", "-length", fmt.Sprintf("%d", SegmentSeconds), path)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("fpcalc %s: %s", path, msg)
	}
	var r Result
	if err := json.NewDecoder(&stdout).Decode(&r); err != nil {
		return "", fmt.Errorf("fpcalc parse %s: %w", path, err)
	}
	if r.Fingerprint == "" {
		return "", fmt.Errorf("fpcalc returned empty fingerprint for %s", path)
	}
	return r.Fingerprint, nil
}

// fpcalcSegmentAt fingerprints SegmentSeconds from offset through the window
// pipeline. Unlike FileWindow it accepts a segment that decodes short (the
// last segments of a file shorter than their offsets assume); callers already
// drop degenerate prints through NormalizeForStorage.
func fpcalcSegmentAt(fpcalc, ffmpeg, path string, offset float64) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), segmentTimeout)
	defer cancel()
	frames, _, err := runWindowPipe(ctx, ffmpeg, fpcalc,
		windowFFmpegArgs(path, offset, SegmentSeconds), windowFpcalcArgs(SegmentSeconds))
	if err != nil {
		return "", fmt.Errorf("segment %s@%.2f: %w", path, offset, err)
	}
	if len(frames) == 0 {
		return "", fmt.Errorf("segment %s@%.2f: empty fingerprint", path, offset)
	}
	raw := make([]byte, len(frames)*4)
	for i, f := range frames {
		binary.LittleEndian.PutUint32(raw[i*4:], f)
	}
	return EncodeWholeFingerprint(raw), nil
}

// ffmpegChromaprintAt runs ffmpeg with the chromaprint muxer to produce a
// base64-encoded fingerprint starting at offset for SegmentSeconds.
func ffmpegChromaprintAt(path string, offset float64) (string, error) {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-ss", fmt.Sprintf("%.2f", offset),
		"-i", path,
		"-t", fmt.Sprintf("%d", SegmentSeconds),
		"-f", "chromaprint",
		"-fp_format", "base64",
		"pipe:1",
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("ffmpeg chromaprint %s@%.2f: %s", path, offset, msg)
	}
	fp := strings.TrimSpace(stdout.String())
	if fp == "" {
		return "", fmt.Errorf("ffmpeg chromaprint returned empty output for %s", path)
	}
	return fp, nil
}

// probeDuration uses ffprobe to return the audio duration in seconds. This is
// a thin wrapper over the shared audioutil.ProbeDurationSeconds (also used by
// internal/mediainfo and internal/transcode — see TODO item 20 / AP-3b); it
// used to shell out to ffprobe independently via a JSON-parsing path, but that
// produced the same duration value as the shared plain-text probe, so the
// duplicate implementation was removed rather than kept as a second source of
// drift.
func probeDuration(path string) (float64, error) {
	return audioutil.ProbeDurationSeconds(context.Background(), "", path)
}

// HammingSimilarity returns the fraction of bits that agree between two
// acoustic fingerprint strings (0.0–1.0). It decodes both AcoustID base62
// fingerprints and standard base64 fingerprints (as produced by ffmpeg
// -fp_format base64).
//
// Returns 0 and an error if either fingerprint cannot be decoded.
func HammingSimilarity(a, b string) (float64, error) {
	intsA, err := decodeAnyFingerprint(a)
	if err != nil {
		return 0, fmt.Errorf("decode a: %w", err)
	}
	intsB, err := decodeAnyFingerprint(b)
	if err != nil {
		return 0, fmt.Errorf("decode b: %w", err)
	}

	n := min(len(intsB), len(intsA))
	if n == 0 {
		return 0, errors.New("empty fingerprint")
	}

	var matching, total uint32
	for i := range n {
		xor := intsA[i] ^ intsB[i]
		matching += 32 - popcount(xor)
		total += 32
	}
	return float64(matching) / float64(total), nil
}

// decodeAnyFingerprint decodes a fingerprint string into its uint32 array.
//
// Production data accumulated multiple ffmpeg/chromaprint output dialects:
// standard alphabet, URL-safe alphabet (`-`/`_`), with-padding, without-
// padding, and — critically — *wrong-length* padding. The previous
// implementation looped four base64 variants but each one is strict about
// padding length, so a URL-safe string with a single trailing `=` (when two
// were needed) failed all four and fell to `decodeBase62Fingerprint`, which
// rejects `-`/`_`. That produced the
// `invalid character '-' in fingerprint` warn-spam in prod.
//
// We now do a single tolerant pass: strip whitespace and any existing `=`
// padding, translate URL-safe chars to the standard alphabet, re-pad to a
// multiple of 4, and decode with `StdEncoding`. The base62 decoder is kept
// as a last-resort for legacy/external AcoustID fingerprints.
func decodeAnyFingerprint(fp string) ([]uint32, error) {
	// Strip whitespace + existing padding, normalize URL-safe alphabet.
	canonical := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t', '=':
			return -1
		case '-':
			return '+'
		case '_':
			return '/'
		}
		return r
	}, fp)
	if pad := len(canonical) % 4; pad > 0 {
		canonical += strings.Repeat("=", 4-pad)
	}

	if b, err := base64.StdEncoding.DecodeString(canonical); err == nil {
		// Chromaprint format: 4-byte header + uint32 little-endian values.
		if len(b) >= 8 && (len(b)-4)%4 == 0 {
			payload := b[4:]
			ints := make([]uint32, len(payload)/4)
			for i := range ints {
				ints[i] = binary.LittleEndian.Uint32(payload[i*4:])
			}
			return ints, nil
		}
		// Raw base64 without the 4-byte header (some ffmpeg versions).
		if len(b) > 0 && len(b)%4 == 0 {
			ints := make([]uint32, len(b)/4)
			for i := range ints {
				ints[i] = binary.LittleEndian.Uint32(b[i*4:])
			}
			return ints, nil
		}
		// Decoded fine but length is not aligned to a chromaprint payload.
		// Truncate the trailing 1–3 bytes that prevent alignment so we can
		// still produce a usable signature instead of failing the whole
		// book. (Most occurrences are off-by-one bytes from a write that
		// pre-dates the canonical-on-write change.)
		if len(b) >= 8 {
			cut := (len(b) - 4) & ^3 // largest multiple of 4 ≤ len-4
			payload := b[4 : 4+cut]
			ints := make([]uint32, len(payload)/4)
			for i := range ints {
				ints[i] = binary.LittleEndian.Uint32(payload[i*4:])
			}
			return ints, nil
		}
	}
	// Real decode failure — only call base62 if the input looks like base62
	// (alphanumeric only). Inputs containing '+', '/', '-', or '_' are
	// definitely base64; falling through to base62 produces misleading
	// "invalid character" errors.
	if !strings.ContainsAny(fp, "+/-_=") {
		return decodeBase62Fingerprint(fp)
	}
	return nil, fmt.Errorf("decode fingerprint: not a valid base64 chromaprint payload (len=%d)", len(fp))
}

// NormalizeFingerprint converts a fingerprint string into the canonical
// base64 form (standard alphabet, with `=` padding) that the rest of this
// codebase uses, without round-tripping through the decoded uint32 array
// (which would risk dropping or mis-attributing the chromaprint header
// byte).
//
// Used by the writer path so the database stops accumulating divergent
// encodings (URL-safe alphabet, missing or wrong-length padding). The
// reader (decodeAnyFingerprint) tolerates both forms, so existing rows are
// not affected.
func NormalizeFingerprint(fp string) string {
	if fp == "" {
		return ""
	}
	canonical := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t', '=':
			return -1
		case '-':
			return '+'
		case '_':
			return '/'
		}
		return r
	}, fp)
	if pad := len(canonical) % 4; pad > 0 {
		canonical += strings.Repeat("=", 4-pad)
	}
	// Validate by decoding; on failure keep the raw input so we never
	// destroy data that may still be readable by some other path.
	if _, err := base64.StdEncoding.DecodeString(canonical); err != nil {
		return fp
	}
	return canonical
}

// decodeBase62Fingerprint base62-decodes an AcoustID fingerprint string into
// its underlying uint32 array. AcoustID uses a custom base62 alphabet.
func decodeBase62Fingerprint(fp string) ([]uint32, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	lookup := [128]int{}
	for i := range lookup {
		lookup[i] = -1
	}
	for i, c := range alphabet {
		lookup[c] = i
	}

	var bits uint64
	numBits := 0
	var result []uint32

	for _, c := range fp {
		if int(c) >= 128 || lookup[c] < 0 {
			return nil, fmt.Errorf("invalid character %q in fingerprint", c)
		}
		bits |= uint64(lookup[c]) << numBits
		numBits += 6
		for numBits >= 32 {
			result = append(result, uint32(bits))
			bits >>= 32
			numBits -= 32
		}
	}
	return result, nil
}

// popcount counts the number of set bits in a uint32.
func popcount(x uint32) uint32 {
	x = x - ((x >> 1) & 0x55555555)
	x = (x & 0x33333333) + ((x >> 2) & 0x33333333)
	x = (x + (x >> 4)) & 0x0f0f0f0f
	return (x * 0x01010101) >> 24
}
