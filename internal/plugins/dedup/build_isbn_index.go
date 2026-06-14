// file: internal/plugins/dedup/build_isbn_index.go
// version: 1.0.0
// guid: 4c5d6e7f-8a9b-0c1d-2e3f-4a5b6c7d8e9f
// last-edited: 2026-06-14

// Package dedup — op dedup.build-isbn-index.
//
// Backfills the book:isbn10:, book:isbn13:, and book:asin: secondary indexes
// for all books that existed before the write-path maintenance was added.
// New books (created/updated after the pebble_store.go change ships) have
// their index rows written inline during CreateBook/UpdateBook.
//
// Once this op completes with apply=true the flag book_isbn_index_v1_done is
// set and checkExactISBN switches to the O(matches) indexed path instead of
// the O(N²) full-scan fallback.
//
// Usage:
//
//	POST /api/v1/operations  {"op":"dedup.build-isbn-index"}           # dry-run
//	POST /api/v1/operations  {"op":"dedup.build-isbn-index","apply":true}  # execute
//
// Idempotent: re-running is safe. The flag prevents double-completion noise
// but does not block re-runs (pass apply=true to force rebuild if needed).

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// isbnIndexDoneFlag is the versioned Settings key set on successful completion.
// Increment to v2 if the key format changes and a forced rebuild is needed.
const isbnIndexDoneFlag = "book_isbn_index_v1_done"

// buildISBNIndexParams are the JSON parameters accepted by the op.
type buildISBNIndexParams struct {
	// Apply, if true, writes the index rows and sets the completion flag.
	// Default false (dry-run) — the op counts what it would write and returns.
	Apply bool `json:"apply"`
}

// buildISBNIndexDef returns the OperationDef for dedup.build-isbn-index.
func (p *Plugin) buildISBNIndexDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.build-isbn-index",
		Plugin:      "dedup",
		DisplayName: "Build ISBN/ASIN secondary index",
		Description: "Backfills the book:isbn10:, book:isbn13:, and book:asin: secondary " +
			"indexes for all books. Dry-run by default (pass apply=true to execute). " +
			"Sets book_isbn_index_v1_done on completion, enabling O(1) ISBN dedup lookups. " +
			"Idempotent — safe to re-run.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh,
		ConcurrencyKey:  "dedup.build-isbn-index",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runBuildISBNIndex,
	}
}

