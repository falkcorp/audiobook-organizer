// file: internal/database/pebble_store_ops_v2_test.go
// version: 1.3.0
// guid: d7e8f9a0-b1c2-4d3e-5f6a-7b8c9d0e1f2a
// last-edited: 2026-08-23

package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// buildTestOpRow constructs a minimal OperationV2Row with the given id and status.
// All other fields are set to non-zero defaults so InsertOperationV2 succeeds.
func buildTestOpRow(id, status string) OperationV2Row {
	return OperationV2Row{
		ID:       id,
		DefID:    "test-def",
		Plugin:   "test-plugin",
		Status:   status,
		Priority: 5,
		QueuedAt: time.Now().UTC(),
	}
}

// TestOpCompletionAndDepRev_RoundTrip verifies the dep_rev bump, completion
// record, and staleness semantics added in Task 2.
func TestOpCompletionAndDepRev_RoundTrip(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	sub := OpSubject{Type: "book", ID: "b1"}

	// dep_rev starts at 0; bump → 1.
	got, err := s.GetDepRev(sub)
	require.NoError(t, err)
	if got != 0 {
		t.Fatalf("expected dep_rev=0 initially, got %d", got)
	}

	newRev, err := s.BumpDepRev(sub)
	require.NoError(t, err)
	if newRev != 1 {
		t.Fatalf("expected bump result=1, got %d", newRev)
	}

	got, err = s.GetDepRev(sub)
	require.NoError(t, err)
	if got != 1 {
		t.Fatalf("expected dep_rev=1 after bump, got %d", got)
	}

	// Record a book-level completion at rev 1.
	err = s.RecordOpCompletion(sub, "acoustid.fingerprint-extract", "", 1)
	require.NoError(t, err)

	// GetOpCompletion should return (rev=1, ok=true).
	rev, ok, err := s.GetOpCompletion(sub, "acoustid.fingerprint-extract")
	require.NoError(t, err)
	if !ok {
		t.Fatal("expected ok=true after recording completion")
	}
	if rev != 1 {
		t.Fatalf("expected completion rev=1, got %d", rev)
	}

	// Bump again → current rev becomes 2; the completion at rev 1 is now stale.
	// The evaluator (Task 3) handles staleness; here we just assert stored values.
	_, err = s.BumpDepRev(sub)
	require.NoError(t, err)

	cur, err := s.GetDepRev(sub)
	require.NoError(t, err)
	if cur != 2 {
		t.Fatalf("expected dep_rev=2 after second bump, got %d", cur)
	}

	// Completion record itself is unchanged (still rev 1).
	rev, ok, err = s.GetOpCompletion(sub, "acoustid.fingerprint-extract")
	require.NoError(t, err)
	if !ok {
		t.Fatal("expected ok=true — completion record survives dep_rev bump")
	}
	if rev != 1 {
		t.Fatalf("expected stored rev still=1 (staleness is evaluator concern), got %d", rev)
	}
}

// TestFileCompletions_RoundTrip verifies per-file completion storage and listing.
func TestFileCompletions_RoundTrip(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	sub := OpSubject{Type: "book", ID: "b2"}

	// Bump dep_rev once so we can record completions at rev 1.
	_, err := s.BumpDepRev(sub)
	require.NoError(t, err)

	// Record file-level completions for two files.
	err = s.RecordOpCompletion(sub, "fp.extract", "file1", 1)
	require.NoError(t, err)
	err = s.RecordOpCompletion(sub, "fp.extract", "file2", 1)
	require.NoError(t, err)

	// ListFileCompletions should return both.
	filemap, err := s.ListFileCompletions(sub, "fp.extract")
	require.NoError(t, err)
	require.Len(t, filemap, 2)
	require.Equal(t, uint64(1), filemap["file1"])
	require.Equal(t, uint64(1), filemap["file2"])
}

// TestWaitingDepsOps_RoundTrip verifies that ListWaitingDepsOps returns ops
// whose status is "waiting_deps".
func TestWaitingDepsOps_RoundTrip(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	// Insert a "waiting_deps" row with the new subject/requirements fields.
	row := buildTestOpRow("op-wd-1", "waiting_deps")
	row.SubjectType = "book"
	row.SubjectID = "b3"
	row.Requirements = `[{"kind":"op_completed","op_type":"fp.extract"}]`
	row.ReqSnapshotRev = 1
	err := s.InsertOperationV2(row)
	require.NoError(t, err)

	// Insert a "queued" row — should NOT appear.
	err = s.InsertOperationV2(buildTestOpRow("op-q-1", "queued"))
	require.NoError(t, err)

	// Insert a "completed" row — should NOT appear.
	err = s.InsertOperationV2(buildTestOpRow("op-done-1", "completed"))
	require.NoError(t, err)

	waiting, err := s.ListWaitingDepsOps()
	require.NoError(t, err)
	require.Len(t, waiting, 1)
	require.Equal(t, "op-wd-1", waiting[0].ID)
	require.Equal(t, "waiting_deps", waiting[0].Status)
	require.Equal(t, "book", waiting[0].SubjectType)
	require.Equal(t, "b3", waiting[0].SubjectID)
	require.Equal(t, uint64(1), waiting[0].ReqSnapshotRev)
}

