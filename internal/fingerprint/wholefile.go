// file: internal/fingerprint/wholefile.go
// version: 1.2.0
// guid: c4d5e6f7-a8b9-4c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-09-12

package fingerprint

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ErrFingerprintTooShort is returned when fpcalc decoded a file but the
// resulting fingerprint has fewer than MinUsefulFingerprintFrames frames.
var ErrFingerprintTooShort = errors.New("fingerprint: extracted fingerprint is too short to be useful")

// DefaultAnalysisLengthSec is fpcalc's own default for -length: it analyses
// the first 120 seconds of audio and stops. Every fingerprint stored before
// 2026-09-12 was made with it (the app never passed -length), which the
// timing run of that date confirmed: a 21 h m4b and an 8 h mp3 both gave 948
// frames, about 120 s. FileWholeFingerprint passes it explicitly so the
// argument vector states what the stored data actually covers.
const DefaultAnalysisLengthSec = 120

// WholeFileAnalysisLength is the -length value that makes fpcalc decode the
// entire file. Measured on 2026-09-12 at roughly 80x the decode work of the
// default; choosing it for a library-wide run is an owner decision.
const WholeFileAnalysisLength = 0

// WholeFile holds the result of an fpcalc fingerprint extraction. Despite the
// name, Raw covers only the first AnalysisLengthSec seconds of the file unless
// AnalysisLengthSec is WholeFileAnalysisLength.
type WholeFile struct {
	// Raw is the chromaprint payload as a little-endian uint32 stream
	// (4 bytes per frame, about 8 frames per second). The 4-byte chromaprint
	// header is NOT included — these are the comparison-ready frames.
	Raw []byte
	// DurationSec is the duration fpcalc reports for the whole file. It is
	// NOT the length of the fingerprinted window: with the default
	// -length 120 a 10-hour file reports ~36000 here while Raw holds ~120 s.
	DurationSec float64
	// AnalysisLengthSec is the -length value passed to fpcalc (0 = whole file).
	AnalysisLengthSec int
}

// FrameCount returns the number of chromaprint frames in the fingerprint.
func (w *WholeFile) FrameCount() int {
	if w == nil {
		return 0
	}
	return len(w.Raw) / 4
}

// FileWholeFingerprint fingerprints the audio file at path with fpcalc's
// default analysis window: the FIRST DefaultAnalysisLengthSec (120) seconds,
// decoded from offset 0. It does not decode to EOF. The "whole file" in the
// name distinguishes it from FileSegments' seek-to-offset mode, nothing more;
// two copies of a book that differ only after minute two fingerprint
// identically. Use FileFingerprintLength to choose another window.
//
// Returns ErrNotAvailable if fpcalc is not on PATH (this path intentionally
// does not fall back to the ffmpeg chromaprint muxer; the muxer's output
// alignment varies by ffmpeg version and the call sites that need these
// fingerprints want a single canonical encoding).
//
// Returns ErrFingerprintTooShort if fpcalc produced fewer than
// MinUsefulFingerprintFrames frames (e.g. <10s of audio, or a corrupt file
// that decoded only its header).
func FileWholeFingerprint(path string) (*WholeFile, error) {
	return FileFingerprintLength(path, DefaultAnalysisLengthSec)
}

// WholeFileAvailable reports whether fpcalc can be found, i.e. whether
// FileWholeFingerprint can produce a raw print at all. Available() also counts
// the ffmpeg segment fallback, which cannot.
func WholeFileAvailable() bool {
	_, err := lookupFpcalc()
	return err == nil
}

// fpcalcArgs is the argument vector for one fingerprint. -length is always
// passed so the analysed window is explicit rather than an fpcalc default.
func fpcalcArgs(path string, lengthSec int) []string {
	return []string{"-json", "-length", strconv.Itoa(lengthSec), path}
}

// FileFingerprintLength is FileWholeFingerprint with an explicit fpcalc
// -length: lengthSec > 0 analyses the first lengthSec seconds, and
// WholeFileAnalysisLength (0) decodes the entire file. Negative is an error.
func FileFingerprintLength(path string, lengthSec int) (*WholeFile, error) {
	if lengthSec < 0 {
		return nil, fmt.Errorf("fingerprint: negative analysis length %d", lengthSec)
	}
	fpcalc, err := lookupFpcalc()
	if err != nil {
		return nil, ErrNotAvailable
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(fpcalc, fpcalcArgs(path, lengthSec)...)
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
		Raw:               raw,
		DurationSec:       r.Duration,
		AnalysisLengthSec: lengthSec,
	}, nil
}

// DeriveSeg0 returns the canonical base64 chromaprint for the first
// SegmentSeconds (5 minutes) of a raw fingerprint — which, at the default
// 120 s analysis window, is the whole of it. Used to
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
//
// Caveat: the trim is a fraction of the FINGERPRINT, not of the book. At the
// default 120 s analysis window it strips ~12 s from each end of the first two
// minutes, so the compared middle (~96 s) is still mostly that shared intro,
// and a book's outro is never in the fingerprint at all.
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
	n := min(len(sliceB), len(sliceA))
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
