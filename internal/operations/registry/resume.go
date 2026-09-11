// file: internal/operations/registry/resume.go
// version: 1.9.0
// guid: 3c4d5e6f-7a8b-9012-cdef-012345678901
// last-edited: 2026-09-11

package registry

import (
	"context"
	"encoding/json"
	"maps"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
)

// reconcileScanDefID is the legacy def-id for the file-hash sweep that must
// always be dropped on restart (it ignores ctx and can't be safely resumed).
const reconcileScanDefID = "reconcile_scan"

// resumeAfterStartup is called from Start() before the dispatcher begins.
// It walks operations_v2 rows with status='queued', 'running' or
// 'interrupted_quiesced' and applies the def's ResumePolicy:
//
//   - ResumeRestart: increment resume_count, dispatch with saved state.
//   - ResumeRequeue: clear state, re-insert as a fresh queued op.
//   - ResumeDrop: set status=interrupted_dropped.
//   - ResumeAsk: set status=interrupted_ask.
//   - ResumeUnspecified / unknown def: treat as ResumeDrop (logged).
//
// Special: any op whose def_id is "reconcile_scan" is always dropped,
// matching existing server_lifecycle.go behaviour.
//
// CANDIDATES COME FROM ListResumableOperationsV2, NOT ListActiveOperationsV2.
// The active index drops a row the moment its status stops being queued/running,
// so every interrupted_quiesced op was invisible here and never came back. See
// isResumableV2Status in pebble_store_ops_v2.go for the full incident history.
func (r *Registry) resumeAfterStartup(ctx context.Context) {
	rows, err := r.store.ListResumableOperationsV2()
	if err != nil {
		r.logger.Warn("registry: resumeAfterStartup: failed to list resumable ops", "error", err)
		return
	}
	if len(rows) == 0 {
		r.logger.Info("registry: resumeAfterStartup: no resumable ops")
		return
	}

	// Scan stand-down boot-consult (BLOCKING reboot-safety): if a stand-down
	// marker survived a restart, a maintenance op held the scan gate when the
	// process died. Its filesystem work is half-applied and the op goroutine is
	// gone — resuming the scan now would run it over that half-applied state, the
	// exact concurrency the gate prevents. Do NOT resume that scan this boot;
	// clear the marker and warn. The row stays interrupted_quiesced and a
	// subsequent marker-free boot resumes it (fail-safe: scan stopped is safe).
	deferredScanOpID := ""
	if marker, ok := r.readScanStandDownMarker(); ok {
		deferredScanOpID = marker.ScanOpID
		r.logger.Warn("registry: scan stand-down marker present at boot; deferring scan resume for reconcile",
			"holder_op_id", marker.HolderOpID, "scan_op_id", marker.ScanOpID,
			"hint", "an apply op holding the scan gate did not release before restart; "+
				"reconcile its partial writes before the next deploy resumes the scan")
		r.clearScanStandDown()
	}

	rows, superseded := supersedeStaleQuiesced(rows, r.defDeclaresMergeQueuedParams)
	for _, sup := range superseded {
		r.resumeDrop(sup.ID, "superseded: a newer run of this op exists")
	}
	if len(superseded) > 0 {
		r.logger.Info("registry: resumeAfterStartup: dropped superseded quiesced ops",
			"count", len(superseded))
	}
	r.logger.Info("registry: resumeAfterStartup: processing resumable ops", "count", len(rows))

	for _, row := range rows {

		// Scan stand-down deferral (see boot-consult above): leave this exact scan
		// row interrupted_quiesced this boot rather than resuming it. Not dropped —
		// a later marker-free boot resumes it from its checkpoint.
		if deferredScanOpID != "" && row.ID == deferredScanOpID {
			r.logger.Info("registry: leaving quiesced scan un-resumed this boot (stand-down reconcile)",
				"op_id", row.ID)
			continue
		}

		// Always drop reconcile_scan.
		if row.DefID == reconcileScanDefID {
			r.resumeDrop(row.ID, "reconcile_scan always dropped on restart")
			continue
		}

		r.mu.RLock()
		def, defOK := r.defs[row.DefID]
		r.mu.RUnlock()

		if !defOK {
			// Unknown def — treat as drop.
			r.logger.Warn("registry: resumeAfterStartup: unknown def, dropping",
				"op_id", row.ID, "def_id", row.DefID)
			r.resumeDrop(row.ID, "unknown def at startup")
			continue
		}

		switch def.ResumePolicy {
		case ResumeRestart:
			r.resumeRestart(ctx, row, def)
		case ResumeRequeue:
			r.resumeRequeue(ctx, row, def)
		case ResumeDrop:
			r.resumeDrop(row.ID, "ResumePolicy=drop")
		case ResumeAsk:
			r.resumeAsk(row.ID)
		default:
			// ResumeUnspecified was rejected at registration but may appear in
			// the DB if a def was deregistered. Treat as drop.
			r.logger.Warn("registry: resumeAfterStartup: unspecified resume policy, dropping",
				"op_id", row.ID, "def_id", row.DefID)
			r.resumeDrop(row.ID, "ResumePolicy=unspecified")
		}
	}
}

