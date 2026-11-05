// file: internal/mediainfo/mediainfo.go
// version: 1.6.1
// guid: f1e2d3c4-b5a6-7c8d-9e0f-1a2b3c4d5e6f
// last-edited: 2026-11-05

package mediainfo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dhowden/tag"

	"github.com/falkcorp/audiobook-organizer/internal/audioext"
	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
)

// MediaInfo holds technical audio file information
type MediaInfo struct {
	Bitrate    int
	Codec      string
	SampleRate int
	Channels   int
	BitDepth   int
	Quality    string
	Format     string
	Duration   int
	// DurationEstimated is true when Duration was derived from fileSize ÷ bitrate
	// rather than read from the actual audio stream. Estimated durations are
	// unreliable (esp. for m4b/AAC where the assumed bitrate is a default) and
	// must NOT be trusted by dedup duration-matching or metadata scoring.
	DurationEstimated bool
	// DurationErr is why Duration is not a value read from the audio stream: nil
	// when it was, non-nil when the stream read failed, whether Duration is then
	// 0 or only an estimate (DurationEstimated). It keeps the underlying error
	// chain, so errors.Is(DurationErr, exec.ErrNotFound) means ffprobe is not
	// installed, errors.Is(DurationErr, fs.ErrNotExist) that the file is gone,
	// and errors.Is(DurationErr, context.DeadlineExceeded) that the probe timed
	// out. bookfileaudio classifies those; a missing tool or file is not a
	// verdict on the file. A MediaInfo built outside this package (a test
	// literal) leaves it nil.
	DurationErr error `json:"-"`
}

// ffprobeDurationTimeout bounds the per-file ffprobe call. ffprobe only reads the
// container header, so this is generous even for large audiobooks. A variable
// only so a test can exercise the timeout path.
var ffprobeDurationTimeout = 20 * time.Second

// realDurationSec reads the TRUE container duration via ffprobe (the stream's own
// duration, not a filesize estimate). It returns an error when ffprobe is
// missing or fails, the file is absent, the probe times out, or the file has no
// usable duration, so the caller can fall back to a flagged estimate AND record
// why. The error chain is the one audioutil.ProbeDurationSeconds returns (see
// MediaInfo.DurationErr for what can be asked of it), with fs.ErrNotExist added
// when the file is not there: ffprobe and mediainfo report a missing input as an
// ordinary failed run, indistinguishable from a corrupt file. ffprobe is
// resolved from PATH via the shared audioutil.ProbeDurationSeconds (also used by
// internal/fingerprint and internal/transcode — see TODO item 20 / AP-3b).
func realDurationSec(filePath string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ffprobeDurationTimeout)
	defer cancel()
	secs, err := audioutil.ProbeDurationSeconds(ctx, "", filePath)
	if err != nil {
		// Not after a timeout: a stalled mount is what timed out, the stat
		// would block on it just as ffprobe did, and a timeout is never
		// "absent".
		if ctx.Err() == nil {
			err = withStatCause(filePath, err)
		}
		return 0, err
	}
	// secs > 0 is false for NaN as well; a sub-half-second file rounds to 0.
	if !(secs > 0) || math.IsInf(secs, 0) || int(secs+0.5) <= 0 {
		return 0, errZeroDuration
	}
	return int(secs + 0.5), nil
}

// withStatCause adds to a failed probe of filePath what a stat of it says, when
// that says the failure was not the file's: ffprobe reports a missing input, an
// unreadable directory and a dead mount as the same ordinary failed run as a
// corrupt file, so the cause has to be asked of the filesystem. Wrapped so
// errors.Is sees it:
//
//   - fs.ErrNotExist: the file is gone;
//   - fs.ErrPermission: the file or a directory above it is not readable by this
//     process (a permissions problem to fix, not a bad file);
//   - syscall.ESTALE / syscall.ENOTCONN: a network mount went away.
//
// bookfileaudio.ClassifyProbeError treats all of them as "not the file's fault".
// Any other stat outcome (success included) leaves err as it was.
func withStatCause(filePath string, err error) error {
	_, serr := os.Stat(filePath)
	return statCause(serr, err)
}

// statCause is withStatCause's decision, on the outcome of the stat (a seam: the
// errnos of a dead mount cannot be provoked from a test).
func statCause(serr, err error) error {
	switch {
	case serr == nil:
		return err
	case errors.Is(serr, fs.ErrNotExist):
		return fmt.Errorf("%w: %w", fs.ErrNotExist, err)
	case errors.Is(serr, fs.ErrPermission), errors.Is(serr, syscall.ESTALE), errors.Is(serr, syscall.ENOTCONN):
		return fmt.Errorf("%w: %w", serr, err)
	}
	return err
}

