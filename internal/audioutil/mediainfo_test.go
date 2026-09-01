// file: internal/audioutil/mediainfo_test.go
// version: 1.0.0
// guid: 98daa3c4-1532-4a6d-870b-49e528645a14
// last-edited: 2026-08-31

package audioutil

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWAV builds a valid PCM WAV of exactly durSec seconds, in Go, with no
// external tool and no committed binary. Generating the fixture rather than
// checking one in matters here: this repo's CI does not fetch Git LFS, so a
// committed audio fixture arrives as a 129-byte pointer and every probe test
// would silently be measuring a text file.
func writeWAV(t *testing.T, dir string, durSec float64) string {
	t.Helper()
	const rate = 8000
	nSamples := int(math.Round(durSec * rate))
	dataLen := nSamples * 2 // 16-bit mono

	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+dataLen))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))    // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1))    // mono
	binary.Write(&b, binary.LittleEndian, uint32(rate)) // sample rate
	binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(dataLen))
	b.Write(make([]byte, dataLen))

	p := filepath.Join(dir, "fixture.wav")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatalf("write wav: %v", err)
	}
	return p
}

// THE test. mediainfo exits 0 with empty stdout for a file it cannot parse and
// for one that does not exist, so exit status carries no information. If this
// ever returns (0, nil), every unreadable file in the library is recorded as a
// successfully-probed zero-length one and the ffprobe fallback never runs.
func TestMediaInfoEmptyOutputIsAnErrorNotZero(t *testing.T) {
	if !MediaInfoAvailable() {
		t.Skip("mediainfo not installed")
	}
	bin, err := LookupMediaInfo()
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	dir := t.TempDir()

	notAudio := filepath.Join(dir, "notaudio.txt")
	if err := os.WriteFile(notAudio, []byte("this is not audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, path string }{
		{"non-media file", notAudio},
		{"missing file", filepath.Join(dir, "does-not-exist.m4b")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := probeDurationMediaInfo(context.Background(), bin, tc.path)
			if err == nil {
				t.Fatalf("expected an error, got duration %v with nil error — "+
					"an unreadable file must never look like a valid zero-length one", got)
			}
			if got != 0 {
				t.Fatalf("error path should return 0, got %v", got)
			}
		})
	}
}

func TestMediaInfoReadsRealDuration(t *testing.T) {
	if !MediaInfoAvailable() {
		t.Skip("mediainfo not installed")
	}
	bin, _ := LookupMediaInfo()
	p := writeWAV(t, t.TempDir(), 2.0)

	got, err := probeDurationMediaInfo(context.Background(), bin, p)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if math.Abs(got-2.0) > 0.05 {
		t.Fatalf("duration %v, want ~2.0", got)
	}
}

// mediainfo and ffprobe must agree, or swapping the primary silently changes
// every duration in the library.
func TestMediaInfoAndFFprobeAgree(t *testing.T) {
	if !MediaInfoAvailable() || !FFprobeAvailable() {
		t.Skip("need both mediainfo and ffprobe")
	}
	p := writeWAV(t, t.TempDir(), 3.5)
	mi, err := probeDurationMediaInfo(context.Background(), "", p)
	if err != nil {
		t.Fatalf("mediainfo: %v", err)
	}
	ff, err := probeDurationFFprobe(context.Background(), "", p)
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	if math.Abs(mi-ff) > 0.01 {
		t.Fatalf("probers disagree: mediainfo %v vs ffprobe %v", mi, ff)
	}
}

// ProbeDurationSeconds must still answer when mediainfo cannot.
func TestProbeDurationFallsBackToFFprobe(t *testing.T) {
	if !FFprobeAvailable() {
		t.Skip("ffprobe not installed")
	}
	t.Setenv(disableMediaInfoEnv, "1")
	if MediaInfoAvailable() {
		t.Fatal("kill switch did not disable mediainfo")
	}
	p := writeWAV(t, t.TempDir(), 1.5)
	got, err := ProbeDurationSeconds(context.Background(), "", p)
	if err != nil {
		t.Fatalf("probe with mediainfo disabled: %v", err)
	}
	if math.Abs(got-1.5) > 0.05 {
		t.Fatalf("duration %v, want ~1.5", got)
	}
}

func TestKillSwitchValues(t *testing.T) {
	if !MediaInfoAvailable() {
		t.Skip("mediainfo not installed")
	}
	for _, v := range []string{"1", "true", "yes"} {
		t.Run("disabled_"+v, func(t *testing.T) {
			t.Setenv(disableMediaInfoEnv, v)
			if MediaInfoAvailable() {
				t.Fatalf("%q should disable mediainfo", v)
			}
		})
	}
	for _, v := range []string{"", "0", "false", "FALSE"} {
		t.Run("enabled_"+v, func(t *testing.T) {
			t.Setenv(disableMediaInfoEnv, v)
			if !MediaInfoAvailable() {
				t.Fatalf("%q should leave mediainfo enabled", v)
			}
		})
	}
}

func TestDurationProbeAvailableAcceptsEitherTool(t *testing.T) {
	if !DurationProbeAvailable() {
		t.Skip("neither prober installed")
	}
	t.Setenv(disableMediaInfoEnv, "1")
	if FFprobeAvailable() && !DurationProbeAvailable() {
		t.Fatal("ffprobe alone must satisfy DurationProbeAvailable")
	}
}

// Proves the FALLBACK PATH runs, not merely that ffprobe works when mediainfo is
// absent. mediainfo is present and fails (empty output); ffprobe is then tried
// and fails too — but the error the caller sees must come from ffprobe. If the
// fallback were removed, this error would name mediainfo instead.
func TestProbeDurationSurfacesFFprobeErrorAfterMediaInfoFails(t *testing.T) {
	if !MediaInfoAvailable() || !FFprobeAvailable() {
		t.Skip("need both mediainfo and ffprobe")
	}
	dir := t.TempDir()
	notAudio := filepath.Join(dir, "notaudio.txt")
	if err := os.WriteFile(notAudio, []byte("this is not audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ProbeDurationSeconds(context.Background(), "", notAudio)
	if err == nil {
		t.Fatal("expected an error for a non-media file")
	}
	if !strings.Contains(err.Error(), "ffprobe") {
		t.Fatalf("error must come from the ffprobe fallback, got: %v", err)
	}
}
