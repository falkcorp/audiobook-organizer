// file: internal/database/pebble_store_ops_v2_test.go
// version: 1.7.0
// guid: d7e8f9a0-b1c2-4d3e-5f6a-7b8c9d0e1f2a
// last-edited: 2026-09-08

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

// TestListOperationsV2Since_IncludesLongOpsThatFinishedInsideTheWindow is the
// other half of the rule above, which was left unfixed for three weeks.
//
// The live-op fix rescued rows with CompletedAt == nil. Rows that had FINISHED
// were still admitted on QueuedAt, so "what completed in the last 24 hours"
// silently answered "what was QUEUED in the last 24 hours" — and the longer an
// operation ran, the more likely it was to be excluded from its own history.
// A backfill queued 30h ago that finished 20 minutes ago is the last 20 minutes
// of history by any reading, and it was invisible at every window under 30h.
//
// This is the same class of defect as the live-op case: an operation punished in
// the timeline for having taken a long time.
func TestListOperationsV2Since_IncludesLongOpsThatFinishedInsideTheWindow(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)

	// Queued well OUTSIDE the window, finished well INSIDE it.
	queued := now.Add(-30 * time.Hour)
	finished := now.Add(-20 * time.Minute)
	longOp := buildTestOpRow("op-long-finished-recently", "completed")
	longOp.QueuedAt = queued
	longOp.StartedAt = &queued
	longOp.CompletedAt = &finished
	require.NoError(t, s.InsertOperationV2(longOp))

	// Queued AND finished outside the window — genuinely older history. Keeping
	// this here is what stops the test from passing if the window were deleted.
	older := now.Add(-40 * time.Hour)
	olderDone := now.Add(-38 * time.Hour)
	ancient := buildTestOpRow("op-ancient", "completed")
	ancient.QueuedAt = older
	ancient.StartedAt = &older
	ancient.CompletedAt = &olderDone
	require.NoError(t, s.InsertOperationV2(ancient))

	rows, err := s.ListOperationsV2Since(since, 200)
	require.NoError(t, err)

	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.ID] = true
	}
	require.True(t, ids["op-long-finished-recently"],
		"an operation that COMPLETED inside the window is history from inside the "+
			"window, however long before it was queued — testing QueuedAt instead "+
			"hides exactly the long-running operations most worth seeing")
	require.False(t, ids["op-ancient"],
		"an operation that both started and finished before the window is still out")
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