// supersedeStaleQuiesced splits the sweep's candidates into the ones to act on
// and the interrupted_quiesced ones that a newer run has made obsolete.
//
// WHY: including interrupted_quiesced rows means a def accumulates one candidate
// per interrupted run, forever — prod held 21 quiesced library.scan rows built up
// over a month of deploys. Handing all of them to ResumeRestart would launch 21
// concurrent full library scans on a single boot, which is far worse than the
// stall this fix exists to cure. The enqueue-time ConcurrencyKey dedupe does not
// help: resumeRestart flips the row straight to "queued" and never goes through
// Enqueue.
//
// The rule, per def_id:
//
//   - A queued/running row always wins. It is the live request; every
//     interrupted_quiesced row for that def is stale by construction.
//   - Otherwise the newest interrupted_quiesced row wins and the rest are
//     superseded. Ops carry ULID ids, which sort lexicographically by creation
//     time, so max(ID) is the most recently created run.
//
// Rows that are queued or running are NEVER superseded — this function only ever
// removes interrupted_quiesced rows, so the pre-existing sweep behaviour for the
// in-flight set is unchanged.
//
// EXCEPTION, via defMerges: the rule above assumes a def is one-run-per-def, so
// that any other run of it is doing the same work and the newest is a superset.
// That holds for library.scan, which this heuristic was written for. It is FALSE
// for a set-parameterized def — one whose runs each carry their own list of items
// (metadata.batch-apply-cached and friends). There, a queued row is a DIFFERENT
// batch of books, not a newer take on this one, and dropping the interrupted run
// discards its checkpoint and abandons every book it still owed. That is an
// ordinary deploy with a second batch queued, not an edge case.
//
// defMerges reports whether a def declares MergeQueuedParams, which is exactly
// the "runs carry their own item set and are unioned rather than replaced"
// property. Using the declaration rather than a def-id list means a new
// set-parameterized op is covered the day it is written. library.scan does not
// declare it, so the case this heuristic exists for is unchanged. A nil
// defMerges treats every def as one-run-per-def (the old behaviour).
func supersedeStaleQuiesced(
	rows []database.OperationV2Row,
	defMerges func(defID string) bool,
) (keep, superseded []database.OperationV2Row) {
	// Winner per def among the quiesced rows, and whether the def has a live row.
	hasLive := make(map[string]bool, len(rows))
	newestQuiesced := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Status == "interrupted_quiesced" {
			if row.ID > newestQuiesced[row.DefID] {
				newestQuiesced[row.DefID] = row.ID
			}
			continue
		}
		hasLive[row.DefID] = true
	}

	for _, row := range rows {
		if row.Status != "interrupted_quiesced" {
			keep = append(keep, row)
			continue
		}
		// A set-parameterized def's runs are not interchangeable, so a live row
		// never makes this one redundant. Its own newest-wins dedupe still
		// applies: two interrupted runs of the same def are still stale relative
		// to each other in the same way, and letting them all restart is the
		// pile-up this function exists to prevent.
		setParameterized := defMerges != nil && defMerges(row.DefID)
		if (hasLive[row.DefID] && !setParameterized) || row.ID != newestQuiesced[row.DefID] {
			superseded = append(superseded, row)
			continue
		}
		keep = append(keep, row)
	}
	return keep, superseded
}