// errZeroDuration is the error for a probe that ran and reported no usable
// length. Its text is stored as a book_file's DurationOK.LastError, so it is
// short and says what happened.
var errZeroDuration = errors.New("probe returned 0s")

// ErrUnsupportedFormat is returned (from Extract, or on DurationErr from
// BuildFromTag) for a path that is not an audio file the library knows: a
// directory, or an extension outside audioext.Default(). No subprocess is
// started for it. It is a statement about the path, not about a file the
// library expects to play, so bookfileaudio does not count it as an attempt.
// A KNOWN audio extension that ffprobe cannot read is an ordinary failed probe.
var ErrUnsupportedFormat = errors.New("unsupported format")

// Extract reads media information from an audio file.
// It opens the file, reads the tags, and calls BuildFromTag internally.
func Extract(filePath string) (*MediaInfo, error) {
	info := &MediaInfo{}
	ext := strings.ToLower(filepath.Ext(filePath))
	info.Format = strings.TrimPrefix(ext, ".")

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	// A directory is not a media file. Callers such as GetAudiobookTags hand
	// Extract a book's directory on every request; without this check the
	// ffprobe/mediainfo fallbacks below would run (mediainfo walks the whole
	// tree) just to produce a zero.
	fi, statErr := f.Stat()
	if statErr == nil && fi.IsDir() {
		return nil, fmt.Errorf("%w: %s is a directory", ErrUnsupportedFormat, filePath)
	}

	m, err := tag.ReadFrom(f)
	if err != nil {
		return inferFromFormat(filePath, info)
	}

	var fileSize int64
	if statErr == nil {
		fileSize = fi.Size()
	}

	return BuildFromTag(m, filePath, fileSize), nil
}

// BuildFromTag builds a MediaInfo struct from an already-parsed tag.Metadata.
// filePath is used for format inference; fileSize is used for duration estimation.
// The file is not re-opened. This is the shared builder used by ProcessFile (single-pass).
func BuildFromTag(m tag.Metadata, filePath string, fileSize int64) *MediaInfo {
	info := &MediaInfo{}
	ext := strings.ToLower(filepath.Ext(filePath))
	info.Format = strings.TrimPrefix(ext, ".")

	fileType := m.FileType()

	switch fileType {
	case tag.MP3:
		extractMP3Info(m, info)
	case tag.M4A, tag.M4B:
		extractM4AInfo(m, info)
	case tag.FLAC:
		extractFLACInfo(m, info)
	case tag.OGG:
		extractOGGInfo(m, info)
	default:
		// Fall back to format inference — inferFromFormat needs a file path but we
		// already know the size, so we call it and accept it may re-stat the file.
		result, err := inferFromFormat(filePath, info)
		if err != nil {
			// Best-effort struct rather than nil; the error stays visible on
			// DurationErr so the probe classifier sees "unsupported".
			info.DurationErr = err
			info.Quality = generateQualityString(info)
			return info
		}
		return result
	}

	// The tag library does not expose real duration, so read it from the audio
	// stream via ffprobe (accurate). Only if that fails do we fall back to a
	// fileSize ÷ bitrate ESTIMATE — which for m4b/AAC uses a default bitrate and
	// is routinely ~2× off — and we FLAG it so dedup/matching can distrust it.
	if info.Duration == 0 {
		if d, err := realDurationSec(filePath); err == nil {
			info.Duration = d
		} else {
			info.DurationErr = err
			if info.Bitrate > 0 && fileSize > 0 {
				bytesPerSec := (info.Bitrate * 1000) / 8
				if bytesPerSec > 0 {
					info.Duration = int(fileSize) / bytesPerSec
					info.DurationEstimated = true
				}
			}
		}
	}

	info.Quality = generateQualityString(info)
	return info
}

func extractMP3Info(m tag.Metadata, info *MediaInfo) {
	info.Codec = "MP3"
	raw := m.Raw()

	if bitrate, ok := raw["bitrate"]; ok {
		if br, ok := bitrate.(int); ok {
			info.Bitrate = br / 1000
		}
	}

	if info.Bitrate == 0 {
		info.Bitrate = 192
	}

	if sampleRate, ok := raw["sample_rate"]; ok {
		if sr, ok := sampleRate.(int); ok {
			info.SampleRate = sr
		}
	}
	if info.SampleRate == 0 {
		info.SampleRate = 44100
	}

	info.Channels = 2
}

