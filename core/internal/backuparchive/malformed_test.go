// file: core/internal/backuparchive/malformed_test.go
// version: 1.0.0
// guid: 7d69a1e0-7a36-4d9f-9871-76cb7222e514
// last-edited: 2026-11-12

package backuparchive

import (
	"archive/tar"
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestIsMalformed(t *testing.T) {
	malformed := map[string]error{
		"truncated stream":   io.ErrUnexpectedEOF,
		"bad tar header":     tar.ErrHeader,
		"gzip header":        gzip.ErrHeader,
		"gzip checksum":      gzip.ErrChecksum,
		"corrupt deflate":    flate.CorruptInputError(17),
		"zstd magic":         zstd.ErrMagicMismatch,
		"zstd crc":           zstd.ErrCRCMismatch,
		"zstd block":         zstd.ErrReservedBlockType,
		"zstd frame size":    zstd.ErrFrameSizeMismatch,
		"empty archive":      ErrEmptyArchive,
		"data after the end": ErrTrailingData,
	}
	for name, err := range malformed {
		if !IsMalformed(err) {
			t.Errorf("%s: IsMalformed(%v) = false", name, err)
		}
		// A cause survives the wrapping the readers and callers add.
		wrapped := fmt.Errorf("backuparchive: read the next entry: %w", fmt.Errorf("verify: %w", err))
		if !IsMalformed(wrapped) {
			t.Errorf("%s: a wrapped error is not recognised: %v", name, wrapped)
		}
		if !IsMalformed(errors.Join(errors.New("batcave: the alarm"), err)) {
			t.Errorf("%s: a joined error is not recognised", name)
		}
	}
	// What is not a stream defect is not one, whatever its text says: a limit, a
	// refused name, a file the OS would not give, the end of the stream.
	for name, err := range map[string]error{
		"nil":               nil,
		"limit":             ErrLimit,
		"entry name":        ErrEntryName,
		"entry type":        ErrEntryType,
		"duplicate":         ErrDuplicateEntry,
		"not regular":       ErrNotRegularFile,
		"manifest mismatch": ErrEntryMismatch,
		"permission":        os.ErrPermission,
		"plain EOF":         io.EOF,
		"text only":         errors.New("backuparchive: unexpected EOF in the archive"),
	} {
		if IsMalformed(err) {
			t.Errorf("%s: IsMalformed(%v) = true", name, err)
		}
	}
}
