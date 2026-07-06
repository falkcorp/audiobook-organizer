// file: internal/plugins/acoustid/lsh_backfill.go
// version: 1.2.0
// guid: 2c4d6e80-3b5a-4f9c-9b1d-7e8f0a2b4c6d
// last-edited: 2026-07-06

package acoustid

import (
	"context"
	"encoding/json"
	"fmt"
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
// exists) rather than len(AcoustIDFingerprint) == 0, because GetAllBookFiles
// returns the memdb-slim projection in production (UseMemDB=true) where
// stripBookFileForMemdb nils the raw AcoustIDFingerprint blob to save RAM —
// the byte-length gate was always true under memdb, making this op a silent
// no-op. The row we re-save still carries a nil AcoustIDFingerprint, but
// PebbleStore.UpdateBookFile restores the stored blob from Pebble whenever
// the incoming value is empty (the "preserve-on-empty" guard added for the
// same memdb-slim-roundtrip class of bug) *before* writeBookFileSecondaryIndexes
// runs, so writeFingerprintLSHIndexes still sees the real fingerprint bytes.
// No hydrate call is needed here — UpdateBookFile does it for us.
//
// Use after deploying the LSH index code to populate the existing ~308K
// fingerprinted rows without re-running fpcalc.
func (p *Plugin) lshBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.lsh-backfill",
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
	checker, _ := p.store.(lshIndexChecker)
	hasChecker := checker != nil

	// Frame 0: loading. Real N comes once we've listed the rows.
	prog := sdk.NewProgress(reporter, 0)
	prog.Start("Loading book files for LSH backfill…")

	files, err := p.store.GetAllBookFiles()
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

	var indexed, skippedNoFP, skippedAlreadyIndexed, failed int

	if err := registry.RunItems(ctx, reporter, files, func(ctx context.Context, f database.BookFile) error {
		// Proxy gate: AcoustIDFingerprintDurationSec > 0 means a whole-file
		// fingerprint was computed, even when the memdb-slim row we're
		// holding has AcoustIDFingerprint stripped to nil. See the doc
		// comment above for why this is safe to re-save unmodified.
		if f.AcoustIDFingerprintDurationSec == 0 {
			skippedNoFP++
		} else if hasChecker && checker.HasLSHIndex(f.ID) {
			skippedAlreadyIndexed++
		} else {
			// Re-save the row so PebbleStore.writeBookFileSecondaryIndexes writes
			// the fpidx + fpidx_meta entries. Passed unmodified — just a hook trigger.
			updated := f
			if err := p.store.UpdateBookFile(f.ID, &updated); err != nil {
				log.Warn("acoustid lsh-backfill: update failed", "id", f.ID, "err", err)
				failed++
			} else {
				indexed++
			}
		}
		return nil
	}, registry.RunItemsOptions{
		Label: func(i, t int) string {
			return fmt.Sprintf("LSH backfill %d/%d (indexed=%d skip-no-fp=%d skip-existing=%d fail=%d)",
				i+1, t, indexed, skippedNoFP, skippedAlreadyIndexed, failed)
		},
	}); err != nil {
		return err
	}

	log.Info("acoustid lsh-backfill: complete",
		"indexed", indexed,
		"skipped_no_fp", skippedNoFP,
		"skipped_already_indexed", skippedAlreadyIndexed,
		"failed", failed,
		"elapsed", time.Since(startedAt).Round(time.Second))

	prog.Done(fmt.Sprintf("LSH backfill complete: indexed=%d skipped_no_fp=%d skipped_existing=%d failed=%d (elapsed %s)",
		indexed, skippedNoFP, skippedAlreadyIndexed, failed, time.Since(startedAt).Round(time.Second)))
	return nil
}
