// file: internal/maintenance/jobs/retention_and_hygiene_test.go
// version: 1.6.1
// guid: f8d0e5b9-c2a4-5b1d-9e7f-8c3d2a1b0f5e
// last-edited: 2026-09-02

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// TestRetentionAndHygieneJob_JobMetadata verifies ID, Name, and Description.
func TestRetentionAndHygieneJob_JobMetadata(t *testing.T) {
	job := &retentionAndHygieneJob{}
	if job.ID() != "retention-and-hygiene" {
		t.Errorf("ID: got %q, want 'retention-and-hygiene'", job.ID())
	}
	if job.Name() == "" {
		t.Errorf("Name is empty")
	}
	if job.Category() != "maintenance" {
		t.Errorf("Category: got %q, want 'maintenance'", job.Category())
	}
	if !job.CanResume() {
		t.Errorf("CanResume: got false, want true")
	}
}

// TestOperationsOlderThan pins the retention decision itself: which operations
// are old enough to delete.
//
// No mock, no fake, no store, no context — the function under test takes a
// slice and a time and returns a slice, so the test states the input as a
// literal and reads the answer. That is the entire point of splitting the pure
// decision out of the I/O (audit §7, Option C).
//
// It replaces TestRetentionBoundaryLogic, which asserted
// `tc.opTime.Before(cutoffTime) == tc.shouldDel` — i.e. it compared
// time.Before against itself and never called production code at all. It could
// not call production code, because the decision was welded to a ListOperations
// call. The table below is the same three boundary cases, now actually routed
// through the function that makes the decision.
func TestOperationsOlderThan(t *testing.T) {
	cutoff := time.Now().AddDate(0, 0, -90) // 90 days ago

	tests := []struct {
		name string
		ops  []database.Operation
		want []string
	}{
		{
			name: "strictly before the cutoff is expired",
			ops:  []database.Operation{{ID: "old", CreatedAt: cutoff.Add(-time.Second)}},
			want: []string{"old"},
		},
		{
			// Before is strict, so an operation stamped exactly at the cutoff
			// survives this run and ages out on the next one. Stating that here
			// costs one line; stating it through a fake store costs a fixture.
			name: "exactly at the cutoff is retained",
			ops:  []database.Operation{{ID: "edge", CreatedAt: cutoff}},
			want: nil,
		},
		{
			name: "after the cutoff is retained",
			ops:  []database.Operation{{ID: "fresh", CreatedAt: cutoff.Add(time.Second)}},
			want: nil,
		},
		{
			name: "mixed input keeps listing order and drops the rest",
			ops: []database.Operation{
				{ID: "fresh-1", CreatedAt: cutoff.Add(time.Hour)},
				{ID: "old-1", CreatedAt: cutoff.Add(-time.Hour)},
				{ID: "edge", CreatedAt: cutoff},
				{ID: "old-2", CreatedAt: cutoff.Add(-48 * time.Hour)},
			},
			want: []string{"old-1", "old-2"},
		},
		{
			name: "empty listing yields nothing",
			ops:  nil,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := operationsOlderThan(tc.ops, cutoff)
			if len(got) != len(tc.want) {
				t.Fatalf("operationsOlderThan = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("operationsOlderThan = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MockStore-based retention tests
// ---------------------------------------------------------------------------

// mockDeleteTracker wraps MockStore and tracks which operation IDs were deleted.
type mockDeleteTracker struct {
	*database.MockStore
	deleted []string
}

func newDeleteTracker(ops []database.Operation) *mockDeleteTracker {
	m := &mockDeleteTracker{MockStore: &database.MockStore{}}
	// ListOperations returns a page of ops from the provided slice (single snapshot).
	m.MockStore.ListOperationsFunc = func(limit, offset int) ([]database.Operation, int, error) {
		total := len(ops)
		if offset >= total {
			return nil, total, nil
		}
		// Mirror PebbleStore.ListOperations, where limit <= 0 means "no limit".
		// This fake previously computed end == offset for limit == 0 and returned
		// an empty page, which would silently turn any caller that asks for the
		// full listing into one that sees nothing.
		end := total
		if limit > 0 {
			end = min(offset+limit, total)
		}
		return ops[offset:end], total, nil
	}
	m.MockStore.DeleteOperationWithLogsFunc = func(id string) error {
		m.deleted = append(m.deleted, id)
		return nil
	}
	return m
}

// TestExpiredOperationIDs_SelectsOldRowsWithoutDeleting is the read half. It
// keeps the selection assertion that TestDeleteOldOperations_MockDryRun used to
// make, plus the stronger structural claim the split buys: the scan step cannot
// delete anything, because operationLister does not expose a way to.
func TestExpiredOperationIDs_SelectsOldRowsWithoutDeleting(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-24 * time.Hour)
	oldTime := now.Add(-48 * time.Hour)
	newTime := now.Add(-1 * time.Hour)

	ops := []database.Operation{
		{ID: "old-1", CreatedAt: oldTime},
		{ID: "new-1", CreatedAt: newTime},
		{ID: "old-2", CreatedAt: oldTime},
	}
	tracker := newDeleteTracker(ops)

	ids, err := expiredOperationIDs(context.Background(), tracker, cutoff)
	if err != nil {
		t.Fatalf("expiredOperationIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "old-1" || ids[1] != "old-2" {
		t.Errorf("expired ids: got %v, want [old-1 old-2]", ids)
	}
	if len(tracker.deleted) != 0 {
		t.Errorf("the scan step must not delete; got deletions: %v", tracker.deleted)
	}
}

// TestDeleteOperations_CountMatchesInput is the write half, and it is the piece
// that keeps the two counts honest after the split.
//
// Before the split, one function returned both the dry-run count and the real
// count, so their agreement was structural. Now Run reports len(expired) for a
// dry run and deleteOperations' return for a real one; nothing about the types
// forces those to agree. Pinning "the return equals len(ids) on full success,
// with exactly one delete call per id" reduces that agreement to a property
// this test can check locally — the same invariant deleteStaleOperationState's
// doc comment states when it insists on counting operations, not raw keys.
func TestDeleteOperations_CountMatchesInput(t *testing.T) {
	tracker := newDeleteTracker(nil)
	ids := []string{"a", "b", "c"}

	count, err := deleteOperations(context.Background(), tracker, ids)
	if err != nil {
		t.Fatalf("deleteOperations: %v", err)
	}
	if count != len(ids) {
		t.Errorf("count: got %d, want %d — a reported count above the delete count is the bug this guards", count, len(ids))
	}
	if len(tracker.deleted) != len(ids) {
		t.Fatalf("delete calls: got %v, want one per id %v", tracker.deleted, ids)
	}
	for i, id := range ids {
		if tracker.deleted[i] != id {
			t.Errorf("delete call %d: got %q, want %q", i, tracker.deleted[i], id)
		}
	}
}

// TestDeleteOperations_PartialCountOnError pins the other half of the same
// invariant: a failure mid-run reports the number that actually succeeded, not
// the number attempted.
func TestDeleteOperations_PartialCountOnError(t *testing.T) {
	var attempted []string
	store := &database.MockStore{
		DeleteOperationWithLogsFunc: func(id string) error {
			attempted = append(attempted, id)
			if id == "boom" {
				return errors.New("pebble is unhappy")
			}
			return nil
		},
	}

	count, err := deleteOperations(context.Background(), store, []string{"a", "boom", "c"})
	if err == nil {
		t.Fatal("expected the delete error to propagate")
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1 (only 'a' was deleted)", count)
	}
	if len(attempted) != 2 {
		t.Errorf("must stop at the failure; attempted %v", attempted)
	}
}

// TestJobRun_DryRunDeletesNoOperations exercises the dryRun branch where it now
// lives — in Run — against a real Pebble store.
//
// The flag used to be threaded down into the helper, so "a dry run deletes
// nothing" was assertable one level below Run. Moving the branch up moved the
// invariant with it, and this test follows it rather than leaving it untested.
func TestJobRun_DryRunDeletesNoOperations(t *testing.T) {
	store, cleanup := newPebbleTestStore(t)
	defer cleanup()

	// Well past the 90-day default retention window, so it is unambiguously
	// eligible whatever OperationLogRetentionDays happens to be configured as.
	const opID = "DRYRUNKEEPME"
	writeOperationRaw(t, store, opID, time.Now().AddDate(0, 0, -400))

	job := &retentionAndHygieneJob{}
	if err := job.Run(context.Background(), store, &nopReporter{}, true /* dryRun */); err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}

	op, err := store.GetOperationByID(opID)
	if err != nil {
		t.Fatalf("GetOperationByID: %v", err)
	}
	if op == nil {
		t.Fatal("dry run deleted an eligible operation — dryRun must not reach the delete path")
	}
}

// TestDeleteDeadPrefixes_Mock verifies the dead-prefix sweep via MockStore.
// Plants dummy keys in ScanPrefix/DeleteRaw and asserts correct invocations.
func TestDeleteDeadPrefixes_Mock(t *testing.T) {
	type prefixEntry struct {
		key   string
		value []byte
	}
	planted := map[string][]prefixEntry{
		"book:series:": {
			{"book:series:01", []byte("v1")},
			{"book:series:02", []byte("v2")},
		},
		"book:author:": {
			{"book:author:99", []byte("v3")},
		},
	}

	var deletedKeys []string
	m := &database.MockStore{
		ScanPrefixFunc: func(prefix string) ([]database.KVPair, error) {
			var pairs []database.KVPair
			for _, e := range planted[prefix] {
				pairs = append(pairs, database.KVPair{Key: e.key, Value: e.value})
			}
			return pairs, nil
		},
		DeleteRawFunc: func(key string) error {
			deletedKeys = append(deletedKeys, key)
			return nil
		},
	}

	count, err := deleteDeadPrefixes(context.Background(), m, false)
	if err != nil {
		t.Fatalf("deleteDeadPrefixes: %v", err)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
	}
	if len(deletedKeys) != 3 {
		t.Errorf("deletedKeys: got %d entries, want 3: %v", len(deletedKeys), deletedKeys)
	}
}

// TestDeleteDeadPrefixes_MockDryRun verifies dry-run mode does not call DeleteRaw.
func TestDeleteDeadPrefixes_MockDryRun(t *testing.T) {
	deleteRawCalled := false
	m := &database.MockStore{
		ScanPrefixFunc: func(prefix string) ([]database.KVPair, error) {
			return []database.KVPair{{Key: prefix + "dummy", Value: []byte("x")}}, nil
		},
		DeleteRawFunc: func(_ string) error {
			deleteRawCalled = true
			return nil
		},
	}

	count, err := deleteDeadPrefixes(context.Background(), m, true /* dryRun */)
	if err != nil {
		t.Fatalf("deleteDeadPrefixes dry-run: %v", err)
	}
	if count != 2 { // one key per prefix (book:series: + book:author:)
		t.Errorf("dry-run count: got %d, want 2", count)
	}
	if deleteRawCalled {
		t.Error("dry-run must not call DeleteRaw")
	}
}

// ---------------------------------------------------------------------------
// PebbleStore integration tests — verify records are actually gone
// ---------------------------------------------------------------------------

// TestDeleteOperationWithLogs_PebbleIntegration plants an operation and its log
// lines into PebbleDB, then calls DeleteOperationWithLogs and asserts both are gone.
func TestDeleteOperationWithLogs_PebbleIntegration(t *testing.T) {
	store, cleanup := newPebbleTestStore(t)
	defer cleanup()

	// Insert operation manually via SetRaw so we can control CreatedAt.
	opID := "integ-op-001"
	oldTime := time.Now().Add(-100 * time.Hour)
	writeOperationRaw(t, store, opID, oldTime)

	// Insert a log line for the operation.
	if err := store.AddOperationLog(opID, "info", "integration test log", nil); err != nil {
		t.Fatalf("AddOperationLog: %v", err)
	}

	// Confirm operation exists.
	op, err := store.GetOperationByID(opID)
	if err != nil {
		t.Fatalf("GetOperationByID before delete: %v", err)
	}
	if op == nil {
		t.Fatal("operation should exist before deletion")
	}

	// Delete it with logs.
	if err := store.DeleteOperationWithLogs(opID); err != nil {
		t.Fatalf("DeleteOperationWithLogs: %v", err)
	}

	// Operation must be gone.
	op, err = store.GetOperationByID(opID)
	if err != nil {
		t.Fatalf("GetOperationByID after delete: %v", err)
	}
	if op != nil {
		t.Errorf("operation still present after DeleteOperationWithLogs")
	}

	// Log lines must be gone.
	logs, err := store.GetOperationLogs(opID)
	if err != nil {
		t.Fatalf("GetOperationLogs after delete: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 log lines after delete; got %d", len(logs))
	}
}

// TestDeadPrefixSweep_PebbleIntegration plants real book:series: and book:author: keys
// into PebbleDB, runs the full sweep, and asserts they are absent afterward.
// The completion flag is verified as set only after a real run, not after a dry run.
func TestDeadPrefixSweep_PebbleIntegration(t *testing.T) {
	store, cleanup := newPebbleTestStore(t)
	defer cleanup()

	ctx := context.Background()
	flagName := "dead_prefix_sweep_v1_done"
	deadKeys := []string{
		"book:series:01SERIES",
		"book:series:02SERIES",
		"book:author:99AUTHOR",
	}

	// --- Dry-run first: nothing deleted, flag not set. ---
	for _, k := range deadKeys {
		if err := store.SetRaw(k, []byte("dummy")); err != nil {
			t.Fatalf("SetRaw %q: %v", k, err)
		}
	}

	dryCount, err := deleteDeadPrefixes(ctx, store, true)
	if err != nil {
		t.Fatalf("deleteDeadPrefixes dry-run: %v", err)
	}
	if dryCount != len(deadKeys) {
		t.Errorf("dry-run count: got %d, want %d", dryCount, len(deadKeys))
	}
	for _, k := range deadKeys {
		val, err := store.GetRaw(k)
		if err != nil {
			t.Fatalf("GetRaw %q after dry-run: %v", k, err)
		}
		if val == nil {
			t.Errorf("key %q was deleted during dry-run; must be preserved", k)
		}
	}
	done, err := isDeadPrefixSweepDone(store, flagName)
	if err != nil {
		t.Fatalf("isDeadPrefixSweepDone: %v", err)
	}
	if done {
		t.Error("flag set after dry-run; must only be set after real run")
	}

	// --- Real run: keys deleted, flag set. ---
	realCount, err := deleteDeadPrefixes(ctx, store, false)
	if err != nil {
		t.Fatalf("deleteDeadPrefixes real: %v", err)
	}
	if realCount != len(deadKeys) {
		t.Errorf("real run count: got %d, want %d", realCount, len(deadKeys))
	}
	for _, k := range deadKeys {
		val, err := store.GetRaw(k)
		if err != nil {
			t.Fatalf("GetRaw %q after real run: %v", k, err)
		}
		if val != nil {
			t.Errorf("key %q still present after real sweep", k)
		}
	}

	// Set flag (normally done by the Run method) and verify.
	if err := setDeadPrefixSweepDone(store, flagName); err != nil {
		t.Fatalf("setDeadPrefixSweepDone: %v", err)
	}
	done, err = isDeadPrefixSweepDone(store, flagName)
	if err != nil {
		t.Fatalf("isDeadPrefixSweepDone after real run: %v", err)
	}
	if !done {
		t.Error("flag not set after real run")
	}
}

// TestJobRun_FlagNotSetOnDryRun exercises the full Run() path and confirms the
// completion flag is absent after a dry-run — verifying review finding #3 fix.
func TestJobRun_FlagNotSetOnDryRun(t *testing.T) {
	// Confirm the job is registered.
	if _, err := maintenance.Get("retention-and-hygiene"); err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	store, cleanup := newPebbleTestStore(t)
	defer cleanup()

	// Plant a dead key so the sweep exercises the deletion path.
	if err := store.SetRaw("book:series:FLAGTEST", []byte("x")); err != nil {
		t.Fatalf("SetRaw: %v", err)
	}

	job := &retentionAndHygieneJob{}
	if err := job.Run(context.Background(), store, &nopReporter{}, true /* dryRun */); err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}

	done, err := isDeadPrefixSweepDone(store, "dead_prefix_sweep_v1_done")
	if err != nil {
		t.Fatalf("isDeadPrefixSweepDone: %v", err)
	}
	if done {
		t.Error("completion flag set after dry-run — this was review finding #3")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newPebbleTestStore creates a temporary PebbleDB Store and returns it with a cleanup func.
func newPebbleTestStore(t *testing.T) (database.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	// Required, not optional — see PebbleStore.WaitForWarmup. Seeding before the
	// async warmup publishes loses those rows from memdb, which every GetAll*
	// read then takes the fast path into.
	store.WaitForWarmup()
	return store, func() { store.Close() }
}

// writeOperationRaw inserts a backdated Operation record directly via SetRaw so the
// CreatedAt field is set to the caller-supplied time. PebbleStore.CreateOperation
// always stamps time.Now(), making it unsuitable for testing age-based retention.
func writeOperationRaw(t *testing.T, store database.Store, id string, createdAt time.Time) {
	t.Helper()
	op := database.Operation{
		ID:        id,
		Type:      "test_type",
		Status:    "completed",
		CreatedAt: createdAt,
	}
	data, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("json.Marshal operation: %v", err)
	}
	key := fmt.Sprintf("operation:%s", id)
	if err := store.SetRaw(key, data); err != nil {
		t.Fatalf("SetRaw %q: %v", key, err)
	}
}

// nopReporter satisfies maintenance.ProgressReporter with no-ops.
// Defined here (internal package) because testhelpers_test.go lives in the
// external jobs_test package and is not visible from the internal jobs package.
type nopReporter struct{}

func (r *nopReporter) SetTotal(_ int)                    {}
func (r *nopReporter) Increment()                        {}
func (r *nopReporter) Log(_ string, _ string, _ *string) {}

// opstateSweepMock builds a MockStore holding opstate keys for four operations:
// one running (must be KEPT — the resume path may still load it), one completed
// (delete), one whose operation record is gone (delete), and one with a status
// the sweep does not recognize (must be KEPT — fail toward keeping). The
// completed op has BOTH key forms so the test also pins that the count is
// per-operation, not per-key.
func opstateSweepMock() (*database.MockStore, *[]string) {
	deleted := &[]string{}
	ops := map[string]*database.Operation{
		"RUN1":   {ID: "RUN1", Status: "running"},
		"DONE1":  {ID: "DONE1", Status: "completed"},
		"WEIRD1": {ID: "WEIRD1", Status: "quarantined"},
		// "GONE1" has opstate keys but no operation record.
	}
	m := &database.MockStore{}
	m.ScanPrefixFunc = func(prefix string) ([]database.KVPair, error) {
		if prefix != "opstate:" {
			return nil, nil
		}
		return []database.KVPair{
			{Key: "opstate:RUN1", Value: []byte("{}")},
			{Key: "opstate:RUN1:params", Value: []byte("{}")},
			{Key: "opstate:DONE1", Value: []byte("{}")},
			{Key: "opstate:DONE1:params", Value: []byte("{}")},
			{Key: "opstate:GONE1:params", Value: []byte("{}")},
			{Key: "opstate:WEIRD1:params", Value: []byte("{}")},
		}, nil
	}
	m.GetOperationByIDFunc = func(id string) (*database.Operation, error) {
		return ops[id], nil // nil for GONE1, matching PebbleStore's not-found contract
	}
	m.DeleteOperationStateFunc = func(opID string) error {
		*deleted = append(*deleted, opID)
		return nil
	}
	return m, deleted
}

func TestDeleteStaleOperationState_DryRunCountsWithoutDeleting(t *testing.T) {
	store, deleted := opstateSweepMock()
	count, err := deleteStaleOperationState(context.Background(), store, true)
	if err != nil {
		t.Fatalf("dry-run sweep: %v", err)
	}
	// DONE1 (terminal) + GONE1 (orphaned) — counted per OPERATION, so DONE1's
	// two keys contribute 1, not 2.
	if count != 2 {
		t.Fatalf("dry-run count = %d, want 2 (per-operation, not per-key)", count)
	}
	if len(*deleted) != 0 {
		t.Fatalf("dry-run deleted state for %v, want none", *deleted)
	}
}

func TestDeleteStaleOperationState_DeletesTerminalAndOrphanedOnly(t *testing.T) {
	store, deleted := opstateSweepMock()
	count, err := deleteStaleOperationState(context.Background(), store, false)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	got := map[string]bool{}
	for _, id := range *deleted {
		got[id] = true
	}
	if !got["DONE1"] || !got["GONE1"] || len(got) != 2 {
		t.Fatalf("deleted %v, want exactly {DONE1, GONE1}", *deleted)
	}
	// The two keep cases are the load-bearing half of this test: a running op's
	// state feeds the restart-resume path, and an unrecognized status must fail
	// toward keeping.
	if got["RUN1"] {
		t.Fatal("sweep deleted state for a RUNNING op — this breaks restart-resume")
	}
	if got["WEIRD1"] {
		t.Fatal("sweep deleted state for an unrecognized status — must fail toward keeping")
	}
}
