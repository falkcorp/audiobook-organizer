// file: internal/plugins/maintenance/retired_versions_apply_revert_test.go
// version: 1.3.1
// guid: 9b2e5d74-1c38-4f06-a7d9-6e0b3f8a2c51
// last-edited: 2026-10-29

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
	require.Zero(t, j[undo.ChangeTypeBookFileRetire], "the tail op holds no retire row: %v", j)
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

// An Apply's own rows are the ones the undo package reads (part 5), so the
// operation is offered an Undo and a revert puts the shell's rows back: the
// retire-operation refusal of part 7 stands only for a retire row the undo
// cannot read.
func TestRetiredApply_RevertOfAWholeApplyRestoresTheShell(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	res := rf.planAll()
	shell, holder := rf.ids["would-retire"], rf.ids["primary"]
	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-whole")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	held, err := rf.st.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.NotEmpty(t, held, "fixture: the retire put rows under the holder")

	changes, err := rf.st.GetOperationChanges("op-whole")
	require.NoError(t, err)
	require.Nil(t, undo.RetireRevertBlock("op-whole", changes), "a well-formed Apply is not blocked")
	require.True(t, undo.OperationRevertible(changes))
	report, err := preflightUndoConflicts(rf.st, "op-whole")
	require.NoError(t, err)
	require.Empty(t, report.CheckFailed, "%+v", report)
	require.NotZero(t, report.Safe, "%+v", report)

	rev, err := audiobooks.NewRevertService(rf.st).RevertOperation("op-whole")
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "%+v", rev)
	held, err = rf.st.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.Empty(t, held, "the retired rows are back on the shell")
	files, err := rf.st.GetBookFiles(shell)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	// The revert un-retires the rows first and deletes the emptied version last,
	// which needs the version to still be retired at that point (review N5 of
	// #4173: clearing the status on the last un-retire would leave it behind).
	vs, err := rf.st.GetRetiredVersionsFromBook(shell)
	require.NoError(t, err)
	require.Empty(t, vs, "the revert deleted the version it emptied")
	n, err := database.RetiredRowsHeldFor(rf.st, shell)
	require.NoError(t, err)
	require.Zero(t, n, "and the shell holds no retired row")
}

// retiredHandBack is what a revert of op leaves on the shell: its merged-into
// link, its external ids, and the retired rows it still holds.
func (rf *retiredFixture) retiredHandBack(shell string) (mergedInto bool, extIDs, heldRows int) {
	rf.t.Helper()
	b, err := rf.st.GetBookByID(shell)
	require.NoError(rf.t, err)
	ids, err := rf.st.GetExternalIDsForBook(shell)
	require.NoError(rf.t, err)
	n, err := database.RetiredRowsHeldFor(rf.st, shell)
	require.NoError(rf.t, err)
	return b.MergedIntoBookID != nil, len(ids), n
}

// refusedTypes counts the rows of a preflight report that were refused, by
// change type.
func refusedTypes(report *undo.UndoConflictReport) map[string]int {
	out := map[string]int{}
	for _, it := range report.CheckFailed {
		out[it.ChangeType]++
	}
	for _, it := range report.ContentChanged {
		out[it.ChangeType]++
	}
	return out
}

