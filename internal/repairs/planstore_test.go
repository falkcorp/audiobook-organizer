// file: internal/repairs/planstore_test.go
// version: 1.2.1
// guid: 3f9c1b7a-6d42-4e08-b5a1-7c20e4d9f813
// last-edited: 2026-10-27

package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// countingRows wraps a plan row store and records every read, so a test can
// say which reads a path made.
type countingRows struct {
	database.OpRowStore
	mu         sync.Mutex
	indexScans int
	gotIDs     []string
	failAfter  int // ReplaceOpRowsPrefix writes then fails when > 0
}

func (c *countingRows) ScanOpRowIndex(op string, fn func(string, []byte) error) error {
	c.mu.Lock()
	c.indexScans++
	c.mu.Unlock()
	return c.OpRowStore.ScanOpRowIndex(op, fn)
}

func (c *countingRows) GetOpRows(op string, ids []string) ([][]byte, error) {
	c.mu.Lock()
	c.gotIDs = append(c.gotIDs, ids...)
	c.mu.Unlock()
	return c.OpRowStore.GetOpRows(op, ids)
}

func (c *countingRows) ReplaceOpRowsPrefix(op, prefix string, n int, at func(int) (database.OpRow, error)) error {
	if err := c.OpRowStore.ReplaceOpRowsPrefix(op, prefix, n, at); err != nil {
		return err
	}
	if c.failAfter > 0 && n > 0 {
		return errors.New("disk full")
	}
	return nil
}

// variedRows is a plan of rows in every combination of class, skip kind and
// owner flag the pager tells apart.
func variedRows(n int) []Row {
	classes := []string{"", "moved", "copy"}
	skips := []string{"", "", SkipITunes, "held"}
	rows := make([]Row, 0, n)
	for i := 0; i < n; i++ {
		r := Row{RowID: fmt.Sprintf("row-%03d", i), Title: fmt.Sprintf("Title %d", i), Reason: "r", Risk: RiskLow,
			Fingerprint: fmt.Sprintf("fp-%d", i), Class: classes[i%len(classes)], Skipped: skips[i%len(skips)]}
		if r.Skipped == "held" && i%2 == 0 {
			r.OwnerApplicable = true
		}
		switch {
		case i%5 == 0:
			r.Flags = []string{"zeta", "alpha"} // unsorted on purpose: RunPlan sorts
		case i%7 == 0:
			r.Flags = []string{"alpha", "alpha"} // duplicated on purpose: RunPlan de-duplicates
		}
		rows = append(rows, r)
	}
	return rows
}

// storeSummary stores res the way the plan op does and returns the store, the
// op reader and the plan LoadPlan reads back.
func storeSummary(t *testing.T, s *memStore, rows database.OpRowStore, res *PlanResult, opID string) *PlanResult {
	t.Helper()
	require.NoError(t, SavePlan(rows, opID, res))
	data, err := json.Marshal(res)
	require.NoError(t, err)
	str := string(data)
	s.ops[opID] = &database.OperationV2Row{ID: opID, DefID: PlanOpID, Status: "completed", ResultData: &str}
	plan, err := LoadPlan(s, rows, opID, res.FixerID)
	require.NoError(t, err)
	return plan
}

// TestStoredPlan_PageEqualsInMemoryPage: a plan read back from the row store
// answers every filter x class x window exactly as the in-memory plan does,
// from the index alone.
func TestStoredPlan_PageEqualsInMemoryPage(t *testing.T) {
	s := newMemStore()
	f := &noBookIDsFixer{s: s, rows: variedRows(60)}
	res := planFor(t, s, f)
	stored := storeSummary(t, s, s, res, "op-plan")
	require.Empty(t, stored.Rows)
	for _, filter := range []string{FilterAll, FilterApplicable, FilterSkipped, FilterOwnerApplicable,
		FilterSkippedKindPrefix + SkipITunes, FilterSkippedKindPrefix + "held", FilterSkippedKindPrefix + "nope",
		FilterFlagPrefix + "alpha", FilterFlagPrefix + "zeta", FilterFlagPrefix + "nope"} {
		for _, class := range []string{"", "moved", "copy", "nope"} {
			for _, win := range [][2]int{{0, 7}, {5, 7}, {50, 7}, {0, 500}, {999, 5}} {
				want, err := res.Page("op-plan", filter, class, win[0], win[1])
				require.NoError(t, err)
				got, err := stored.Page("op-plan", filter, class, win[0], win[1])
				require.NoError(t, err, "%s/%s/%v", filter, class, win)
				wj, _ := json.Marshal(want)
				gj, _ := json.Marshal(got)
				require.JSONEq(t, string(wj), string(gj), "%s/%s/%v", filter, class, win)
			}
		}
	}
	_, err := stored.Page("op-plan", "bogus", "", 0, 1)
	require.ErrorIs(t, err, ErrUnknownFilter)
}