func extractM4AInfo(m tag.Metadata, info *MediaInfo) {
	info.Codec = "AAC"
	raw := m.Raw()

	if bitrate, ok := raw["bitrate"]; ok {
		if br, ok := bitrate.(int); ok {
			info.Bitrate = br / 1000
		}
	}

	if info.Bitrate == 0 {
		info.Bitrate = 128
	}

	if sampleRate, ok := raw["sample_rate"]; ok {
		if sr, ok := sampleRate.(int); ok {
			info.SampleRate = sr
		}
	}
	if info.SampleRate == 0 {
		info.SampleRate = 44100
	}

	info.Channels = 2
}

func extractFLACInfo(m tag.Metadata, info *MediaInfo) {
	info.Codec = "FLAC"
	info.Channels = 2
	raw := m.Raw()

	if sampleRate, ok := raw["sample_rate"]; ok {
		if sr, ok := sampleRate.(int); ok {
			info.SampleRate = sr
		}
	}
	if info.SampleRate == 0 {
		info.SampleRate = 44100
	}

	if bitDepth, ok := raw["bits_per_sample"]; ok {
		if bd, ok := bitDepth.(int); ok {
			info.BitDepth = bd
		}
	}
	if info.BitDepth == 0 {
		info.BitDepth = 16
	}

	info.Bitrate = (info.SampleRate * info.BitDepth * info.Channels) / 1000
}

func extractOGGInfo(m tag.Metadata, info *MediaInfo) {
	info.Codec = "Vorbis"
	raw := m.Raw()

	if bitrate, ok := raw["bitrate"]; ok {
		if br, ok := bitrate.(int); ok {
			info.Bitrate = br / 1000
		}
	}

	if info.Bitrate == 0 {
		info.Bitrate = 160
	}

	if sampleRate, ok := raw["sample_rate"]; ok {
		if sr, ok := sampleRate.(int); ok {
			info.SampleRate = sr
		}
	}
	if info.SampleRate == 0 {
		info.SampleRate = 44100
	}

	info.Channels = 2
}

func estimateDurationFromFile(filePath string, bitrateKbps int) int {
	if bitrateKbps <= 0 {
		return 0
	}
	fi, err := os.Stat(filePath)
	if err != nil || fi.Size() == 0 {
		return 0
	}
	bytesPerSec := (bitrateKbps * 1000) / 8
	if bytesPerSec <= 0 {
		return 0
	}
	return int(fi.Size()) / bytesPerSec
}

func inferFromFormat(filePath string, info *MediaInfo) (*MediaInfo, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		info.Codec = "MP3"
		info.Bitrate = 192
		info.SampleRate = 44100
		info.Channels = 2
		info.Quality = "192kbps MP3"

	case ".m4a", ".m4b":
		info.Codec = "AAC"
		info.Bitrate = 128
		info.SampleRate = 44100
		info.Channels = 2
		info.Quality = "128kbps AAC"

	case ".flac":
		info.Codec = "FLAC"
		info.SampleRate = 44100
		info.BitDepth = 16
		info.Channels = 2
		info.Bitrate = (44100 * 16 * 2) / 1000
		info.Quality = "FLAC Lossless (16-bit/44.1kHz)"

	case ".ogg", ".oga":
		info.Codec = "Vorbis"
		info.Bitrate = 160
		info.SampleRate = 44100
		info.Channels = 2
		info.Quality = "160kbps Vorbis"

	default:
		// No codec table for this extension. An extension in audioext
		// (.wav, .opus, .aax, .wma, .aac, .mka ...) is usually a perfectly
		// readable container, so its duration comes from ffprobe as for any
		// other file; Codec stays empty and Bitrate 0, so there is no estimate
		// to fall back to. (This used to return "unsupported format" without
		// probing, which made every good file of these types fail its probe.)
		// Anything else (cover.jpg, notes.txt, a path with no extension) is not
		// audio: report ErrUnsupportedFormat WITHOUT starting a subprocess.
		if !audioext.DefaultSet()[ext] {
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedFormat, ext)
		}
	}

	// Real duration first (ffprobe); estimate only as a flagged fallback.
	if info.Duration == 0 {
		if d, err := realDurationSec(filePath); err == nil {
			info.Duration = d
		} else {
			info.DurationErr = err
			if info.Bitrate > 0 {
				info.Duration = estimateDurationFromFile(filePath, info.Bitrate)
				if info.Duration > 0 {
					info.DurationEstimated = true
				}
			}
		}
	}

	return info, nil
}

func generateQualityString(info *MediaInfo) string {
	if info.Codec == "FLAC" {
		sampleRateKHz := float64(info.SampleRate) / 1000.0
		return fmt.Sprintf("FLAC Lossless (%d-bit/%.1fkHz)", info.BitDepth, sampleRateKHz)
	}

	return fmt.Sprintf("%dkbps %s", info.Bitrate, info.Codec)
}
