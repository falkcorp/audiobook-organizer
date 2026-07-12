// file: internal/plugins/dedup/check_book.go
// version: 1.0.1
// guid: 3f2e1d0c-b9a8-7654-3210-fedcba987654
// last-edited: 2026-07-12

// check_book.go implements the dedup.check-book UOS operation (M4).
//
// Design:
//   - Batchable: EnqueueOp("dedup.check-book", {"book_id": id}) parks the subject
//     into a per-op bucket. When the debounce window fires, one OperationV2Row is
//     created whose params carry all collected subjects as {"subjects":[...]}.
//   - Requires: {Kind: ReqFieldSet, Field: "book_sig_v1"} — the book-level audio
//     signature is set by the fingerprint aggregation pipeline; its presence guarantees
//     per-file AcoustID fingerprinting has completed. No discrete per-file
//     "acoustid.fingerprint-extract" UOS op exists in this codebase (the AcoustID
//     plugin exposes only library-wide scan/backfill ops that do not record per-book
//     completion records), so book_sig_v1 field-set is the correct dependency signal.
//     Metadata is extracted synchronously in the importer pipeline before db.CreateBook,
//     so no "metadata-apply" op requirement is needed.
//   - Run: iterates the batched subjects and calls engine.CheckBook for each, honouring
//     ctx cancellation between books.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/logging"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// bookChecker is the narrow interface used by runCheckBook. It is satisfied by
// *dedupengine.Engine in production and by a test double in tests.
type bookChecker interface {
	CheckBook(ctx context.Context, bookID string) (bool, error)
}

// checkBookParams is the batched params shape written by the registry when a
// dedup.check-book batch flushes. Each Subject carries Type="book" and the book ID.
type checkBookParams struct {
	Subjects []database.OpSubject `json:"subjects"`
}

// checkBookDef returns the OperationDef for dedup.check-book.
func (p *Plugin) checkBookDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.check-book",
		Plugin:      "dedup",
		DisplayName: "Dedup check (per-book)",
		Description: "Runs a per-book dedup check after import, coalescing burst enqueues " +
			"into a single batch. Requires the book audio signature to be set (book_sig_v1), " +
			"ensuring fingerprinting has completed before dedup analysis runs.",

		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "dedup.check-book",
		Cancellable:     true,
		Isolate:         false,
		// Timeout: 0 → registry default (120m in-process)

		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},

		// Requires: book_sig_v1 being set guarantees that per-file AcoustID fingerprinting
		// has been aggregated into the whole-book signature. No discrete fingerprint UOS op
		// exists at the per-book level, so ReqFieldSet is the correct dependency gate.
		// Metadata is synchronous in the import pipeline — no separate op requirement needed.
		Requires: []registry.Requirement{
			{Kind: registry.ReqFieldSet, Field: "book_sig_v1"},
		},

		// Batchable: true — burst enqueues from the import path are coalesced by the
		// M3 batch engine. BatchWindow and BatchMaxWait are left at zero to use registry
		// defaults (5s window, 60s hard cap).
		Batchable: true,

		NotifyLevel: registry.NotifyActivity, // background op; no bell badge needed

		Run: p.runCheckBook,
	}
}

// runCheckBook is the Run function for dedup.check-book. It receives a batched
// params payload produced by the M3 batch engine and calls engine.CheckBook for
// each subject, logging warnings on error. Context cancellation is honoured
// between books so the op can be cancelled cleanly mid-batch.
func (p *Plugin) runCheckBook(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	return runCheckBookWith(ctx, p.engine, raw, reporter)
}

// runCheckBookWith is the testable core of runCheckBook. It accepts a bookChecker
// so tests can inject a fake without constructing a real engine.
func runCheckBookWith(ctx context.Context, checker bookChecker, raw json.RawMessage, reporter sdk.Reporter) error {
	var params checkBookParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("dedup.check-book: unmarshal params: %w", err)
	}

	total := len(params.Subjects)
	if total == 0 {
		reporter.Logger().Info("dedup.check-book: no subjects in batch, nothing to do")
		return nil
	}

	reporter.Logger().Info("dedup.check-book: starting batch", "count", total)

	for i, sub := range params.Subjects {
		// Honour cancellation between books so the op exits cleanly.
		select {
		case <-ctx.Done():
			reporter.Logger().Info("dedup.check-book: context cancelled, stopping mid-batch",
				"processed", i, "total", total)
			return ctx.Err()
		default:
		}

		if sub.Type != "book" {
			reporter.Logger().Warn("dedup.check-book: unexpected subject type, skipping",
				"type", sub.Type, "id", sub.ID)
			continue
		}

		isDup, err := checker.CheckBook(ctx, sub.ID)
		if err != nil {
			// Mirror the eager import path: log warning, continue to next book.
			logging.Warn(ctx, "dedup.check-book CheckBook error", "id", sub.ID, "err", err)
			continue
		}

		if isDup {
			reporter.Logger().Info("dedup.check-book: duplicate candidate created",
				"book_id", sub.ID)
		}
	}

	reporter.Logger().Info("dedup.check-book: batch complete", "processed", total)
	return nil
}
