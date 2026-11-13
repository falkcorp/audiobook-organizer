// file: internal/plugins/maintenance/booksig_sidecar_migrate.go
// version: 1.4.1
// guid: 2c8f6a90-4b17-4e35-9d82-1a5e703c6f84
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
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
// internal/books/store/pebble_store_booksig_migrate.go.

// bookSigSidecarMigrateParams controls the migration. DryRun defaults to true:
// callers must explicitly pass dryRun=false to write anything.
type bookSigSidecarMigrateParams struct {
	// DryRun defaults to TRUE when omitted (opmode.ResolveDryRun): a request
	// that states no mode is a preview. dry_run is accepted as an alias, and
	// sending both with different values is refused rather than guessed.
	DryRun      *bool `json:"dryRun,omitempty"`
	DryRunSnake *bool `json:"dry_run,omitempty"`
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

// t6g2BookSigSidecarMigrateID is HEAD's def ID; the native kind is "books." + it.
const t6g2BookSigSidecarMigrateID = "maintenance.booksig-sidecar-migrate"

// t6g2KindBookSigSidecarMigrate is the native kind (F5-07, rule R3).
const t6g2KindBookSigSidecarMigrate tasks.Kind = "books." + t6g2BookSigSidecarMigrateID

// DisplayName and Description of the def, shared by HEAD's def and the native
// kind so the two cannot drift.
const (
	t6g2BookSigSidecarMigrateTitle = "Migrate inline book signatures into the book_sig: sidecar"
	t6g2BookSigSidecarMigrateHelp  = "Dry-run audit (default) or explicit apply of the #2387 storage migration. " +
		"Walks every book: row and, for any row still carrying the five inline BookSig* " +
		"fields, writes the book_sig:<id> sidecar and rewrites the row without them — both " +
		"in ONE Pebble batch, so a row is never stripped without its sidecar. Reads are " +
		"fallback-first, so un-migrated and migrated books both work throughout and the op " +
		"is safe to re-run or to stop midway. Dry-run (default) only classifies and reports " +
		"counts. Apply mode (dryRun=false, explicit only) performs the only irreversible " +
		"step in the sidecar design. Set limit=N to migrate a small canary first. Books " +
		"whose row changes underneath the migration are skipped and counted, never " +
		"half-written."
)

func (p *Plugin) bookSigSidecarMigrateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              t6g2BookSigSidecarMigrateID,
		Preview:         sdk.PreviewSpec{Params: bookSigSidecarMigrateParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     t6g2BookSigSidecarMigrateTitle,
		Description:     t6g2BookSigSidecarMigrateHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  t6g2BookSigSidecarMigrateID,
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
	var params bookSigSidecarMigrateParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(t6g2BookSigSidecarMigrateID, params.DryRunSnake, params.DryRun)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	env := headT6G2Wire(headT6G2Env(p, reporter), p, store)
	env.Items = headT6G2Items(reporter, runtime.NumCPU())
	_, err = runBookSigSidecarMigrateCore(ctx, params.Limit, dryRun, env)
	return err
}

// runBookSigSidecarMigrateCore is the body shared by HEAD's shell and the native
// kind: limit is the canary cap and dryRun the resolved mode.
func runBookSigSidecarMigrateCore(ctx context.Context, limit int, dryRun bool, env t6g2Env) (t6g2NoResult, error) {
	log := env.Logger
	store := env.Store

	// Resolve through the capability probe, never a bare type assertion: the
	// server used to install the Bleve indexedStore decorator (removed in 07-S3),
	// and `store.(*PebbleStore)` against a wrapped store is indistinguishable
	// from a genuinely unsupported backend. Several ops silently no-opped in
	// production exactly that way. A hard error here is deliberate — "unsupported
	// store, 0 books migrated" must never be reportable as success.
	if !store.Supports(t6g2CapSigMigrate) {
		return t6g2NoResult{}, fmt.Errorf("store does not support book signature migration (got %s); "+
			"this op requires a Pebble-backed store and must not report success without one", store.Describe())
	}

	if dryRun {
		env.Log(slog.LevelInfo, "DRY RUN — classifying only, no rows or sidecars will be written")
	} else {
		env.Log(slog.LevelWarn, "APPLY MODE — rewriting book: rows and writing book_sig: sidecars (irreversible)")
	}

	ids, err := store.ListBookIDs()
	if err != nil {
		return t6g2NoResult{}, fmt.Errorf("ListBookIDs: %w", err)
	}
	libraryTotal := len(ids)
	if limit > 0 && limit < len(ids) {
		ids = ids[:limit]
		env.Log(slog.LevelInfo, fmt.Sprintf(
			"CANARY — limit=%d, examining the first %d of %d books; the remainder are untouched and a later run resumes from here",
			limit, len(ids), libraryTotal))
	}
	total := len(ids)
	if total == 0 {
		env.Progress(1, 1, "No books to examine")
		return t6g2NoResult{}, nil
	}
	env.Progress(0, total, "Migrating inline book signatures to the sidecar…")

	var (
		examined     atomic.Int64
		migrated     atomic.Int64
		strippedOnly atomic.Int64
		notCandidate atomic.Int64
		skippedRaced atomic.Int64
		errCount     atomic.Int64
	)

	pages := chunkIDs(ids, bookSigMigratePageSize)
	runErr := env.Items(ctx, len(pages), t6g2Loop{
		ProgressTotal: len(pages),
		// Label runs inside the worker goroutines: it reads only atomics.
		Label: func(i, t int) string {
			return fmt.Sprintf("Page %d/%d — %d migrated, %d stripped-only, %d raced",
				i+1, t, migrated.Load(), strippedOnly.Load(), skippedRaced.Load())
		},
	}, func(ctx context.Context, pi int) error {
		// CPU-bound work (JSON decode/encode) plus a small Pebble write per
		// candidate. Books partition by ID across disjoint pages, so workers
		// never contend for a row.
		for _, id := range pages[pi] {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			examined.Add(1)
			// A preview classifies through the store (a read): the writer does
			// not run in a preview, and must not be asked to.
			var outcome database.BookSigMigrateOutcome
			var err error
			if dryRun {
				outcome, err = store.ClassifyBookSigMigration(id)
			} else {
				outcome, err = env.Writer.MigrateBookSigToSidecar(id)
			}
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
	})

	candidates := migrated.Load() + strippedOnly.Load() + skippedRaced.Load()
	impliedMB := float64(candidates*bookSigInlineBytesPerBook) / (1024 * 1024)

	mode := "DRY RUN"
	verb := "would migrate"
	if !dryRun {
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
		"dry_run", dryRun,
		"examined", examined.Load(),
		"library_total", libraryTotal,
		"migrated", migrated.Load(),
		"stripped_only", strippedOnly.Load(),
		"not_candidate", notCandidate.Load(),
		"skipped_raced", skippedRaced.Load(),
		"errors", errCount.Load(),
		"implied_inline_mb", impliedMB)

	if skippedRaced.Load() > 0 {
		env.Log(slog.LevelInfo, fmt.Sprintf(
			"%d books were written by another path mid-migration and were skipped rather than reverted; re-run to pick them up",
			skippedRaced.Load()))
	}

	env.Log(slog.LevelInfo, report)
	env.Progress(len(pages), len(pages), report)
	return t6g2NoResult{}, runErr
}

// t6g2BookSigSidecarMigrateParallelism is HEAD's page pool: one worker per CPU
// (JSON decode/encode plus a small Pebble write per candidate).
func t6g2BookSigSidecarMigrateParallelism() tasks.Parallelism { return tasks.CPU() }

func t6g2BookSigSidecarMigrateMeta() t6g2Meta {
	return t6g2Meta{
		Kind:     t6g2KindBookSigSidecarMigrate,
		Title:    t6g2BookSigSidecarMigrateTitle,
		Help:     t6g2BookSigSidecarMigrateHelp,
		Effects:  tasks.Writes(t6g2Res("books")...),
		Resume:   tasks.ResumeDrop,
		Priority: tasks.PriorityLow,
		Timeout:  90 * time.Minute,
		Key:      t6g2BookSigSidecarMigrateID,
	}
}

// t6g2BookSigSidecarMigrateNativeParams is the native kind's params: the canary
// cap only. The mode is the run's mode, never a param.
type t6g2BookSigSidecarMigrateNativeParams struct {
	Limit int `json:"limit"`
}

func t6g2BookSigSidecarMigrateKind(deps t6g2Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	m := t6g2BookSigSidecarMigrateMeta()
	par := t6g2BookSigSidecarMigrateParallelism()
	run := func(rc *tasks.RC, raw json.RawMessage) (t6g2NoResult, error) {
		if err := t6g2RefuseModeKeys(m.Kind, raw); err != nil {
			return t6g2NoResult{}, err
		}
		var params t6g2BookSigSidecarMigrateNativeParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return t6g2NoResult{}, fmt.Errorf("%s: invalid params: %w", m.Kind, err)
			}
		}
		env, err := t6g2NativeEnv(rc, deps, par)
		if err != nil {
			return t6g2NoResult{}, err
		}
		return runBookSigSidecarMigrateCore(rc.Context(), params.Limit, !t6g2Live(rc), env)
	}
	return tasks.Task(m.Kind, tasks.TaskSpec[json.RawMessage, t6g2NoResult]{Common: m.common(sched), Run: run})
}
