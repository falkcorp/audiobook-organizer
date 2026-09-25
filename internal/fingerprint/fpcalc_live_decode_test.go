// file: internal/fingerprint/fpcalc_live_decode_test.go
// version: 1.0.0
// guid: 3f8a1c6e-94b2-4d7a-8e05-b6c2d9f14a73
// last-edited: 2026-09-25

package fingerprint

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// liveClip synthesises a 40 s MP3 (pink noise under a tremolo tone, so the
// print has both smooth and busy frames and the decompressor's 5-bit
// exceptional stream is exercised) and returns its path. It skips when
// ffmpeg or fpcalc is missing, and pins lookupFpcalc to PATH for the test.
// No recorded audio is involved: the clip is generated from lavfi sources.
func liveClip(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "fpcalc"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH — skipping live fpcalc decode test", bin)
		}
	}
	prev := resolvedFpcalcPath
	SetResolvedFpcalcPath("")
	t.Cleanup(func() { SetResolvedFpcalcPath(prev) })

	path := filepath.Join(t.TempDir(), "live.mp3")
	out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anoisesrc=color=pink:duration=40:amplitude=0.25:seed=7",
		"-f", "lavfi", "-i", "sine=frequency=330:duration=40",
		"-filter_complex", "[1:a]tremolo=f=3:d=0.8[t];[0:a][t]amix=inputs=2:duration=shortest",
		"-ac", "1", "-ar", "44100", "-c:a", "libmp3lame", "-b:a", "96k", path,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg synthesise clip: %v\n%s", err, out)
	}
	return path
}

// fpcalcRawFrames runs `fpcalc -raw -json -length <lengthSec>` on path: the
// ground-truth frames for the same window the production path analyses.
func fpcalcRawFrames(t *testing.T, path string, lengthSec int) []uint32 {
	t.Helper()
	out, err := exec.Command("fpcalc", "-raw", "-json", "-length", strconv.Itoa(lengthSec), path).Output()
	if err != nil {
		t.Fatalf("fpcalc -raw: %v", err)
	}
	var r struct {
		Fingerprint []uint32 `json:"fingerprint"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("parse fpcalc -raw: %v", err)
	}
	if len(r.Fingerprint) < MinUsefulFingerprintFrames {
		t.Fatalf("fpcalc -raw gave only %d frames", len(r.Fingerprint))
	}
	return r.Fingerprint
}

func assertFramesEqual(t *testing.T, got, want []uint32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("frame count: decoded %d, fpcalc -raw has %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("frame %d: decoded %#08x, fpcalc -raw has %#08x", i, got[i], want[i])
		}
	}
}

// TestLive_FileFingerprintLength_MatchesFpcalcRaw drives the production head
// print path that acoustid.backfill stores (FileFingerprintLength with the
// default 120 s window: fpcalc -json → decompress → LE frames) against the
// installed fpcalc's -raw output for the same file. The frozen golden
// fixtures prove the decoder against fpcalc 1.6.1; this catches a different
// fpcalc build or a change in the production invocation.
func TestLive_FileFingerprintLength_MatchesFpcalcRaw(t *testing.T) {
	path := liveClip(t)
	wf, err := FileFingerprintLength(path, DefaultAnalysisLengthSec)
	if err != nil {
		t.Fatalf("FileFingerprintLength: %v", err)
	}
	got := make([]uint32, wf.FrameCount())
	for i := range got {
		got[i] = binary.LittleEndian.Uint32(wf.Raw[i*4:])
	}
	assertFramesEqual(t, got, fpcalcRawFrames(t, path, DefaultAnalysisLengthSec))

	// The stored Seg0 (DeriveSeg0 of those frames) must decode to the same
	// frames too: it is what the fuzzy segment lookups compare.
	seg0, err := decodeAnyFingerprint(DeriveSeg0(wf.Raw))
	if err != nil {
		t.Fatalf("decode DeriveSeg0: %v", err)
	}
	assertFramesEqual(t, seg0, got)
}

// TestLive_FileHeadSegment_CompressedDecodesToFpcalcRaw covers the segment
// path, which stores fpcalc's COMPRESSED string verbatim. The header must
// carry a non-zero frame count (so the decompressor ran, not the app's
// uncompressed branch) and the decode must equal fpcalc -raw over the same
// SegmentSeconds window.
func TestLive_FileHeadSegment_CompressedDecodesToFpcalcRaw(t *testing.T) {
	path := liveClip(t)
	seg, err := FileHeadSegment(path)
	if err != nil {
		t.Fatalf("FileHeadSegment: %v", err)
	}
	hdr, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("head segment is not fpcalc's URL-safe base64: %v", err)
	}
	if len(hdr) < 4 || (int(hdr[1])<<16|int(hdr[2])<<8|int(hdr[3])) == 0 {
		t.Fatalf("head segment header %x: want the compressed form with a frame count", hdr[:min(4, len(hdr))])
	}
	got, err := decodeAnyFingerprint(seg)
	if err != nil {
		t.Fatalf("decode head segment: %v", err)
	}
	assertFramesEqual(t, got, fpcalcRawFrames(t, path, SegmentSeconds))
}
