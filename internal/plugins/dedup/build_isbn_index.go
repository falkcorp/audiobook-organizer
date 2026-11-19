// file: internal/plugins/dedup/build_isbn_index.go
// version: 1.6.0
// guid: 4c5d6e7f-8a9b-0c1d-2e3f-4a5b6c7d8e9f
// last-edited: 2026-11-18

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

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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
		ID:              "dedup.build-isbn-index",
		Preview:         sdk.PreviewSpec{Params: buildISBNIndexParams{}},
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Build ISBN/ASIN secondary index",
		Description:     buildISBNIndexHelp,
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

// buildISBNIndexKind is rule R3's native kind for dedup.build-isbn-index.
const buildISBNIndexKind = tasks.Kind("books.dedup.build-isbn-index")

// buildISBNIndexHelp is HEAD's Description and the native Help (rule R5).
const buildISBNIndexHelp = "Backfills the book:isbn10:, book:isbn13:, and book:asin: secondary " +
	"indexes for all books. Dry-run by default (pass apply=true to execute). " +
	"Sets book_isbn_index_v1_done on completion, enabling O(1) ISBN dedup lookups. " +
	"Idempotent — safe to re-run."

// buildISBNIndexStore is the reads the core takes: the paged book scan and the
// completion flag.
type buildISBNIndexStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	IsISBNIndexBuilt() bool
}

// buildISBNIndexWriter is the writes the core makes: one index-row batch per
// book and the completion flag.
type buildISBNIndexWriter interface {
	WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error
	SetISBNIndexBuilt() error
}

// buildISBNIndexEnv is what the core takes from its shell. Live is HEAD's
// apply=true; a dry run never calls Writer, which may then be nil.
type buildISBNIndexEnv struct {
	kindEnv
	Store  buildISBNIndexStore
	Writer buildISBNIndexWriter
}

// buildISBNIndexResult is the native run's result. HEAD stored none.
type buildISBNIndexResult struct {
	TotalBooks    int `json:"total_books"`
	BooksWithISBN int `json:"books_with_isbn"`
	IndexRows     int `json:"index_rows"`
	Written       int `json:"written"`
	Failed        int `json:"failed"`
}

// headBuildISBNIndex is HEAD's reads and writes for the core: the plugin's main
// store for the scan and the ISBNIndexStore capability for the rows and flag.
type headBuildISBNIndex struct {
	books pluginBookReader
	isbn  ISBNIndexStore
}

func (h headBuildISBNIndex) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return h.books.GetAllBooksCore(limit, offset)
}
func (h headBuildISBNIndex) IsISBNIndexBuilt() bool { return h.isbn.IsISBNIndexBuilt() }
func (h headBuildISBNIndex) WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error {
	return h.isbn.WriteISBNIndexForBook(bookID, isbn10, isbn13, asin)
}
func (h headBuildISBNIndex) SetISBNIndexBuilt() error { return h.isbn.SetISBNIndexBuilt() }

