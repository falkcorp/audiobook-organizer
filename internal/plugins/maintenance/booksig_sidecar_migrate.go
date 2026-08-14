// file: internal/plugins/maintenance/booksig_sidecar_migrate.go
// version: 1.0.0
// guid: 2c8f6a90-4b17-4e35-9d82-1a5e703c6f84
// last-edited: 2026-08-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenance.booksig-sidecar-migrate finishes the job PR #2387 started.
//
// #2387 moved the five BookSig* fields to a `book_sig:<id>` sidecar with
// FALLBACK-FIRST reads, so all 67,824 existing rows kept working un-migrated.
// That is what made the read path safe to ship — and it is also why the saving
// is not realized: production warmup on 2026-08-13 21:15 still reported
//
//	discarded_field_mb[book_sig_v1_and_mask] = 580
//	phase_mb[books]                          = 729
//
// exactly as designed. Startup still reads, decodes and allocates ~580 MB of
// signature that stripBookForMemdb discards on the spot. This op walks the
// `book:` rows and moves the data, which is the only irreversible step in the
// design — hence the dry-run gate and the Limit canary below.
//
// The per-book primitive, its CAS-and-skip race handling, and the reasoning for
// not doing this via UpdateBook all live in
// internal/database/pebble_store_booksig_migrate.go.

// bookSigSidecarMigrateParams controls the migration. DryRun defaults to true:
// callers must explicitly pass dryRun=false to write anything.
type bookSigSidecarMigrateParams struct {
	DryRun bool `json:"dryRun"`
	// Limit caps how many books are examined (0 = the whole library). It exists
	// so the first apply can be a small canary — migrate 100 books, verify the
	// pairing held, then run the rest — rather than an all-or-nothing 67,824-row
	// write. Books are examined in ListBookIDs order, which is stable, so a
	// limited run is a prefix and a later full run picks up where it left off.
	Limit int `json:"limit"`
}

// bookSigMigratePageSize batches IDs into pages so RunItems dispatches a
// reasonable number of work units rather than one goroutine hop per book.
// Pages partition the ID list into DISJOINT sets, so no two workers can ever
// touch the same row — which matters because PebbleStore has no per-book write
// lock (see MigrateBookSigToSidecar's concurrency note).
const bookSigMigratePageSize = 200

// bookSigInlineBytesPerBook is the measured average cost of one inline
// signature: 580 MB of discarded_field_mb spread across the books that actually
// carry one. It is used ONLY to print an expected-magnitude cross-check next to
// the dry-run candidate count. If the op reports all 67,824 books as candidates
// the detector is matching books with no signature; if it reports a few hundred
// it is not recognizing the inline shape. Either way the operator sees it
// BEFORE authorizing the irreversible pass, instead of having to remember the
// warmup numbers.
const bookSigInlineBytesPerBook = 22 * 1024

func (p *Plugin) bookSigSidecarMigrateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.booksig-sidecar-migrate",
		Plugin:      "maintenance",
		DisplayName: "Migrate inline book signatures into the book_sig: sidecar",
		Description: "Dry-run audit (default) or explicit apply of the #2387 storage migration. " +
			"Walks every book: row and, for any row still carrying the five inline BookSig* " +
			"fields, writes the book_sig:<id> sidecar and rewrites the row without them — both " +
			"in ONE Pebble batch, so a row is never stripped without its sidecar. Reads are " +
			"fallback-first, so un-migrated and migrated books both work throughout and the op " +
			"is safe to re-run or to stop midway. Dry-run (default) only classifies and reports " +
			"counts. Apply mode (dryRun=false, explicit only) performs the only irreversible " +
			"step in the sidecar design. Set limit=N to migrate a small canary first. Books " +
			"whose row changes underneath the migration are skipped and counted, never " +
			"half-written.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.booksig-sidecar-migrate",
		Cancellable:     true,
		// Isolate means out-of-process, and Pebble is single-writer: a child
		// cannot reopen the database. Must stay false.
		Isolate:      false,
		Timeout:      90 * time.Minute,
		Schedule:     nil,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runBookSigSidecarMigrate,
	}
}

