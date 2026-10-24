// file: internal/plugins/maintenance/master_list_import_apply_test.go
// version: 1.1.0
// guid: 2f6a9c13-d874-4e05-b1a7-83c5e0d94f68
// last-edited: 2026-10-24

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/masterlist"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
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
	return l.applyWith(l.fixer, plan, rowIDs, dryRun)
}

// applyWith is apply through fixer f (the lab's own, or a wrapper that acts
// between a row's re-plan and its write).
func (l *mlLab) applyWith(f repairs.Fixer, plan *repairs.PlanResult, rowIDs []string, dryRun bool) map[string]repairs.RowResult {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	w := repairs.NewWriter(l.store, l.store, f.ID(), "bulk_update", "repairs-").
		WithJournal(l.store, l.store, mlTestApplyOp).WithUserState(l.p.deps.UserReadStateStore())
	res, err := repairs.RunApply(context.Background(), f, plan, mlTestPlanOp, rowIDs, dryRun,
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
	require.Equal(t, 2, plan.ByClass[mlSkipAlreadyFinished], "two rows, both counted under their class")
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
// list with the same row) is seen by the apply's own re-check: the row reports
// changed_since_plan as skipped_already_recorded and nothing is written.
func TestMasterListImport_RecordCreatedSincePlanIsSeenByTheRecheck(t *testing.T) {
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

// hidingLists is a MasterListStore whose GetElsewhere never finds a record, so
// the re-check at apply time reads "absent" while the record is really there: a
// record created in the window between that read and the create (the store's
// create-if-absent is the only thing that sees it).
type hidingLists struct{ MasterListStore }

func (hidingLists) GetElsewhere(string) (*database.MasterListElsewhere, error) {
	return nil, database.ErrElsewhereNotFound
}

type hidingDeps struct {
	fakeDeps
	lists MasterListStore
}

func (d hidingDeps) MasterLists() MasterListStore { return d.lists }

// A record that appears AFTER the apply's re-check, inside the write (another
// apply of a list with the same row), is not overwritten: the create refuses,
// the row reports changed_since_plan, the other apply's record is untouched, and
// the journal row written for this create is voided, so the revert of this apply
// does not delete the record it never made.
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
	l.p.deps = hidingDeps{fakeDeps: fakeDeps{store: l.store}, lists: hidingLists{st}}

	res := l.apply(plan, []string{"asin:B100000099"}, false)
	require.Equal(t, repairs.OutcomeChangedSincePlan, res["asin:B100000099"].Outcome, "%+v", res)
	require.Empty(t, res["asin:B100000099"].Skipped, "the re-check passed: the refusal came from the create inside the write")
	require.Contains(t, res["asin:B100000099"].Error, "created since the plan")

	got, err := st.GetElsewhere(id)
	require.NoError(t, err)
	require.Equal(t, "Someone else's", got.Title, "the other apply's record is untouched")
	recs, err := st.ListElsewhere("")
	require.NoError(t, err)
	require.Len(t, recs, 1)

	changes, err := l.store.GetOperationChanges(mlTestApplyOp)
	require.NoError(t, err)
	require.Len(t, changes, 1, "the create was journaled before it was tried")
	require.True(t, changes[0].Voided, "and voided when it failed")
	require.Equal(t, undo.ChangeTypeMasterListElsewhereCreate, changes[0].ChangeType)

	// The revert of this apply restores nothing and leaves the record alone.
	_, _ = audiobooks.NewRevertService(l.store).RevertOperation(mlTestApplyOp)
	got, err = st.GetElsewhere(id)
	require.NoError(t, err, "a revert must not delete the record this apply never made")
	require.Equal(t, "Someone else's", got.Title)
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
	require.NotContains(t, strings.ToLower(l.fixer.Title()), "preview")
	require.Equal(t, []auth.Permission{auth.PermUsersManage}, l.fixer.RequiredPermissions())
}

// byTitle indexes a plan's rows by title (rows with no identifier have an id
// that is a hash).
func mlRowsByTitle(plan *repairs.PlanResult) map[string]repairs.Row {
	out := map[string]repairs.Row{}
	for _, r := range plan.Rows {
		out[r.Title] = r
	}
	return out
}

// An unmatched row with a data problem of its own is held for review like a
// matched row is, never a bulk-applicable read-elsewhere record: a dropped bad
// identifier may have been the one that matched, and a future or unreadable
// finish time must not be stored as the record's.
func TestMasterListImport_ReadElsewhereHeldForRowDataProblems(t *testing.T) {
	l := newMLLab(t)
	st := database.NewMasterListStore(l.store.DB())
	fin := mlTS(mlFinishedAt)
	future := mlTS(l.now.Add(30 * 24 * time.Hour))
	listID := l.uploadWithHeader(mlCSVHeader,
		csvLine("Clean Absent", "Zed Elsewhere", "B100000041", "", "", "", "", "", "finished", fin),
		csvLine("Bad Code Absent", "Yan Away", "", "9780306406158", "", "", "", "", "finished", fin),
		csvLine("Future Absent", "Xan Yu", "B100000043", "", "", "", "", "", "finished", future),
		csvLine("Zoneless Absent", "Wes Nowhere", "B100000044", "", "", "", "", "", "finished", "2026-09-01T10:00:00"),
		csvLine("Conflict Absent", "Val Elsewhere", "", mlISBN13b, mlISBN10, "", "", "", "finished", fin),
	)
	plan, _ := l.plan(listID)
	byTitle := mlRowsByTitle(plan)
	require.Len(t, byTitle, 5)

	clean := byTitle["Clean Absent"]
	require.Equal(t, mlReadElsewhere, clean.Class)
	require.True(t, clean.Applicable())
	require.Equal(t, 1, plan.Applicable, "only the clean row is bulk-applicable")

	for title, flag := range map[string]string{
		"Bad Code Absent": masterlist.IssueBadIdentifier,
		"Future Absent":   masterlist.IssueFutureTimestamp,
		"Zoneless Absent": masterlist.IssueBadTimestamp,
		"Conflict Absent": masterlist.IssueIdentifierConflict,
	} {
		r := byTitle[title]
		require.Equal(t, mlUnmatched, r.Class, "%s", title)
		require.False(t, r.Applicable(), "%s must not be bulk-applicable", title)
		require.Equal(t, repairs.RiskReview, r.Risk, "%s", title)
		require.Contains(t, r.Flags, flag, "%s", title)
		require.Contains(t, r.Reason, "held for review", "%s: the lane says why it is not a record", title)
		require.Contains(t, r.Reason, flag, "%s", title)
	}

	var applicable []string
	for _, r := range plan.Rows {
		if r.Applicable() {
			applicable = append(applicable, r.RowID)
		}
	}
	res := l.apply(plan, applicable, false)
	require.Len(t, res, 1)
	require.Equal(t, repairs.OutcomeApplied, res[clean.RowID].Outcome, "%+v", res)
	recs, err := st.ListElsewhere("")
	require.NoError(t, err)
	require.Len(t, recs, 1, "the held rows wrote no record")
	require.Equal(t, "Clean Absent", recs[0].Title)
	require.Empty(t, recs[0].Issues)
}

// A list time that is in the future when the apply re-plans (the clock stepped
// back since the plan) is its own skip kind, for a matched row and for a
// read-elsewhere record alike, and is never written.
func TestMasterListImport_FutureTimeAtApplyIsItsOwnSkip(t *testing.T) {
	l := newMLLab(t)
	l.book("fresh", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", minutes: 600})
	finished := mlTS(l.now.Add(-24 * time.Hour))
	listID := l.uploadWithHeader(mlApplyHeader,
		applyLine("Fresh Finish", "Pat Example", "B100000001", 600, "finished", finished, "", 0, 0),
		applyLine("Never In The Library", "Zed Elsewhere", "B100000099", 300, "finished", finished, "", 0, 0),
	)
	plan, _ := l.plan(listID)
	require.Equal(t, 2, plan.Applicable)

	l.fixer.now = func() time.Time { return l.now.Add(-30 * 24 * time.Hour) }
	res := l.apply(plan, []string{"asin:B100000001", "asin:B100000099"}, false)
	for _, id := range []string{"asin:B100000001", "asin:B100000099"} {
		require.Equal(t, repairs.OutcomeChangedSincePlan, res[id].Outcome, "%s: %+v", id, res[id])
		require.Equal(t, mlSkipFutureTimestamp, res[id].Skipped, "%s", id)
		require.NotEqual(t, mlSkipNoTimestamp, res[id].Skipped, "%s: the no-timestamp filter counts rows that give no time", id)
	}
	require.Nil(t, l.state("fresh"), "nothing was written")
	recs, err := database.NewMasterListStore(l.store.DB()).ListElsewhere("")
	require.NoError(t, err)
	require.Empty(t, recs, "the future finish time was not copied into a record")
}

// actingFixer runs after on a row's re-plan, before the Apply that follows: a
// merge, a delete or a user removal that lands while an apply run is working
// through its rows.
type actingFixer struct {
	*masterListImportFixer
	after func()
}

func (a *actingFixer) Replan(ctx context.Context, params json.RawMessage, planned repairs.Row, rep registry.Reporter) (repairs.Row, error) {
	row, err := a.masterListImportFixer.Replan(ctx, params, planned, rep)
	a.after()
	return row, err
}

// vanishingUsers reports no user once gone is set.
type vanishingUsers struct {
	UserReadStateStore
	gone *atomic.Bool
}

func (v vanishingUsers) GetUserByID(id string) (*database.User, error) {
	if v.gone.Load() {
		return nil, nil
	}
	return v.UserReadStateStore.GetUserByID(id)
}

type vanishingDeps struct {
	fakeDeps
	us UserReadStateStore
}

func (d vanishingDeps) UserReadStateStore() UserReadStateStore { return d.us }

func (l *mlLab) updateBook(key string, mutate func(b *database.Book)) {
	l.t.Helper()
	b, err := l.store.GetBookByID(l.ids[key])
	require.NoError(l.t, err)
	require.NotNil(l.t, b)
	mutate(b)
	_, err = l.store.UpdateBook(b.ID, b)
	require.NoError(l.t, err)
}

// The apply run's library snapshot is read once, so a book trashed or merged
// after a row's re-plan (while other rows were being written) is still live in
// it. The write re-reads the book and the user: a trashed or merged book, or a
// removed user, writes nothing and reports changed_since_plan.
func TestMasterListImport_ApplyRechecksTargetBookAndUser(t *testing.T) {
	fin := mlTS(mlFinishedAt)
	setup := func(t *testing.T) (*mlLab, *repairs.PlanResult) {
		l := newMLLab(t)
		l.book("fresh", mlBook{title: "Fresh Finish", author: "Pat Example", asin: "B100000001", minutes: 600})
		l.book("survivor", mlBook{title: "Fresh Finish Survivor", author: "Pat Example", minutes: 600})
		listID := l.uploadWithHeader(mlApplyHeader,
			applyLine("Fresh Finish", "Pat Example", "B100000001", 600, "finished", fin, "", 0, 0),
			applyLine("Never In The Library", "Zed Elsewhere", "B100000099", 300, "finished", fin, "", 0, 0),
		)
		plan, _ := l.plan(listID)
		require.Equal(t, 2, plan.Applicable)
		return l, plan
	}
	nothingWritten := func(t *testing.T, l *mlLab, elsewhere bool) {
		t.Helper()
		require.Nil(t, l.state("fresh"), "no user state was written")
		require.Empty(t, l.positions("fresh"))
		changes, err := l.store.GetOperationChanges(mlTestApplyOp)
		require.NoError(t, err)
		for _, c := range changes {
			if c.ChangeType == undo.ChangeTypeUserBookStateSet || elsewhere {
				require.True(t, c.Voided, "a journaled step that did not write is voided: %+v", c)
			}
		}
		recs, err := database.NewMasterListStore(l.store.DB()).ListElsewhere("")
		require.NoError(t, err)
		if elsewhere {
			require.Empty(t, recs, "no record for a user who is gone")
		}
	}

	t.Run("book moved to the trash", func(t *testing.T) {
		l, plan := setup(t)
		f := &actingFixer{masterListImportFixer: l.fixer, after: func() {
			l.updateBook("fresh", func(b *database.Book) {
				b.MarkedForDeletion = new(true)
				now := l.now
				b.MarkedForDeletionAt = &now
			})
		}}
		res := l.applyWith(f, plan, []string{"asin:B100000001"}, false)
		r := res["asin:B100000001"]
		require.Equal(t, repairs.OutcomeChangedSincePlan, r.Outcome, "%+v", r)
		require.Contains(t, r.Error, "trash")
		nothingWritten(t, l, false)
	})

	t.Run("book merged into another", func(t *testing.T) {
		l, plan := setup(t)
		f := &actingFixer{masterListImportFixer: l.fixer, after: func() {
			l.updateBook("fresh", func(b *database.Book) {
				into := l.ids["survivor"]
				b.MergedIntoBookID = &into
				b.MarkedForDeletion = new(true)
				now := l.now
				b.MarkedForDeletionAt = &now
			})
		}}
		res := l.applyWith(f, plan, []string{"asin:B100000001"}, false)
		r := res["asin:B100000001"]
		require.Equal(t, repairs.OutcomeChangedSincePlan, r.Outcome, "%+v", r)
		require.Contains(t, r.Error, "merged into "+l.ids["survivor"], "held with the survivor named, not followed")
		nothingWritten(t, l, false)
		require.Nil(t, func() *database.UserBookState {
			s, err := l.store.GetUserBookState(l.user, l.ids["survivor"])
			require.NoError(t, err)
			return s
		}(), "the survivor got no state either: the match was never reviewed for it")
	})

	for _, rowID := range []string{"asin:B100000001", "asin:B100000099"} {
		// One row per run: rows of a run may be re-planned in any order, so
		// the removal is triggered by the only row there is.
		t.Run("user removed before the write "+rowID, func(t *testing.T) {
			l, plan := setup(t)
			var gone atomic.Bool
			l.p.deps = vanishingDeps{fakeDeps: fakeDeps{store: l.store}, us: vanishingUsers{UserReadStateStore: NewUserReadStateStore(l.store), gone: &gone}}
			f := &actingFixer{masterListImportFixer: l.fixer, after: func() { gone.Store(true) }}
			res := l.applyWith(f, plan, []string{rowID}, false)
			require.Equal(t, repairs.OutcomeChangedSincePlan, res[rowID].Outcome, "%+v", res[rowID])
			require.Contains(t, res[rowID].Error, "no longer exists")
			nothingWritten(t, l, true)
		})
	}

	t.Run("user already gone at the re-plan", func(t *testing.T) {
		l, plan := setup(t)
		var gone atomic.Bool
		gone.Store(true)
		l.p.deps = vanishingDeps{fakeDeps: fakeDeps{store: l.store}, us: vanishingUsers{UserReadStateStore: NewUserReadStateStore(l.store), gone: &gone}}
		for _, dry := range []bool{true, false} {
			res := l.apply(plan, []string{"asin:B100000001"}, dry)
			r := res["asin:B100000001"]
			require.Equal(t, repairs.OutcomeFailed, r.Outcome, "dry=%v: %+v", dry, r)
			require.Contains(t, r.Error, "no longer exists", "a dry run must not say would_apply for a user who is gone")
		}
		nothingWritten(t, l, false)
	})

	t.Run("live book and user write as planned", func(t *testing.T) {
		l, plan := setup(t)
		res := l.apply(plan, []string{"asin:B100000001", "asin:B100000099"}, false)
		require.Equal(t, repairs.OutcomeApplied, res["asin:B100000001"].Outcome, "%+v", res)
		require.Equal(t, repairs.OutcomeApplied, res["asin:B100000099"].Outcome, "%+v", res)
		require.Equal(t, database.UserBookStatusFinished, l.state("fresh").Status)
	})
}

// One book is one read-elsewhere record whichever of its identifiers a list
// names: an earlier list gave the ASIN and the ISBN, a later one gives the ISBN
// alone. Another user's record of the same book does not count, and a different
// book is still its own.
func TestMasterListImport_ReadElsewhereDedupedByAnyIdentifier(t *testing.T) {
	l := newMLLab(t)
	st := database.NewMasterListStore(l.store.DB())
	fin := mlTS(mlFinishedAt)

	list1 := l.uploadWithHeader(mlCSVHeader,
		csvLine("Same Book", "Zed Elsewhere", "B100000051", mlISBN13, "", "", "", "", "finished", fin))
	plan1, rows1 := l.plan(list1)
	require.Equal(t, mlReadElsewhere, rows1["asin:B100000051"].Class)
	res := l.apply(plan1, []string{"asin:B100000051"}, false)
	require.Equal(t, repairs.OutcomeApplied, res["asin:B100000051"].Outcome, "%+v", res)

	// Another user has the second book on record: no bearing on this user.
	require.NoError(t, st.CreateElsewhere(database.MasterListElsewhere{
		ID: database.ElsewhereID("another-user", "isbn13:"+mlISBN13b), UserID: "another-user", ListID: "l0", ISBN13: mlISBN13b, Title: "Theirs"}))

	list2 := l.uploadWithHeader(mlCSVHeader,
		csvLine("Same Book (Unabridged)", "Zed Elsewhere", "", mlISBN13, "", "", "", "", "finished", fin),
		csvLine("Different Book", "Yan Away", "", mlISBN13b, "", "", "", "", "finished", fin),
	)
	plan2, _ := l.plan(list2)
	byTitle := mlRowsByTitle(plan2)
	same := byTitle["Same Book (Unabridged)"]
	require.Equal(t, mlSkipAlreadyRecorded, same.Class, "%+v", same)
	require.False(t, same.Applicable())
	require.Contains(t, same.Reason, "under another identifier")
	diff := byTitle["Different Book"]
	require.Equal(t, mlReadElsewhere, diff.Class)
	require.True(t, diff.Applicable())

	res = l.apply(plan2, []string{diff.RowID}, false)
	require.Equal(t, repairs.OutcomeApplied, res[diff.RowID].Outcome, "%+v", res)
	mine, err := st.ListElsewhere(l.user)
	require.NoError(t, err)
	require.Len(t, mine, 2, "one record per book for this user")
	everyone, err := st.ListElsewhere("")
	require.NoError(t, err)
	require.Len(t, everyone, 3)

	// A third list naming the first book by its ASIN alone is found too.
	list3 := l.uploadWithHeader(mlCSVHeader,
		csvLine("Same Book Again", "Zed Elsewhere", "B100000051", "", "", "", "", "", "finished", fin))
	_, rows3 := l.plan(list3)
	require.Equal(t, mlSkipAlreadyRecorded, rows3["asin:B100000051"].Class)
}