// defDeclaresMergeQueuedParams reports whether a registered def unions new
// requests into an existing queued run rather than replacing it. See
// supersedeStaleQuiesced for why that distinction decides supersession.
func (r *Registry) defDeclaresMergeQueuedParams(defID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.defs[defID]
	return ok && def.MergeQueuedParams != nil
}

// resumeRestart increments resume_count, merges any saved checkpoint state
// into the re-queued row's params, resets status to queued, and signals the
// dispatcher. The dispatcher picks it up via ListQueuedOperationsV2 on its
// next cycle — same path as a fresh enqueue.
//
// Checkpoint state (schema_version=2, JSON) is merged into params so that
// the op's Run function receives it via json.Unmarshal(params, &state) on
// the resumed run. Schema_version=1 (gob) blobs are ignored — the op
// restarts from scratch once, which is safe for all idempotent ops.
//
// Returns false when the row could not be flipped back to "queued". In that
// case nothing is announced (no op.created, no dispatcher nudge) and the row is
// left in the resumable status it already had; see the reset block for why.
func (r *Registry) resumeRestart(ctx context.Context, row database.OperationV2Row, def OperationDef) bool {
	_ = ctx // context used only for cancel guard; dispatcher started after us

	if err := r.store.IncrementResumeCountV2(row.ID); err != nil {
		r.logger.Warn("registry: resumeAfterStartup: failed to increment resume_count",
			"op_id", row.ID, "error", err)
	}

	// Restore checkpoint state into params so Run can read it on resume.
	if stateRow, err := r.store.GetOpStateV2(row.ID); err == nil &&
		stateRow != nil && stateRow.SchemaVersion == 2 {
		if merged, mergeErr := mergeJSONParams([]byte(row.Params), stateRow.StateBlob); mergeErr == nil {
			if updateErr := r.store.UpdateOperationV2Params(row.ID, merged); updateErr != nil {
				r.logger.Warn("registry: resumeAfterStartup: failed to merge checkpoint into params",
					"op_id", row.ID, "error", updateErr)
			} else {
				row.Params = string(merged)
				r.logger.Info("registry: resumeAfterStartup: merged checkpoint state into params",
					"op_id", row.ID, "state_bytes", len(stateRow.StateBlob))

				// CONSUME the blob now that params carry it. Leaving it behind
				// makes it a stale authority that outlives the data it described:
				// the row is now "queued", so a def with MergeQueuedParams can
				// union newly requested work into these params, and a SECOND
				// restart before dispatch would overlay this same old blob back
				// on top and silently discard everything merged in between. That
				// is the 2026-08-21 incident shape (new items dropped while the
				// run reports success), reached by a different route.
				//
				// Safe to delete unconditionally: params were persisted first, so
				// the resume position is already durable, and an op that keeps
				// running simply writes a fresh checkpoint. Deleting after the
				// params write (never before) is what makes the ordering safe.
				if delErr := r.store.DeleteOpStateV2(row.ID); delErr != nil {
					r.logger.Warn("registry: resumeAfterStartup: failed to delete consumed checkpoint state",
						"op_id", row.ID, "error", delErr)
				}
			}
		}
	}

	// Reset status to queued so the dispatcher picks it up normally, and CLEAR
	// the interrupt-time CompletedAt back to nil. UpdateOperationV2Status cannot
	// un-set CompletedAt (nil = leave unchanged), so a plain status flip left the
	// stale stamp in place and the resumed op stayed out of the Active-Operations
	// timeline — a genuinely running resumed scan was invisible in the UI.
	//
	// If that write FAILS, stop here (OPS-01). The dispatcher only ever runs
	// what ListQueuedOperationsV2 returns from the store, so a row whose status
	// never became "queued" can never dispatch; publishing op.created for it
	// would tell every connected client about an op that is structurally unable
	// to start, and nothing would ever correct that. The row is deliberately
	// left in the status it already has (queued/running/interrupted_quiesced —
	// all of which ListResumableOperationsV2 returns), so the NEXT boot's sweep
	// retries it from the same position: the checkpoint has already been merged
	// into params above, and that write succeeded, so no position is lost. It
	// is NOT flipped to interrupted_ask/dropped: that is a second write to a
	// store that just refused one, and if it did land it would remove the row
	// from the automatic retry set and hand the user a decision the system can
	// make on its own once the store is healthy again. The failure is surfaced
	// twice — the process log at Error, and an op_errors_v2 row (best effort,
	// same store) so it shows in the op's own error list rather than only in
	// journalctl.
	if err := r.store.ResetOperationV2ForResume(row.ID); err != nil {
		r.logger.Error("registry: resume: failed to reset op for resume; leaving row un-announced for the next sweep",
			"op_id", row.ID, "def_id", def.ID, "status", row.Status, "error", err)
		r.recordResumeResetFailure(row, err)
		return false
	}

	// The row is queued again, and its params have just been rewritten to the
	// unfinished tail, so the size it advertises is from before the interrupt.
	// Restate it here rather than waiting for Run: for an op like
	// metadata.batch-apply-cached this is the COMMON path — every restart takes
	// it — and the row can then sit queued for a long time behind its own
	// ConcurrencyKey while displaying a count that predates the resume.
	//
	// Copy the summary back onto the local row as well. The op.created event
	// below is built from THIS struct, not from a re-read, so a row patched only
	// in the store would announce itself to every connected client as holding no
	// work and would not correct itself until the next poll.
	if done, total, message, written := r.persistQueuedSummary(
		def, row.ID, json.RawMessage(row.Params),
	); written {
		row.ProgressCurrent = done
		row.ProgressTotal = total
		row.ProgressMessage = message
	}

	r.logger.Info("registry: resumeAfterStartup: re-queued restart op",
		"op_id", row.ID, "def_id", def.ID, "resume_count_new", row.ResumeCount+1)

	// Emit op.created so the UI can pick the op back up — without this,
	// connected clients only ever see op.updated for a row they don't know
	// exists locally.
	row.Status = "queued"
	r.publishOpCreated(row, true)

	r.pingDispatch()
	return true
}

