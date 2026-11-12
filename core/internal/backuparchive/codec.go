// file: core/internal/backuparchive/codec.go
// version: 1.0.0
// guid: bdd292e7-b0bd-4696-8a55-34c110675c5e
// last-edited: 2026-11-12

// Package backuparchive is the archive layer under core/backup (05-backup
// §5B.3.3, §5B.10): the codecs (gzip, zstd, none, sniffed by magic bytes), a
// hardened tar reader and extractor, and the reader of HEAD's `legacy-abo/0`
// archives (B7). It holds no core import, so it sits at the bottom of the
// layer table, and it never touches a live store: every extraction goes into a
// directory the caller made, and every read of an archive is bounded (entry
// count, each entry's size, the total) before any buffer is sized from a
// header.
//
// The codecs are ported from ABO's internal/backup/codec.go. The age layer
// lands with archive encryption (§5B.3.9, deferred-build).
package backuparchive

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Codec names accepted in core.backup.compression.
const (
	Gzip = "gzip"
	Zstd = "zstd"
	None = "none"
)

// LevelDefault means "whatever this codec considers its default".
//
// It is deliberately 0, and 0 deliberately does NOT mean gzip.NoCompression:
// a zero value that meant "off" would arm itself on upgrade for every config
// that never set the field (ABO lost chapter consolidation to exactly that).
// Someone who wants no compression asks for it by name, with None.
const LevelDefault = 0

// maxZstdWindow bounds the window a zstd stream may demand of the reader, so a
// hostile archive cannot ask for a gigabyte of decoder memory. The writer's
// own windows are far below it.
const maxZstdWindow = 128 << 20

// Codec is one compression format for backup archives. Only the compression
// layer varies; the archive inside is always tar.
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

// ErrUnknownCodec: a configured codec name is none of gzip, zstd, none.
var ErrUnknownCodec = errors.New("backuparchive: unknown compression")

// Resolve maps a configured name to a codec. The empty name is refused:
// core.backup.compression is an enum with a default, so an empty value is a
// caller's mistake, and a silent choice here would change what an upgrade
// writes (§5B.5 "Codec invalid: closed before any work").
func Resolve(name string) (Codec, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case Gzip, "gz":
		return gzipCodec{}, nil
	case Zstd, "zst":
		return zstdCodec{}, nil
	case None, "store", "uncompressed":
		return noneCodec{}, nil
	default:
		return nil, fmt.Errorf("%w %q (want %s, %s or %s)", ErrUnknownCodec, name, Gzip, Zstd, None)
	}
}

// Extensions are every suffix an archive can carry, so archives written under
// a since-changed setting stay listed and restorable.
func Extensions() []string { return []string{".tar.gz", ".tar.zst", ".tar"} }

// ValidateLevel rejects a level the codec cannot use, before anything
// destructive runs.
func ValidateLevel(c Codec, level int) error {
	if level == LevelDefault {
		return nil
	}
	if c.Name() == Gzip && (level < gzip.HuffmanOnly || level > gzip.BestCompression) {
		return fmt.Errorf("core.backup.compression_level %d is out of range for gzip (%d..%d)", level, gzip.HuffmanOnly, gzip.BestCompression)
	}
	return nil
}

// Sniff identifies a codec from an archive's leading bytes (at least 4 when
// the archive has them). The extension is a hint, never a fact: a renamed file
// or an archive copied in from another install must still restore.
func Sniff(magic []byte) Codec {
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		return gzipCodec{}
	case len(magic) >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		return zstdCodec{}
	default:
		// An uncompressed tar. Not an error: none is a supported setting.
		return noneCodec{}
	}
}

// safeCloser makes Close idempotent, so a deferred Close and an explicit one
// (whose error fails the backup) do not depend on each library's second-Close
// semantics.
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

type gzipCodec struct{}

func (gzipCodec) Name() string      { return Gzip }
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

type zstdCodec struct{}

func (zstdCodec) Name() string      { return Zstd }
func (zstdCodec) Extension() string { return ".tar.zst" }

// NewWriter maps a numeric level onto one of the four levels klauspost/compress
// implements (<3 fastest, 3..5 default, 6..9 better, >=10 best), which is not
// the zstd CLI's 1..19 scale; the default is "better", where the ratio is
// close to best and the time cost is not.
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
	zr, err := zstd.NewReader(r, zstd.WithDecoderMaxWindow(maxZstdWindow), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	return zr.IOReadCloser(), nil
}

type noneCodec struct{}

func (noneCodec) Name() string      { return None }
func (noneCodec) Extension() string { return ".tar" }

func (noneCodec) NewWriter(w io.Writer, _ int) (io.WriteCloser, error) {
	return &safeCloser{w: nopWriteCloser{w}}, nil
}

func (noneCodec) NewReader(r io.Reader) (io.ReadCloser, error) { return io.NopCloser(r), nil }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
