// file: internal/plugins/maintenance/master_list_import_apply_test.go
// version: 1.0.0
// guid: 2f6a9c13-d874-4e05-b1a7-83c5e0d94f68
// last-edited: 2026-10-23

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Every title, author, identifier and user here is synthetic.

const (
	mlTestApplyOp = "op-ml-apply"
	mlTestPlanOp  = "op-ml-plan"

	// mlApplyHeader carries the read-status columns the apply reads.
	mlApplyHeader = "title,author,asin,isbn13,runtime_min,status,finished_at,last_listened_at,progress_pct,position_sec,source,source_id"
)

// mlFinishedAt is the list's finish time in the fixtures (before the lab's
// clock, 2026-10-23).
var mlFinishedAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func mlTS(t time.Time) string { return t.Format(time.RFC3339) }

// applyLine is one list row in mlApplyHeader order.
func applyLine(title, author, asin string, runtimeMin int, status, finishedAt, lastListened string, pct, posSec int) string {
	num := func(n int) string {
		if n == 0 {
			return ""
		}
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%s,%s,%s,,%s,%s,%s,%s,%s,%s,,", title, author, asin, num(runtimeMin), status, finishedAt, lastListened, num(pct), num(posSec))
}

func (l *mlLab) apply(plan *repairs.PlanResult, rowIDs []string, dryRun bool) map[string]repairs.RowResult {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	w := repairs.NewWriter(l.store, l.store, l.fixer.ID(), "bulk_update", "repairs-").
		WithJournal(l.store, l.store, mlTestApplyOp).WithUserState(l.p.deps.UserReadStateStore())
	res, err := repairs.RunApply(context.Background(), l.fixer, plan, mlTestPlanOp, rowIDs, dryRun,
		repairs.ApplyDeps{Guard: l.store, Tags: l.p.repairsGuardTags(), Series: repairs.SeriesNamesFrom(series), Writer: w, OpID: mlTestApplyOp}, &fakeReporter{})
	require.NoError(l.t, err)
	out := map[string]repairs.RowResult{}
	for _, r := range res.Rows {
		out[r.RowID] = r
	}
	return out
}

func (l *mlLab) state(key string) *database.UserBookState {
	l.t.Helper()
	s, err := l.store.GetUserBookState(l.user, l.ids[key])
	require.NoError(l.t, err)
	return s
}

func (l *mlLab) setState(key string, s database.UserBookState) {
	l.t.Helper()
	s.UserID, s.BookID = l.user, l.ids[key]
	require.NoError(l.t, l.store.SetUserBookState(&s))
}

func (l *mlLab) positions(key string) []database.UserPosition {
	l.t.Helper()
	p, err := l.store.ListUserPositionsForBook(l.user, l.ids[key])
	require.NoError(l.t, err)
	return p
}

func (l *mlLab) dump() map[string]string {
	l.t.Helper()
	kvs, err := l.store.ScanPrefix("")
	require.NoError(l.t, err)
	out := map[string]string{}
	for _, kv := range kvs {
		out[kv.Key] = string(kv.Value)
	}
	return out
}

// Every decision of a matched row, in one plan: what is written, and the class
// of every row that is not.
func TestMasterListImport_DecisionClasses(t *testing.T) {
	l := newMLLab(t)
	yes := true
	older, newer := mlFinishedAt.Add(-48*time.Hour), mlFinishedAt.Add(48*time.Hour)

	l.book("fresh", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", minutes: 600})
	l.book("done", mlBook{title: "Done Already", author: "Pat Example", asin: "B100000002", minutes: 600})
	l.setState("done", database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: older})
	l.book("doneprog", mlBook{title: "Finished Here", author: "Pat Example", asin: "B100000003", minutes: 600})
	l.setState("doneprog", database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: older})
	l.book("newer", mlBook{title: "Newer Here", author: "Pat Example", asin: "B100000004", minutes: 600})
	l.setState("newer", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 40, LastActivityAt: older})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["newer"], "abs", 900, newer))
	l.book("partial", mlBook{title: "Partial Older", author: "Pat Example", asin: "B100000005", minutes: 600})
	l.setState("partial", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 30, LastActivityAt: older, HideFromContinueListening: true})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["partial"], "abs", 600, older))
	l.book("prog", mlBook{title: "Halfway There", author: "Pat Example", asin: "B100000006", minutes: 500})
	l.book("pctonly", mlBook{title: "Percent Only", author: "Pat Example", asin: "B100000007", minutes: 400})
	l.book("haspos", mlBook{title: "Has Position", author: "Pat Example", asin: "B100000008", minutes: 600})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["haspos"], "abs", 60, older))
	l.book("quit", mlBook{title: "Gave Up", author: "Pat Example", asin: "B100000009", minutes: 600})
	l.setState("quit", database.UserBookState{Status: database.UserBookStatusAbandoned, StatusManual: true, LastActivityAt: older})
	l.book("handun", mlBook{title: "Marked Unstarted", author: "Pat Example", asin: "B100000010", minutes: 600})
	l.setState("handun", database.UserBookState{Status: database.UserBookStatusUnstarted, StatusManual: true, LastActivityAt: older})
	l.book("nots", mlBook{title: "No Stamp", author: "Pat Example", asin: "B100000011", minutes: 600})
	l.book("nots2", mlBook{title: "No Listen Stamp", author: "Pat Example", asin: "B100000012", minutes: 600})
	l.book("noprog", mlBook{title: "No Progress", author: "Pat Example", asin: "B100000013", minutes: 600})
	l.book("nostatus", mlBook{title: "No Status", author: "Pat Example", asin: "B100000014", minutes: 600})
	l.book("idle", mlBook{title: "Not Yet", author: "Pat Example", asin: "B100000015", minutes: 600})
	l.book("listquit", mlBook{title: "List Quit", author: "Pat Example", asin: "B100000016", minutes: 600})
	// A twin copy in the same version group: listening on it counts.
	l.book("twin", mlBook{title: "Twin Copy", author: "Pat Example", asin: "B100000017", vg: "vg-a", primary: &yes, minutes: 600})
	l.book("twin2", mlBook{title: "Twin Copy", author: "Pat Example", vg: "vg-a", minutes: 600})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["twin2"], "abs", 300, newer))

	fin, listen := mlTS(mlFinishedAt), mlTS(mlFinishedAt)
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Fresh Finish", "Pat Example", "B100000001", 600, "finished", fin, "", 0, 0),
		applyLine("Done Already", "Pat Example", "B100000002", 600, "finished", fin, "", 0, 0),
		applyLine("Finished Here", "Pat Example", "B100000003", 600, "in_progress", "", mlTS(newer), 20, 0),
		applyLine("Newer Here", "Pat Example", "B100000004", 600, "finished", fin, "", 0, 0),
		applyLine("Partial Older", "Pat Example", "B100000005", 600, "finished", fin, "", 0, 0),
		applyLine("Halfway There", "Pat Example", "B100000006", 500, "in_progress", "", listen, 0, 7200),
		applyLine("Percent Only", "Pat Example", "B100000007", 400, "in_progress", "", listen, 25, 0),
		applyLine("Has Position", "Pat Example", "B100000008", 600, "in_progress", "", listen, 40, 0),
		applyLine("Gave Up", "Pat Example", "B100000009", 600, "finished", fin, "", 0, 0),
		applyLine("Marked Unstarted", "Pat Example", "B100000010", 600, "finished", fin, "", 0, 0),
		applyLine("No Stamp", "Pat Example", "B100000011", 600, "finished", "", "", 0, 0),
		applyLine("No Listen Stamp", "Pat Example", "B100000012", 600, "in_progress", "", "", 0, 3600),
		applyLine("No Progress", "Pat Example", "B100000013", 600, "in_progress", "", listen, 0, 0),
		applyLine("No Status", "Pat Example", "B100000014", 600, "", "", "", 0, 0),
		applyLine("Not Yet", "Pat Example", "B100000015", 600, "unstarted", "", "", 0, 0),
		applyLine("List Quit", "Pat Example", "B100000016", 600, "abandoned", "", "", 0, 0),
		applyLine("Twin Copy", "Pat Example", "B100000017", 600, "finished", fin, "", 0, 0),
	)
	plan, rows := l.plan(listID)

	want := map[string]string{
		"asin:B100000001": mlMatchedHigh,
		"asin:B100000002": mlSkipAlreadyFinished,
		"asin:B100000003": mlSkipAlreadyFinished,
		"asin:B100000004": mlSkipNewerLocal,
		"asin:B100000005": mlMatchedHigh,
		"asin:B100000006": mlMatchedHigh,
		"asin:B100000007": mlMatchedHigh,
		"asin:B100000008": mlSkipLocalProgress,
		"asin:B100000009": mlSkipUserAbandoned,
		"asin:B100000010": mlSkipManualUnstarted,
		"asin:B100000011": mlSkipNoTimestamp,
		"asin:B100000012": mlSkipNoTimestamp,
		"asin:B100000013": mlSkipNoProgress,
		"asin:B100000014": mlSkipNoStatus,
		"asin:B100000015": mlSkipNotStarted,
		"asin:B100000016": mlSkipListAbandoned,
		"asin:B100000017": mlSkipNewerLocal,
	}
	require.Len(t, rows, len(want))
	for id, class := range want {
		require.Contains(t, rows, id)
		require.Equal(t, class, rows[id].Class, "%s: %s", id, rows[id].Reason)
		applicable := class == mlMatchedHigh
		require.Equal(t, applicable, rows[id].Applicable(), id)
		if !applicable {
			require.Equal(t, class, rows[id].Skipped, id)
		}
	}
	require.Equal(t, 4, plan.Applicable)
	require.Equal(t, 4, plan.ByClass[mlMatchedHigh])
	require.Equal(t, 1, plan.ByClass[mlSkipAlreadyFinished]-1, "two rows, both counted under their class")
	require.Contains(t, rows["asin:B100000017"].Reason, l.ids["twin2"], "the newer listen was on the other copy")

	// The row proposes what the list says and shows what is there now.
	r := rows["asin:B100000006"]
	assert.Equal(t, map[string]string{"user": l.user, "read_status": "in_progress", "position_sec": "7200"}, r.Proposed)
	assert.Equal(t, "(none)", r.Current["status"])
	assert.Equal(t, "30", rows["asin:B100000005"].Current["progress_pct"])

	// The plan wrote nothing.
	assert.Nil(t, l.state("fresh"))
	assert.Empty(t, l.positions("prog"))
}