// runBuildISBNIndex implements the build-isbn-index op.
func (p *Plugin) runBuildISBNIndex(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	// --- Parse params ---
	var params buildISBNIndexParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	reporter.Logger().Info("build-isbn-index start", "apply", params.Apply)

	// --- Check if already complete ---
	if done, err := p.isFlagSet(isbnIndexDoneFlag); err != nil {
		reporter.Logger().Warn("build-isbn-index: flag check error (proceeding)", "error", err)
	} else if done && params.Apply {
		reporter.Logger().Info("build-isbn-index: already completed; re-running to ensure consistency",
			"flag", isbnIndexDoneFlag)
	}

	// --- Load all books ---
	_ = reporter.UpdateProgress(0, 3, "Loading all books…")

	// Use GetAllBooks in batches so we don't materialise 50K books at once.
	const batchSize = 500
	var (
		totalBooks    int
		booksWithISBN int
		indexRows     int
	)

	// Collect the (bookID, isbn10, isbn13, asin) tuples to write.
	type isbnEntry struct {
		bookID string
		isbn10 string
		isbn13 string
		asin   string
	}
	var toWrite []isbnEntry

	offset := 0
	for {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		batch, err := p.store.GetAllBooks(batchSize, offset)
		if err != nil {
			return fmt.Errorf("get all books at offset %d: %w", offset, err)
		}
		if len(batch) == 0 {
			break
		}

		for i := range batch {
			b := &batch[i]
			totalBooks++

			isbn10 := derefStrPlugin(b.ISBN10)
			isbn13 := derefStrPlugin(b.ISBN13)
			asin := derefStrPlugin(b.ASIN)

			if isbn10 == "" && isbn13 == "" && asin == "" {
				continue
			}

			booksWithISBN++
			toWrite = append(toWrite, isbnEntry{
				bookID: b.ID,
				isbn10: isbn10,
				isbn13: isbn13,
				asin:   asin,
			})

			if isbn10 != "" {
				indexRows++
			}
			if isbn13 != "" {
				indexRows++
			}
			if asin != "" {
				indexRows++
			}
		}

		if len(batch) < batchSize {
			break
		}
		offset += batchSize
	}

	reporter.Logger().Info("build-isbn-index: scan complete",
		"total_books", totalBooks,
		"books_with_isbn", booksWithISBN,
		"index_rows", indexRows,
		"apply", params.Apply)

	summary := fmt.Sprintf(
		"Scan: %d books total, %d with ISBN/ASIN, %d index rows to write",
		totalBooks, booksWithISBN, indexRows,
	)

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3,
			fmt.Sprintf("Dry-run complete — %d index rows would be written. Pass apply=true to execute.", indexRows))
		reporter.Logger().Info("build-isbn-index: dry-run only; no changes written", "would_write", indexRows)
		return nil
	}

	// --- Apply: write index rows via the store's write path ---
	// We call the individual PebbleStore methods through the database.Store
	// interface.  The index write happens via WriteISBNIndexRows which is
	// package-private in database; we trigger it by calling UpdateBook for
	// each affected book.  However, UpdateBook is expensive (full JSON
	// marshal + snapshot).  Instead, we call the public
	// GetBookIDsByISBNASIN-style helpers on the store… but the write helpers
	// are package-private.
	//
	// The cleanest solution: check if the store exposes the typed write
	// interface. We type-assert to ISBNIndexPebbleStore (which is *PebbleStore)
	// to call SetISBNIndexBuilt.  For the row writes we use UpdateBook on a
	// no-op update — it's slightly expensive but preserves all maintenance
	// (snapshots, memdb write-through, dirty-flag invalidation) and avoids
	// any direct Pebble batch access from outside the database package.
	//
	// For the actual row writes we call a dedicated typed interface:
	// ISBNIndexWriter which *PebbleStore satisfies via WriteISBNIndexForBook.
	writer, ok := p.store.(ISBNIndexWriter)
	if !ok {
		return fmt.Errorf("store does not implement ISBNIndexWriter (expected *database.PebbleStore)")
	}

	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Writing %d index rows…", indexRows))

	var written int
	for _, e := range toWrite {
		if reporter.IsCanceled() {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := writer.WriteISBNIndexForBook(e.bookID, e.isbn10, e.isbn13, e.asin); err != nil {
			reporter.Logger().Error("build-isbn-index: write error",
				"book_id", e.bookID, "error", err)
			// Continue — partial progress is better than aborting; the op is idempotent.
		} else {
			written++
		}
	}

	reporter.Logger().Info("build-isbn-index: write complete",
		"written", written, "intended", booksWithISBN)

	// --- Set completion flag ---
	if err := p.store.SetSetting(isbnIndexDoneFlag, "true", "bool", false); err != nil {
		reporter.Logger().Warn("build-isbn-index: could not set done flag",
			"flag", isbnIndexDoneFlag, "error", err)
	} else {
		reporter.Logger().Info("build-isbn-index: set done flag", "flag", isbnIndexDoneFlag)
	}

	_ = reporter.UpdateProgress(3, 3,
		fmt.Sprintf("Complete — %d/%d books indexed. %s", written, booksWithISBN, summary))
	reporter.Logger().Info("build-isbn-index: complete",
		"written", written, "intended", booksWithISBN)
	return nil
}

// ISBNIndexWriter is the narrow write interface the backfill op uses to write
// index rows for a single book without going through the full UpdateBook path.
// *database.PebbleStore implements this via WriteISBNIndexForBook.
type ISBNIndexWriter interface {
	WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error
}

// derefStrPlugin is a nil-safe string pointer deref for use in the dedup plugin.
func derefStrPlugin(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
