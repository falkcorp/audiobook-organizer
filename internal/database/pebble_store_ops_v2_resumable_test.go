// file: internal/database/pebble_store_ops_v2_resumable_test.go
// version: 1.0.0
// guid: 4a8c1e57-9d02-4b6f-83a1-7c5e0f2b9d34
// last-edited: 2026-08-24

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ListResumableOperationsV2 exists because ListActiveOperationsV2 could not see
// the rows that most need resuming. These tests run against a real Pebble store
// and go through UpdateOperationV2Status rather than writing rows by hand, so
// they exercise the actual index maintenance that caused the defect: a
// hand-written row would keep whatever index membership the test gave it and
// prove nothing.

// THE REGRESSION TEST. A row driven to interrupted_quiesced leaves the active
// index and must still be resumable.
func TestListResumableOperationsV2_SeesQuiescedRowsTheActiveIndexDropped(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	require.NoError(t, s.InsertOperationV2(buildTestOpRow("op-quiesced", "queued")))
	// Drive it through the real transition: queued -> running -> quiesced. This
	// is what a dispatch followed by a shutdown does, and it is what deletes the
	// opv2:act: key.
	require.NoError(t, s.UpdateOperationV2Status("op-quiesced", "running", nil, nil, nil))
	require.NoError(t, s.UpdateOperationV2Status("op-quiesced", "interrupted_quiesced", nil, nil, nil))

	active, err := s.ListActiveOperationsV2()
	require.NoError(t, err)
	require.Empty(t, active,
		"the active index must drop a quiesced row; four callers read it as 'in flight'")

	resumable, err := s.ListResumableOperationsV2()
	require.NoError(t, err)
	require.Len(t, resumable, 1,
		"a quiesced row is unfinished business and must stay resumable; this is "+
			"the exact row that stranded library.scan on 2026-08-17 and again on 2026-08-24")
	require.Equal(t, "op-quiesced", resumable[0].ID)
}

// The predicate must not swallow the statuses the sweep already decided on, and
// must not miss the ordinary in-flight ones.
func TestListResumableOperationsV2_StatusMembership(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	cases := []struct {
		status string
		want   bool
		why    string
	}{
		{"queued", true, "an undispatched row is the common resume shape"},
		{"running", true, "a hard kill leaves the row running"},
		{"interrupted_quiesced", true, "the row a graceful shutdown leaves behind"},
		{"interrupted_dropped", false, "the sweep already applied ResumePolicy=drop; " +
			"re-including it relitigates a settled decision on every boot"},
		{"interrupted_ask", false, "awaiting a user decision; resuming it answers for them"},
		{"completed", false, "terminal"},
		{"failed", false, "terminal"},
		{"canceled", false, "a USER cancelled this; resuming it overrides them"},
		{"waiting_deps", false, "owned by the dependency scheduler, not the resume sweep"},
	}

	for _, tc := range cases {
		require.NoError(t, s.InsertOperationV2(buildTestOpRow("op-"+tc.status, "queued")))
		require.NoError(t, s.UpdateOperationV2Status("op-"+tc.status, tc.status, nil, nil, nil))
	}

	resumable, err := s.ListResumableOperationsV2()
	require.NoError(t, err)
	got := make(map[string]bool, len(resumable))
	for _, row := range resumable {
		got[row.Status] = true
	}

	for _, tc := range cases {
		require.Equal(t, tc.want, got[tc.status],
			"status %q: resumable=%v, want %v — %s", tc.status, got[tc.status], tc.want, tc.why)
	}
}

// A row this scan cannot read is an operation that will never resume. It must be
// skipped rather than handed onward as a zero-valued row — resumeAfterStartup
// would call UpdateOperationV2Status("") on it — and the skip is logged, because
// silently dropping it looks exactly like the blindness this method cures.
func TestListResumableOperationsV2_SkipsUnreadableRows(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)
	p := store.(*PebbleStore)

	// One good row, so the scan cannot pass by returning nothing at all.
	require.NoError(t, s.InsertOperationV2(buildTestOpRow("op-good", "queued")))

	// Garbage that is not JSON at all.
	require.NoError(t, p.db.Set([]byte("opv2:op:op-garbage"), []byte("{not json"), nil))
	// Valid JSON, resumable status, but no id — the shape ListActiveOperationsV2
	// already guards against and this one used not to.
	require.NoError(t, p.db.Set([]byte("opv2:op:op-noid"),
		[]byte(`{"Status":"interrupted_quiesced","DefID":"library.scan"}`), nil))

	resumable, err := s.ListResumableOperationsV2()
	require.NoError(t, err, "one bad row must not fail the whole scan; "+
		"an error here strands every resumable op on the server")
	require.Len(t, resumable, 1, "want only op-good: got %+v", resumable)
	require.Equal(t, "op-good", resumable[0].ID)
}

