// file: internal/metadata/verify_write.go
// version: 1.0.1
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
// Only a write the native taglib writer made is verified. If native TagLib
// failed and the AtomicParsley/ffmpeg fallback wrote the file, the result is
// not compared: those tools write a different tag set (no AUDIOBOOK_ORGANIZER_*
// freeform tags, for one), so the native writer's map is not what they were
// asked for.
func WriteMetadataToFileAtomic(filePath string, metadata map[string]any, config fileops.OperationConfig,
	opts fileops.WriteTagsSafeOptions, deps tagger.SafeWriteDeps) error {
	nativeWrote := false
	_, postHash, err := fileops.WriteTagsSafe(filePath, func(tmpPath string) error {
		var werr error
		nativeWrote, werr = writeMetadataToFileInPlace(tmpPath, metadata, config)
		return werr
	}, opts)
	if err != nil || !nativeWrote {
		return err
	}
	abs, aerr := filepath.Abs(filePath)
	if aerr != nil {
		abs = filePath
	}
	want := tagger.FoldTagKeys(buildWriteTagMap(metadata))
	if verr := deps.VerifyWritten(abs, want, opts.BookFileID, postHash); verr != nil {
		return fmt.Errorf("write metadata to %s: %w", filePath, verr)
	}
	return nil
}
