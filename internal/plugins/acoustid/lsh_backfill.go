// file: internal/plugins/acoustid/lsh_backfill.go
// version: 1.7.0
// guid: 2c4d6e80-3b5a-4f9c-9b1d-7e8f0a2b4c6d
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// lshBackfillKind is rule R3's native kind for acoustid.lsh-backfill.
const lshBackfillKind = tasks.Kind("books.acoustid.lsh-backfill")

// lshBackfillHelp is the definition's description, HEAD's and the native kind's
// Help (rule R5: verbatim).
const lshBackfillHelp = "Walks every BookFile and populates the fpidx LSH index for rows that have a stored AcoustIDFingerprint but no fpidx_meta entry. Idempotent."

// lshBackfillEnv is what the lsh-backfill core takes from its shell (rules R2
// and R10): reads through Store, the one write through Writer.
type lshBackfillEnv struct {
	kindEnv
	Store  kindStore
	Writer kindWriter
}

// lshBackfillResult is the native run's result: the tallies HEAD logged and put
// in its closing message. HEAD stored no result, so its shell drops it.
type lshBackfillResult struct {
	Total                 int `json:"total"`
	Indexed               int `json:"indexed"`
	SkippedNoFingerprint  int `json:"skipped_no_fingerprint"`
	SkippedAlreadyIndexed int `json:"skipped_already_indexed"`
	Failed                int `json:"failed"`
}

// lshBackfillParallelism is the item pool (rule R4): HEAD's
// RunItemsOptions.Concurrency is runtime.NumCPU() (lshBackfillConcurrency), one
// DB read plus one DB write per row with the index hook's CPU in the write.
func lshBackfillParallelism() tasks.Parallelism { return tasks.CPU() }

// lshIndexChecker is satisfied by any store that can answer "do I already
// have an LSH index row for this BookFile?". The PebbleStore agent on the
// sibling branch is adding this method; until it ships, the type assertion
// just fails and we fall through to the unconditional rewrite path. Either
// way the op is idempotent — UpdateBookFile delete-then-writes the index.
type lshIndexChecker interface {
	HasLSHIndex(bookFileID string) bool
}

// lshBackfillDef registers acoustid.lsh-backfill — the one-shot admin op
// that walks every BookFile with a whole-file fingerprint and forces
// the LSH secondary index (`fpidx:` + `fpidx_meta:`) to be (re)written.
//
// The index hook fires inside PebbleStore.UpdateBookFile, so the operation
// itself does nothing fancy: it filters for rows that have a whole-file fp
// but no existing fpidx_meta entry, then re-saves them. Safe to re-run — if
// the index is already present the row is skipped (via HasLSHIndex when the
// store implements it) or harmlessly rewritten (when it does not).
//
// Gate uses AcoustIDFingerprintDurationSec (>0 ⇒ a whole-file fingerprint
// exists) rather than len(AcoustIDFingerprint) == 0, because
// GetAllBookFilesCore returns BookFileCore — the raw AcoustIDFingerprint blob
// is a heavy field that never exists on Core. DurationSec is the KEPT proxy.
//
// The row we re-save is hydrated via GetBookFiles(bookID) first and the
// UpdateBookFile call writes THAT full row — not the Core candidate (which
// has no fingerprint bytes at all and can't be passed to UpdateBookFile
// without either a compile error or a hand-built BookFile{} literal that
// would wipe the stored fingerprint). This drops the prior reliance on
// PebbleStore.UpdateBookFile's preserve-on-empty guard, which only restores
// AcoustIDFingerprint — not the diagnostic fields
// (FingerprintFailureReason/Detail/DiagnosticJSON) — so a hydrate-then-write
// here also closes that residual gap for this op. See
// docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md.
//
// Use after deploying the LSH index code to populate the existing ~308K
// fingerprinted rows without re-running fpcalc.
func (p *Plugin) lshBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.lsh-backfill",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "acoustid",
		DisplayName:     "Backfill LSH fingerprint index",
		Description:     lshBackfillHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "acoustid.fingerprint",
		Cancellable:     true,
		Timeout:         2 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runLSHBackfill,
	}
}

// lshBackfillConcurrency is the size of the worker pool the core hands its
// item loop. One row per item and the work is a DB read plus a DB write
// (CPU for the index hook), so the pool is sized to the cores. It is a function,
// not an inline expression, so the cancel test can derive its guaranteed bound
// (cancel point + in-flight workers) from the same value the op uses: it reads
// the declared parallelism, runtime.NumCPU() for tasks.CPU().
func lshBackfillConcurrency() int { return lshBackfillParallelism().Workers() }

func (p *Plugin) runLSHBackfill(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_, err := lshBackfillCore(ctx, p.headLSHBackfillEnv(reporter))
	return err
}

// headLSHBackfillEnv is HEAD's env: the plugin's store for reads and for the
// write. A plugin with no store hands the core a nil Store, which it refuses
// with HEAD's message.
func (p *Plugin) headLSHBackfillEnv(reporter sdk.Reporter) lshBackfillEnv {
	env := lshBackfillEnv{kindEnv: headEnv(reporter, true)}
	if p.store != nil {
		env.Store = p.store
		env.Writer = headKindWriter{store: p.store}
	}
	return env
}

