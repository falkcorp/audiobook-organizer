// file: internal/operations/registry/finish.go
// version: 1.0.0
// guid: 7c1e5a42-9d3b-4f68-b0a7-3e2d8c6f1a95
// last-edited: 2026-10-30

package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// ErrOpNotFinishable is returned by FinishWithoutResuming for a run the third
// decision does not apply to: it is not a native v3 Batch or Fixer run, or it
// is not waiting in awaiting_decision. The HTTP layer maps it to 409.
var ErrOpNotFinishable = errors.New("registry: operation cannot be finished without resuming")

// CanFinishWithoutResuming reports whether a run of defID can take the third
// decision, "Finish without resuming": the def is registered through the v3
// adapter (Register) as a Batch or Fixer, the only kinds with a ledger and a
// Finish to run over it. A v2 def has neither, and its "finish" would be a
// full Run, which is Retry under another name, so it is refused. A former
// (renamed) def id resolves to its canonical def.
//
// The Activity page asks this per interrupted_ask row to decide whether to
// offer the button, and FinishWithoutResuming asks it again before it writes.
func (r *Registry) CanFinishWithoutResuming(defID string) bool {
	def, ok := r.lookupDef(defID)
	if !ok {
		return false
	}
	r.mu.RLock()
	v3, native := r.v3defs[def.ID]
	r.mu.RUnlock()
	return native && ops.Compile(v3).OpenBatch != nil
}

// FinishWithoutResuming is the third decision on a v3 run in awaiting_decision,
// next to Retry (RetryInterrupted) and Discard. A ResumeAsk run (the Fixer
// default) stopped by a shutdown skipped its Finish, leaving it to the resumed
// attempt, which would run it once over every attempt's ledger. When the
// person declines to resume, Discard deletes the record, ledger and marks, so
// rows the fixer already applied keep their writes but never get AfterApply.
// This decision runs the follow-up instead: the run goes back to queued under
// the SAME op id with RunRecord.FinishOnly set (state.EvDecideFinish), and its
// next attempt is dispatched like any other, with a fresh lease and fence. That
// attempt leases no chunks and reads no source; it runs only the def's Finish
// over the stored ledger and marks (batchConfig.finishOnly), so AfterApply
// sees exactly the rows the ledger records as applied. The run then ends
// succeeded, or failed when Finish errs.
//
// Rows a chunk applied but never completed carry a row marker and no ledger
// mark, so they are not in the applied set; recovering them needs the items
// to run again, which is what Retry is for.
//
// Queued-param merging cannot touch the re-queued row: buildV3Def sets neither
// MergeQueuedParams nor DedupeQueuedRuns, so no new enqueue is folded into it.
//
// Refusals, all before any write:
//   - ErrOpNotFound: no such row.
//   - ErrOpActive: the op still has a live run handle (a zombie included).
//   - ErrOpNotFinishable: no v3 record, the record is not awaiting_decision,
//     or the def cannot take the decision (CanFinishWithoutResuming).
//
// actor labels the op log line that marks the decision.
func (r *Registry) FinishWithoutResuming(ctx context.Context, opID, actor string) error {
	_ = ctx
	// Serialized with RetryInterrupted and the stand-down resume: all three
	// read an interrupted row and flip it back to queued.
	r.retryMu.Lock()
	defer r.retryMu.Unlock()

	requestedAt := time.Now().UTC()
	row, err := r.store.GetOperationV2(opID)
	if err != nil {
		return fmt.Errorf("registry: finish op %s: %w", opID, err)
	}
	if row == nil || row.ID == "" {
		return ErrOpNotFound
	}
	if r.hasLiveHandle(opID) {
		return fmt.Errorf("%w: op %s still has a live run handle", ErrOpActive, opID)
	}
	rec, err := r.getRunV3(opID)
	if err != nil {
		return fmt.Errorf("registry: finish op %s: read the run record: %w", opID, err)
	}
	if rec == nil {
		return fmt.Errorf("%w: op %s has no v3 run record", ErrOpNotFinishable, opID)
	}
	if rec.State != state.V3AwaitingDecision {
		return fmt.Errorf("%w: op %s is %s, not awaiting a decision", ErrOpNotFinishable, opID, rec.State)
	}
	if !r.CanFinishWithoutResuming(row.DefID) {
		return fmt.Errorf("%w: op %s's definition %q is not a registered v3 Batch or Fixer", ErrOpNotFinishable, opID, row.DefID)
	}
	// Counted under the "retry" entry: the metric's entry label is a closed
	// set, and this is a decision on an interrupted row, the same door.
	r.noteAliasUse(row.DefID, aliasEntryRetry)

	if _, err := r.transitionRunV3(database.TransitionRequest{
		ID: rec.ID, ExpectState: rec.State, ExpectAttempt: rec.Attempt, ExpectFenceEpoch: rec.FenceEpoch,
		Event: state.EvDecideFinish, Now: requestedAt,
	}); err != nil {
		if errors.Is(err, database.ErrStaleTransition) {
			return fmt.Errorf("%w: op %s changed while the decision was being made", ErrOpNotFinishable, opID)
		}
		return fmt.Errorf("registry: finish op %s: %w", opID, err)
	}
	r.logger.Info("registry: run queued to finish without resuming", "op_id", opID, "def_id", row.DefID)
	r.appendFinishLogLine(*row, actor, requestedAt)

	// The row is queued again: announce it so the UI picks it up, and nudge the
	// dispatcher (the same tail as requeueInPlace).
	row.Status = string(state.Queued)
	r.publishOpCreated(*row, true)
	r.pingDispatch()
	return nil
}

// appendFinishLogLine writes the boundary line into the op's own log, stamped
// at the moment the decision was made so it sorts before anything the
// finish-only attempt logs. Best effort: the decision already took effect.
func (r *Registry) appendFinishLogLine(row database.OperationV2Row, actor string, at time.Time) {
	if strings.TrimSpace(actor) == "" {
		actor = "an unidentified caller"
	}
	line := database.OpLogV2Row{
		OperationID: row.ID,
		Level:       "info",
		Message:     fmt.Sprintf("finish without resuming requested by %s; running this operation's Finish over the rows already applied, without leasing any work", actor),
		Attrs:       `{"event":"finish_without_resuming"}`,
		CreatedAt:   at,
	}
	if err := r.store.AppendOpLogsV2([]database.OpLogV2Row{line}); err != nil {
		r.logger.Warn("registry: finish: failed to append the decision log line", "op_id", row.ID, "error", err)
	}
}
