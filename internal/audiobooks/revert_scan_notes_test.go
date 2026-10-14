// file: internal/audiobooks/revert_scan_notes_test.go
// version: 1.0.0
// guid: 4cba3ce8-6fd4-42a0-a546-052ba5a369da
// last-edited: 2026-10-14

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// An AI scan apply whose results were all merged already wrote nothing but its
// "result applied" notes. Its revert says so, not the Repairs plan-record message
// ("finish the run's books by hand to clear its hold"), which names a hold this
// operation never had.
func TestRevertOperation_AnAIApplyThatOnlyMarkedResultsSaysSo(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })

	row, err := undo.NewAIScanResultAppliedRow(7, 11)
	require.NoError(t, err)
	row.ID, row.OperationID = "note-1", "op-notes-only"
	require.NoError(t, st.CreateOperationChange(row))

	res, err := NewRevertService(st).RevertOperation("op-notes-only")
	require.Nil(t, res)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already merged")
	require.NotContains(t, err.Error(), "plan record")
	require.NotContains(t, err.Error(), "hold")
}
