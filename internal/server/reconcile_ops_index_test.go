// file: internal/server/reconcile_ops_index_test.go
// version: 1.3.0
// guid: 6d0a3c84-1b57-4e92-8f36-9c2e5a710db4
// last-edited: 2026-09-07

package server

import (
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// reconcileIndexStore models the two listings. ListOperationsV2Since is modelled
// faithfully on the point that matters: it sorts StartedAt DESC NULLS LAST and
// truncates to `limit` BEFORE the caller can filter by DefID.
type reconcileIndexStore struct {
	v1     []database.Operation
	v1Err  error
	v2     []database.OperationV2Row
	v2Err  error
	v2ByID map[string]*database.OperationV2Row
	v1ByID map[string]*database.Operation

	// The v1 methods are still implemented so the fake can prove they are never
	// reached. A fake that simply dropped them would let a reintroduced v1 read
	// fail to compile in the test while compiling fine in production.
	v1Calls int
}

// Honours limit. It sorts on CreatedAt, which is never nil, so it lacks the
// NULLS-LAST trap the v2 listing has — but it still truncates BEFORE the caller
// can filter by Type, so a small limit passed through would drop old reconcile
// rows behind newer rows of other types. Ignoring limit here would leave the v1
// half of storeScanBound guarded by nothing.
func (s *reconcileIndexStore) ListOperations(limit, offset int) ([]database.Operation, int, error) {
	s.v1Calls++
	if s.v1Err != nil {
		return nil, 0, s.v1Err
	}
	all := append([]database.Operation(nil), s.v1...)
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, len(s.v1), nil
}

func (s *reconcileIndexStore) ListOperationsV2Since(_ time.Time, limit int) ([]database.OperationV2Row, error) {
	if s.v2Err != nil {
		return nil, s.v2Err
	}
	all := append([]database.OperationV2Row(nil), s.v2...)
	sort.SliceStable(all, func(i, j int) bool {
		si, sj := all[i].StartedAt, all[j].StartedAt
		if si == nil && sj == nil {
			return all[i].QueuedAt.After(all[j].QueuedAt)
		}
		if si == nil {
			return false
		}
		if sj == nil {
			return true
		}
		return si.After(*sj)
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (s *reconcileIndexStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	if s.v2ByID == nil {
		return nil, nil
	}
	return s.v2ByID[id], nil
}

func (s *reconcileIndexStore) GetOperationByID(id string) (*database.Operation, error) {
	s.v1Calls++
	if s.v1ByID == nil {
		return nil, nil
	}
	return s.v1ByID[id], nil
}

// ---------------------------------------------------------------------------

// The response shape is a contract: web/src/services/api.ts reads raw.id off the
// 202 and renders `operation` from /latest. A v2-keyed run has to arrive in the
// same shape a v1 one did.
func TestReconcileV2RowAsOperation_PreservesTheResponseShape(t *testing.T) {
	now := time.Now()
	done := now.Add(time.Minute)
	payload := `{"broken":1}`
	row := &database.OperationV2Row{
		ID: "op-1", DefID: reconcileScanDefIDV2, Status: "completed",
		ProgressCurrent: 7, ProgressTotal: 7, ProgressMessage: "done",
		QueuedAt: now, CompletedAt: &done, ResultData: &payload,
	}
	op := reconcileV2RowAsOperation(row, reconcileScanLegacyType)

	if op.ID != "op-1" {
		t.Errorf("id lost: %q", op.ID)
	}
	if op.Type != reconcileScanLegacyType {
		t.Errorf("the client keys off type; want %q got %q", reconcileScanLegacyType, op.Type)
	}
	if op.Progress != 7 || op.Total != 7 {
		t.Errorf("progress lost: %d/%d", op.Progress, op.Total)
	}
	if op.ResultData == nil || *op.ResultData != payload {
		t.Errorf("ResultData is the preview payload the /latest endpoint reads back; got %v", op.ResultData)
	}
	if op.CompletedAt == nil {
		t.Error("CompletedAt lost")
	}
}

// This test used to be _ResolvesFromEitherKeyspace and asserted that a v1-keyed
// id still resolved through the GetOperationByID fallback. That fallback was
// removed on 2026-09-07 with the rest of the v1 reads, so the assertion is
// inverted rather than deleted: an id minted before the v1 minter was retired on
// 2026-08-23 now resolves to nil, and that is the accepted, authorized cost of
// dropping v1 history. Keeping the case (rather than dropping it) is what makes
// a silent re-introduction of the fallback fail here.
func TestReconcileOperationView_ResolvesV2AndNoLongerV1(t *testing.T) {
	store := &reconcileIndexStore{
		v2ByID: map[string]*database.OperationV2Row{
			"v2": {ID: "v2", DefID: reconcileScanDefIDV2, Status: "queued"},
		},
		v1ByID: map[string]*database.Operation{
			"v1": {ID: "v1", Type: reconcileScanLegacyType, Status: "completed"},
		},
	}
	if op := reconcileOperationView(store, "v2", reconcileScanLegacyType); op == nil || op.ID != "v2" {
		t.Errorf("a v2-keyed run must resolve, got %+v", op)
	}
	if op := reconcileOperationView(store, "v1", reconcileScanLegacyType); op != nil {
		t.Errorf("a v1-keyed run must no longer resolve, got %+v", op)
	}
	if store.v1Calls != 0 {
		t.Errorf("the v1 keyspace must not be consulted at all, got %d calls", store.v1Calls)
	}
	if op := reconcileOperationView(store, "nope", reconcileScanLegacyType); op != nil {
		t.Errorf("an unknown id must resolve to nil, got %+v", op)
	}
}

// THE TRUNCATION TRAP. ListOperationsV2Since sorts StartedAt DESC NULLS LAST and
// truncates before this code filters by DefID, so a QUEUED scan (StartedAt nil)
// sorts last and is dropped first. Passing the caller's limit through would mean
// a scan disappears from the view whose entire purpose is to show it, precisely
// when it has just been started.
func TestRecentReconcileScans_QueuedScanSurvivesACrowdedOpsTable(t *testing.T) {
	now := time.Now()
	started := now.Add(-time.Minute)

	var rows []database.OperationV2Row
	for range 300 {
		rows = append(rows, database.OperationV2Row{
			ID: "other", DefID: "library.scan", Status: "running",
			QueuedAt: now, StartedAt: &started,
		})
	}
	rows[0].ID = "other-0" // ids need not be unique for the non-matching def
	rows = append(rows, database.OperationV2Row{
		ID: "queued-scan", DefID: reconcileScanDefIDV2, Status: "queued", QueuedAt: now,
	})

	got := recentReconcileScans(&reconcileIndexStore{v2: rows}, 50)
	if len(got) != 1 || got[0].ID != "queued-scan" {
		t.Fatalf("a queued reconcile scan must survive a crowded ops table, got %d rows", len(got))
	}
}

func TestRecentReconcileScans_ListsScansNewestFirstExcludingOtherDefs(t *testing.T) {
	now := time.Now()
	store := &reconcileIndexStore{
		v2: []database.OperationV2Row{
			{ID: "new", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now},
			{ID: "apply", DefID: reconcileApplyDefIDV2, Status: "completed", QueuedAt: now},
			{ID: "old", DefID: reconcileScanDefIDV2, Status: "completed", QueuedAt: now.Add(-time.Hour)},
		},
	}
	got := recentReconcileScans(store, 50)
	if len(got) != 2 {
		t.Fatalf("want exactly the 2 reconcile SCANS (apply excluded), got %d", len(got))
	}
	if got[0].ID != "new" || got[1].ID != "old" {
		t.Fatalf("want newest-first [new old], got [%s %s]", got[0].ID, got[1].ID)
	}
}

// TestRecentReconcileScans_KeepsLegacyHistoryVisible and
// _OldLegacyScanSurvivesACrowdedV1Table were deleted on 2026-09-07 with the v1
// listing pass they covered. Their concern — that the scan-bound must not
// truncate the answer before the type filter runs — still applies to the v2
// pass and is covered by _OldScanSurvivesACrowdedTable above.
