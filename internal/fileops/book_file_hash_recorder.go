// file: internal/fileops/book_file_hash_recorder.go
// version: 1.1.0
// guid: 8e2d4b17-3c95-4f60-a1d8-6b7e9c2f0a43
// last-edited: 2026-09-13

package fileops

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// BookFileHashRecorder finds the book_file row that owns a path and records a
// write's hashes on it. It is what a byte-rewriting caller that knows only the
// file's path needs to keep the row's file_hash describing the bytes on disk.
type BookFileHashRecorder interface {
	database.BookFileHashUpdater
	GetBookFileByPath(filePath string) (*database.BookFile, error)
}

// HashOptionsForPath returns the WriteTagsSafe options that record a write's
// hashes on the book_file row at path, or zero options when there is no row.
//
// The row is the one the book_file_path index names. That index holds one
// reference per path, and GetBookFileByPath checks the row's FilePath against
// path, so a CRC collision reads as "no row" rather than as another file's
// row. It is also the index the scanner's upsert matches on, so the row whose
// hash is updated here is the row the next rescan compares against.
//
// A lookup error is logged at Warn and the write goes ahead without recording:
// the tag write is what the caller asked for, and a stale hash costs one
// re-read on the next scan, where the replaced-file probe keeps the audio data
// of a file whose length did not change.
func HashOptionsForPath(store BookFileHashRecorder, path string) WriteTagsSafeOptions {
	if store == nil || path == "" {
		return WriteTagsSafeOptions{}
	}
	row, err := store.GetBookFileByPath(path)
	if err != nil {
		logger.New("fileops").Warn("book_file lookup failed; this write will not update the row's hashes: path=%s error=%v",
			logger.SanitizeLogValue(path), err)
		return WriteTagsSafeOptions{}
	}
	if row == nil {
		return WriteTagsSafeOptions{}
	}
	return WriteTagsSafeOptions{BookFileID: row.ID, Store: store}
}

// RecordRewrite runs rewrite, which replaces the file at path by some means
// other than WriteTagsSafe (an ffmpeg remux or transcode, the CLI tag writer),
// and records the new bytes' hashes on the book_file row at path: the sampled
// digest before and after, and the whole-file SHA-256 after -- the three
// WriteTagsSafe records. With no store, or no row at path, it only runs
// rewrite. A hashing or recording failure is logged at Warn and not returned:
// the rewrite has already happened, and a stale hash costs one re-read on the
// next scan.
func RecordRewrite(store BookFileHashRecorder, path string, rewrite func() error) error {
	opts := HashOptionsForPath(store, path)
	if opts.BookFileID == "" || opts.Store == nil {
		return rewrite()
	}
	pre, err := filehash.BookFileHash(path)
	if err != nil {
		pre = ""
		logger.New("fileops").Warn("pre-rewrite identity hash not computed; original_file_hash left as is: path=%s error=%v",
			logger.SanitizeLogValue(path), err)
	}
	if err := rewrite(); err != nil {
		return err
	}
	_ = recordHashes(opts.Store, opts.BookFileID, pre, path)
	return nil
}

// RecordCurrentHashes records the hashes of the bytes now at path on book_file
// id: file_hash (the sampled digest) and post_metadata_hash (the whole-file
// SHA-256). original_file_hash is left as stored. It is for a write whose
// by-path lookup could not find the row because the row named another path at
// the time: the organizer tags the library copy of a protected source before
// it repoints the row to that copy. Failures are logged at Warn and returned.
func RecordCurrentHashes(store database.BookFileHashUpdater, id, path string) error {
	if store == nil || id == "" {
		return nil
	}
	return recordHashes(store, id, "", path)
}

// recordHashes hashes the bytes at path and records them on row id, with pre
// as the pre-write sampled digest ("" leaves original_file_hash as stored).
func recordHashes(store database.BookFileHashUpdater, id, pre, path string) error {
	log := logger.New("fileops")
	post, postErr := filehash.BookFileHash(path)
	if postErr != nil {
		post = ""
		log.Warn("identity hash not computed after a rewrite; file_hash left unchanged: path=%s error=%v",
			logger.SanitizeLogValue(path), postErr)
	}
	full, _, fullErr := ComputeFileHashAndSize(path)
	if fullErr != nil {
		full = ""
		log.Warn("SHA-256 not computed after a rewrite: path=%s error=%v", logger.SanitizeLogValue(path), fullErr)
	}
	if err := store.UpdateBookFileHashes(id, pre, full, post); err != nil {
		log.Warn("hash columns not updated after a rewrite; the file was written: book_file_id=%s path=%s error=%v",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(path), err)
		return err
	}
	return postErr
}