// lshBackfillCore is the body of runLSHBackfill, shared by both shells.
func lshBackfillCore(ctx context.Context, env lshBackfillEnv) (lshBackfillResult, error) {
	if env.Store == nil {
		return lshBackfillResult{}, fmt.Errorf("database store not available")
	}

	log := env.Logger
	startedAt := time.Now()

	// Optional fast-skip check: if the store exposes HasLSHIndex we skip
	// rows that already have an index entry, otherwise we re-write.
	checker, _ := database.AsCapability[lshIndexChecker](env.Store)
	hasChecker := checker != nil

	// Frame 0: loading. Real N comes once we've listed the rows.
	prog := newProgress(env.kindEnv, 0)
	prog.Start("Loading book files for LSH backfill…")

	files, err := env.Store.GetAllBookFilesCore()
	if err != nil {
		return lshBackfillResult{}, fmt.Errorf("load book files: %w", err)
	}
	total := len(files)

	prog = newProgress(env.kindEnv, total)
	prog.Start(fmt.Sprintf("Backfilling LSH index for %d book files…", total))

	if total == 0 {
		prog.Done("No book files to scan.")
		return lshBackfillResult{}, nil
	}

	// Tallies are atomics: the item loop runs fn on a worker pool, and its Label
	// closure is called from the worker goroutines too (HEAD's registry.RunItems
	// invokes Label inside each worker), so a plain int here would be a data
	// race in both places. The Label is a counter label, so only HEAD's pool
	// carries it (seams.d/F5-05.md).
	var indexed, skippedNoFP, skippedAlreadyIndexed, failed atomic.Int64

	if err := env.Items(ctx, lshBackfillParallelism(), total, kindItemsOptions{
		// One row per item and the work is a DB read plus a DB write (CPU
		// for the index hook), so size the pool to the cores. Rows are
		// disjoint: each item writes only its own book_file row, and
		// PebbleStore serializes writers to one row on a per-file lock.
		// Two files of one book can run together. UpdateBookFile does
		// recompute that book's aggregates after each write, and the
		// recompute reads sibling rows outside any per-book lock, but this
		// re-save changes no path, Duration or FileSize, so every snapshot
		// sums to the same totals and whichever recompute commits last
		// writes the same value.
		Label: func(i, t int) string {
			return fmt.Sprintf("LSH backfill %d/%d (indexed=%d skip-no-fp=%d skip-existing=%d fail=%d)",
				i+1, t, indexed.Load(), skippedNoFP.Load(), skippedAlreadyIndexed.Load(), failed.Load())
		},
	}, func(ctx context.Context, i int) error {
		f := files[i]
		// A cancelled op must not start another write. The pool only checks
		// ctx while dispatching, and its dispatcher picks at random when a
		// worker slot is free AND ctx is done, so items already handed to a
		// worker (and a few dispatched after the cancel) reach this function
		// with a dead ctx. Without this guard each of them still hydrates and
		// rewrites its row. The skipped item is not a failure and is not
		// counted; the cancel is reported once, after the pool returns.
		if ctx.Err() != nil {
			return nil
		}
		// Proxy gate: AcoustIDFingerprintDurationSec > 0 means a whole-file
		// fingerprint was computed, even though BookFileCore never carries
		// the AcoustIDFingerprint bytes themselves. See the doc comment
		// above for why this is safe.
		if f.AcoustIDFingerprintDurationSec == 0 {
			skippedNoFP.Add(1)
		} else if hasChecker && checker.HasLSHIndex(f.ID) {
			skippedAlreadyIndexed.Add(1)
		} else {
			// Hydrate the full row and re-save THAT so
			// PebbleStore.writeBookFileSecondaryIndexes writes the fpidx +
			// fpidx_meta entries with the real fingerprint bytes intact.
			target, herr := env.Store.GetBookFileByID(f.BookID, f.ID)
			if herr != nil {
				log.Warn("acoustid lsh-backfill: hydrate failed", "id", f.ID, "err", herr)
				failed.Add(1)
				return nil
			}
			if target == nil {
				log.Warn("acoustid lsh-backfill: hydrate: row not found", "id", f.ID)
				failed.Add(1)
				return nil
			}
			if err := env.Writer.UpdateBookFile(target.ID, target); err != nil {
				log.Warn("acoustid lsh-backfill: update failed", "id", f.ID, "err", err)
				failed.Add(1)
			} else {
				indexed.Add(1)
			}
		}
		return nil
	}); err != nil {
		return lshBackfillResult{}, err
	}
	// Items skipped by the per-item guard return nil, and a sequential
	// pool (one core) can finish its last item without re-checking ctx,
	// so surface the cancel here rather than report a partial run complete.
	if err := ctx.Err(); err != nil {
		return lshBackfillResult{}, err
	}

	log.Info("acoustid lsh-backfill: complete",
		"indexed", indexed.Load(),
		"skipped_no_fp", skippedNoFP.Load(),
		"skipped_already_indexed", skippedAlreadyIndexed.Load(),
		"failed", failed.Load(),
		"elapsed", time.Since(startedAt).Round(time.Second))

	prog.Done(fmt.Sprintf("LSH backfill complete: indexed=%d skipped_no_fp=%d skipped_existing=%d failed=%d (elapsed %s)",
		indexed.Load(), skippedNoFP.Load(), skippedAlreadyIndexed.Load(), failed.Load(), time.Since(startedAt).Round(time.Second)))
	return lshBackfillResult{
		Total: total, Indexed: int(indexed.Load()), SkippedNoFingerprint: int(skippedNoFP.Load()),
		SkippedAlreadyIndexed: int(skippedAlreadyIndexed.Load()), Failed: int(failed.Load()),
	}, nil
}
