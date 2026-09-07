// file: internal/server/reconcile_ops_index.go
// version: 1.1.0
// guid: 2c8f5b91-7a34-4e60-b9d2-1f6e0a83c574
// last-edited: 2026-09-07

package server

import (
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Reconcile runs used to live on a v1 operations row that the HTTP handler
// minted; the run's preview payload was written back onto it and read out by
// latestReconcileScan. New runs write to their own v2 row instead.
//
// The transitional seam that read BOTH keyspaces was removed on 2026-09-07:
// nothing has minted a v1 row since the minter was retired, so the v1 side could
// only ever return pre-2026-08-23 history, and dropping that history is
// deliberate and authorized. What remains is the v1 SHAPE — these helpers still
// return database.Operation because the reconcile endpoints put that object
// straight into their response bodies and the frontend parses it. The shape is a
// wire contract; the keyspace is not.
const (
	reconcileScanDefIDV2  = "reconcile.scan"
	reconcileApplyDefIDV2 = "reconcile.apply"

	// The `type` strings the responses advertise. A run's KIND did not change
	// when its id did, and the frontend keys off these — they are part of the
	// wire contract, not leftovers of the v1 keyspace.
	reconcileScanLegacyType  = "reconcile_scan"
	reconcileApplyLegacyType = "reconcile"
)

// reconcileOpLister is the store slice these helpers read.
type reconcileOpLister interface {
	ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error)
	GetOperationV2(id string) (*database.OperationV2Row, error)
}

// reconcileV2RowAsOperation maps a v2 row onto the v1 shape.
//
// The reconcile endpoints put this object straight into their response bodies,
// and web/src/services/api.ts reads `raw.id` off the 202 and renders
// `operation` from the /latest payload. Handing back a different shape for a
// v2-keyed run would break the client for what is now the common case, so the
// row is translated rather than the contract changed.
//
// legacyType is what the response advertises as `type`. A run's KIND did not
// change when its id did, and the frontend keys off these strings.
func reconcileV2RowAsOperation(row *database.OperationV2Row, legacyType string) *database.Operation {
	if row == nil {
		return nil
	}
	op := &database.Operation{
		ID:           row.ID,
		Type:         legacyType,
		Status:       row.Status,
		Progress:     row.ProgressCurrent,
		Total:        row.ProgressTotal,
		Message:      row.ProgressMessage,
		CreatedAt:    row.QueuedAt,
		StartedAt:    row.StartedAt,
		CompletedAt:  row.CompletedAt,
		ErrorMessage: row.ErrorMessage,
		ResultData:   row.ResultData,
	}
	if row.ActorUserID != nil {
		op.UserID = *row.ActorUserID
	}
	return op
}

// reconcileOperationView loads one reconcile run by id from either keyspace.
//
// Used right after EnqueueOp so the handler can answer with the run it actually
// created. EnqueueOp may return the id of an ALREADY-ACTIVE op when it merges a
// duplicate request, which is why this reads the row back rather than
// synthesising one from what the handler happens to know.
//
// The v1 GetOperationByID fallback that followed was removed on 2026-09-07 with
// the rest of the v1 reads. An id minted before 2026-08-23 no longer resolves
// here; that is the accepted cost of dropping v1 history.
func reconcileOperationView(store reconcileOpLister, opID, legacyType string) *database.Operation {
	if row, err := store.GetOperationV2(opID); err == nil && row != nil {
		return reconcileV2RowAsOperation(row, legacyType)
	}
	return nil
}

// recentReconcileScans returns reconcile scans, newest first.
//
// The limit bounds the ANSWER, not the store scan. ListOperationsV2Since sorts
// StartedAt DESC NULLS LAST and truncates BEFORE this function can filter by
// DefID, so passing a small limit through would drop QUEUED rows first — they
// have no StartedAt and sort last. A scan that had just been enqueued would be
// the first thing to disappear from a view whose whole job is to show it.
func recentReconcileScans(store reconcileOpLister, limit int) []*database.Operation {
	if limit <= 0 {
		limit = 200
	}
	const storeScanBound = 5000

	var out []*database.Operation
	seen := make(map[string]bool)

	if rows, err := store.ListOperationsV2Since(time.Time{}, storeScanBound); err == nil {
		for i := range rows {
			row := rows[i]
			if row.DefID != reconcileScanDefIDV2 || seen[row.ID] {
				continue
			}
			seen[row.ID] = true
			out = append(out, reconcileV2RowAsOperation(&row, reconcileScanLegacyType))
		}
	}

	// The v1 fallback that used to follow is gone (2026-09-07). It scanned the
	// `operation:` keyspace for rows this index could still show, but nothing has
	// minted a v1 row since the minter was retired, so it could only ever return
	// pre-2026-08-23 history. Dropping that history is deliberate and authorized.

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