// recordResumeResetFailure writes an op_errors_v2 row for a resume whose
// status reset was refused by the store, the same table dbReporter feeds for
// Error-level log lines during a run. Best effort: the store has just failed a
// write, so this one may fail too, and that is only warned about — the process
// log already carries the Error line.
func (r *Registry) recordResumeResetFailure(row database.OperationV2Row, resetErr error) {
	attrs, _ := json.Marshal(map[string]string{
		"error":      resetErr.Error(),
		"row_status": row.Status,
		"hint":       "row left in its resumable status; the next startup resume sweep retries it",
	})
	errRow := database.OpErrorV2Row{
		OperationID: row.ID,
		Plugin:      row.Plugin,
		DefID:       row.DefID,
		Message:     "resume: failed to reset op for resume; op not announced as queued",
		Attrs:       string(attrs),
		OccurredAt:  time.Now().UTC(),
	}
	if err := r.store.InsertOpErrorV2(errRow); err != nil {
		r.logger.Warn("registry: resume: failed to record reset failure in op_errors_v2",
			"op_id", row.ID, "error", err)
	}
}

// resumeQuiescedOp re-queues a single interrupted_quiesced op from its
// checkpoint at runtime, used by releaseScanStandDown when the last gate holder
// releases. It reuses resumeRestart — the same verified checkpoint-merge path the
// boot sweep uses — rather than hand-rolling param reconstruction, so an op
// resumed after a stand-down and one resumed after a reboot take an identical
// path. A no-op if the row is missing, no longer quiesced (already terminal or
// resumed by another path), or its def is unknown.
func (r *Registry) resumeQuiescedOp(opID string) {
	row, err := r.store.GetOperationV2(opID)
	if err != nil || row == nil {
		r.logger.Warn("registry: resumeQuiescedOp: op row missing", "op_id", opID, "error", err)
		return
	}
	if row.Status != "interrupted_quiesced" {
		r.logger.Info("registry: resumeQuiescedOp: op not quiesced, skipping",
			"op_id", opID, "status", row.Status)
		return
	}
	r.mu.RLock()
	def, ok := r.defs[row.DefID]
	r.mu.RUnlock()
	if !ok {
		r.logger.Warn("registry: resumeQuiescedOp: unknown def, cannot resume",
			"op_id", opID, "def_id", row.DefID)
		return
	}
	if !r.resumeRestart(context.Background(), *row, def) {
		// resumeRestart has already logged and recorded the failure. Name the
		// runtime consequence here: nothing retries at runtime, so this scan
		// stays parked until the next startup sweep (or a fresh enqueue).
		r.logger.Error("registry: resumeQuiescedOp: scan could not be re-queued; it stays interrupted_quiesced until the next startup resume sweep",
			"op_id", opID, "def_id", row.DefID)
	}
}

