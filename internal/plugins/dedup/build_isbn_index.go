// file: internal/plugins/dedup/build_isbn_index.go
// version: 1.1.0
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

	// --- Resolve the typed ISBN index interface ---
	// The store must implement ISBNIndexStore so the op can write index rows
	// and set/read the completion flag without going through the generic
	// SetSetting path (which uses a different key from IsISBNIndexBuilt).
	isbnStore, ok := p.store.(ISBNIndexStore)
	if !ok {
		return fmt.Errorf("store does not implement ISBNIndexStore (expected *database.PebbleStore)")
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
	if isbnStore.IsISBNIndexBuilt() && params.Apply {
		reporter.Logger().Info("build-isbn-index: already completed; re-running to ensure consistency")
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

	// --- Apply: write index rows via the store's typed write path ---
	// Row writes go through ISBNIndexStore.WriteISBNIndexForBook (a dedicated
	// single-book batch write, idempotent, no UpdateBook overhead).
	// The completion flag is set only when ALL books were written successfully
	// (written == booksWithISBN), covering both per-book write failures and
	// mid-run cancellation.
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Writing %d index rows…", indexRows))

	var written, failed int
	for _, e := range toWrite {
		if reporter.IsCanceled() {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := isbnStore.WriteISBNIndexForBook(e.bookID, e.isbn10, e.isbn13, e.asin); err != nil {
			reporter.Logger().Error("build-isbn-index: write error",
				"book_id", e.bookID, "error", err)
			// Continue — partial progress is better than aborting; the op is idempotent.
			failed++
		} else {
			written++
		}
	}

	reporter.Logger().Info("build-isbn-index: write complete",
		"written", written, "failed", failed, "intended", booksWithISBN)

	// --- Set completion flag only on a complete, error-free backfill ---
	// written == booksWithISBN guards against both write errors (failed > 0)
	// and mid-run cancellation (written + failed < booksWithISBN).
	if written == booksWithISBN {
		if err := isbnStore.SetISBNIndexBuilt(); err != nil {
			reporter.Logger().Warn("build-isbn-index: could not set done flag", "error", err)
		} else {
			reporter.Logger().Info("build-isbn-index: set done flag")
		}
	} else {
		reporter.Logger().Warn("build-isbn-index: done flag NOT set — backfill incomplete",
			"written", written, "failed", failed, "intended", booksWithISBN,
			"action", "re-run with apply=true to complete")
	}

	_ = reporter.UpdateProgress(3, 3,
		fmt.Sprintf("Complete — %d/%d books indexed (%d failed). %s", written, booksWithISBN, failed, summary))
	reporter.Logger().Info("build-isbn-index: complete",
		"written", written, "failed", failed, "intended", booksWithISBN)
	return nil
}

// ISBNIndexStore is the narrow typed interface the build-isbn-index op uses.
// It covers both the per-book row write and the single-source-of-truth
// completion flag methods.  *database.PebbleStore implements all three.
type ISBNIndexStore interface {
	// WriteISBNIndexForBook writes isbn10/isbn13/asin secondary-index rows for
	// a single book (idempotent, one-shot Pebble batch).
	WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error
	// IsISBNIndexBuilt reports whether the backfill has completed.
	IsISBNIndexBuilt() bool
	// SetISBNIndexBuilt marks the backfill complete.  Must use the same
	// Settings key as IsISBNIndexBuilt — both are on *database.PebbleStore,
	// so this is guaranteed by construction.
	SetISBNIndexBuilt() error
}

// derefStrPlugin is a nil-safe string pointer deref for use in the dedup plugin.
func derefStrPlugin(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
