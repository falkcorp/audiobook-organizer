// file: internal/fileops/book_file_hash_recorder.go
// version: 1.0.0
// guid: 8e2d4b17-3c95-4f60-a1d8-6b7e9c2f0a43
// last-edited: 2026-09-13

package fileops

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
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
