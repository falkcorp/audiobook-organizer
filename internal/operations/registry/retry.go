// file: internal/operations/registry/retry.go
// version: 1.0.0
// guid: 0b6f3d2e-9a41-4c7e-8f25-6d1e7a3c9b58
// last-edited: 2026-09-12

package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrOpNotRetryable is returned by RetryInterrupted for a row that cannot be
// re-queued in place: it is not in the interrupted family, or its def is no
// longer registered. The HTTP layer maps it to 409.
var ErrOpNotRetryable = errors.New("registry: operation cannot be retried in place")

// IsInterruptedStatus reports whether a v2 status belongs to the interrupted
// family: the legacy bare "interrupted" plus every "interrupted_*" status the
// registry mints (quiesced, ask, restart, dropped). Prefix-matched on purpose,
// so a status added later is covered the day it is minted. This is the single
// server-side definition; the handler's retry predicate and RetryInterrupted
// both use it, and web/src/utils/operationPolling.ts isInterrupted mirrors it.
func IsInterruptedStatus(status string) bool {
	return status == "interrupted" || strings.HasPrefix(status, "interrupted_")
}

// RetryInterrupted is the operator's Retry on an interrupted run. It re-queues
// the SAME row — same id, so the run's logs continue under one id and the row
// leaves the Interrupted list — instead of minting a new row next to it.
//
// Minting a new row (what the handler used to do for every status) left the
// old one behind: in Interrupted forever and, for interrupted_quiesced, in the
// boot resume set. supersedeStaleQuiesced drops such a row only if a
// queued/running row of the def exists at that instant, so once the retry had
// finished, the next restart ran the same work a second time. Re-queuing the
// row itself removes that path: the row ends completed/failed like any other.
//
// Checkpoint: for Restart, Ask and Drop defs a saved checkpoint is merged into
// params (requeueInPlace, the same path the boot sweep uses), so the run
// resumes where it stopped. A ResumeRequeue def declares "re-run from zero",
// so its checkpoint is deleted first and the row reruns with its params. A row
// whose checkpoint the retention sweep already removed (interrupted_dropped is
// terminal to it) simply restarts from zero.
//
// A manual retry does not increment resume_count; MarkOperationV2ManualRetry
// moves the restart-strike baseline instead (see checkInfiniteRestart).
//
// Refusals, all before any write:
//   - ErrOpNotFound: no such row.
//   - ErrOpNotRetryable: not in the interrupted family, or def unregistered.
//   - ErrOpActive: the op still has a live run handle, its abandoned Run
//     goroutine has not returned, or another queued/running run of the same
//     def exists. The boot sweep's supersedeStaleQuiesced and the enqueue-time
//     ConcurrencyKey dedupe never see this path, so this is the double-run
//     guard. A def with MergeQueuedParams is exempt from the same-def check:
//     its runs each carry their own item set, so another run is different
//     work (the same exemption supersedeStaleQuiesced makes).
//
// actor labels the op log line that marks the retry boundary.
func (r *Registry) RetryInterrupted(ctx context.Context, opID, actor string) error {
	_ = ctx
	r.retryMu.Lock()
	defer r.retryMu.Unlock()

	requestedAt := time.Now().UTC()

	row, err := r.store.GetOperationV2(opID)
	if err != nil {
		return fmt.Errorf("registry: retry op %s: %w", opID, err)
	}
	if row == nil || row.ID == "" {
		return ErrOpNotFound
	}
	if !IsInterruptedStatus(row.Status) {
		return fmt.Errorf("%w: op %s is %s, not interrupted", ErrOpNotRetryable, opID, row.Status)
	}

	r.mu.RLock()
	def, defOK := r.defs[row.DefID]
	_, live := r.running[opID]
	_, runaway := r.abandonedAlive[opID]
	r.mu.RUnlock()
	if !defOK {
		return fmt.Errorf("%w: op %s's definition %q is not registered", ErrOpNotRetryable, opID, row.DefID)
	}
	if live {
		return fmt.Errorf("%w: op %s still has a live run handle", ErrOpActive, opID)
	}
	if runaway {
		return fmt.Errorf("%w: op %s's previous run was abandoned by the watchdog and has not exited yet; retry once it does",
			ErrOpActive, opID)
	}
	other, err := r.activeRunOfDef(def, opID)
	if err != nil {
		return fmt.Errorf("registry: retry op %s: list active ops: %w", opID, err)
	}
	if other != "" {
		return fmt.Errorf("%w: another run of %s (%s) is already queued or running", ErrOpActive, def.ID, other)
	}

	// Baseline first: if this write fails, nothing has been re-queued, so the
	// row cannot reach checkInfiniteRestart still carrying the old count.
	if err := r.store.MarkOperationV2ManualRetry(opID); err != nil {
		return fmt.Errorf("registry: retry op %s: record manual retry: %w", opID, err)
	}
	if def.ResumePolicy == ResumeRequeue {
		if err := r.store.DeleteOpStateV2(opID); err != nil {
			r.logger.Warn("registry: retry: failed to clear checkpoint of a requeue-policy op",
				"op_id", opID, "error", err)
		}
	}
	previous := row.Status
	if !r.requeueInPlace(*row, def, "manual retry") {
		return fmt.Errorf("registry: retry op %s: the store refused to reset it to queued", opID)
	}
	r.appendRetryLogLine(*row, previous, actor, requestedAt)
	return nil
}

// activeRunOfDef returns the id of another queued/running run of def, or "".
// Mirrors EnqueueOp's dedupe source (ListActiveOperationsV2) and its C-3 rule:
// a "running" row with no live handle is a crash leftover and does not count,
// or a stale row would refuse every retry of the def until restart.
func (r *Registry) activeRunOfDef(def OperationDef, excludeID string) (string, error) {
	if def.MergeQueuedParams != nil {
		return "", nil
	}
	active, err := r.store.ListActiveOperationsV2()
	if err != nil {
		return "", err
	}
	for _, op := range active {
		if op.DefID != def.ID || op.ID == excludeID {
			continue
		}
		if op.Status == "running" && !r.hasLiveHandle(op.ID) {
			continue
		}
		return op.ID, nil
	}
	return "", nil
}

// appendRetryLogLine writes the boundary line into the op's own log, stamped
// at the moment the retry was requested so it sorts before anything the
// resumed run logs. Best effort: the retry already happened.
func (r *Registry) appendRetryLogLine(row database.OperationV2Row, previous, actor string, at time.Time) {
	if strings.TrimSpace(actor) == "" {
		actor = "an unidentified caller"
	}
	line := database.OpLogV2Row{
		OperationID: row.ID,
		Level:       "info",
		Message:     fmt.Sprintf("manual retry requested by %s (was %s); resuming this operation in place", actor, previous),
		Attrs:       `{"event":"manual_retry"}`,
		CreatedAt:   at,
	}
	if err := r.store.AppendOpLogsV2([]database.OpLogV2Row{line}); err != nil {
		r.logger.Warn("registry: retry: failed to append retry log line", "op_id", row.ID, "error", err)
	}
}
