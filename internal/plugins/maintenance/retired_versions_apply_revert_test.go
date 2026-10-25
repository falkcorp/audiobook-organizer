// file: internal/plugins/maintenance/retired_versions_apply_revert_test.go
// version: 1.0.0
// guid: 9b2e5d74-1c38-4f06-a7d9-6e0b3f8a2c51
// last-edited: 2026-10-25

package maintenance

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// cutOffThenFinishedByAFreshPlan leaves one retired shell whose retire was cut
// off after its commit point (op-cut: its rows retired, its id and link not
// moved) and finished by a fresh plan (op-fresh: the id move and the link, no
// retire row). It returns the shell and the holder.
func cutOffThenFinishedByAFreshPlan(t *testing.T, rf *retiredFixture) (shell, holder string) {
	t.Helper()
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	var armed atomic.Bool
	armed.Store(true)
	rf.f.retireWriterFor = func(w *repairs.Writer) (retireWriter, error) {
		inner, err := rf.f.newJournaledRetireWriter(w)
		return failingExternalIDs{inner, &armed}, err
	}
	shell, holder = rf.ids["would-retire"], rf.ids["primary"]
	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-cut")
	require.Equal(t, 1, out.Partial, "%v %+v", out.ByOutcome, out.Rows)
	armed.Store(false)

	fresh := rf.planAll()
	require.Equal(t, retCatTailPending, rf.retiredPlanRow(fresh, "would-retire").Class)
	out = rf.applyRows(rf.f, fresh, []string{shell}, false, "op-fresh")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	j := rf.journalByType("op-fresh")
	require.Zero(t, j[retChangeBookFileRetire], "the tail op holds no retire row: %v", j)
	require.Equal(t, 1, j[undo.ChangeTypeExternalIDReassign], "%v", j)
	require.Equal(t, 1, j[undo.ChangeTypeBookMergedInto], "%v", j)
	return shell, holder
}

// B1' of the review of #4144: the tail a fresh plan finishes is an operation
// with an id move and a merged-into row and NO retire row, so the retire-op
// block does not see it. Reverting it would hand the shell its iTunes id back
// and clear its link while its rows stay retired under the holder: a live-looking
// book that owns no file. The revert refuses both rows (changing nothing) and
// the preflight offers no Undo.
func TestRetiredApply_RevertOfAFreshPlanTailOpIsRefused(t *testing.T) {
	rf := newRetiredFixture(t)
	shell, holder := cutOffThenFinishedByAFreshPlan(t, rf)

	report, err := preflightUndoConflicts(rf.st, "op-fresh")
	require.NoError(t, err)
	require.Zero(t, report.Safe, "%+v", report)
	require.Len(t, report.CheckFailed, 2, "%+v", report)
	for _, it := range report.CheckFailed {
		require.Equal(t, undo.ReasonBookIsRetiredShell, it.Reason, "%+v", it)
	}

	res, err := audiobooks.NewRevertService(rf.st).RevertOperation("op-fresh")
	require.Error(t, err)
	require.NotNil(t, res)
	require.Zero(t, res.Restored, "%+v", res)
	require.Equal(t, 2, res.Failed, "%+v", res)

	owner, err := rf.st.GetBookByExternalID("itunes", "PID-WR-1")
	require.NoError(t, err)
	require.Equal(t, holder, owner, "the id stays on the holder")
	b, err := rf.st.GetBookByID(shell)
	require.NoError(t, err)
	require.NotNil(t, b.MergedIntoBookID)
	require.Equal(t, holder, *b.MergedIntoBookID, "the link stays")
	changes, err := rf.st.GetOperationChanges("op-fresh")
	require.NoError(t, err)
	for _, c := range changes {
		require.Nil(t, c.RevertedAt, "no row is marked reverted: %s", c.ChangeType)
	}
}