// TestResetOperationV2ForResume_ClearsCompletedAtAndRestoresVisibility pins B1:
// a ResumeRestart op reuses its row, and on interrupt the row got a CompletedAt
// stamped. UpdateOperationV2Status cannot un-set CompletedAt (nil = leave
// unchanged), so a resumed op stayed excluded from the Active-Operations
// timeline (ListOperationsV2Since admits a row only if CompletedAt == nil OR its
// QueuedAt is inside the window). ResetOperationV2ForResume clears CompletedAt so
// the running resumed op is visible again — without touching QueuedAt.
func TestResetOperationV2ForResume_ClearsCompletedAtAndRestoresVisibility(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	// A row queued 30m ago (older than the 15m window) and interrupted 20m ago.
	oldQueued := time.Now().UTC().Add(-30 * time.Minute)
	row := OperationV2Row{
		ID:       "op-resume-1",
		DefID:    "library.scan",
		Plugin:   "library",
		Status:   "queued",
		Priority: 5,
		QueuedAt: oldQueued,
	}
	require.NoError(t, s.InsertOperationV2(row))

	// Stamp it interrupted_quiesced with a CompletedAt + error, as the worker does.
	interruptedAt := time.Now().UTC().Add(-20 * time.Minute)
	interruptMsg := "quiesced: scan stand-down"
	require.NoError(t, s.UpdateOperationV2Status("op-resume-1", "interrupted_quiesced", nil, &interruptedAt, &interruptMsg))

	since := time.Now().UTC().Add(-15 * time.Minute)

	// Excluded before reset: CompletedAt set AND QueuedAt older than the window.
	before, err := s.ListOperationsV2Since(since, 100)
	require.NoError(t, err)
	for _, r := range before {
		if r.ID == "op-resume-1" {
			t.Fatalf("row should be excluded from the timeline before reset")
		}
	}

	// Reset for resume.
	require.NoError(t, s.ResetOperationV2ForResume("op-resume-1"))

	got, err := s.GetOperationV2("op-resume-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "queued", got.Status)
	require.Nil(t, got.CompletedAt, "CompletedAt must be cleared to nil")
	require.Nil(t, got.ErrorMessage, "stale interrupt error must be cleared")
	require.True(t, got.QueuedAt.Equal(oldQueued), "QueuedAt must be left untouched")

	// Included after reset: CompletedAt == nil satisfies the filter.
	after, err := s.ListOperationsV2Since(since, 100)
	require.NoError(t, err)
	found := false
	for _, r := range after {
		if r.ID == "op-resume-1" {
			found = true
			break
		}
	}
	require.True(t, found, "reset-for-resume row must appear in the timeline")
}

// TestSetOperationV2StatusIfQueued_StampsCompletedAtOnTerminalStatus pins the
// invariant that CompletedAt is set whenever a row leaves the live states.
//
// CompletedAt is the canonical liveness signal for this store: both
// ListOperationsV2Since and the timeline handler decide "still in flight" on
// CompletedAt == nil, never on a status list. A terminal status written with no
// stamp therefore produced a row that was dead to the worker and alive to every
// reader — a canceled maintenance.transcribe-book-intros op sat in the UI's
// Active Operations panel for 73 days that way, and no user action could clear
// it, because "Clear Stale" filters on queued/running/pending and it was none
// of those.
func TestSetOperationV2StatusIfQueued_StampsCompletedAtOnTerminalStatus(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	t.Run("canceling a queued op stamps CompletedAt", func(t *testing.T) {
		row := buildTestOpRow("op-cancel", "queued")
		require.NoError(t, s.InsertOperationV2(row))

		updated, err := s.SetOperationV2StatusIfQueued("op-cancel", "canceled")
		require.NoError(t, err)
		require.True(t, updated)

		got, err := s.GetOperationV2("op-cancel")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "canceled", got.Status)
		require.NotNil(t, got.CompletedAt,
			"a terminal status with no CompletedAt reads as in-flight forever")
	})

	t.Run("an unrecognized status is NOT stamped", func(t *testing.T) {
		// This subtest asserted the opposite until 2026-09-07, when the
		// condition was the complement of the live states ("anything that is not
		// running or queued is terminal"). That is wrong in the dangerous
		// direction: "interrupted_quiesced" is resumable and "waiting_deps" is
		// waiting on the dependency scheduler, and the complement called both
		// terminal. A stamped live row reads as finished work that never ran.
		//
		// So the predicate is now an allowlist and an unknown status is treated
		// as live. Adding a real terminal state means adding it to
		// isTerminalV2Status; the cost of forgetting is a row that lingers
		// visibly, not work silently discarded.
		row := buildTestOpRow("op-future", "queued")
		require.NoError(t, s.InsertOperationV2(row))

		updated, err := s.SetOperationV2StatusIfQueued("op-future", "invented_by_a_future_feature")
		require.NoError(t, err)
		require.True(t, updated, "the status write itself still happens")

		got, err := s.GetOperationV2("op-future")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Nil(t, got.CompletedAt,
			"an unrecognized status must be treated as live, not stamped")
	})

	t.Run("resumable and waiting statuses are NOT stamped", func(t *testing.T) {
		// The two concrete statuses the old complement predicate got wrong.
		// isResumableV2Status lists interrupted_quiesced as resumable, and
		// ListWaitingDepsOps hands waiting_deps rows to the dependency scheduler.
		for _, status := range []string{"interrupted_quiesced", "waiting_deps"} {
			id := "op-live-" + status
			require.NoError(t, s.InsertOperationV2(buildTestOpRow(id, "queued")))

			updated, err := s.SetOperationV2StatusIfQueued(id, status)
			require.NoError(t, err)
			require.True(t, updated)

			got, err := s.GetOperationV2(id)
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Nil(t, got.CompletedAt,
				"%s is live; stamping it hides an op the resume/deps machinery still owns", status)
		}
	})

	t.Run("promoting to running leaves CompletedAt nil", func(t *testing.T) {
		// "running" is live. Stamping it would mark a working op complete.
		row := buildTestOpRow("op-run", "queued")
		require.NoError(t, s.InsertOperationV2(row))

		updated, err := s.SetOperationV2StatusIfQueued("op-run", "running")
		require.NoError(t, err)
		require.True(t, updated)

		got, err := s.GetOperationV2("op-run")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Nil(t, got.CompletedAt)
	})

	t.Run("an existing stamp is never overwritten", func(t *testing.T) {
		// ResetOperationV2ForResume clears CompletedAt on the way back to
		// queued, so a queued row normally has none. If one does survive, it is
		// the earlier, truer completion time and must win.
		earlier := time.Now().UTC().Add(-72 * time.Hour)
		row := buildTestOpRow("op-stamped", "queued")
		row.CompletedAt = &earlier
		require.NoError(t, s.InsertOperationV2(row))

		updated, err := s.SetOperationV2StatusIfQueued("op-stamped", "canceled")
		require.NoError(t, err)
		require.True(t, updated)

		got, err := s.GetOperationV2("op-stamped")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.CompletedAt)
		require.WithinDuration(t, earlier, *got.CompletedAt, time.Second)
	})
}