// TestStoredPlan_PageAndSelectReadOnlyWhatTheyReturn: a page scans the small
// index and reads exactly its own rows; Select reads exactly the ids it is
// given and no index.
func TestStoredPlan_PageAndSelectReadOnlyWhatTheyReturn(t *testing.T) {
	s := newMemStore()
	cr := &countingRows{OpRowStore: s}
	res := planFor(t, s, &noBookIDsFixer{s: s, rows: variedRows(60)})
	stored := storeSummary(t, s, cr, res, "op-plan")

	page, err := stored.Page("op-plan", FilterAll, "", 10, 3)
	require.NoError(t, err)
	require.Len(t, page.Rows, 3)
	require.Equal(t, 1, cr.indexScans)
	require.Equal(t, []string{PlanRowPrefix + "row-010", PlanRowPrefix + "row-011", PlanRowPrefix + "row-012"}, cr.gotIDs, "a page reads its own rows, not the plan")

	cr.indexScans, cr.gotIDs = 0, nil
	sel, err := stored.Select([]string{"row-042", "nope", "row-007", "row-042"})
	require.NoError(t, err)
	require.Len(t, sel.Rows, 2, "an unknown id is absent, a repeated one is read once")
	require.Zero(t, cr.indexScans)
	require.Equal(t, []string{PlanRowPrefix + "row-042", PlanRowPrefix + "nope", PlanRowPrefix + "row-007", PlanRowPrefix + "row-042"}, cr.gotIDs)
	require.Equal(t, res.Total, sel.Total, "the selection keeps the plan's summary")
}

// TestLoadPlan_LegacyFormatIsRefused: a plan stored with its rows inside the op
// result carries no row-store marker and is refused, naming the remedy.
func TestLoadPlan_LegacyFormatIsRefused(t *testing.T) {
	s := newMemStore()
	legacy := `{"fixer_id":"fx","total":1,"applicable":1,"skipped_by_kind":{},"rows":[{"row_id":"r","book_ids":["b"]}]}`
	s.ops["old"] = &database.OperationV2Row{ID: "old", DefID: PlanOpID, Status: "completed", ResultData: &legacy}
	_, err := LoadPlan(s, s, "old", "fx")
	require.ErrorIs(t, err, ErrPlanLegacyFormat)
	require.Contains(t, err.Error(), "run the plan again")
}

// TestLoadPlan_UndecodableSummaryNeedsARerun: a plan op whose result is not a
// plan summary is one more way the stored plan cannot be read, with the same
// remedy, not an unclassified error.
func TestLoadPlan_UndecodableSummaryNeedsARerun(t *testing.T) {
	s := newMemStore()
	bad := `not json`
	s.ops["bad"] = &database.OperationV2Row{ID: "bad", DefID: PlanOpID, Status: "completed", ResultData: &bad}
	_, err := LoadPlan(s, s, "bad", "fx")
	require.ErrorIs(t, err, ErrPlanSummaryUnreadable)
	require.True(t, PlanNeedsRerun(err))
}

// TestLegacyPlanMigrationMatchesThePlanOp pins the def id migration 66 looks
// for to the one the engine stores plans under, and that a plan result with
// its inline rows stripped (what the migration leaves) is read as a legacy plan.
func TestLegacyPlanMigrationMatchesThePlanOp(t *testing.T) {
	require.Equal(t, PlanOpID, database.LegacyPlanOpDefID)
	s := newMemStore()
	stripped := `{"fixer_id":"fx","total":1,"applicable":1,"skipped_by_kind":{}}`
	s.ops["old"] = &database.OperationV2Row{ID: "old", DefID: PlanOpID, Status: "completed", ResultData: &stripped}
	_, err := LoadPlan(s, s, "old", "fx")
	require.ErrorIs(t, err, ErrPlanLegacyFormat)
}

