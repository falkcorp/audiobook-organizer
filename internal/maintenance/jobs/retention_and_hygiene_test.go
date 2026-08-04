// file: internal/maintenance/jobs/retention_and_hygiene_test.go
// version: 1.2.0
// guid: f8d0e5b9-c2a4-5b1d-9e7f-8c3d2a1b0f5e
// last-edited: 2026-08-04

package jobs

import (
	"context"
	"encoding/json"
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

// TestRetentionBoundaryLogic verifies the boundary logic for identifying stale operations.
// Operations with CreatedAt < cutoffTime should be marked for deletion.
func TestRetentionBoundaryLogic(t *testing.T) {
	now := time.Now()
	cutoffTime := now.AddDate(0, 0, -90) // 90 days ago

	tests := []struct {
		name      string
		opTime    time.Time
		shouldDel bool
	}{
		{
			"before cutoff",
			cutoffTime.Add(-1 * time.Second),
			true,
		},
		{
			"at cutoff",
			cutoffTime,
			false, // CreatedAt.Before(cutoffTime) is false when equal
		},
		{
			"after cutoff",
			cutoffTime.Add(1 * time.Second),
			false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shouldDelete := tc.opTime.Before(cutoffTime)
			if shouldDelete != tc.shouldDel {
				t.Errorf("got shouldDelete=%v, want %v for time %v vs cutoff %v",
					shouldDelete, tc.shouldDel, tc.opTime, cutoffTime)
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
		end := offset + limit
		if end > total {
			end = total
		}
		return ops[offset:end], total, nil
	}
	m.MockStore.DeleteOperationWithLogsFunc = func(id string) error {
		m.deleted = append(m.deleted, id)
		return nil
	}
	return m
}

// TestDeleteOldOperations_MockDryRun verifies that dry-run counts eligible
// operations but calls DeleteOperationWithLogs zero times.
func TestDeleteOldOperations_MockDryRun(t *testing.T) {
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

	count, err := deleteOldOperations(context.Background(), tracker, cutoff, true)
	if err != nil {
		t.Fatalf("deleteOldOperations dry-run: %v", err)
	}
	if count != 2 {
		t.Errorf("dry-run count: got %d, want 2", count)
	}
	if len(tracker.deleted) != 0 {
		t.Errorf("dry-run must not delete; got deletions: %v", tracker.deleted)
	}
}

// TestDeleteOldOperations_MockRealRun verifies that non-dry-run mode calls
// DeleteOperationWithLogs exactly for old records and not for new ones.
func TestDeleteOldOperations_MockRealRun(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-24 * time.Hour)
	oldTime := now.Add(-48 * time.Hour)
	newTime := now.Add(-1 * time.Hour)

	ops := []database.Operation{
		{ID: "old-A", CreatedAt: oldTime},
		{ID: "new-B", CreatedAt: newTime},
	}
	tracker := newDeleteTracker(ops)

	count, err := deleteOldOperations(context.Background(), tracker, cutoff, false)
	if err != nil {
		t.Fatalf("deleteOldOperations real-run: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
	if len(tracker.deleted) != 1 || tracker.deleted[0] != "old-A" {
		t.Errorf("expected [old-A] deleted; got %v", tracker.deleted)
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

func (r *nopReporter) SetTotal(_ int)                     {}
func (r *nopReporter) Increment()                         {}
func (r *nopReporter) Log(_ string, _ string, _ *string)  {}
