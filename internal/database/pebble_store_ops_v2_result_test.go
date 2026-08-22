// file: internal/database/pebble_store_ops_v2_result_test.go
// version: 1.0.0
// guid: 8c4b1e73-2a95-4f60-b8d1-6e037a95c2f4
// last-edited: 2026-08-22

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSetOperationV2Result_RoundTrip is the basic contract: what goes in comes out.
func TestSetOperationV2Result_RoundTrip(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	require.NoError(t, s.InsertOperationV2(buildTestOpRow("op-res-1", "running")))

	// A payload containing a value nothing else in this test could produce, so a
	// passing assertion cannot be explained by an empty or defaulted field.
	const payload = `{"mode":"groups","suggestions":41879}`
	require.NoError(t, s.SetOperationV2Result("op-res-1", payload))

	row, err := s.GetOperationV2("op-res-1")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.ResultData, "ResultData must be non-nil after a write")
	require.Equal(t, payload, *row.ResultData)
}

// TestSetOperationV2Result_MissingRowErrors pins the behaviour that matters most.
//
// A setter that silently succeeds on a nonexistent row loses the payload with no
// signal — the exact shape of the defect that left 1,737 v1 operation rows stranded
// at "pending". Half of UpdateOperationResultData's callers discard its error, so
// the store level is the only place this can be guaranteed.
func TestSetOperationV2Result_MissingRowErrors(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	err := s.SetOperationV2Result("op-does-not-exist", `{"x":1}`)
	require.Error(t, err, "writing a result to an absent row must not silently succeed")
	require.Contains(t, err.Error(), "op-does-not-exist",
		"the error must name the id, or a caller cannot tell which op lost its result")
}

// TestSetOperationV2Result_Overwrites verifies a second write replaces the first
// rather than appending or being ignored. Ops that report progressively (the batch
// poller rewrites its payload as batches land) depend on this.
func TestSetOperationV2Result_Overwrites(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	require.NoError(t, s.InsertOperationV2(buildTestOpRow("op-res-2", "running")))
	require.NoError(t, s.SetOperationV2Result("op-res-2", `{"pass":1}`))
	require.NoError(t, s.SetOperationV2Result("op-res-2", `{"pass":2}`))

	row, err := s.GetOperationV2("op-res-2")
	require.NoError(t, err)
	require.NotNil(t, row.ResultData)
	require.Equal(t, `{"pass":2}`, *row.ResultData)
}

// TestSetOperationV2Result_PreservesAdjacentFields guards the read-modify-write.
//
// SetOperationV2Result reloads the whole row, mutates one field and re-marshals it.
// If it ever reloaded into a zero-valued row — or was rewritten to construct one —
// it would silently blank Status, progress and Params. Nothing else in the system
// would report that; the op would just quietly forget what it was doing.
func TestSetOperationV2Result_PreservesAdjacentFields(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	s := store.(OpsV2Store)

	row := buildTestOpRow("op-res-3", "running")
	row.ProgressCurrent = 37
	row.ProgressTotal = 99
	row.ProgressMessage = "halfway"
	row.Params = `{"scan_id":7}`
	row.ResumeCount = 2
	require.NoError(t, s.InsertOperationV2(row))

	require.NoError(t, s.SetOperationV2Result("op-res-3", `{"done":true}`))

	got, err := s.GetOperationV2("op-res-3")
	require.NoError(t, err)
	require.Equal(t, "running", got.Status)
	require.Equal(t, 37, got.ProgressCurrent)
	require.Equal(t, 99, got.ProgressTotal)
	require.Equal(t, "halfway", got.ProgressMessage)
	require.Equal(t, `{"scan_id":7}`, got.Params)
	require.Equal(t, 2, got.ResumeCount)
	require.Equal(t, "test-def", got.DefID)
}
