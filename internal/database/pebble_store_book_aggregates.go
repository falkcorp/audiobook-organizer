// file: internal/database/pebble_store_book_aggregates.go
// version: 1.6.1
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-09-25

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
	"context"
	"fmt"
	"log/slog"

	"github.com/cockroachdb/pebble/v2"
)

const bookAggregatesBackfillKey = "system:backfill:book_aggregates_v1_done"

// RecomputeBookAggregates sums Duration and FileSize from the BookFile records
// for the given bookID — all of them, except the out-of-folder rows that are
// copies of a present own-folder row (OwnFolderFiles / IsBookFileCopy) — and
// updates the parent Book atomically using a
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

	// The sums are computed inside the ModifyBook closure below because they
	// need the book: when its rows span its own folder and somewhere else
	// (iTunes copy + library copy + old chapter files), an out-of-folder row
	// that is a copy of a present own-folder row does not count
	// (OwnFolderFiles); every other row, including a merge's moved rows that
	// still sit in the loser's folder, does. Summing every copy inflated the
	// total.
	var (
		sumDuration       int
		filesWithDuration int
		sumFileSize       int64
		filesWithFileSize int
		countedFiles      int
	)
	sumCounted := func(book *Book) {
		counted := OwnFolderFiles(book, files)
		countedFiles = len(counted)
		// Duration comes from the canonical runtime (ComputeBookRuntime) so the
		// stored aggregate and every runtime comparison count the same rows:
		// present rows plus every missing row that is not a repoint duplicate of
		// a present one, millisecond rows normalized. Rule: this is the all-rows
		// sum this function always stored, minus ONLY the old copy a repoint
		// leaves beside its present row (the raw sum counted that content twice).
		// A chapter that goes missing without a present copy stays counted, so a
		// missing chapter can never lower Book.Duration.
		rt := ComputeBookRuntime(nil, counted)
		sumDuration, _ = rt.StoredAggregateSec()
		filesWithDuration = rt.FilesKnown
		sumFileSize, filesWithFileSize = 0, 0
		for _, f := range counted {
			if f.FileSize > 0 {
				sumFileSize += f.FileSize
				filesWithFileSize++
			}
		}
	}

	// Read-modify-write of the book row under its write stripe (ModifyBook):
	// the file rows above are read outside it; only the row read, the sums
	// that depend on the book's own folder, the partial-data decision and the
	// commit run inside.
	var wantDuration int
	var wantFileSize int64
	found, wrote := false, false
	_, err = p.ModifyBook(bookID, func(book *Book) error {
		found = true
		sumCounted(book)

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
		if writeDuration {
			wantDuration = sumDuration
		} else if book.Duration != nil {
			wantDuration = *book.Duration // keep old
		}

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
			// "caller" is the redundancy signal: a book recomputed many times from
			// the same originator with no change to show for it is exactly what the
			// coalescing fix is meant to eliminate.
			//
			// The Enabled guard is NOT redundant with slog's own level check. Go
			// evaluates arguments before the call, so an unguarded
			// slog.Debug(..., "caller", aggregateCaller()) would walk the stack on
			// every no-change return regardless of log level — and this is the
			// hottest path in the function: the maintenance backfill sweeps the whole
			// library and most books have nothing to update. That would add an
			// unconditional stack walk to a full-library loop inside the very
			// function this instrumentation exists to make cheaper.
			if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
				slog.Debug("RecomputeBookAggregates: no change needed",
					"book_id", bookID,
					"caller", aggregateCaller(),
				)
			}
			return ErrSkipBookWrite
		}

		// Apply changes.
		book.Duration = &wantDuration
		book.FileSize = &wantFileSize
		wrote = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("RecomputeBookAggregates ModifyBook %s: %w", bookID, err)
	}
	if !found {
		// Book deleted between the BookFile mutation and this call — harmless.
		slog.Warn("RecomputeBookAggregates book not found, skipping", "book_id", bookID)
		return nil
	}
	if !wrote {
		return nil
	}

	// "caller" names the subsystem that drove this write. This is the line the
	// 126,928-sample production count was drawn from, so adding the field here
	// (rather than at some new log site) keeps any future measurement directly
	// comparable with that baseline. "total_files" is also the per-call read
	// count — caller x total_files is the amplification attributable to each
	// originator.
	slog.Info("RecomputeBookAggregates updated",
		"book_id", bookID,
		"caller", aggregateCaller(),
		"duration_sec", wantDuration,
		"file_size_bytes", wantFileSize,
		"files_with_duration", filesWithDuration,
		"files_with_file_size", filesWithFileSize,
		"total_files", len(files),
		"counted_files", countedFiles,
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
// inconsistent state (file committed, book not updated). Failing the caller's
// write over a derived value that can be rebuilt is the worse trade.
//
// ⚠️ THE SAFETY NET IS OPERATOR-DRIVEN, NOT AUTOMATIC. This comment once ended
// "the backfill job acts as a safety net for any misses", which was false in the
// stronger way between then and 2026-08-29: maintenance.recompute-book-aggregates
// short-circuits on a one-time sentinel, and its documented escape hatch — Force —
// was declared but never read, absent from the params struct the dispatcher
// populates, and unbound by the dispatcher's request body. Once the sentinel was
// set the job refused to run, forever, while printing an override that did
// nothing.
//
// Force is now wired end to end (dispatcher body → maintenanceJobOpParams →
// maintenance.WithRawParams → the sentinel gate), so the remedy exists: an
// operator can POST {"dry_run": false, "force": true} and rebuild every book's
// aggregates. What is still true is that NOTHING does this on its own. A book
// whose recompute fails here stays wrong until either a later write to its files
// recomputes it or someone runs the forced backfill — which is why the failure
// must be loud.
//
// That is why the failure must at least be loud. For batch writes use
// notifyBookFileChanges, which additionally emits one aggregated Error for the
// whole batch — a per-book warning buried in a 175K-row backfill is not a signal
// anyone will see.
func (p *PebbleStore) notifyBookFileChange(bookID string) {
	if err := p.RecomputeBookAggregates(bookID); err != nil {
		slog.Warn("notifyBookFileChange RecomputeBookAggregates failed (best-effort)",
			"book_id", bookID,
			"caller", aggregateCaller(),
			"error", err,
		)
	}
}

// notifyBookFileChanges recomputes aggregates for a batch's affected books and
// reports the batch's failures as ONE line rather than N.
//
// Each book still gets its per-book warning, so per-book counting and debugging
// are unchanged. What this adds is the summary an operator can actually notice:
// without it, a backfill in which every recompute failed and one in which none
// did are distinguishable only by grepping warnings out of a very large log, and
// the operation reports success either way.
//
// The summary message deliberately does NOT contain the string
// "RecomputeBookAggregates". The test helper countAggregateInvocations counts
// terminal log lines to detect per-row recompute regressions, and a per-batch
// line carrying that token would be counted as an extra invocation and break the
// coalescing assertions. Keep it that way.
func (p *PebbleStore) notifyBookFileChanges(bookIDs []string) {
	var failed []string
	for _, bookID := range bookIDs {
		if err := p.RecomputeBookAggregates(bookID); err != nil {
			slog.Warn("notifyBookFileChange RecomputeBookAggregates failed (best-effort)",
				"book_id", bookID,
				"caller", aggregateCaller(),
				"error", err,
			)
			failed = append(failed, bookID)
		}
	}
	if len(failed) == 0 {
		return
	}
	sample := failed
	if len(sample) > 10 {
		sample = sample[:10]
	}
	slog.Error("book aggregate recompute failed after a batch write; these books' "+
		"totals are now stale and nothing re-derives them automatically",
		"failed_count", len(failed),
		"affected_count", len(bookIDs),
		"failed_sample", sample,
		"caller", aggregateCaller(),
	)
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