func TestMasterListImport_ApplyWritesAndRevertRestores(t *testing.T) {
	l := newMLLab(t)
	older := mlFinishedAt.Add(-48 * time.Hour)
	l.book("fresh", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", minutes: 600})
	l.book("partial", mlBook{title: "Partial Older", author: "Pat Example", asin: "B100000005", minutes: 600})
	l.setState("partial", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 30, LastActivityAt: older,
		HideFromContinueListening: true, LastSegmentID: "abs", TotalListenedSeconds: 600})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["partial"], "abs", 600, older))
	l.book("prog", mlBook{title: "Halfway There", author: "Pat Example", asin: "B100000006", minutes: 500})
	l.book("pct", mlBook{title: "Percent Only", author: "Pat Example", asin: "B100000007", minutes: 400})
	l.book("done", mlBook{title: "Done Already", author: "Pat Example", asin: "B100000002", minutes: 600})
	l.setState("done", database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: older})

	fin := mlTS(mlFinishedAt)
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Fresh Finish", "Pat Example", "B100000001", 600, "finished", fin, "", 0, 0),
		applyLine("Partial Older", "Pat Example", "B100000005", 600, "finished", fin, "", 0, 0),
		applyLine("Halfway There", "Pat Example", "B100000006", 500, "in_progress", "", fin, 0, 7200),
		applyLine("Percent Only", "Pat Example", "B100000007", 400, "in_progress", "", fin, 25, 0),
		applyLine("Done Already", "Pat Example", "B100000002", 600, "finished", fin, "", 0, 0),
	)
	doneBefore := l.state("done")
	plan, _ := l.plan(listID)
	require.Equal(t, 4, plan.Applicable)
	ids := []string{"asin:B100000001", "asin:B100000005", "asin:B100000006", "asin:B100000007", "asin:B100000002"}
	res := l.apply(plan, ids, false)
	for _, id := range ids[:4] {
		require.Equal(t, repairs.OutcomeApplied, res[id].Outcome, "%s: %+v", id, res[id])
	}
	require.Equal(t, repairs.OutcomeNotApplicable, res["asin:B100000002"].Outcome, "never re-finishes a finished book")

	// Finished: manual, 100%, the list's finish time as finished_at and last
	// activity (never now); a book with no position gets one at its end.
	s := l.state("fresh")
	require.NotNil(t, s)
	require.Equal(t, database.UserBookStatusFinished, s.Status)
	require.True(t, s.StatusManual)
	require.Equal(t, 100, s.ProgressPct)
	require.True(t, s.FinishedAt.Equal(mlFinishedAt), "finished_at %v", s.FinishedAt)
	require.True(t, s.LastActivityAt.Equal(mlFinishedAt))
	pos := l.positions("fresh")
	require.Len(t, pos, 1)
	require.Equal(t, "abs", pos[0].SegmentID)
	require.InDelta(t, 600*60, pos[0].PositionSeconds, 0.1)
	require.True(t, pos[0].UpdatedAt.Equal(mlFinishedAt))

	// A partly listened book keeps its hide flag and its position.
	s = l.state("partial")
	require.Equal(t, database.UserBookStatusFinished, s.Status)
	require.True(t, s.HideFromContinueListening)
	require.Len(t, l.positions("partial"), 1)

	// In progress: the list's position, on the ABS segment, at the list's time.
	s = l.state("prog")
	require.Equal(t, database.UserBookStatusInProgress, s.Status)
	require.False(t, s.StatusManual)
	require.True(t, s.LastActivityAt.Equal(mlFinishedAt))
	pos = l.positions("prog")
	require.Len(t, pos, 1)
	require.InDelta(t, 7200, pos[0].PositionSeconds, 0.1)
	require.Equal(t, 24, s.ProgressPct, "7200 of 30000 seconds")
	require.True(t, pos[0].UpdatedAt.Equal(mlFinishedAt))

	// A percent only is applied to the local length.
	pos = l.positions("pct")
	require.Len(t, pos, 1)
	require.InDelta(t, 400*60*0.25, pos[0].PositionSeconds, 1)
	require.Equal(t, 25, l.state("pct").ProgressPct)

	// The finished book was not touched, not even rewritten.
	s = l.state("done")
	require.True(t, undo.SameUserBookState(doneBefore, s))
	require.True(t, s.UpdatedAt.Equal(doneBefore.UpdatedAt))

	changes, err := l.store.GetOperationChanges(mlTestApplyOp)
	require.NoError(t, err)
	require.Len(t, changes, 4)
	for _, c := range changes {
		require.Equal(t, undo.ChangeTypeUserBookStateSet, c.ChangeType)
		require.Equal(t, undo.UserStateField(l.user), c.FieldName)
		require.Empty(t, undo.NotRestorableLabel(c))
	}

	// Planning again: nothing is left to do.
	plan2, rows2 := l.plan(listID)
	require.Zero(t, plan2.Applicable)
	require.Equal(t, mlSkipAlreadyFinished, rows2["asin:B100000001"].Class)
	require.Equal(t, mlSkipLocalProgress, rows2["asin:B100000006"].Class)

	// A device listens to one book after the import: its revert is refused and
	// the listen is kept; the rest are restored.
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["prog"], "abs", 13000, mlFinishedAt.Add(time.Hour)))
	rr, err := audiobooks.NewRevertService(l.store).RevertOperation(mlTestApplyOp)
	require.Error(t, err)
	require.Equal(t, 3, rr.Restored, "%+v", rr)
	require.Equal(t, 1, rr.Failed, "%+v", rr)

	require.Nil(t, l.state("fresh"), "a row the import created is deleted")
	require.Empty(t, l.positions("fresh"))
	s = l.state("partial")
	require.Equal(t, database.UserBookStatusInProgress, s.Status)
	require.Equal(t, 30, s.ProgressPct)
	require.Nil(t, s.FinishedAt)
	require.True(t, s.HideFromContinueListening)
	require.Len(t, l.positions("partial"), 1)
	require.Nil(t, l.state("pct"))
	pos = l.positions("prog")
	require.Len(t, pos, 1)
	require.InDelta(t, 13000, pos[0].PositionSeconds, 0.1, "the later listen is the user's")
}