// buildISBNIndexCore is the body of runBuildISBNIndex, shared by both shells.
func buildISBNIndexCore(ctx context.Context, env buildISBNIndexEnv) (buildISBNIndexResult, error) {
	env.Logger.Info("build-isbn-index start", "apply", env.Live)

	// --- Check if already complete ---
	if env.Store.IsISBNIndexBuilt() && env.Live {
		env.Logger.Info("build-isbn-index: already completed; re-running to ensure consistency")
	}

	// --- Load all books ---
	env.Progress(0, 3, "Loading all books…")

	// The scan pages GetAllBooksCore by offset, so it is sequential by nature:
	// each page's offset depends on the previous page's length.
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
		if env.Canceled() {
			return buildISBNIndexResult{}, context.Canceled
		}
		select {
		case <-ctx.Done():
			return buildISBNIndexResult{}, ctx.Err()
		default:
		}

		batch, err := env.Store.GetAllBooksCore(batchSize, offset)
		if err != nil {
			return buildISBNIndexResult{}, fmt.Errorf("get all books at offset %d: %w", offset, err)
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

	env.Logger.Info("build-isbn-index: scan complete",
		"total_books", totalBooks,
		"books_with_isbn", booksWithISBN,
		"index_rows", indexRows,
		"apply", env.Live)

	res := buildISBNIndexResult{TotalBooks: totalBooks, BooksWithISBN: booksWithISBN, IndexRows: indexRows}
	summary := fmt.Sprintf(
		"Scan: %d books total, %d with ISBN/ASIN, %d index rows to write",
		totalBooks, booksWithISBN, indexRows,
	)

	if !env.Live {
		env.Progress(3, 3,
			fmt.Sprintf("Dry-run complete — %d index rows would be written. Pass apply=true to execute.", indexRows))
		env.Logger.Info("build-isbn-index: dry-run only; no changes written", "would_write", indexRows)
		return res, nil
	}

	// --- Apply: write index rows via the store's typed write path ---
	// The write tail stays sequential, as HEAD ran it: each write is one
	// single-book batch on rows keyed by that book, the completion flag below
	// depends on the exact written count, and a mid-run cancel must stop with a
	// prefix written. (A pool here is a candidate follow-up: the rows are
	// disjoint per book; see the F5-04 report.)
	env.Progress(1, 3, fmt.Sprintf("Writing %d index rows…", indexRows))

	var written, failed int
	for _, e := range toWrite {
		if env.Canceled() {
			break
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		if err := env.Writer.WriteISBNIndexForBook(e.bookID, e.isbn10, e.isbn13, e.asin); err != nil {
			env.Logger.Error("build-isbn-index: write error",
				"book_id", e.bookID, "error", err)
			// Continue — partial progress is better than aborting; the op is idempotent.
			failed++
		} else {
			written++
		}
	}
	res.Written, res.Failed = written, failed

	env.Logger.Info("build-isbn-index: write complete",
		"written", written, "failed", failed, "intended", booksWithISBN)

	// --- Set completion flag only on a complete, error-free backfill ---
	if written == booksWithISBN {
		if err := env.Writer.SetISBNIndexBuilt(); err != nil {
			env.Logger.Warn("build-isbn-index: could not set done flag", "error", err)
		} else {
			env.Logger.Info("build-isbn-index: set done flag")
		}
	} else {
		env.Logger.Warn("build-isbn-index: done flag NOT set — backfill incomplete",
			"written", written, "failed", failed, "intended", booksWithISBN,
			"action", "re-run with apply=true to complete")
	}

	env.Progress(3, 3,
		fmt.Sprintf("Complete — %d/%d books indexed (%d failed). %s", written, booksWithISBN, failed, summary))
	env.Logger.Info("build-isbn-index: complete",
		"written", written, "failed", failed, "intended", booksWithISBN)
	return res, nil
}

// runBuildISBNIndex is the v2 shell: HEAD's Run (nil checks, capability
// resolution, params) over buildISBNIndexCore.
func (p *Plugin) runBuildISBNIndex(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	// --- Resolve the typed ISBN index interface ---
	// The store must implement ISBNIndexStore so the op can write index rows
	// and set/read the completion flag without going through the generic
	// SetSetting path (which uses a different key from IsISBNIndexBuilt).
	//
	// Resolved through the decorator chain rather than asserted on p.store
	// directly -- see resolveISBNIndexStore below for why a bare assertion is
	// fragile here.
	isbnStore := resolveISBNIndexStore(p.store)
	if isbnStore == nil {
		return fmt.Errorf("store does not provide the ISBNIndexStore capability " +
			"(WriteISBNIndexForBook, IsISBNIndexBuilt, SetISBNIndexBuilt)")
	}

	// --- Parse params ---
	var params buildISBNIndexParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	h := headBuildISBNIndex{books: p.store, isbn: isbnStore}
	_, err := buildISBNIndexCore(ctx, buildISBNIndexEnv{kindEnv: headEnv(reporter, params.Apply), Store: h, Writer: h})
	return err
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

// resolveISBNIndexStore walks the decorator chain for ISBNIndexStore instead of
// asserting on the store value directly.
//
// Why this is not a bare assertion. p.store is a pluginStore, whose dynamic
// value today is the bare *database.PebbleStore seeded into the service
// registry -- so `p.store.(ISBNIndexStore)` succeeds as things currently
// stand, and this change fixes no live failure. It stops the op from being one
// decorator away from breaking: any wrapper that embeds the database.Store
// interface (internal/server's indexedStore was one, until 07-S3) satisfies pluginStore
// while hiding the concrete type, and the assertion would then fail.
//
// All THREE methods are absent from database.Store -- compile-probed
// 2026-08-19 individually with `var _ interface{ M(...) } = (Store)(nil)`
// under -gcflags=-e, plus a positive control to prove the probe distinguishes
// both answers. This composite is therefore UNIFORMLY unreachable, unlike
// internal/dedup's same-named ISBNIndexStore, which is a mixed composite
// (GetBookIDsByISBNASIN is on database.Store, IsISBNIndexBuilt is not).
//
// Unlike the silent-fallback conversions in this sweep (#2597-#2601), this
// call site fails LOUDLY: the op returns an error naming the missing
// capability. Nothing degrades silently here; the op just refuses to run.
// See docs/plans/2026-08-19-split-the-pebblestore-surface.md.
func resolveISBNIndexStore(s any) ISBNIndexStore {
	if c, ok := database.AsCapability[ISBNIndexStore](s); ok {
		return c
	}
	return nil
}

// derefStrPlugin is a nil-safe string pointer deref for use in the dedup plugin.
func derefStrPlugin(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
