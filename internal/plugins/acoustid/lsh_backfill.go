// file: internal/plugins/acoustid/lsh_backfill.go
// version: 1.6.0
// guid: 2c4d6e80-3b5a-4f9c-9b1d-7e8f0a2b4c6d
// last-edited: 2026-10-13

package acoustid

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

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
		Description:     "Walks every BookFile and populates the fpidx LSH index for rows that have a stored AcoustIDFingerprint but no fpidx_meta entry. Idempotent.",
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

func (p *Plugin) runLSHBackfill(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("database store not available")
	}

	log := reporter.Logger()
	startedAt := time.Now()

	// Optional fast-skip check: if the store exposes HasLSHIndex we skip
	// rows that already have an index entry, otherwise we re-write.
	checker, _ := database.AsCapability[lshIndexChecker](p.store)
	hasChecker := checker != nil

	// Frame 0: loading. Real N comes once we've listed the rows.
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Loading book files for LSH backfill…")

	files, err := p.store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("load book files: %w", err)
	}
	total := len(files)

	prog = sdk.NewProgress(reporter, total)
	prog.Start(fmt.Sprintf("Backfilling LSH index for %d book files…", total))

	if total == 0 {
		prog.Done("No book files to scan.")
		return nil
	}

	// Tallies are atomics: RunItems runs fn on a worker pool, and its Label
	// closure is called from the worker goroutines too, so a plain int here
	// would be a data race in both places.
	var indexed, skippedNoFP, skippedAlreadyIndexed, failed atomic.Int64

	if err := registry.RunItems(ctx, reporter, files, func(ctx context.Context, f database.BookFileCore) error {
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
			target, herr := p.store.GetBookFileByID(f.BookID, f.ID)
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
			if err := p.store.UpdateBookFile(target.ID, target); err != nil {
				log.Warn("acoustid lsh-backfill: update failed", "id", f.ID, "err", err)
				failed.Add(1)
			} else {
				indexed.Add(1)
			}
		}
		return nil
	}, registry.RunItemsOptions{
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
		Concurrency: runtime.NumCPU(),
		Label: func(i, t int) string {
			return fmt.Sprintf("LSH backfill %d/%d (indexed=%d skip-no-fp=%d skip-existing=%d fail=%d)",
				i+1, t, indexed.Load(), skippedNoFP.Load(), skippedAlreadyIndexed.Load(), failed.Load())
		},
	}); err != nil {
		return err
	}

	log.Info("acoustid lsh-backfill: complete",
		"indexed", indexed.Load(),
		"skipped_no_fp", skippedNoFP.Load(),
		"skipped_already_indexed", skippedAlreadyIndexed.Load(),
		"failed", failed.Load(),
		"elapsed", time.Since(startedAt).Round(time.Second))

	prog.Done(fmt.Sprintf("LSH backfill complete: indexed=%d skipped_no_fp=%d skipped_existing=%d failed=%d (elapsed %s)",
		indexed.Load(), skippedNoFP.Load(), skippedAlreadyIndexed.Load(), failed.Load(), time.Since(startedAt).Round(time.Second)))
	return nil
}
