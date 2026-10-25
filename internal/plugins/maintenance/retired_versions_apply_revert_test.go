// file: internal/plugins/maintenance/retired_versions_apply_revert_test.go
// version: 1.1.0
// guid: 9b2e5d74-1c38-4f06-a7d9-6e0b3f8a2c51
// last-edited: 2026-10-25

package maintenance

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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

	report, err := undo.PreflightUndoConflicts(rf.st, "op-fresh")
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

// planRecordOf decodes the plan record op opID journaled for shell.
func (rf *retiredFixture) planRecordOf(opID, shell string) retirePlanRecord {
	rf.t.Helper()
	changes, err := rf.st.GetOperationChanges(opID)
	require.NoError(rf.t, err)
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeRepairPlanRecord && c.FieldName == "row:"+shell {
			var rec retirePlanRecord
			require.NoError(rf.t, json.Unmarshal([]byte(c.NewValue), &rec))
			return rec
		}
	}
	rf.t.Fatalf("op %s journaled no plan record for %s", opID, shell)
	return retirePlanRecord{}
}

// S2 of the review of #4144: the tail of a retire moves only the external ids
// the retire PLANNED (its plan record). An id that attached to the shell after
// the retire was cut off was never judged by the classifier's guards; a fresh
// plan's finish must not hand it to the holder.
func TestRetiredApply_TailMovesOnlyThePlannedExternalIDs(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	var armed atomic.Bool
	armed.Store(true)
	rf.f.retireWriterFor = func(w *repairs.Writer) (retireWriter, error) {
		inner, err := rf.f.newJournaledRetireWriter(w)
		return failingExternalIDs{inner, &armed}, err
	}
	shell, holder := rf.ids["would-retire"], rf.ids["primary"]
	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-cut")
	require.Equal(t, 1, out.Partial, "%v %+v", out.ByOutcome, out.Rows)
	armed.Store(false)

	rec := rf.planRecordOf("op-cut", shell)
	require.Equal(t, []plannedExternalID{{Source: "itunes", ExternalID: "PID-WR-1"}}, rec.ExternalIDs)

	// The id arrives after the plan record was written.
	rf.addExternalID("would-retire", "PID-LATE")

	fresh := rf.planAll()
	row := rf.retiredPlanRow(fresh, "would-retire")
	require.Equal(t, retCatTailPending, row.Class)
	require.Contains(t, row.Reason, "1 external id(s)", "only the planned id is pending: %s", row.Reason)
	require.Contains(t, row.Reason, "not in the retire's plan record", row.Reason)

	out = rf.applyRows(rf.f, fresh, []string{shell}, false, "op-fresh")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	require.Equal(t, 1, rf.journalByType("op-fresh")[undo.ChangeTypeExternalIDReassign])
	onHolder, err := rf.st.GetExternalIDsForBook(holder)
	require.NoError(t, err)
	require.Len(t, onHolder, 1)
	require.Equal(t, "PID-WR-1", onHolder[0].ExternalID)
	onShell, err := rf.st.GetExternalIDsForBook(shell)
	require.NoError(t, err)
	require.Len(t, onShell, 1, "the unplanned id stays on the shell")
	require.Equal(t, "PID-LATE", onShell[0].ExternalID)

	// Nothing planned is left: the shell reads as done, not as a tail forever
	// pending on an id the finish will not move.
	after := rf.retiredPlanRow(rf.planAll(), "would-retire")
	require.Equal(t, retCatAlreadyRetired, after.Class, "%+v", after)
	require.False(t, after.Applicable())
	require.Contains(t, after.Reason, "not in the retire's plan record")
}

// A finished retire whose shell later gains an external id is not a pending tail:
// the plan lists nothing to write, where it used to list the new id for a move.
func TestRetiredApply_AnIDAttachedAfterAFinishedRetireIsNotPending(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	shell := rf.ids["would-retire"]
	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-whole")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	rf.addExternalID("would-retire", "PID-LATE")

	fresh := rf.planAll()
	row := rf.retiredPlanRow(fresh, "would-retire")
	require.Equal(t, retCatAlreadyRetired, row.Class, "%+v", row)
	require.False(t, row.Applicable(), "%+v", row)
	require.NotContains(t, rf.applicableIDs(fresh), shell)
	onShell, err := rf.st.GetExternalIDsForBook(shell)
	require.NoError(t, err)
	require.Len(t, onShell, 1)
}

// A retired version with no plan record to read (a journal that is gone, or a
// version made outside this fixer) moves no id: the rest of its tail is still
// finished, and the row says why the ids stay.
func TestRetiredApply_TailWithoutAPlanRecordMovesNoID(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.live("primary", "g1", rf.file("g1/primary.mp3"))
	rf.trashed("manual", "g1", 30, rf.file("g1/manual-1.mp3"))
	shell, holder := rf.ids["manual"], rf.ids["primary"]
	rf.addExternalID("manual", "PID-MANUAL")
	rows, err := rf.st.GetBookFiles(shell)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	at := rf.now.Add(-time.Hour).UTC().Truncate(time.Microsecond)
	_, err = rf.st.CreateBookVersion(&database.BookVersion{ID: "rv-no-record", BookID: holder,
		Status: database.BookVersionStatusRetired, Source: retiredVersionSource,
		RetiredFromBookID: shell, RetireOpID: "op-journal-gone", RetiredAt: &at})
	require.NoError(t, err)
	require.NoError(t, rf.st.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rows[0].ID}, SourceBookID: shell}}, holder, "rv-no-record"))

	res := rf.planAll()
	row := rf.retiredPlanRow(res, "manual")
	require.Equal(t, retCatTailPending, row.Class, "%+v", row)
	require.Contains(t, row.Reason, "no plan record")
	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-tail")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	j := rf.journalByType("op-tail")
	require.Zero(t, j[undo.ChangeTypeExternalIDReassign], "%v", j)
	require.Equal(t, 1, j[undo.ChangeTypeBookMergedInto], "the rest of the tail still finishes: %v", j)
	onShell, err := rf.st.GetExternalIDsForBook(shell)
	require.NoError(t, err)
	require.Len(t, onShell, 1, "the id stays on the shell")
}
