// file: internal/fingerprint/fingerprint_decompress_test.go
// version: 1.0.0
// guid: feab9e1d-b0c5-4e5a-bad1-297a66a82688
// last-edited: 2026-09-19

package fingerprint

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// goldenClip is one entry of testdata/chromaprint_golden.json. Each entry was
// produced on 2026-09-19 with fpcalc 1.6.1 from a clip synthesised by ffmpeg
// (or macOS `say` for the speech clip):
//
//	compressed = fpcalc -json -length 120 <clip>        (what the app stores)
//	raw        = fpcalc -raw -json -length 120 <clip>   (the real frames)
//
// Clips: tone10 (10 s 440 Hz sine), noise30 (30 s pink noise), speech60 (60 s
// synthesised speech over faint brown noise), mix120 (120 s two-tone chord +
// white noise with tremolo), and speech60 re-encoded as 128 kb/s MP3 and
// 64 kb/s AAC. noise30 and the speech clips exercise the 5-bit exceptional
// stream; tone10 and mix120 do not, so both decoder branches are covered.
type goldenClip struct {
	Compressed string   `json:"compressed"`
	Raw        []uint32 `json:"raw"`
	Duration   float64  `json:"duration"`
}

func loadGolden(t *testing.T) map[string]goldenClip {
	t.Helper()
	data, err := os.ReadFile("testdata/chromaprint_golden.json")
	if err != nil {
		t.Fatalf("read golden fixtures: %v", err)
	}
	var clips map[string]goldenClip
	if err := json.Unmarshal(data, &clips); err != nil {
		t.Fatalf("parse golden fixtures: %v", err)
	}
	if len(clips) < 4 {
		t.Fatalf("expected at least 4 golden clips, got %d", len(clips))
	}
	return clips
}

// TestDecodeAnyFingerprint_CompressedMatchesRawGolden asserts that decoding
// fpcalc's compressed output yields exactly the frames fpcalc -raw prints for
// the same audio. Before the decompressor existed the compressed bitstream
// was read as little-endian uint32s, giving roughly a quarter as many
// "frames", none of them real.
func TestDecodeAnyFingerprint_CompressedMatchesRawGolden(t *testing.T) {
	for name, clip := range loadGolden(t) {
		t.Run(name, func(t *testing.T) {
			got, err := decodeAnyFingerprint(clip.Compressed)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got) != len(clip.Raw) {
				t.Fatalf("frame count: got %d, fpcalc -raw has %d", len(got), len(clip.Raw))
			}
			for i := range got {
				if got[i] != clip.Raw[i] {
					t.Fatalf("frame %d: got %#08x, fpcalc -raw has %#08x", i, got[i], clip.Raw[i])
				}
			}
		})
	}
}

// TestHammingSimilarity_TwoEncodesOfSameSourceMatch is the user-visible
// consequence: the same 60 s of speech encoded as 128 kb/s MP3 and as
// 64 kb/s AAC must score as the same recording. Pre-fix the score was about
// 0.52 (bitstream noise), below FuzzyMinSimilarity, so any re-encode of a
// book was invisible to fuzzy matching.
func TestHammingSimilarity_TwoEncodesOfSameSourceMatch(t *testing.T) {
	clips := loadGolden(t)
	mp3, aac := clips["speech_128k.mp3"], clips["speech_64k.m4a"]
	if mp3.Compressed == aac.Compressed {
		t.Fatal("fixture error: the two encodes produced byte-identical prints")
	}
	sim, err := HammingSimilarity(mp3.Compressed, aac.Compressed)
	if err != nil {
		t.Fatalf("HammingSimilarity: %v", err)
	}
	if sim < 0.95 {
		t.Fatalf("two encodes of one source scored %.4f, want >= 0.95", sim)
	}

	// And different content must stay well apart.
	other, err := HammingSimilarity(mp3.Compressed, clips["noise30.wav"].Compressed)
	if err != nil {
		t.Fatalf("HammingSimilarity (different content): %v", err)
	}
	if other >= FuzzyMinSimilarity {
		t.Fatalf("speech vs pink noise scored %.4f, want < %.2f", other, FuzzyMinSimilarity)
	}
}

// TestDecodeAnyFingerprint_EncodeWholeFingerprintRoundTrip covers the
// app's own uncompressed form: EncodeWholeFingerprint prepends the header
// {algorithm 1, frame count 0} to little-endian frames. DeriveSeg0 and the
// window pipeline's offset > 0 segments (fpcalc -raw) store this form, and
// it must decode to exactly the frames it was built from.
func TestDecodeAnyFingerprint_EncodeWholeFingerprintRoundTrip(t *testing.T) {
	frames := loadGolden(t)["mix120.wav"].Raw
	raw := make([]byte, len(frames)*4)
	for i, f := range frames {
		binary.LittleEndian.PutUint32(raw[i*4:], f)
	}
	enc := EncodeWholeFingerprint(raw)
	for name, fp := range map[string]string{
		"std":    enc,
		"rawurl": base64.RawURLEncoding.EncodeToString(mustStdDecode(t, enc)),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeAnyFingerprint(fp)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got) != len(frames) {
				t.Fatalf("frame count: got %d want %d", len(got), len(frames))
			}
			for i := range got {
				if got[i] != frames[i] {
					t.Fatalf("frame %d: got %#08x want %#08x", i, got[i], frames[i])
				}
			}
		})
	}
}

// TestDecodeAnyFingerprint_RejectsCorruptPayloads: a payload that is neither
// a complete compressed print nor an aligned uncompressed one must fail, not
// be truncated into plausible-looking frames.
func TestDecodeAnyFingerprint_RejectsCorruptPayloads(t *testing.T) {
	clip := loadGolden(t)["speech60.wav"]
	full := mustStdDecode(t, NormalizeFingerprint(clip.Compressed))

	cases := map[string][]byte{
		// Last byte lost: the exceptional stream is short.
		"compressed_truncated": full[:len(full)-1],
		// Half the normal stream gone: frame terminators run out.
		"compressed_half": full[:len(full)/2],
		// Trailing garbage after a complete print.
		"compressed_trailing_byte": append(append([]byte{}, full...), 0x00),
		// Header claims more frames than the stream holds.
		"compressed_count_too_high": append([]byte{full[0], 0x7f, 0xff, 0xff}, full[4:]...),
		// Uncompressed form whose body is not a multiple of 4 bytes.
		"uncompressed_misaligned": {0x01, 0x00, 0x00, 0x00, 0xaa, 0xbb, 0xcc, 0xdd, 0xee},
		// Unknown algorithm byte.
		"bad_algorithm": append([]byte{0xfb}, full[1:]...),
		// Shorter than a header.
		"short": {0x01, 0x00},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			fp := base64.RawURLEncoding.EncodeToString(b)
			if got, err := decodeAnyFingerprint(fp); err == nil {
				t.Fatalf("expected an error, decoded %d frames", len(got))
			}
		})
	}
}

// TestDecodeAnyFingerprint_EmptySentinel: fpcalc prints "AQAAAA" (algorithm
// 1, zero frames) when it gets too little audio. It decodes to zero frames,
// which IsUsefulFingerprint rejects.
func TestDecodeAnyFingerprint_EmptySentinel(t *testing.T) {
	got, err := decodeAnyFingerprint("AQAAAA")
	if err != nil {
		t.Fatalf("decode sentinel: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("sentinel decoded to %d frames, want 0", len(got))
	}
	if IsUsefulFingerprint("AQAAAA") {
		t.Fatal("sentinel reported as useful")
	}
}

func mustStdDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	return b
}