func (p *Plugin) runBookSigSidecarMigrate(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params := bookSigSidecarMigrateParams{DryRun: true} // safe default
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	log := reporter.Logger()

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	// Resolve through the capability helper, never a bare type assertion: prod
	// always installs the Bleve indexedStore decorator, and `store.(*PebbleStore)`
	// against a wrapped store is indistinguishable from a genuinely unsupported
	// backend. Several ops silently no-opped in production exactly that way. A
	// hard error here is deliberate — "unsupported store, 0 books migrated" must
	// never be reportable as success.
	migrator := database.AsBookSigMigrateStore(store)
	if migrator == nil {
		return fmt.Errorf("store does not support book signature migration (got %T); "+
			"this op requires a Pebble-backed store and must not report success without one", store)
	}

	if params.DryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — classifying only, no rows or sidecars will be written")
	} else {
		_ = reporter.Log(slog.LevelWarn, "APPLY MODE — rewriting book: rows and writing book_sig: sidecars (irreversible)")
	}

	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("ListBookIDs: %w", err)
	}
	libraryTotal := len(ids)
	if params.Limit > 0 && params.Limit < len(ids) {
		ids = ids[:params.Limit]
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
			"CANARY — limit=%d, examining the first %d of %d books; the remainder are untouched and a later run resumes from here",
			params.Limit, len(ids), libraryTotal))
	}
	total := len(ids)
	if total == 0 {
		_ = reporter.UpdateProgress(1, 1, "No books to examine")
		return nil
	}
	_ = reporter.UpdateProgress(0, total, "Migrating inline book signatures to the sidecar…")

	var (
		examined     atomic.Int64
		migrated     atomic.Int64
		strippedOnly atomic.Int64
		notCandidate atomic.Int64
		skippedRaced atomic.Int64
		errCount     atomic.Int64
	)

	pages := chunkIDs(ids, bookSigMigratePageSize)
	runErr := registry.RunItems(ctx, reporter, pages, func(ctx context.Context, page []string) error {
		for _, id := range page {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			examined.Add(1)
			outcome, err := migrator.MigrateBookSigToSidecar(id, params.DryRun)
			if err != nil {
				// One unreadable or malformed row must not abort a
				// whole-library pass; it is counted and reported, and the row
				// is left exactly as it was.
				errCount.Add(1)
				log.Warn("booksig-sidecar-migrate: book failed", "book_id", id, "err", err)
				continue
			}
			switch outcome {
			case database.BookSigMigrateMigrated:
				migrated.Add(1)
			case database.BookSigMigrateStrippedOnly:
				strippedOnly.Add(1)
			case database.BookSigMigrateSkippedRaced:
				skippedRaced.Add(1)
			default:
				notCandidate.Add(1)
			}
		}
		return nil
	}, registry.RunItemsOptions{
		// CPU-bound work (JSON decode/encode) plus a small Pebble write per
		// candidate. Books partition by ID across disjoint pages, so workers
		// never contend for a row.
		Concurrency:   runtime.NumCPU(),
		ProgressTotal: len(pages),
		Label: func(i, t int) string {
			return fmt.Sprintf("Page %d/%d — %d migrated, %d stripped-only, %d raced",
				i+1, t, migrated.Load(), strippedOnly.Load(), skippedRaced.Load())
		},
	})

	candidates := migrated.Load() + strippedOnly.Load() + skippedRaced.Load()
	impliedMB := float64(candidates*bookSigInlineBytesPerBook) / (1024 * 1024)

	mode := "DRY RUN"
	verb := "would migrate"
	if !params.DryRun {
		mode = "APPLY MODE"
		verb = "migrated"
	}
	report := fmt.Sprintf(
		"%s — examined %d of %d books: %s %d, stripped-only %d (sidecar already newer), "+
			"not candidates %d, skipped-raced %d, errors %d. "+
			"Candidates imply ~%.0f MB of inline signature (warmup measured 580 MB).",
		mode, examined.Load(), libraryTotal, verb, migrated.Load(), strippedOnly.Load(),
		notCandidate.Load(), skippedRaced.Load(), errCount.Load(), impliedMB)

	log.Info("booksig-sidecar-migrate: complete",
		"dry_run", params.DryRun,
		"examined", examined.Load(),
		"library_total", libraryTotal,
		"migrated", migrated.Load(),
		"stripped_only", strippedOnly.Load(),
		"not_candidate", notCandidate.Load(),
		"skipped_raced", skippedRaced.Load(),
		"errors", errCount.Load(),
		"implied_inline_mb", impliedMB)

	if skippedRaced.Load() > 0 {
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
			"%d books were written by another path mid-migration and were skipped rather than reverted; re-run to pick them up",
			skippedRaced.Load()))
	}

	_ = reporter.Log(slog.LevelInfo, report)
	_ = reporter.UpdateProgress(len(pages), len(pages), report)
	return runErr
}