// A dry run (the default of the apply routes) reports what would be applied
// and writes nothing: no state, no journal row, no record.
func TestMasterListImport_DryRunWritesNothing(t *testing.T) {
	l := newMLLab(t)
	l.book("fresh", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", minutes: 600})
	fin := mlTS(mlFinishedAt)
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Fresh Finish", "Pat Example", "B100000001", 600, "finished", fin, "", 0, 0),
		applyLine("Never In The Library", "Zed Elsewhere", "B100000099", 300, "finished", fin, "", 0, 0),
	)
	plan, rows := l.plan(listID)
	require.Equal(t, 2, plan.Applicable)
	require.Equal(t, mlReadElsewhere, rows["asin:B100000099"].Class)
	before := l.dump()
	res := l.apply(plan, []string{"asin:B100000001", "asin:B100000099"}, true)
	assert.Equal(t, repairs.OutcomeWouldApply, res["asin:B100000001"].Outcome, "%+v", res)
	assert.Equal(t, repairs.OutcomeWouldApply, res["asin:B100000099"].Outcome, "%+v", res)
	assert.Equal(t, before, l.dump(), "a dry run changes no key")
}

// A local listen between the plan and the apply wins: the row is refused as
// changed_since_plan, nothing is written and nothing is journaled.
func TestMasterListImport_NewerLocalWinsAtApply(t *testing.T) {
	l := newMLLab(t)
	l.book("fresh", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", minutes: 600})
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Fresh Finish", "Pat Example", "B100000001", 600, "finished", mlTS(mlFinishedAt), "", 0, 0))
	plan, rows := l.plan(listID)
	require.True(t, rows["asin:B100000001"].Applicable())

	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["fresh"], "abs", 120, mlFinishedAt.Add(time.Hour)))
	l.setState("fresh", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 1,
		LastActivityAt: mlFinishedAt.Add(time.Hour), LastSegmentID: "abs"})
	res := l.apply(plan, []string{"asin:B100000001"}, false)
	require.Equal(t, repairs.OutcomeChangedSincePlan, res["asin:B100000001"].Outcome)
	require.Equal(t, mlSkipNewerLocal, res["asin:B100000001"].Skipped)
	require.Equal(t, database.UserBookStatusInProgress, l.state("fresh").Status)
	changes, err := l.store.GetOperationChanges(mlTestApplyOp)
	require.NoError(t, err)
	require.Empty(t, changes)
}