// F1 of the review of #4236 (store-level repro): after an Apply, the retired
// version is re-homed under another book (RehomeRetiredVersion, as a later
// operation's chain step does). Reverting the Apply could not un-retire the
// shell's rows (they are no longer the holder's), yet its hand-back rows ran
// first and put the external id and the merged-into link back on the shell,
// leaving a shell that looks live and owns no file. The id and link go back
// only together with the files: here neither does, the preflight offers no
// Undo of those rows, and the revert reports them failed.
func TestRetiredApply_RevertAfterTheVersionWasRehomedKeepsTheShellWhole(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	rf.seedApplyLibrary()
	h2 := rf.live("robin-holder", "g2")
	res := rf.planAll()
	shell, holder := rf.ids["would-retire"], rf.ids["primary"]
	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-whole")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)
	ids0, err := rf.st.GetExternalIDsForBook(holder)
	require.NoError(t, err)
	require.NotEmpty(t, ids0, "fixture: the retire moved an external id onto the holder")
	held, err := rf.st.GetRetiredBookFiles(holder)
	require.NoError(t, err)
	require.NotEmpty(t, held)
	require.NoError(t, rf.st.RehomeRetiredVersion(held[0].VersionID, h2))
	linked0, _, heldBefore := rf.retiredHandBack(shell)
	require.True(t, linked0)
	require.Equal(t, len(held), heldBefore)

	report, err := preflightUndoConflicts(rf.st, "op-whole")
	require.NoError(t, err)
	refused := refusedTypes(report)
	require.NotZero(t, refused[undo.ChangeTypeBookFileRetire], "the retire rows cannot be un-retired: %+v", report)
	require.NotZero(t, refused[undo.ChangeTypeBookMergedInto], "the link must not be offered alone: %+v", report)
	require.NotZero(t, refused[undo.ChangeTypeExternalIDReassign], "the id must not be offered alone: %+v", report)
	for _, it := range report.CheckFailed {
		if it.ChangeType == undo.ChangeTypeBookMergedInto || it.ChangeType == undo.ChangeTypeExternalIDReassign {
			require.Equal(t, undo.ReasonDependentNotReverted, it.Reason, "%+v", it)
		}
	}

	rev, err := audiobooks.NewRevertService(rf.st).RevertOperation("op-whole")
	require.ErrorContains(t, err, "partially reverted", "a revert that left the shell's files retired is reported, not a success")
	require.NotNil(t, rev)
	require.NotZero(t, rev.Failed, "a revert that could not un-retire the shell is not a success: %+v", rev)
	require.Zero(t, rev.RestoredTypes[undo.ChangeTypeBookMergedInto], "%+v", rev)
	require.Zero(t, rev.RestoredTypes[undo.ChangeTypeExternalIDReassign], "%+v", rev)
	linked, extIDs, heldAfter := rf.retiredHandBack(shell)
	require.True(t, linked, "the shell keeps its merged-into link")
	require.Zero(t, extIDs, "the shell is not given its external id back")
	require.Equal(t, heldBefore, heldAfter, "its files stay retired")
}

// F1, production path: the holder is trashed and a second Apply retires it onto
// a live book; its chain step re-homes the first Apply's version. Reverting the
// first Apply restored the shell's merged-into link while both its retire rows
// and the version's creation failed as changed-since.
func TestRetiredApply_RevertAfterAChainApplyKeepsTheShellWhole(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.enableApply()
	shared := rf.file("g1/shared.mp3")
	rf.live("batman", "g1", shared)
	rf.trashed("robin", "g1", 30, rf.file("g1/own-1.mp3"), rf.file("g1/own-2.mp3"))
	rf.addExternalID("robin", "PID-ROBIN-1")
	shell, holder := rf.ids["robin"], rf.ids["batman"]
	res := rf.planAll()
	out := rf.applyRows(rf.f, res, []string{shell}, false, "op-1")
	require.Equal(t, 1, out.Applied, "%v %+v", out.ByOutcome, out.Rows)

	rf.live("nightwing", "g1", rf.file("g1/new.mp3"))
	_, err := rf.st.ModifyBook(holder, func(b *database.Book) error {
		b.MarkedForDeletion = retiredPtr(true)
		b.MarkedForDeletionAt = retiredPtr(rf.now.Add(-30 * 24 * time.Hour))
		b.IsPrimaryVersion = retiredPtr(false)
		return nil
	})
	require.NoError(t, err)
	res2 := rf.planAll()
	out2 := rf.applyRows(rf.f, res2, []string{holder}, false, "op-2")
	require.Equal(t, 1, out2.Applied, "the second Apply must run: %v %+v", out2.ByOutcome, out2.Rows)
	_, _, heldBefore := rf.retiredHandBack(shell)
	require.NotZero(t, heldBefore)

	report, err := preflightUndoConflicts(rf.st, "op-1")
	require.NoError(t, err)
	refused := refusedTypes(report)
	require.NotZero(t, refused[undo.ChangeTypeBookMergedInto], "%+v", report)
	require.NotZero(t, refused[undo.ChangeTypeExternalIDReassign], "%+v", report)

	rev, err := audiobooks.NewRevertService(rf.st).RevertOperation("op-1")
	require.ErrorContains(t, err, "partially reverted", "a revert that left the shell's files retired is reported, not a success")
	require.NotNil(t, rev)
	require.NotZero(t, rev.Failed, "%+v", rev)
	linked, extIDs, heldAfter := rf.retiredHandBack(shell)
	require.True(t, linked, "the shell keeps its merged-into link")
	require.Zero(t, extIDs, "the shell is not given its external id back")
	require.Equal(t, heldBefore, heldAfter, "its files stay retired")
}