// mergeJSONParams overlays checkpoint keys onto base params. Keys present in
// overlay take precedence; keys present only in base are preserved. Both base
// and overlay must be valid JSON objects (or empty/nil). Returns the merged
// JSON. Pure function — no store calls.
func mergeJSONParams(base, overlay []byte) ([]byte, error) {
	merged := make(map[string]any)
	if len(base) > 0 {
		if err := json.Unmarshal(base, &merged); err != nil {
			return nil, err
		}
	}
	if len(overlay) == 0 {
		return json.Marshal(merged)
	}
	var over map[string]any
	if err := json.Unmarshal(overlay, &over); err != nil {
		return nil, err
	}
	maps.Copy(merged, over)
	return json.Marshal(merged)
}

// resumeRequeue clears state and re-inserts as a brand-new queued op.
func (r *Registry) resumeRequeue(ctx context.Context, row database.OperationV2Row, def OperationDef) {
	_ = ctx

	// Clear any saved state.
	_ = r.store.DeleteOpStateV2(row.ID)

	// Mark the old op as dropped to avoid double-running.
	now := time.Now().UTC()
	msg := "requeued: original op replaced"
	_ = r.store.UpdateOperationV2Status(row.ID, "interrupted_dropped", nil, &now, &msg)

	// Insert a fresh queued row with a new ULID.
	newID := ulid.Make().String()
	newRow := database.OperationV2Row{
		ID:       newID,
		DefID:    row.DefID,
		Plugin:   row.Plugin,
		TraceID:  ulid.Make().String(),
		SpanID:   ulid.Make().String(),
		Status:   "queued",
		Priority: row.Priority,
		Params:   row.Params,
		QueuedAt: time.Now().UTC(),
	}
	stampQueuedSummary(def, &newRow)
	if err := r.store.InsertOperationV2(newRow); err != nil {
		r.logger.Warn("registry: resumeAfterStartup: failed to insert requeued op",
			"old_op_id", row.ID, "new_op_id", newID, "error", err)
		return
	}

	r.logger.Info("registry: resumeAfterStartup: requeued op",
		"old_op_id", row.ID, "new_op_id", newID, "def_id", def.ID)

	r.publishOpCreated(newRow, true)

	r.pingDispatch()
}

// resumeDrop sets status=interrupted_dropped.
func (r *Registry) resumeDrop(opID, reason string) {
	now := time.Now().UTC()
	_ = r.store.UpdateOperationV2Status(opID, "interrupted_dropped", nil, &now, &reason)
	r.logger.Info("registry: resumeAfterStartup: dropped op", "op_id", opID, "reason", reason)
}

// resumeAsk sets status=interrupted_ask.
func (r *Registry) resumeAsk(opID string) {
	now := time.Now().UTC()
	reason := "awaiting user decision"
	_ = r.store.UpdateOperationV2Status(opID, "interrupted_ask", nil, &now, &reason)
	r.logger.Info("registry: resumeAfterStartup: op awaiting user decision", "op_id", opID)
}
