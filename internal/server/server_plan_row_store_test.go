// file: internal/server/server_plan_row_store_test.go
// version: 1.0.1
// guid: 2e8c5a14-9b73-4d06-a1f2-6c0d3e7b5a98
// last-edited: 2026-10-27

package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The plan rows live in a capability the Store interface does not carry
// (database.OpRowStore), so what the server hands the maintenance plugin as its
// operation store must still resolve it: a Repairs plan fails at its start
// (and the rows routes answer 503) on a server where it does not. This is the
// production seam; the unit tests use a fake.
func TestOperationQueueStoreResolvesThePlanRowStore(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	srv := newTestServer(t, st)

	rs, err := repairs.PlanRowStoreOf(srv.OperationQueueStore())
	require.NoError(t, err, "the server's operation store cannot keep plan rows")

	// And it is the live one: a plan saved through it reads back.
	plan := &repairs.PlanResult{FixerID: "f", Total: 1, Rows: []repairs.Row{{RowID: "r1", BookIDs: []string{"r1"}, Fingerprint: "x"}}}
	require.NoError(t, repairs.SavePlan(rs, "op-seam", plan))
	t.Cleanup(func() { _ = repairs.ClearPlanRows(rs, "op-seam") })
	require.Equal(t, repairs.PlanRowStoreV2, plan.RowStore)
	got, err := rs.GetOpRows("op-seam", []string{repairs.PlanRowPrefix + "r1"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotEmpty(t, got[0])
}