// TestOperationV2Row_SubjectFields_RoundTrip ensures the new fields survive a
// write-read cycle on the existing InsertOperationV2 / GetOperationV2 path.
func TestOperationV2Row_SubjectFields_RoundTrip(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	row := buildTestOpRow("op-subj-1", "queued")
	row.SubjectType = "book"
	row.SubjectID = "b4"
	row.Requirements = `[{"kind":"op_completed","op_type":"scan"}]`
	row.ReqSnapshotRev = 7

	err := s.InsertOperationV2(row)
	require.NoError(t, err)

	got, err := s.GetOperationV2("op-subj-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "book", got.SubjectType)
	require.Equal(t, "b4", got.SubjectID)
	require.Equal(t, row.Requirements, got.Requirements)
	require.Equal(t, uint64(7), got.ReqSnapshotRev)
}

// TestListOperationsV2Since_KeepsLiveOpsOutsideTheWindow pins the rule that a
// still-running operation is never filtered out by the time window.
//
// The timeline endpoint defaults to since=15m and filtered purely on QueuedAt, so
// an operation simply had to RUN longer than the window to vanish from its own
// timeline. Measured against production 2026-08-16: a library.scan that had been
// running for 1h50m returned {"operations":[]} while it was actively logging once
// a second. The one operation a user most needs to see — the long one still going —
// was the one guaranteed to be hidden, and an empty list is indistinguishable from
// "nothing is running."
//
// The window bounds HISTORY. Anything unfinished is current by definition, so
// membership keys on CompletedAt rather than on a list of status strings, which
// would drift the first time a new status is added.
func TestListOperationsV2Since_KeepsLiveOpsOutsideTheWindow(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	done := now.Add(-90 * time.Minute)

	// Running for two hours, never finished — the production case.
	live := buildTestOpRow("op-live", "running")
	live.QueuedAt = old
	live.StartedAt = &old
	require.NoError(t, s.InsertOperationV2(live))

	// Finished two hours ago. Genuinely history; the window must still exclude it,
	// otherwise this test would pass with the filter removed entirely.
	fin := buildTestOpRow("op-finished", "completed")
	fin.QueuedAt = old
	fin.StartedAt = &old
	fin.CompletedAt = &done
	require.NoError(t, s.InsertOperationV2(fin))

	rows, err := s.ListOperationsV2Since(now.Add(-15*time.Minute), 200)
	require.NoError(t, err)

	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.ID] = true
	}
	require.True(t, ids["op-live"],
		"a still-running operation must appear regardless of how long it has been "+
			"running; filtering it out is what made the live scan invisible")
	require.False(t, ids["op-finished"],
		"a finished operation outside the window is history and must stay filtered — "+
			"without this the test would also pass if the window were simply deleted")
}

// TestUpdateOpProgressV2_AdvancesHighWaterProgress pins that high_water_progress
// tracks reported PROGRESS, not merely the last Checkpoint call.
//
// registry.checkInfiniteRestart force-drops an op at resume_count>=3 whose
// high_water_progress is still 0, on the reasoning that it has accomplished
// nothing across three restarts. Until 2026-08-23 the only writer of that column
// was UpdateOpCheckpointV2, so it stayed permanently 0 for every op that reports
// progress without checkpointing -- which is every maintenance job, because
// maintenance.ProgressReporter declares only SetTotal/Increment/Log and has no
// Checkpoint method to call. Those ops were force-dropped no matter how many
// thousands of items they had genuinely completed.
func TestUpdateOpProgressV2_AdvancesHighWaterProgress(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	require.NoError(t, s.InsertOperationV2(buildTestOpRow("op-hwm-1", "running")))

	row, err := s.GetOperationV2("op-hwm-1")
	require.NoError(t, err)
	require.Equal(t, 0, row.HighWaterProgress, "fresh row should start at 0")

	require.NoError(t, s.UpdateOpProgressV2("op-hwm-1", 42, 100, "working"))
	row, err = s.GetOperationV2("op-hwm-1")
	require.NoError(t, err)
	require.Equal(t, 42, row.HighWaterProgress,
		"progress must advance the high-water mark; checkInfiniteRestart reads this "+
			"to decide whether a resumed op has done any work at all")

	// A HIGH-WATER mark, not a mirror of current progress. A resumed run restarts
	// its counter from zero, and that must not erase the evidence of prior work --
	// which is exactly the state checkInfiniteRestart force-drops on.
	require.NoError(t, s.UpdateOpProgressV2("op-hwm-1", 5, 100, "resumed from the top"))
	row, err = s.GetOperationV2("op-hwm-1")
	require.NoError(t, err)
	require.Equal(t, 42, row.HighWaterProgress,
		"high-water mark must not regress when a resumed run reports a lower current")
	require.Equal(t, 5, row.ProgressCurrent, "current progress should still track the live value")
}
