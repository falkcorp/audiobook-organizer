// file: internal/backup/codec.go
// version: 1.0.0
// guid: 3b7c41d8-6e52-4a19-9f03-c8d5a2e71b64
// last-edited: 2026-08-29

package backup

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Compression algorithm names accepted in configuration. These are the values
// a user puts in `backup_compression`.
const (
	CompressionGzip = "gzip"
	CompressionZstd = "zstd"
	CompressionNone = "none"
)

// LevelDefault means "whatever this codec considers its default".
//
// It is deliberately 0, and 0 deliberately does NOT mean gzip.NoCompression.
//
// Configuration in this project is persisted as a full-struct marshal with no
// `omitempty`, so every field added here is written into every pre-existing
// config blob as its zero value. A field whose zero value means "off" is
// therefore a silent kill switch that arms itself on upgrade -- exactly how
// `chapter_consolidation_threshold_min = 0` disabled chapter consolidation for
// 12,525 books. Passing an unset level straight to gzip.NewWriterLevel would
// reproduce that failure precisely: level 0 is gzip.NoCompression, so upgrading
// would start writing ~15 GB uncompressed archives with no error and no log
// line. Someone who genuinely wants no compression asks for it by name, with
// CompressionNone.
const LevelDefault = 0

// Codec is one compression format for backup archives.
//
// Only the compression layer varies; the archive inside is always tar. That is
// what keeps a format switch cheap: the tar writer, the walk, the checksum and
// the retention logic are all format-agnostic.
type Codec interface {
	// Name is the configuration value that selects this codec.
	Name() string
	// Extension is the full archive suffix, including ".tar".
	Extension() string
	// NewWriter wraps w. level may be LevelDefault.
	NewWriter(w io.Writer, level int) (io.WriteCloser, error)
	// NewReader wraps r.
	NewReader(r io.Reader) (io.ReadCloser, error)
}

// ResolveCodec maps a configured name to a codec.
//
// The empty string resolves to gzip -- NOT to "none". Every archive written
// before this field existed is gzip, and every config blob written before this
// field existed will deserialize it as "". Both of those have to keep meaning
// gzip or the upgrade silently changes behaviour. See LevelDefault.
func ResolveCodec(name string) (Codec, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", CompressionGzip, "gz":
		return gzipCodec{}, nil
	case CompressionZstd, "zst":
		return zstdCodec{}, nil
	case CompressionNone, "store", "uncompressed":
		return noneCodec{}, nil
	default:
		return nil, fmt.Errorf("unknown backup compression %q (want %s, %s or %s)",
			name, CompressionGzip, CompressionZstd, CompressionNone)
	}
}

// SupportedCompression lists the accepted names, for validation messages and
// for the settings UI to render as choices.
func SupportedCompression() []string {
	return []string{CompressionGzip, CompressionZstd, CompressionNone}
}

// archiveExtensions are every suffix a backup archive can carry. ListBackups
// matches against all of them so archives written by an older build, or under a
// since-changed setting, stay visible and restorable.
func archiveExtensions() []string {
	return []string{".tar.gz", ".tar.zst", ".tar"}
}

// safeCloser makes Close idempotent.
//
// The archive path both defers a Close and calls it explicitly, because the
// explicit call is the one whose error is allowed to fail the backup while the
// deferred one covers the error paths that return early. gzip tolerates the
// double close; not every codec promises to. Rather than depend on each
// library's second-Close semantics, close once.
type safeCloser struct {
	w    io.WriteCloser
	once sync.Once
	err  error
}

func (s *safeCloser) Write(p []byte) (int, error) { return s.w.Write(p) }

func (s *safeCloser) Close() error {
	s.once.Do(func() { s.err = s.w.Close() })
	return s.err
}

// ---------- gzip ----------

type gzipCodec struct{}

func (gzipCodec) Name() string      { return CompressionGzip }
func (gzipCodec) Extension() string { return ".tar.gz" }

func (gzipCodec) NewWriter(w io.Writer, level int) (io.WriteCloser, error) {
	if level == LevelDefault {
		level = gzip.BestCompression
	}
	zw, err := gzip.NewWriterLevel(w, level)
	if err != nil {
		return nil, fmt.Errorf("gzip level %d: %w", level, err)
	}
	return &safeCloser{w: zw}, nil
}

func (gzipCodec) NewReader(r io.Reader) (io.ReadCloser, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	return zr, nil
}

// ---------- zstd ----------

type zstdCodec struct{}

func (zstdCodec) Name() string      { return CompressionZstd }
func (zstdCodec) Extension() string { return ".tar.zst" }

// NewWriter maps a numeric level onto one of the four levels this library
// actually implements.
//
// klauspost/compress does NOT have the zstd CLI's 1..19 scale. It has four
// levels, and EncoderLevelFromZstd buckets a CLI number into them:
//
//	<3 -> fastest, 3..5 -> default, 6..9 -> better, >=10 -> best
//
// This matters when transcribing a benchmark taken with the `zstd` binary: CLI
// -12 and CLI -19 land on the SAME level here, so a config copied from a CLI
// measurement that found -12 much faster than -19 would get none of that
// speedup. Levels 6..9 ("better") are the ones that behave like the CLI's
// mid-range. The default is "better" rather than "best" for that reason: it is
// the point on this library's curve where ratio is close to best and the time
// cost is not.
func (zstdCodec) NewWriter(w io.Writer, level int) (io.WriteCloser, error) {
	enc := zstd.SpeedBetterCompression
	if level != LevelDefault {
		enc = zstd.EncoderLevelFromZstd(level)
	}
	zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(enc))
	if err != nil {
		return nil, fmt.Errorf("zstd writer (level %s): %w", enc, err)
	}
	return &safeCloser{w: zw}, nil
}

func (zstdCodec) NewReader(r io.Reader) (io.ReadCloser, error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	return zr.IOReadCloser(), nil
}

// ---------- none ----------

type noneCodec struct{}

func (noneCodec) Name() string      { return CompressionNone }
func (noneCodec) Extension() string { return ".tar" }

func (noneCodec) NewWriter(w io.Writer, _ int) (io.WriteCloser, error) {
	return &safeCloser{w: nopWriteCloser{w}}, nil
}

func (noneCodec) NewReader(r io.Reader) (io.ReadCloser, error) {
	return io.NopCloser(r), nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// SniffCodec identifies an archive's compression from its leading magic bytes.
//
// The file extension is a hint, not a fact. Every archive written before this
// setting existed is gzip regardless of what it is named; a user can rename a
// file; an archive copied in from another install can carry any suffix. The
// magic bytes are what the decompressor actually acts on, so they are what
// decides. Restoring by extension instead would turn a renamed file into a
// confusing "gzip: invalid header" rather than a successful restore.
//
// r must be positioned at the start. SniffCodec restores that position before
// returning, so the caller can hand r straight to the codec's reader.
func SniffCodec(r io.ReadSeeker) (Codec, error) {
	var magic [4]byte
	n, err := io.ReadFull(r, magic[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read archive magic: %w", err)
	}
	if _, serr := r.Seek(0, io.SeekStart); serr != nil {
		return nil, fmt.Errorf("rewind archive: %w", serr)
	}
	switch {
	case n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		return gzipCodec{}, nil
	case n >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		return zstdCodec{}, nil
	default:
		// An uncompressed tar. Not an error: "none" is a supported setting.
		return noneCodec{}, nil
	}
}

// hasArchiveExtension reports whether name looks like a backup archive in any
// supported format.
func hasArchiveExtension(name string) bool {
	for _, ext := range archiveExtensions() {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}
