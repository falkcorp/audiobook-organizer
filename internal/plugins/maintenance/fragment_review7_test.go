// file: internal/plugins/maintenance/fragment_review7_test.go
// version: 1.2.0
// guid: 05f9ce23-f920-4f23-9612-af3d87fce747
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// cutRunRecorded reports whether op journaled a plan record.
func (f *fragFixture) cutRunRecorded(t *testing.T, op string) bool {
	t.Helper()
	cs, err := f.s.GetOperationChanges(op)
	require.NoError(t, err)
	for _, c := range cs {
		if c.ChangeType == undo.ChangeTypeRepairPlanRecord {
			return true
		}
	}
	return false
}

// cutAt runs r's apply as op-cut, cut at event at; false when the run never
// reached that event or went through (nothing to test there).
func (f *fragFixture) cutAt(t *testing.T, r repairs.Row, at int) (cut, more bool) {
	t.Helper()
	cs := &cutStore{PebbleStore: f.s, at: at}
	f.applyOp("op-cut", fragFixerID)
	w := repairs.NewWriter(cs, cs, fragFixerID, "bulk_update", "repairs-").WithJournal(cs, cs, "op-cut")
	err := newFragmentFixer(f.p).Apply(context.Background(), w, r)
	if !cs.hit {
		return false, false
	}
	return err != nil, true
}

// TestFragmentFixer_SurvivorRetiredByAnotherFixer (review 7 B1): a run is cut,
// then ANOTHER fixer moves the survivor's rows onto a book X and retires the
// survivor into X. The run's plan record must still hold the set: no
// applicable row over its books (before the fix the leftovers regrouped
// around a new survivor: 68 of 120 cut points in the prod shape, 106 of 120
// with no version group), and the record's row says where the survivor went.
func TestFragmentFixer_SurvivorRetiredByAnotherFixer(t *testing.T) {
	stride := fragSweepStride(1, 10) // every cut point under AORG_FRAG_CUT_MATRIX=full
	for i, sh := range [][2]string{{"all", cutVGOrig}, {"none", cutVGNone}} {
		t.Run(sh[0]+"/"+sh[1], func(t *testing.T) {
			held := 0
			for at := fragSweepStart(i, stride); ; at += stride {
				f, r, closeF := newCutFixture(t, sh[0], sh[1])
				cut, more := f.cutAt(t, r, at)
				if !more {
					closeF()
					break
				}
				if !cut || !f.cutRunRecorded(t, "op-cut") {
					closeF()
					continue
				}
				survivor := r.Proposed["survivor"]
				xp := f.file(t, fmt.Sprintf("lib/Other/x%d.mp3", at), 5000)
				x := f.book(t, fmt.Sprintf("x%d", at), "Another edition", xp, nil)
				f.row(t, fmt.Sprintf("xr%d", at), x, xp, "x.mp3", 5000, 3600, 0)
				f.applyOp("op-other", "some-other-fixer")
				w := repairs.NewWriter(f.s, f.s, "some-other-fixer", "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-other")
				rows, err := f.s.GetBookFiles(survivor)
				require.NoError(t, err)
				var ids []string
				for _, br := range rows {
					ids = append(ids, br.ID)
				}
				require.NoError(t, w.MoveBookFiles(ids, survivor, x))
				_, err = retireInto(context.Background(), f.p, f.s, w, time.Now, "some-other-fixer", survivor, x, nil)
				require.NoError(t, err, "cut at %d: the other fixer's retire", at)

				res := f.plan(t, "op-plan2")
				require.Empty(t, applicableRowsWith(res, r.BookIDs), "cut at %d: no applicable row over the cut set", at)
				var row *repairs.Row
				for i := range res.Rows {
					if res.Rows[i].RowID == r.RowID {
						row = &res.Rows[i]
					}
				}
				if row == nil {
					// Nothing listed: only right when the run's work all
					// ended in X (every book merged into it, every member
					// file on a live book), which recordDone counts done.
					require.Empty(t, f.plannedRowsOnRetiredBooks(t, r), "cut at %d", at)
					for _, id := range r.BookIDs {
						b, err := f.s.GetBookByID(id)
						require.NoError(t, err)
						require.True(t, b.IsSoftDeleted(), "cut at %d: book %s live with no hold", at, id)
					}
					closeF()
					continue
				}
				require.Equal(t, fragSkipInterrupted, row.Skipped, "cut at %d", at)
				require.Contains(t, row.SkipReason, "merged into "+x, "cut at %d", at)
				require.Contains(t, strings.Join(row.Evidence, " "), "op-cut", "cut at %d: the hold names the run's operation", at)
				require.Contains(t, row.SkipReason, actFinish(x), "cut at %d: it offers finishing into X, the book the survivor became", at)
				require.NotContains(t, row.SkipReason, "revert", "cut at %d: a revert cannot clear this hold, so it is not offered", at)
				held++
				closeF()
			}
			t.Logf("%s/%s: %d cut points held after another fixer retired the survivor", sh[0], sh[1], held)
			require.Greater(t, held, 40/stride)
		})
	}
}

// finishByHand is an owner finishing an interrupted run by hand: every live
// book of the row but the survivor has its rows moved onto the survivor and
// is merged into it (not by this fixer, so no journal row of ours).
func (f *fragFixture) finishByHand(t *testing.T, r repairs.Row) {
	t.Helper()
	survivor := r.Proposed["survivor"]
	now := time.Now().UTC()
	for _, id := range r.BookIDs {
		if id == survivor {
			continue
		}
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		if b.IsSoftDeleted() {
			continue
		}
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		var ids []string
		for _, br := range rows {
			ids = append(ids, br.ID)
		}
		if len(ids) > 0 {
			require.NoError(t, f.s.MoveBookFilesToBook(ids, id, survivor))
		}
		yes := true
		_, err = f.s.ModifyBook(id, func(bk *database.Book) error {
			bk.MarkedForDeletion, bk.MarkedForDeletionAt, bk.MergedIntoBookID = &yes, &now, &survivor
			return nil
		})
		require.NoError(t, err)
	}
}

