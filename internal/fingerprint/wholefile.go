// file: internal/fingerprint/wholefile.go
// version: 1.1.0
// guid: c4d5e6f7-a8b9-4c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-06-15

package fingerprint

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrFingerprintTooShort is returned when fpcalc decoded a file but the
// resulting fingerprint has fewer than MinUsefulFingerprintFrames frames.
var ErrFingerprintTooShort = errors.New("fingerprint: extracted fingerprint is too short to be useful")

// WholeFile holds the result of a whole-file fingerprint extraction.
type WholeFile struct {
	// Raw is the chromaprint payload as a little-endian uint32 stream
	// (4 bytes per frame, 8 frames per second). The 4-byte chromaprint
	// header is NOT included — these are the comparison-ready frames.
	Raw []byte
	// DurationSec is the audio duration as fpcalc measured it while
	// decoding (which is more trustworthy than container metadata).
	DurationSec float64
}

// FrameCount returns the number of chromaprint frames in the fingerprint.
func (w *WholeFile) FrameCount() int {
	if w == nil {
		return 0
	}
	return len(w.Raw) / 4
}

// FileWholeFingerprint extracts a whole-file chromaprint for the audio file
// at path. Unlike FileSegments it does not seek into the file or cap the
// analysed length — fpcalc decodes from offset 0 to EOF, so this works on
// any playable file regardless of whether the container's duration metadata
// is correct.
//
// Returns ErrNotAvailable if fpcalc is not on PATH (this path intentionally
// does not fall back to the ffmpeg chromaprint muxer; the muxer's output
// alignment varies by ffmpeg version and the call sites that need whole-file
// fingerprints want a single canonical encoding).
//
// Returns ErrFingerprintTooShort if fpcalc produced fewer than
// MinUsefulFingerprintFrames frames (e.g. <10s of audio, or a corrupt file
// that decoded only its header).
func FileWholeFingerprint(path string) (*WholeFile, error) {
	fpcalc, err := lookupFpcalc()
	if err != nil {
		return nil, ErrNotAvailable
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(fpcalc, "-json", path)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("fpcalc %s: %s", path, msg)
	}

	var r Result
	if err := json.NewDecoder(&stdout).Decode(&r); err != nil {
		return nil, fmt.Errorf("fpcalc parse %s: %w", path, err)
	}
	if r.Fingerprint == "" {
		return nil, fmt.Errorf("fpcalc returned empty fingerprint for %s", path)
	}

	frames, err := decodeAnyFingerprint(r.Fingerprint)
	if err != nil {
		return nil, fmt.Errorf("fpcalc decode %s: %w", path, err)
	}
	if len(frames) < MinUsefulFingerprintFrames {
		return nil, fmt.Errorf("%w: %d frames (< %d)", ErrFingerprintTooShort, len(frames), MinUsefulFingerprintFrames)
	}

	raw := make([]byte, len(frames)*4)
	for i, f := range frames {
		binary.LittleEndian.PutUint32(raw[i*4:], f)
	}

	return &WholeFile{
		Raw:         raw,
		DurationSec: r.Duration,
	}, nil
}

// DeriveSeg0 returns the canonical base64 chromaprint for the first
// SegmentSeconds (5 minutes) of a whole-file raw fingerprint. Used to
// populate the legacy AcoustIDSeg0 field during the whole-file migration
// so callers still reading the segment fields keep working without a
// second fpcalc invocation.
func DeriveSeg0(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	maxFrames := SegmentSeconds * 8 // 8 fps
	maxBytes := maxFrames * 4
	slice := raw
	if len(slice) > maxBytes {
		slice = slice[:maxBytes]
	}
	return EncodeWholeFingerprint(slice)
}

// EdgeSkipFraction is the fraction trimmed from each end of a whole-file
// fingerprint before similarity comparison. Default 0.10 = skip first and
// last 10% of frames.
//
// Why: Audible (and many other publishers) prepend a near-identical
// intro/sting to every book, and append a similar outro. Comparing those
// shared sections as if they were content makes every Audible book
// partially match every other one. Trimming both ends of the fingerprint
// before comparison kills the false-positive baseline while keeping all
// extracted data on disk so we can refine the rule later without
// re-fingerprinting.
const EdgeSkipFraction = 0.10

// MinMiddleFrames is the smallest middle slice we'll compare. Below this
// the file is short enough that edge-trimming would leave nothing useful
// (e.g. a 30-second clip), so we fall back to whole-fingerprint compare.
const MinMiddleFrames = 240 // ≈30 seconds at 8 fps

// WholeFileSimilarity compares two whole-file fingerprints (raw LE uint32
// byte streams) using a middle slice of each. It trims EdgeSkipFraction
// from the head and tail of each fingerprint before Hamming-comparing the
// overlapping length. For very short fingerprints it compares the whole
// thing.
//
// Returns 0 and an error if either input is empty.
func WholeFileSimilarity(a, b []byte) (float64, error) {
	if len(a) == 0 || len(b) == 0 {
		return 0, errors.New("empty fingerprint")
	}
	if len(a)%4 != 0 || len(b)%4 != 0 {
		return 0, errors.New("fingerprint bytes not uint32-aligned")
	}
	sliceA := middleSliceFrames(a)
	sliceB := middleSliceFrames(b)
	n := len(sliceA)
	if len(sliceB) < n {
		n = len(sliceB)
	}
	if n == 0 {
		return 0, errors.New("middle slice is empty")
	}

	var matching, total uint32
	for i := 0; i < n; i += 4 {
		ai := binary.LittleEndian.Uint32(sliceA[i:])
		bi := binary.LittleEndian.Uint32(sliceB[i:])
		matching += 32 - popcount(ai^bi)
		total += 32
	}
	return float64(matching) / float64(total), nil
}

// middleSliceFrames returns the inner 1-2*EdgeSkipFraction of fp, aligned
// to uint32 boundaries. If fp is too short, returns fp unchanged.
func middleSliceFrames(fp []byte) []byte {
	frames := len(fp) / 4
	if frames < MinMiddleFrames {
		return fp
	}
	skip := int(float64(frames) * EdgeSkipFraction)
	if skip <= 0 {
		return fp
	}
	start := skip * 4
	end := (frames - skip) * 4
	if end-start < MinMiddleFrames*4 {
		return fp
	}
	return fp[start:end]
}


// canonical base64 chromaprint string (with the standard 4-byte
// version-1 header). Useful when interoperating with code paths that still
// expect the base64 form, including potential online AcoustID lookup.
func EncodeWholeFingerprint(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	header := []byte{0x01, 0x00, 0x00, 0x00}
	buf := make([]byte, 0, 4+len(raw))
	buf = append(buf, header...)
	buf = append(buf, raw...)
	return base64.StdEncoding.EncodeToString(buf)
}
