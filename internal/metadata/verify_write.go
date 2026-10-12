// file: internal/metadata/verify_write.go
// version: 1.1.0
// guid: 9d2f6c80-1b7e-4e53-a4c9-3f8b5a0d7e61
// last-edited: 2026-10-11
//
// verify_after_write for the metafetch write-back, whose tag write runs inside
// its own fileops.WriteTagsSafe rather than through tagger.WriteTagsSafe. It
// verifies through tagger's SafeWriteDeps.VerifyWritten, the code
// tagger.WriteTagsSafe and the native (cgo) writers in taglib_cgo.go use, so
// the setting behaves the same in every build variant.

package metadata

import (
	"fmt"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// WriteMetadataToFileAtomic writes metadata to filePath through
// fileops.WriteTagsSafe (temp copy, tag write, atomic rename; opts decides the
// hash recording and backup), then, when verify_after_write is on, reads the
// file back and fails with tagger.ErrVerifyAfterWrite unless the tags landed.
// The file stays as written on that error.
//
// It is WriteMetadataToFileInPlace inside the safe wrapper, for the callers
// that already resolved the protected-path question on filePath (the metafetch
// write-back). deps carries the book_file store the verification event is
// recorded through, and bookFileID (opts.BookFileID) the row it names.
//
// What is verified depends on which writer made the write. A write the native
// taglib writer made is checked against everything it was asked to write
// (buildWriteTagMap). If native TagLib failed and a CLI writer (AtomicParsley,
// eyeD3 or metaflac) wrote the file, it is checked against the tags that
// writer sets and TagLib reads back under a stable name (cliWriterTags): those
// tools write a different tag set (no AUDIOBOOK_ORGANIZER_* freeform tags from
// AtomicParsley, no LABEL or DESCRIPTION), so the native map is not what they
// were asked for. A CLI write that left such a tag missing fails verification
// like a native one.
func WriteMetadataToFileAtomic(filePath string, metadata map[string]any, config fileops.OperationConfig,
	opts fileops.WriteTagsSafeOptions, deps tagger.SafeWriteDeps) error {
	nativeWrote := false
	_, postHash, err := fileops.WriteTagsSafe(filePath, func(tmpPath string) error {
		var werr error
		nativeWrote, werr = writeMetadataToFileInPlace(tmpPath, metadata, config)
		return werr
	}, opts)
	if err != nil {
		return err
	}
	abs, aerr := filepath.Abs(filePath)
	if aerr != nil {
		abs = filePath
	}
	var want map[string][]string
	if nativeWrote {
		want = tagger.FoldTagKeys(buildWriteTagMap(metadata))
	} else {
		want = cliWriterTags(filePath, metadata)
	}
	if verr := deps.VerifyWritten(abs, want, opts.BookFileID, postHash); verr != nil {
		return fmt.Errorf("write metadata to %s: %w", filePath, verr)
	}
	return nil
}
