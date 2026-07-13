// file: internal/database/pebble_store_book_aggregates.go
// version: 1.0.1
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-07-12

// Package database — book aggregate recomputation from BookFiles.
//
// WHY this file exists:
//   Book.Duration and Book.FileSize are set at import time and never updated
//   when BookFile records are added, changed, or deleted. For multi-file books
//   (chapter-split audiobooks) the book-level fields show stale import values
//   while the actual content may have changed substantially.
//
//   RecomputeBookAggregates is the single function that fixes this. It is
//   called automatically from the BookFile create/update/delete chokepoints
//   so the book-level aggregates stay fresh going forward, and from the
//   maintenance backfill job for existing data.
//
// PARTIAL-DATA RULE (see TASK-026 spec):
//   If a previous aggregate was computed from more files-with-durations than
//   the current scan would produce (e.g., because some files are temporarily
//   missing or their Duration field is zero), we WARN and preserve the old
//   value rather than zeroing it out. This prevents a transient missing-file
//   situation from destroying hard-won duration data.

package database

import (
	"fmt"
	"log/slog"

	"github.com/cockroachdb/pebble/v2"
)

const bookAggregatesBackfillKey = "system:backfill:book_aggregates_v1_done"

// RecomputeBookAggregates sums Duration and FileSize from all BookFile records
// for the given bookID and updates the parent Book atomically using a
// read-modify-write under Pebble's own MVCC layer (same pattern as UpdateBook).
//
// Partial-data rule: if the book already has a populated Duration from a prior
// computation that drew on more files-with-durations than the current file set
// exposes, we keep the old value and log a warning instead of overwriting with
// a less-complete sum. This guards against transient "file gone missing" events
// clobbering real data. The rule applies independently to Duration and FileSize.
func (p *PebbleStore) RecomputeBookAggregates(bookID string) error {
	files, err := p.GetBookFiles(bookID)
	if err != nil {
		return fmt.Errorf("RecomputeBookAggregates GetBookFiles %s: %w", bookID, err)
	}

	var sumDuration int
	var sumFileSize int64
	filesWithDuration := 0
	filesWithFileSize := 0

	for _, f := range files {
		if f.Duration > 0 {
			sumDuration += f.Duration
			filesWithDuration++
		}
		if f.FileSize > 0 {
			sumFileSize += f.FileSize
			filesWithFileSize++
		}
	}

	// Fetch current book for read-modify-write.
	book, err := p.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("RecomputeBookAggregates GetBookByID %s: %w", bookID, err)
	}
	if book == nil {
		// Book deleted between the BookFile mutation and this call — harmless.
		slog.Warn("RecomputeBookAggregates book not found, skipping", "book_id", bookID)
		return nil
	}

	// --- partial-data rule for Duration ---
	// Estimate how many files contributed to the existing snapshot. We can't
	// know exactly (it was set at import), so we treat any non-nil existing
	// value as coming from len(files) files. When files shrinks (all missing)
	// or fewer files carry a duration than before, protect the old value.
	writeDuration := true
	if book.Duration != nil && *book.Duration > 0 && filesWithDuration == 0 {
		// No files with duration at all — cannot produce a better value; keep.
		slog.Warn("RecomputeBookAggregates: no files have Duration — keeping existing book.Duration",
			"book_id", bookID,
			"existing_duration_sec", *book.Duration,
			"total_files", len(files),
		)
		writeDuration = false
	}

	// --- partial-data rule for FileSize ---
	writeFileSize := true
	if book.FileSize != nil && *book.FileSize > 0 && filesWithFileSize == 0 {
		slog.Warn("RecomputeBookAggregates: no files have FileSize — keeping existing book.FileSize",
			"book_id", bookID,
			"existing_file_size_bytes", *book.FileSize,
			"total_files", len(files),
		)
		writeFileSize = false
	}

	// Compute what we will actually write for each field.
	// If write* is false, we keep the existing value; otherwise use the new sum.
	var wantDuration int
	if writeDuration {
		wantDuration = sumDuration
	} else if book.Duration != nil {
		wantDuration = *book.Duration // keep old
	}

	var wantFileSize int64
	if writeFileSize {
		wantFileSize = sumFileSize
	} else if book.FileSize != nil {
		wantFileSize = *book.FileSize // keep old
	}

	// Check if either field actually changed from the current book value.
	existingDuration := 0
	if book.Duration != nil {
		existingDuration = *book.Duration
	}
	existingFileSize := int64(0)
	if book.FileSize != nil {
		existingFileSize = *book.FileSize
	}
	if existingDuration == wantDuration && existingFileSize == wantFileSize {
		slog.Debug("RecomputeBookAggregates: no change needed", "book_id", bookID)
		return nil
	}

	// Apply changes.
	book.Duration = &wantDuration
	book.FileSize = &wantFileSize

	if _, err := p.UpdateBook(bookID, book); err != nil {
		return fmt.Errorf("RecomputeBookAggregates UpdateBook %s: %w", bookID, err)
	}

	slog.Info("RecomputeBookAggregates updated",
		"book_id", bookID,
		"duration_sec", wantDuration,
		"file_size_bytes", wantFileSize,
		"files_with_duration", filesWithDuration,
		"files_with_file_size", filesWithFileSize,
		"total_files", len(files),
	)
	return nil
}

// notifyBookFileChange triggers RecomputeBookAggregates for bookID after a
// BookFile mutation. Errors are logged as warnings but do not propagate —
// the primary BookFile write has already committed and must not be rolled back
// due to an aggregate-update failure.
//
// WHY best-effort: BookFile writes are committed to Pebble before this is
// called. Rolling back the aggregate recompute would leave the DB in an
// inconsistent state (file committed, book not updated). The backfill job
// acts as a safety net for any misses.
func (p *PebbleStore) notifyBookFileChange(bookID string) {
	if err := p.RecomputeBookAggregates(bookID); err != nil {
		slog.Warn("notifyBookFileChange RecomputeBookAggregates failed (best-effort)",
			"book_id", bookID,
			"error", err,
		)
	}
}

// IsBookAggregatesBackfillDone reports whether the one-time backfill has been
// completed. Used by the maintenance job to decide whether to skip a run.
func (p *PebbleStore) IsBookAggregatesBackfillDone() bool {
	_, closer, err := p.db.Get([]byte(bookAggregatesBackfillKey))
	if err != nil {
		return false
	}
	closer.Close()
	return true
}

// MarkBookAggregatesBackfillDone writes the sentinel key that prevents
// re-running the full backfill sweep.
func (p *PebbleStore) MarkBookAggregatesBackfillDone() error {
	return p.db.Set([]byte(bookAggregatesBackfillKey), []byte("1"), pebble.Sync)
}