// TestStoredPlan_IncompleteRowsFailClosed: a summary whose rows are not all
// there (a truncated set, or an index entry without its row) is an error, never
// a short page.
func TestStoredPlan_IncompleteRowsFailClosed(t *testing.T) {
	s := newMemStore()
	res := planFor(t, s, &noBookIDsFixer{s: s, rows: variedRows(10)})
	stored := storeSummary(t, s, s, res, "op-plan")

	// Fewer rows than the summary's total.
	require.NoError(t, s.ReplaceOpRowsPrefix("op-plan", PlanRowPrefix, 4, func(i int) (database.OpRow, error) {
		r := res.Rows[i]
		data, _ := json.Marshal(r)
		return database.OpRow{ID: PlanRowPrefix + r.RowID, Index: encodeRowIndex(&r), Data: data}, nil
	}))
	_, err := stored.Page("op-plan", FilterAll, "", 0, 5)
	require.ErrorIs(t, err, ErrPlanRowsIncomplete)

	// The index lists every row but one row's body is gone.
	require.NoError(t, s.ReplaceOpRowsPrefix("op-plan", PlanRowPrefix, 10, func(i int) (database.OpRow, error) {
		r := res.Rows[i]
		data, _ := json.Marshal(r)
		if i == 3 {
			data = nil
		}
		return database.OpRow{ID: PlanRowPrefix + r.RowID, Index: encodeRowIndex(&r), Data: data}, nil
	}))
	// A row with no body (an empty body reads as absent) is an index entry
	// without its row; a page past it is fine.
	_, err = stored.Page("op-plan", FilterAll, "", 0, 5)
	require.ErrorIs(t, err, ErrPlanRowsIncomplete)
	require.True(t, PlanNeedsRerun(err))
	_, err = stored.Page("op-plan", FilterAll, "", 5, 5)
	require.NoError(t, err)

	// An index entry with the wrong number of fields is unreadable too, and so
	// is a selection that reads a body that does not decode.
	require.NoError(t, s.ReplaceOpRowsPrefix("op-plan", PlanRowPrefix, 10, func(i int) (database.OpRow, error) {
		r := res.Rows[i]
		data, _ := json.Marshal(r)
		ix := encodeRowIndex(&r)
		if i == 3 {
			ix = []byte("moved\x00\x001")
			data = []byte("{not json")
		}
		return database.OpRow{ID: PlanRowPrefix + r.RowID, Index: ix, Data: data}, nil
	}))
	_, err = stored.Page("op-plan", FilterAll, "", 0, 5)
	require.ErrorIs(t, err, ErrPlanRowsUnreadable)
	require.True(t, PlanNeedsRerun(err))
	_, err = stored.Select([]string{res.Rows[3].RowID})
	require.ErrorIs(t, err, ErrPlanRowsUnreadable, "the body of the named row does not decode")
	require.True(t, PlanNeedsRerun(err))
}

// TestPlanNeedsRerun lists exactly the four errors that mean "run the plan
// again"; a plan that is simply not complete, or another fixer's, is not one.
func TestPlanNeedsRerun(t *testing.T) {
	for _, err := range []error{ErrPlanLegacyFormat, ErrPlanRowsIncomplete, ErrPlanRowsUnreadable, ErrPlanSummaryUnreadable,
		fmt.Errorf("wrapped: %w", ErrPlanRowsUnreadable)} {
		require.True(t, PlanNeedsRerun(err), err.Error())
	}
	for _, err := range []error{nil, ErrPlanNotComplete, ErrPlanNotFound, ErrPlanOtherFixer, ErrNoPlanRowStore, errors.New("boom")} {
		require.False(t, PlanNeedsRerun(err), fmt.Sprint(err))
	}
}

// TestStripLegacyPlanRowsLeavesCurrentPlans runs migration 66's strip over a
// plan SavePlan really wrote, in a real Pebble store: the marker SavePlan puts
// on the plan (PlanRowStoreV2) is not a legacy plan, so the strip writes
// nothing and LoadPlan still reads the plan. database's own test cannot import
// this package, so this is where the marker the strip must respect is real.
func TestStripLegacyPlanRowsLeavesCurrentPlans(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s := newMemStore()
	res := planFor(t, s, &noBookIDsFixer{s: s, rows: variedRows(6)})
	require.NoError(t, SavePlan(st, "op-plan", res))
	require.Equal(t, PlanRowStoreV2, res.RowStore)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	// The plan's summary as the plan op stores it: inline rows are not part of
	// the wire shape, and the row-store marker is.
	require.NotContains(t, string(data), `"rows"`)
	now := time.Now().UTC()
	str := string(data)
	require.NoError(t, st.InsertOperationV2(database.OperationV2Row{ID: "op-plan", DefID: PlanOpID, Status: "completed",
		QueuedAt: now, CompletedAt: &now, ResultData: &str}))

	rep, err := st.StripLegacyPlanRows()
	require.NoError(t, err)
	require.Equal(t, 1, rep.Plans)
	require.Zero(t, rep.Stripped, "a plan in the current format has nothing to strip")
	plan, err := LoadPlan(st, st, "op-plan", res.FixerID)
	require.NoError(t, err, "the strip must not turn a current plan into a legacy one")
	require.Equal(t, res.Total, plan.Total)
}

