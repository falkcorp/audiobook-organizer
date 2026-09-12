// file: internal/server/library_writeback_op.go
// version: 1.6.1
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-09-12

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	ulid "github.com/oklog/ulid/v2"
)

// bulkWriteBackOpParams is the JSON params for the library.bulk-write-back op.
//
// It doubles as the checkpoint payload: resumeRestart overlays the checkpoint
// onto the row's params, so BookIDs carries NO omitempty — an empty remaining
// set must replace the original list in the overlay rather than let it show
// through and re-write every file (see batch_apply_op.go for that incident).
type bulkWriteBackOpParams struct {
	BookIDs []string `json:"book_ids"`
	Rename  bool     `json:"rename"`
}

// bulkWriteBackCheckpointEvery is how many examined books sit between
// checkpoints. A write-back is a file rewrite per book, so an interrupt costs
// at most this many idempotent re-writes.
const bulkWriteBackCheckpointEvery = 25

func mergeBulkWriteBackQueuedParams(existing, incoming json.RawMessage) (json.RawMessage, bool, error) {
	var current, next bulkWriteBackOpParams
	if err := json.Unmarshal(existing, &current); err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(incoming, &next); err != nil {
		return nil, false, err
	}
	if current.Rename != next.Rename {
		return nil, false, nil
	}
	merged, err := json.Marshal(bulkWriteBackOpParams{
		BookIDs: mergeUniqueBookIDs(current.BookIDs, next.BookIDs),
		Rename:  current.Rename,
	})
	return merged, err == nil, err
}

// summarizeBulkWriteBackQueued reports the size of a queued bulk write-back.
// This op has no OriginalTotal to carry prior work across a resume, so the
// completed half is always zero and the total is simply what is queued.
func summarizeBulkWriteBackQueued(params json.RawMessage) (int, int, string) {
	p, ok := decodeQueuedParams[bulkWriteBackOpParams](params)
	if !ok {
		return 0, 0, ""
	}
	return formatQueuedBookSummary(0, len(p.BookIDs), "write back")
}

// RegisterBulkWriteBackOp registers the "library.bulk-write-back" v2 OperationDef.
// The HTTP handler handleBulkWriteBack pre-filters books and passes the resulting
// book IDs as params; the Run func executes the actual tag-write work.
func (s *Server) RegisterBulkWriteBackOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.bulk-write-back",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "library",
		DisplayName:     "Bulk Tag Write-back",
		Description:     "Write metadata from the database back to audio file tags for a set of audiobooks.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         6 * time.Hour,
		// RESUME AUDIT 2026-09-11 (a): keep ResumeRestart, and back it with a
		// real checkpoint. Until now the only checkpoint on this path was the v1
		// SaveCheckpoint inside runBulkWriteBack, keyed on a ULID minted per
		// attempt and never read back, so every restart rewrote every file from
		// the top of a six-hour run. Run now persists the remaining book ids as a
		// done-set (workers finish out of order, so a count or cursor would skip
		// stragglers) every bulkWriteBackCheckpointEvery books and once more on
		// cancel; resumeRestart overlays it onto params, and MergeQueuedParams
		// unions any newly queued books into that reduced set.
		ResumePolicy:      opsregistry.ResumeRestart,
		ConcurrencyKey:    "library.bulk-write-back",
		MergeQueuedParams: mergeBulkWriteBackQueuedParams,
		SummarizeQueued:   summarizeBulkWriteBackQueued,
		Permissions:       []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:      []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run:               s.runBulkWriteBackOp,
	})
}

// runBulkWriteBackOp is the Run body of library.bulk-write-back. It is a named
// method rather than a closure so the resume test can drive it with a
// recording reporter and a cancelled context.
func (s *Server) runBulkWriteBackOp(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) (retErr error) {
	var p bulkWriteBackOpParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &p); err != nil {
			return fmt.Errorf("bulk-write-back: decode params: %w", err)
		}
	}
	if len(p.BookIDs) == 0 {
		return nil
	}
	// Metadata is never applied during a library scan: hold the scan stand-down
	// before the first write (fails the op if the scan does not park).
	hold, sdErr := s.holdMetadataScanStandDown(ctx, reporter, "library.bulk-write-back apply")
	if sdErr != nil {
		return sdErr
	}
	defer func() { retErr = hold.Finish(retErr) }()
	ctx, reporter = hold.Context(), hold.Reporter()

	opID := ulid.Make().String()
	progress := registryProgressAdapter{r: reporter}

	// CHECKPOINTING. onDone fires after every examined book (written, failed
	// or skipped — see runBulkWriteBack), so the remaining set is exactly the
	// books this attempt has not yet looked at. ckptMu serialises the
	// Checkpoint calls so two workers crossing a cadence boundary together
	// cannot write an older remaining set over a newer one.
	done := newDoneSet(bulkWriteBackCheckpointEvery)
	var ckptMu sync.Mutex
	writeCheckpoint := func() {
		ckptMu.Lock()
		defer ckptMu.Unlock()
		if err := reporter.Checkpoint(bulkWriteBackOpParams{BookIDs: done.remaining(p.BookIDs), Rename: p.Rename}); err != nil {
			slog.Warn("bulk-write-back checkpoint failed", "opID", opID, "err", err)
		}
	}
	onDone := func(bookID string) {
		if done.mark(bookID) {
			writeCheckpoint()
		}
	}

	runErr := s.runBulkWriteBack(ctx, opID, p.BookIDs, p.Rename, 0, progress, onDone)
	if ctx.Err() != nil {
		// Final checkpoint on the way out, so the books examined since the last
		// periodic one are not re-owed by the resumed run. The store does not
		// take ctx, so this write succeeds under a cancelled context.
		writeCheckpoint()
	}
	if s.activityWriter != nil {
		activity.FlushOperation(s.activityWriter, opID)
		summary := fmt.Sprintf("Bulk tag write-back completed for %d books", len(p.BookIDs))
		if runErr != nil {
			summary = fmt.Sprintf("Bulk tag write-back failed: %v", runErr)
		}
		activity.EmitInfo(s.activityWriter, opID, "library.bulk-write-back", "library", summary, activity.AlwaysShow)
	}
	return runErr
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBulkWriteBackOp(reg) })
}
