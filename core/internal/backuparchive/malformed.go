// file: core/internal/backuparchive/malformed.go
// version: 1.0.0
// guid: 7e7366af-5be4-4100-b16a-f16365faf7f8
// last-edited: 2026-11-12

package backuparchive

import (
	"archive/tar"
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"

	"github.com/klauspost/compress/zstd"
)

// IsMalformed reports whether err says the archive's bytes are not a
// well-formed stream of the codec and tar format they claim: a truncated or
// corrupt compressed stream, a bad tar header, an empty archive, or data after
// the archive's end.
//
// It is the one place that knows which values the codecs return for that, so a
// caller that must sort a failure (core/backup's verification reason codes)
// does not import the codecs. It answers only for the cases it names; anything
// else is false, and a caller then treats the failure as unclassified, never as
// well-formed.
func IsMalformed(err error) bool {
	if err == nil {
		return false
	}
	for _, target := range []error{
		ErrEmptyArchive, ErrTrailingData,
		tar.ErrHeader, io.ErrUnexpectedEOF,
		gzip.ErrHeader, gzip.ErrChecksum,
		zstd.ErrMagicMismatch, zstd.ErrCRCMismatch, zstd.ErrReservedBlockType,
		zstd.ErrCompressedSizeTooBig, zstd.ErrBlockTooSmall, zstd.ErrUnexpectedBlockSize,
		zstd.ErrFrameSizeMismatch, zstd.ErrWindowSizeTooSmall,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	var corrupt flate.CorruptInputError
	return errors.As(err, &corrupt)
}