// TestSavePlan_FailureClearsRowsAndLeavesPlanUnmarked: a write that fails
// part-way leaves no rows behind and no marker on the plan.
func TestSavePlan_FailureClearsRowsAndLeavesPlanUnmarked(t *testing.T) {
	s := newMemStore()
	cr := &countingRows{OpRowStore: s, failAfter: 1}
	res := planFor(t, s, &noBookIDsFixer{s: s, rows: variedRows(5)})
	err := SavePlan(cr, "op-plan", res)
	require.Error(t, err)
	require.Empty(t, res.RowStore)
	n := 0
	require.NoError(t, s.ScanOpRowIndex("op-plan", func(string, []byte) error { n++; return nil }))
	require.Zero(t, n, "rows of a failed save were left behind")
	require.ErrorIs(t, SavePlan(nil, "op-plan", res), ErrNoPlanRowStore)
}

// TestRunApply_ReadsOnlyTheRowsItIsAskedFor: an apply of one row of a stored
// plan reads that row, not the plan.
func TestRunApply_ReadsOnlyTheRowsItIsAskedFor(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &trimFixer{s: s}
	cr := &countingRows{OpRowStore: s}
	stored := storeSummary(t, s, cr, planFor(t, s, f), "op-plan")
	res, err := RunApply(context.Background(), f, stored, "op-plan", []string{"b1", "zz"}, true, deps(s, nil), nopReporter{})
	require.NoError(t, err)
	require.Equal(t, []string{"zz"}, res.NotInPlan)
	require.Equal(t, []string{PlanRowPrefix + "b1", PlanRowPrefix + "zz"}, cr.gotIDs)
	require.Zero(t, cr.indexScans)
	require.Equal(t, 1, res.ByOutcome[OutcomeWouldApply])
}

// previewOnly is a fixer whose Apply would succeed but which declares itself
// plan-only.
type previewOnly struct{ trimFixer }

func (*previewOnly) PlanOnly() bool { return true }

// TestRunApply_RefusesAPlanOnlyFixerEvenWhenItsApplyWouldSucceed (N4): the
// engine is the backstop behind the handler and the apply op.
func TestRunApply_RefusesAPlanOnlyFixerEvenWhenItsApplyWouldSucceed(t *testing.T) {
	s := newMemStore()
	seed(s)
	f := &previewOnly{trimFixer{s: s}}
	plan := planFor(t, s, f)
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"b1"}, false, deps(s, nil), nopReporter{})
	require.ErrorIs(t, err, ErrPlanOnly)
	require.Empty(t, res.Rows)
	require.Empty(t, s.history, "nothing was written")
	require.Equal(t, " One ", s.title("b1"))
}

// TestPage_FlagFilterListsExactlyTheFlaggedRows: ByFlag tallies the flags once
// per row (sorted, de-duplicated by RunPlan), "flag:<name>" pages exactly those
// rows in memory and from the stored index, whatever their class or skip kind,
// and a malformed flag filter is an unknown filter.
func TestPage_FlagFilterListsExactlyTheFlaggedRows(t *testing.T) {
	s := newMemStore()
	f := &noBookIDsFixer{s: s, rows: variedRows(60)}
	res := planFor(t, s, f)
	wantAlpha, wantZeta := 0, 0
	for i := range res.Rows {
		require.True(t, sort.StringsAreSorted(res.Rows[i].Flags))
		if slices.Contains(res.Rows[i].Flags, "alpha") {
			wantAlpha++
		}
		if slices.Contains(res.Rows[i].Flags, "zeta") {
			wantZeta++
		}
	}
	require.Positive(t, wantAlpha)
	require.Equal(t, map[string]int{"alpha": wantAlpha, "zeta": wantZeta}, res.ByFlag)
	stored := storeSummary(t, s, s, res, "op-plan")
	for _, p := range []*PlanResult{res, stored} {
		page, err := p.Page("op-plan", FilterFlagPrefix+"alpha", "", 0, 500)
		require.NoError(t, err)
		require.Equal(t, wantAlpha, page.Total, "the chip count is the rows it opens")
		require.Len(t, page.Rows, wantAlpha)
		for _, r := range page.Rows {
			require.Contains(t, r.Flags, "alpha")
		}
		require.Equal(t, res.ByFlag, page.ByFlag)
		_, err = p.Page("op-plan", FilterFlagPrefix, "", 0, 5)
		require.ErrorIs(t, err, ErrUnknownFilter)
	}
}