// The re-plan re-matches the row against the library as it is at apply time:
// a second book that now carries the identifier makes it ambiguous, so it is
// changed_since_plan rather than a write to a book that may be the wrong one.
func TestMasterListImport_ReplanRematchesTheLibrary(t *testing.T) {
	l := newMLLab(t)
	yes := true
	l.book("fresh", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", vg: "vg-a", primary: &yes, minutes: 600})
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Fresh Finish", "Pat Example", "B100000001", 600, "finished", mlTS(mlFinishedAt), "", 0, 0))
	plan, rows := l.plan(listID)
	require.True(t, rows["asin:B100000001"].Applicable())

	l.book("rival", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", vg: "vg-b", primary: &yes, minutes: 600})
	res := l.apply(plan, []string{"asin:B100000001"}, false)
	require.Equal(t, repairs.OutcomeChangedSincePlan, res["asin:B100000001"].Outcome, "%+v", res)
	require.Equal(t, mlAmbiguous, res["asin:B100000001"].Skipped)
	require.Nil(t, l.state("fresh"))
}

// A list deleted (or pruned by retention) after the plan cannot break a pending
// apply: the row stored the list row it was planned from.
func TestMasterListImport_ApplySurvivesTheListBeingDeleted(t *testing.T) {
	l := newMLLab(t)
	l.book("fresh", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", minutes: 600})
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Fresh Finish", "Pat Example", "B100000001", 600, "finished", mlTS(mlFinishedAt), "", 0, 0))
	plan, _ := l.plan(listID)
	require.NoError(t, database.NewMasterListStore(l.store.DB()).DeleteList(listID))
	res := l.apply(plan, []string{"asin:B100000001"}, false)
	require.Equal(t, repairs.OutcomeApplied, res["asin:B100000001"].Outcome, "%+v", res)
	require.Equal(t, database.UserBookStatusFinished, l.state("fresh").Status)
}

func TestMasterListImport_ReadElsewhereRecordApplyIdempotentAndUndo(t *testing.T) {
	l := newMLLab(t)
	st := database.NewMasterListStore(l.store.DB())
	fin := mlTS(mlFinishedAt)
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Never In The Library", "Zed Elsewhere", "B100000099", 300, "finished", fin, "", 0, 0),
		applyLine("Also Not Here", "Yan Away", "B100000098", 300, "in_progress", "", fin, 0, 100),
	)
	plan, rows := l.plan(listID)
	r := rows["asin:B100000099"]
	require.Equal(t, mlReadElsewhere, r.Class)
	require.True(t, r.Applicable())
	require.Empty(t, r.BookIDs)
	require.Equal(t, 2, plan.ByClass[mlReadElsewhere], "the count the plan summary shows")
	require.Equal(t, map[string]string{"user": l.user, "read_status": "finished", "finished_at": fin}, r.Proposed)

	res := l.apply(plan, []string{"asin:B100000099", "asin:B100000098"}, false)
	require.Equal(t, repairs.OutcomeApplied, res["asin:B100000099"].Outcome, "%+v", res)
	require.Equal(t, repairs.OutcomeApplied, res["asin:B100000098"].Outcome, "%+v", res)

	id := database.ElsewhereID(l.user, "asin:B100000099")
	rec, err := st.GetElsewhere(id)
	require.NoError(t, err)
	assert.Equal(t, database.MasterListElsewhere{
		ID: id, UserID: l.user, ListID: listID, UploadedBy: l.user, Lines: []int{2},
		Title: "Never In The Library", Authors: []string{"Zed Elsewhere"}, ASIN: "B100000099",
		Status: "finished", FinishedAt: fin, CreatedAt: l.now,
	}, *rec)
	all, err := st.ListElsewhere(l.user)
	require.NoError(t, err)
	require.Len(t, all, 2)

	// No book was created and no user state written.
	books, err := l.store.GetAllBooksCoreComplete(100, 0)
	require.NoError(t, err)
	require.Empty(t, books)

	// Journaled on no book, restorable.
	changes, err := l.store.GetOperationChanges(mlTestApplyOp)
	require.NoError(t, err)
	require.Len(t, changes, 2)
	for _, c := range changes {
		require.Equal(t, undo.ChangeTypeMasterListElsewhereCreate, c.ChangeType)
		require.Empty(t, c.BookID)
		require.Empty(t, undo.NotRestorableLabel(c))
	}

	// Importing the same list again, or another list with the same row, plans
	// the rows as already recorded.
	plan2, rows2 := l.plan(listID)
	require.Zero(t, plan2.Applicable)
	require.Equal(t, mlSkipAlreadyRecorded, rows2["asin:B100000099"].Class)
	list2 := l.uploadWithHeader(mlApplyHeader,
		applyLine("Never In The Library", "Zed Elsewhere", "B100000099", 300, "finished", fin, "", 0, 0))
	_, rows3 := l.plan(list2)
	require.Equal(t, mlSkipAlreadyRecorded, rows3["asin:B100000099"].Class)

	// The apply's revert deletes the records it made.
	rr, err := audiobooks.NewRevertService(l.store).RevertOperation(mlTestApplyOp)
	require.NoError(t, err, "%+v", rr)
	require.Equal(t, 2, rr.Restored)
	all, err = st.ListElsewhere("")
	require.NoError(t, err)
	require.Empty(t, all)
}

// A record that appears between the plan and the apply (another apply of a
// list with the same row) is not overwritten, and the revert of this apply
// does not delete it.
func TestMasterListImport_RecordCreatedSincePlanIsKept(t *testing.T) {
	l := newMLLab(t)
	st := database.NewMasterListStore(l.store.DB())
	fin := mlTS(mlFinishedAt)
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Never In The Library", "Zed Elsewhere", "B100000099", 300, "finished", fin, "", 0, 0))
	plan, _ := l.plan(listID)

	id := database.ElsewhereID(l.user, "asin:B100000099")
	other := database.MasterListElsewhere{ID: id, UserID: l.user, ListID: "another-list", Title: "Someone else's"}
	require.NoError(t, st.CreateElsewhere(other))

	res := l.apply(plan, []string{"asin:B100000099"}, false)
	require.Equal(t, repairs.OutcomeChangedSincePlan, res["asin:B100000099"].Outcome, "%+v", res)
	require.Equal(t, mlSkipAlreadyRecorded, res["asin:B100000099"].Skipped)
	got, err := st.GetElsewhere(id)
	require.NoError(t, err)
	require.Equal(t, "Someone else's", got.Title)
	changes, err := l.store.GetOperationChanges(mlTestApplyOp)
	require.NoError(t, err)
	require.Empty(t, changes)
}

// An unmatched row that might still be a library book is not recorded: the
// title is there under another author, or a trashed copy carries the identifier.
func TestMasterListImport_ReadElsewhereOnlyForRowsNothingCouldBe(t *testing.T) {
	l := newMLLab(t)
	l.book("delta", mlBook{title: "Delta Fields", author: "Dee Penn", minutes: 500})
	l.book("lost", mlBook{title: "Lost Book", author: "Lou Gone", asin: "B100000090", trashed: true})
	fin := mlTS(mlFinishedAt)
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Delta Fields", "Zed Other", "", 500, "finished", fin, "", 0, 0),
		applyLine("Lost Book", "Lou Gone", "B100000090", 0, "finished", fin, "", 0, 0),
		applyLine("Truly Absent", "Xan Yu", "", 0, "", "", "", 0, 0),
	)
	plan, rows := l.plan(listID)
	classes := make([]string, 0, len(plan.Rows))
	for _, r := range plan.Rows {
		classes = append(classes, r.Class)
	}
	require.ElementsMatch(t, []string{mlUnmatched, mlUnmatched, mlReadElsewhere}, classes)
	require.Equal(t, 1, plan.Applicable)
	for _, r := range rows {
		if r.Title == "Truly Absent" {
			require.Equal(t, mlReadElsewhere, r.Class)
		}
	}
}

func TestMasterListImport_SummaryAndRegistration(t *testing.T) {
	l := newMLLab(t)
	f, ok := l.p.Repairs().Get(masterListImportFixerID)
	require.True(t, ok)
	require.False(t, repairs.IsPlanOnly(f), "the fixer applies now")
	require.False(t, repairs.SkipsScanStandDown(f), "it writes user state and must hold the scan stand-down")
	require.False(t, repairs.AllowsBookTagsOnly(f), "the framework guard must run")
	require.False(t, repairs.AllowsITunesDatabaseOnly(f), "the iTunes path guard must run")
	var _ repairs.ApplyScoped = l.fixer
	b, err := json.Marshal(l.fixer.Title())
	require.NoError(t, err)
	require.NotContains(t, string(b), "preview")
}