// plannedRowsOnRetiredBooks lists the planned member rows of r that sit on a
// soft-deleted book: files out of every view.
func (f *fragFixture) plannedRowsOnRetiredBooks(t *testing.T, r repairs.Row) []string {
	t.Helper()
	var st fragGroupState
	require.NoError(t, json.Unmarshal(r.State, &st))
	want := map[string]bool{}
	for id, fid := range st.Files {
		if !st.Roles[id].Copy {
			want[fid] = true
		}
	}
	var out []string
	for _, id := range r.BookIDs {
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		if !b.IsSoftDeleted() {
			continue
		}
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		for _, br := range rows {
			if want[br.ID] {
				out = append(out, id+"/"+br.ID)
			}
		}
	}
	return out
}

// TestFragmentFixer_FinishedByHandClearsTheHold (review 7 S1): an owner who
// finishes a cut run by hand (merging the rest into the survivor) clears its
// hold: the next plan offers nothing over the set and holds nothing in the
// folder. Reverting the cut run afterwards never moves a file back onto a
// book the owner retired (it would sit there out of view), and the plan
// after that revert forms no applicable row over the set.
func TestFragmentFixer_FinishedByHandClearsTheHold(t *testing.T) {
	f, r, closeF := newCutFixture(t, "none", cutVGNone)
	defer closeF()
	cut, more := f.cutAt(t, r, 40)
	require.True(t, more && cut, "event 40 cuts the run")
	require.True(t, f.cutRunRecorded(t, "op-cut"))
	f.finishByHand(t, r)

	res := f.plan(t, "op-plan2")
	for _, row := range res.Rows {
		require.NotEqual(t, r.RowID, row.RowID, "a run finished by hand is not offered or held: %s %s %v", row.Skipped, row.SkipReason, row.BookIDs)
		require.NotEqual(t, fragSkipInterrupted, row.Skipped, "nothing is held for it: %s", row.SkipReason)
	}
	require.Empty(t, f.plannedRowsOnRetiredBooks(t, r))

	// The moves off books the owner retired are refused, and the revert
	// says so; everything else of the run is undone.
	_, err := audiobooks.NewRevertService(f.s).RevertOperation("op-cut")
	require.Error(t, err)
	require.Contains(t, err.Error(), "would leave it on a deleted book")
	require.Empty(t, f.plannedRowsOnRetiredBooks(t, r), "the revert strands no file on a retired book")
	require.Empty(t, applicableRowsWith(f.plan(t, "op-plan3"), r.BookIDs), "no new survivor after the revert")
}

// TestFragmentFixer_ScopedPlanHoldsTheFolder (review 7): a book_ids-scoped
// plan that names none of an interrupted run's books still holds a new row
// of that run's folder.
func TestFragmentFixer_ScopedPlanHoldsTheFolder(t *testing.T) {
	f := newFragFixture(t)
	ids := f.looseGroup(t, "lib/Walk", "Chap", 6, func(int) bool { return true })
	r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
	require.True(t, r.Applicable(), r.SkipReason)
	plan := r.Detail.(*fragGroupPlan)
	w := f.fragWriter(t, "op-cut")
	f.journalPlanRecord(t, w, r)
	for _, m := range plan.Members {
		if m.Frag.Book.ID != plan.SurvivorID {
			require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
			break
		}
	}
	others := f.looseGroup(t, "lib/Walk", "Part", 3, func(int) bool { return true })
	raw, err := json.Marshal(fragParams{BookIDs: others})
	require.NoError(t, err)
	rows, err := newFragmentFixer(f.p).Plan(context.Background(), raw, &repairsOpReporter{id: "op-scoped"})
	require.NoError(t, err)
	got := rowWithBooks(t, &repairs.PlanResult{Rows: rows}, others)
	require.Equal(t, fragSkipInterrupted, got.Skipped, "the scoped row in the run's folder is held: %s", got.SkipReason)
}

// TestFragmentFixer_PlanRecordSurvivesThePrune (review 7 S4): the journal
// prune never deletes a plan record, so an interrupted run's folder stays
// held (or continued) after its other rows age out.
func TestFragmentFixer_PlanRecordSurvivesThePrune(t *testing.T) {
	f := newFragFixture(t)
	planned, survivor, others, otherRows := f.looseCut(t)
	w := f.fragWriter(t, "op-cut")
	f.journalPlanRecord(t, w, planned)
	f.moveAndRetire(t, w, fragFixerID, survivor, others, otherRows, 1)
	n, err := f.s.PruneOperationChanges(time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Positive(t, n, "the run's other rows aged out")
	require.True(t, f.cutRunRecorded(t, "op-cut"), "the plan record is kept")

	res := f.plan(t, "op-plan2")
	for _, row := range applicableRowsWith(res, planned.BookIDs) {
		require.Equal(t, planned.RowID, row.RowID, "only the run's own row may apply")
		require.Equal(t, survivor, row.Proposed["survivor"])
	}
	row := findRow(t, res, planned.RowID)
	require.True(t, row.Applicable() || row.Skipped == fragSkipInterrupted, "%s: %s", row.Skipped, row.SkipReason)
}