func TestRunPlan_RefusesAFlagTheIndexCannotCarry(t *testing.T) {
	for _, bad := range []string{"", "a,b", "a b", "a\x00b", "tab\there"} {
		s := newMemStore()
		rows := variedRows(3)
		rows[1].Flags = []string{bad}
		f := &noBookIDsFixer{s: s, rows: rows}
		_, err := RunPlan(context.Background(), f, nil, PlanDeps{Guard: s, Series: seriesNamer}, nopReporter{})
		require.Error(t, err, "flag %q", bad)
		require.Contains(t, err.Error(), "invalid row flag")
	}
}

// storeWithTornPlan opens a real Pebble store holding one operation record
// whose bytes are a repairs.plan operation with a timestamp that does not
// parse (a torn or hand-edited value). The value is written straight into the
// database, before the store opens, because no store method can produce it.
// The key layout is pinned by the assertion at the end: if it changes, the
// store would see no such operation and that assertion fails.
func storeWithTornPlan(t *testing.T, opID string) *database.PebbleStore {
	t.Helper()
	dir := t.TempDir()
	st, err := database.NewPebbleStore(dir)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	db, err := pebble.Open(dir, &pebble.Options{})
	require.NoError(t, err)
	torn := `{"ID":"` + opID + `","DefID":"` + PlanOpID + `","Status":"completed","QueuedAt":"garbage","ResultData":"{}"}`
	require.NoError(t, db.Set([]byte("opv2:op:"+opID), []byte(torn), pebble.Sync))
	require.NoError(t, db.Close())
	st, err = database.NewPebbleStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.GetOperationV2(opID)
	require.ErrorIs(t, err, database.ErrOpRecordUndecodable, "the torn record must be found under the operation's key")
	return st
}

// TestLoadPlan_UndecodableOperationRecordNeedsARerun: a plan operation whose
// stored record cannot be decoded at all (migration 66 leaves such a legacy
// plan as it found it) is a plan to run again, not a 500: the store's read
// error carries database.ErrOpRecordUndecodable and LoadPlan maps it.
func TestLoadPlan_UndecodableOperationRecordNeedsARerun(t *testing.T) {
	st := storeWithTornPlan(t, "op-torn")
	_, err := LoadPlan(st, st, "op-torn", "any-fixer")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPlanSummaryUnreadable)
	require.True(t, PlanNeedsRerun(err), err.Error())
	require.NotErrorIs(t, err, ErrPlanNotFound)
}

// Plan rows and class-list rows can belong to one operation. SavePlan and
// ClearPlanRows replace only the plan/ namespace, so neither deletes the class
// lists stored beside it, and the plan reads back with the class lists present.
func TestPlanRows_ShareAnOperationWithClassListsWithoutDeletingThem(t *testing.T) {
	s := newMemStore()
	res := planFor(t, s, &noBookIDsFixer{s: s, rows: variedRows(12)})
	lists := map[string][]string{"fills": {"b1", "b2", "b3"}, "held": {"b9"}}
	summary, err := database.SaveClassLists(s, "op-plan", lists)
	require.NoError(t, err)

	// Storing the plan after the class lists leaves them.
	stored := storeSummary(t, s, s, res, "op-plan")
	page, err := stored.Page("op-plan", FilterAll, "", 0, 20)
	require.NoError(t, err)
	require.Len(t, page.Rows, 12)
	require.True(t, summary.Valid())
	got, err := summary.Page(s, "op-plan", "fills", 0, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"b1", "b2", "b3"}, got, "SavePlan must not delete the class lists")

	// Clearing the plan rows leaves them too.
	require.NoError(t, ClearPlanRows(s, "op-plan"))
	got, err = summary.Page(s, "op-plan", "held", 0, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"b9"}, got, "ClearPlanRows must not delete the class lists")
	_, err = stored.Page("op-plan", FilterAll, "", 0, 20)
	require.True(t, PlanNeedsRerun(err), "a plan whose rows were cleared needs a re-run, got %v", err)

	// Storing class lists again leaves the plan rows.
	stored = storeSummary(t, s, s, res, "op-plan")
	_, err = database.SaveClassLists(s, "op-plan", map[string][]string{"fills": {"b4"}})
	require.NoError(t, err)
	page, err = stored.Page("op-plan", FilterAll, "", 0, 20)
	require.NoError(t, err)
	require.Len(t, page.Rows, 12, "SaveClassLists must not delete the plan rows")
}
