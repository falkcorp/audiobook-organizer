// file: internal/server/reconcile_ops_index.go
// version: 1.0.0
// guid: 2c8f5b91-7a34-4e60-b9d2-1f6e0a83c574
// last-edited: 2026-08-22

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
// These two helpers are the whole of the transitional seam. Both keyspaces are
// read until the v1 rows age out, and keeping that decision in one file means
// dropping v1 later is a single deletion rather than an archaeology exercise.
const (
	reconcileScanDefIDV2  = "reconcile.scan"
	reconcileApplyDefIDV2 = "reconcile.apply"

	// v1 type strings. New runs no longer write these.
	reconcileScanLegacyType  = "reconcile_scan"
	reconcileApplyLegacyType = "reconcile"
)

// reconcileOpLister is the store slice these helpers read.
type reconcileOpLister interface {
	ListOperations(limit int, offset int) ([]database.Operation, int, error)
	ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error)
	GetOperationV2(id string) (*database.OperationV2Row, error)
	GetOperationByID(id string) (*database.Operation, error)
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
func reconcileOperationView(store reconcileOpLister, opID, legacyType string) *database.Operation {
	if row, err := store.GetOperationV2(opID); err == nil && row != nil {
		return reconcileV2RowAsOperation(row, legacyType)
	}
	if op, err := store.GetOperationByID(opID); err == nil && op != nil {
		return op
	}
	return nil
}

// recentReconcileScans returns reconcile scans from both keyspaces, newest
// first.
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

	if ops, _, err := store.ListOperations(storeScanBound, 0); err == nil {
		for i := range ops {
			op := ops[i]
			if op.Type != reconcileScanLegacyType || seen[op.ID] {
				continue
			}
			seen[op.ID] = true
			out = append(out, &op)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