// TestRepairOpsV2MissingCompletedAt covers the repair behind the v2 half of
// POST /operations/clear-stale.
//
// The rows it targets are the ones the pre-2026-09-07
// SetOperationV2StatusIfQueued produced: a terminal status with completed_at
// null, which is dead to the worker and in-flight to every reader. The dangerous
// mistake would be stamping a row that is still LIVE, so most of these subtests
// assert what the repair must NOT touch.
func TestRepairOpsV2MissingCompletedAt(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	// insert writes a row with CompletedAt forced to nil, bypassing the status
	// setters so the test can construct the corrupt shape directly.
	insert := func(t *testing.T, id, status string) {
		t.Helper()
		row := buildTestOpRow(id, status)
		row.CompletedAt = nil
		require.NoError(t, s.InsertOperationV2(row))
	}

	t.Run("stamps terminal rows and leaves live rows alone", func(t *testing.T) {
		// Terminal: every status in isTerminalV2Status.
		terminal := []string{"completed", "failed", "canceled", "interrupted_dropped"}
		for _, st := range terminal {
			insert(t, "term-"+st, st)
		}
		// Live: the scheduler, the deps waiter, and the startup resume sweep each
		// still own one of these. Stamping any of them hides real work.
		live := []string{"queued", "running", "waiting_deps", "interrupted_quiesced", "interrupted_ask"}
		for _, st := range live {
			insert(t, "live-"+st, st)
		}

		n, err := s.RepairOpsV2MissingCompletedAt()
		require.NoError(t, err)
		require.Equal(t, len(terminal), n,
			"the count must come from rows actually written, not candidates seen")

		for _, st := range terminal {
			got, err := s.GetOperationV2("term-" + st)
			require.NoError(t, err)
			require.NotNil(t, got)
			require.NotNil(t, got.CompletedAt, "%s is terminal and must be stamped", st)
		}
		for _, st := range live {
			got, err := s.GetOperationV2("live-" + st)
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Nil(t, got.CompletedAt, "%s is live and must not be stamped", st)
		}
	})

	t.Run("is idempotent and reports zero on a clean store", func(t *testing.T) {
		// Everything repairable was repaired by the subtest above, so a second
		// pass must find nothing. A non-zero result here would mean the repair
		// is re-stamping rows it already fixed.
		n, err := s.RepairOpsV2MissingCompletedAt()
		require.NoError(t, err)
		require.Zero(t, n)
	})

	t.Run("never overwrites an existing stamp", func(t *testing.T) {
		earlier := time.Now().UTC().Add(-73 * 24 * time.Hour)
		row := buildTestOpRow("term-already-stamped", "canceled")
		row.CompletedAt = &earlier
		require.NoError(t, s.InsertOperationV2(row))

		n, err := s.RepairOpsV2MissingCompletedAt()
		require.NoError(t, err)
		require.Zero(t, n, "an already-stamped row is not a candidate")

		got, err := s.GetOperationV2("term-already-stamped")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.WithinDuration(t, earlier, *got.CompletedAt, time.Second,
			"the original completion time is the truer one and must win")
	})

	t.Run("has no age cutoff", func(t *testing.T) {
		// The row this repair was written for had been stuck for 73 days. Any
		// "recent N" window -- GetRecentOperations(500), the timeline's default
		// range -- would miss it, which is why the scan is unbounded.
		old := time.Now().UTC().Add(-200 * 24 * time.Hour)
		row := buildTestOpRow("term-ancient", "canceled")
		row.CompletedAt = nil
		row.QueuedAt = old
		require.NoError(t, s.InsertOperationV2(row))

		n, err := s.RepairOpsV2MissingCompletedAt()
		require.NoError(t, err)
		require.Equal(t, 1, n)

		got, err := s.GetOperationV2("term-ancient")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.CompletedAt)
	})
}
