// file: internal/operations/registry/resume.go
// version: 1.2.0
// guid: 3c4d5e6f-7a8b-9012-cdef-012345678901
// last-edited: 2026-06-24

package registry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
)

// reconcileScanDefID is the legacy def-id for the file-hash sweep that must
// always be dropped on restart (it ignores ctx and can't be safely resumed).
const reconcileScanDefID = "reconcile_scan"

// resumeAfterStartup is called from Start() before the dispatcher begins.
// It walks operations_v2 rows with status='queued' or status='running' and
// applies the def's ResumePolicy:
//
//   - ResumeRestart: increment resume_count, dispatch with saved state.
//   - ResumeRequeue: clear state, re-insert as a fresh queued op.
//   - ResumeDrop: set status=interrupted_dropped.
//   - ResumeAsk: set status=interrupted_ask.
//   - ResumeUnspecified / unknown def: treat as ResumeDrop (logged).
//
// Special: any op whose def_id is "reconcile_scan" is always dropped,
// matching existing server_lifecycle.go behaviour.
func (r *Registry) resumeAfterStartup(ctx context.Context) {
	rows, err := r.store.ListActiveOperationsV2()
	if err != nil {
		r.logger.Warn("registry: resumeAfterStartup: failed to list active ops", "error", err)
		return
	}
	if len(rows) == 0 {
		r.logger.Info("registry: resumeAfterStartup: no active ops to resume")
		return
	}
	r.logger.Info("registry: resumeAfterStartup: processing active ops", "count", len(rows))

	for _, row := range rows {
		row := row // capture

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

// resumeRestart increments resume_count, merges any saved checkpoint state
// into the re-queued row's params, resets status to queued, and signals the
// dispatcher. The dispatcher picks it up via ListQueuedOperationsV2 on its
// next cycle — same path as a fresh enqueue.
//
// Checkpoint state (schema_version=2, JSON) is merged into params so that
// the op's Run function receives it via json.Unmarshal(params, &state) on
// the resumed run. Schema_version=1 (gob) blobs are ignored — the op
// restarts from scratch once, which is safe for all idempotent ops.
func (r *Registry) resumeRestart(ctx context.Context, row database.OperationV2Row, def OperationDef) {
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
			}
		}
	}

	// Reset status to queued so the dispatcher picks it up normally.
	_ = r.store.UpdateOperationV2Status(row.ID, "queued", nil, nil, nil)

	r.logger.Info("registry: resumeAfterStartup: re-queued restart op",
		"op_id", row.ID, "def_id", def.ID, "resume_count_new", row.ResumeCount+1)

	// Emit op.created so the UI can pick the op back up — without this,
	// connected clients only ever see op.updated for a row they don't know
	// exists locally.
	row.Status = "queued"
	r.publishOpCreated(row, true)

	r.pingDispatch()
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
	for k, v := range over {
		merged[k] = v
	}
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
