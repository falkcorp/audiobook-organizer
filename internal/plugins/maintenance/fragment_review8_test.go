// file: internal/plugins/maintenance/fragment_review8_test.go
// version: 1.0.0
// guid: 2dc36c12-0ba5-4ca9-90b1-ea5c5c5b27c9
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
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// recordReverted reports whether op's plan record is marked reverted.
func (f *fragFixture) recordReverted(t *testing.T, op string) bool {
	t.Helper()
	cs, err := f.s.GetOperationChanges(op)
	require.NoError(t, err)
	for _, c := range cs {
		if c.ChangeType == undo.ChangeTypeRepairPlanRecord {
			return c.RevertedAt != nil
		}
	}
	t.Fatalf("op %s has no plan record", op)
	return false
}

// newLiveBook adds a live single-file book outside every fixture folder.
func (f *fragFixture) newLiveBook(t *testing.T, name string) string {
	t.Helper()
	p := f.file(t, "lib/Other/"+name+".mp3", 5000)
	id := f.book(t, name, "Another edition "+name, p, nil)
	f.row(t, name+"-row", id, p, name+".mp3", 5000, 3600, 0)
	return id
}

// otherFixerRetiresSurvivor is review 7's probe: another fixer moves the
// survivor's rows onto x and retires the survivor into x.
func (f *fragFixture) otherFixerRetiresSurvivor(t *testing.T, survivor, x string) {
	t.Helper()
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
	require.NoError(t, err)
}

// finishInto is an owner finishing a run by hand: every live book of ids but
// target has its rows moved onto target and is merged into it.
func (f *fragFixture) finishInto(t *testing.T, ids []string, target string) {
	t.Helper()
	now := time.Now().UTC()
	for _, id := range ids {
		if id == target {
			continue
		}
		b, err := f.s.GetBookByID(id)
		require.NoError(t, err)
		if b == nil || b.IsSoftDeleted() {
			continue
		}
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		var fids []string
		for _, br := range rows {
			fids = append(fids, br.ID)
		}
		if len(fids) > 0 {
			require.NoError(t, f.s.MoveBookFilesToBook(fids, id, target))
		}
		yes := true
		_, err = f.s.ModifyBook(id, func(bk *database.Book) error {
			bk.MarkedForDeletion, bk.MarkedForDeletionAt, bk.MergedIntoBookID = &yes, &now, &target
			return nil
		})
		require.NoError(t, err)
	}
}

// interruptedRows lists the plan's held interrupted rows.
func interruptedRows(res *repairs.PlanResult) []repairs.Row {
	var out []repairs.Row
	for _, r := range res.Rows {
		if r.Skipped == fragSkipInterrupted {
			out = append(out, r)
		}
	}
	return out
}

// requireCleared: the plan holds nothing for the run any more.
func requireCleared(t *testing.T, res *repairs.PlanResult, rowID string) {
	t.Helper()
	for _, r := range res.Rows {
		require.False(t, r.Skipped == fragSkipInterrupted, "still held: %s: %s", r.RowID, r.SkipReason)
		require.False(t, r.RowID == rowID && !r.Applicable(), "row %s still listed: %s", rowID, r.SkipReason)
	}
}

// multiHolders counts the live books holding two or more of r's planned
// member files: more than one is one work split into two live books.
func (f *fragFixture) multiHolders(t *testing.T, r repairs.Row) int {
	t.Helper()
	var st fragGroupState
	require.NoError(t, json.Unmarshal(r.State, &st))
	want := map[string]bool{}
	for id, fid := range st.Files {
		if !st.Roles[id].Copy {
			want[fid] = true
		}
	}
	books, err := f.s.GetAllBooksCore(0, 0) // live books only
	require.NoError(t, err)
	n := 0
	for _, b := range books {
		rows, err := f.s.GetBookFiles(b.ID)
		require.NoError(t, err)
		c := 0
		for _, br := range rows {
			if want[br.ID] {
				c++
			}
		}
		if c >= 2 {
			n++
		}
	}
	return n
}

// applyAllOver applies every applicable row of a fresh plan that touches r's
// books.
func (f *fragFixture) applyAllOver(t *testing.T, r repairs.Row, planOp string) {
	t.Helper()
	res := f.plan(t, planOp)
	for _, row := range applicableRowsWith(res, r.BookIDs) {
		f.apply(t, planOp, "op-x-"+row.RowID, []string{row.RowID}, nil)
	}
}

// TestFragmentFixer_RevertKeepsTheHoldOfARetiredSurvivor (review 8 B1): the
// survivor of a cut run is retired into X by another fixer, and the owner
// then reverts the cut run. That revert cannot restore the moves the other
// fixer took over, so it must not mark the plan record reverted: the hold
// stays and no plan splits the work. Before the fix the record's no-op
// revert always succeeded (22 of 24 cut points split with organized=all, 24
// of 24 with none).
func TestFragmentFixer_RevertKeepsTheHoldOfARetiredSurvivor(t *testing.T) {
	for _, sh := range [][2]string{{"all", cutVGOrig}, {"none", cutVGNone}} {
		t.Run(sh[0]+"/"+sh[1], func(t *testing.T) {
			tried := 0
			for at := 1; ; at += 5 {
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
				x := f.newLiveBook(t, fmt.Sprintf("x%d", at))
				f.otherFixerRetiresSurvivor(t, r.Proposed["survivor"], x)
				_, rerr := audiobooks.NewRevertService(f.s).RevertOperation("op-cut")
				if rerr != nil {
					require.False(t, f.recordReverted(t, "op-cut"), "cut at %d: a failed revert keeps the plan record", at)
					require.Empty(t, applicableRowsWith(f.plan(t, "op-plan2"), r.BookIDs), "cut at %d: no row over the set while it is held", at)
				}
				f.applyAllOver(t, r, "op-plan3")
				require.LessOrEqual(t, f.multiHolders(t, r), 1, "cut at %d: one work, at most one live book holding it", at)
				tried++
				closeF()
			}
			require.Greater(t, tried, 15)
		})
	}
}

// TestFragmentFixer_PruneThenRevertRefuses (review 8 B1): after the prune the
// plan record is the op's only row. Reverting the op restores nothing, so it
// is refused with a clear error and the record (and hold) stays.
func TestFragmentFixer_PruneThenRevertRefuses(t *testing.T) {
	f := newFragFixture(t)
	planned, survivor, others, otherRows := f.looseCut(t)
	w := f.fragWriter(t, "op-cut")
	f.journalPlanRecord(t, w, planned)
	f.moveAndRetire(t, w, fragFixerID, survivor, others, otherRows, 1)
	_, err := f.s.PruneOperationChanges(time.Now().Add(time.Hour))
	require.NoError(t, err)
	res, err := audiobooks.NewRevertService(f.s).RevertOperation("op-cut")
	require.Error(t, err)
	require.Nil(t, res)
	require.Contains(t, err.Error(), "nothing left to revert but its plan record")
	require.False(t, f.recordReverted(t, "op-cut"))
	for _, row := range applicableRowsWith(f.plan(t, "op-plan2"), planned.BookIDs) {
		require.Equal(t, planned.RowID, row.RowID, "only the run's own row may apply")
		require.Equal(t, survivor, row.Proposed["survivor"])
	}
}

// TestFragmentFixer_PrunedRunStillContinues (review 8 N3): with every row of
// the run but its plan record pruned, the run's own retires still count as
// its own (the record is the evidence) and the continuation finishes it.
func TestFragmentFixer_PrunedRunStillContinues(t *testing.T) {
	f := newFragFixture(t)
	planned, survivor, others, otherRows := f.looseCut(t)
	w := f.fragWriter(t, "op-cut")
	f.journalPlanRecord(t, w, planned)
	f.moveAndRetire(t, w, fragFixerID, survivor, others, otherRows, 1)
	_, err := f.s.PruneOperationChanges(time.Now().Add(time.Hour))
	require.NoError(t, err)
	row := findRow(t, f.plan(t, "op-plan2"), planned.RowID)
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	out := f.apply(t, "op-plan2", "op-apply2", []string{row.RowID}, nil)
	require.Equal(t, 1, out.Applied, "outcomes %v %+v", out.ByOutcome, out.Rows)
	f.requireOneLiveBookHoldsAll(t, planned, survivor)
	requireCleared(t, f.plan(t, "op-plan3"), planned.RowID)
}

// forgeRecord journals a second plan record for r under op, with survivor
// and plan time replaced, as a second interrupted run of the folder would.
func (f *fragFixture) forgeRecord(t *testing.T, r repairs.Row, op, survivor string, at time.Time) {
	t.Helper()
	plan := r.Detail.(*fragGroupPlan)
	var st fragGroupState
	require.NoError(t, json.Unmarshal(r.State, &st))
	st.Survivor, st.PlannedAt = survivor, at
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	proposed := map[string]string{}
	for k, v := range r.Proposed {
		proposed[k] = v
	}
	proposed["survivor"] = survivor
	rec, err := json.Marshal(fragPlanRecord{RowID: r.RowID, Fingerprint: r.Fingerprint, Dir: plan.Dir, Key: plan.Key,
		Survivor: survivor, BookIDs: r.BookIDs, Proposed: proposed, PlannedAt: at, State: raw})
	require.NoError(t, err)
	f.applyOp(op, fragFixerID)
	w := f.fragWriter(t, op)
	require.NoError(t, w.Journal(survivor, undo.ChangeTypeRepairPlanRecord, fragRecordField(r.RowID), "", string(rec)))
}

// TestFragmentFixer_HeldRowActionsClearTheHold (review 8 S1-S4): for every
// reason an interrupted run is held, the row's text offers only actions
// that clear it, and each offered action, carried out, does.
func TestFragmentFixer_HeldRowActionsClearTheHold(t *testing.T) {
	cut40 := func(t *testing.T) (*fragFixture, repairs.Row, func()) {
		f, r, closeF := newCutFixture(t, "none", cutVGNone)
		cut, more := f.cutAt(t, r, 40)
		require.True(t, cut && more)
		require.True(t, f.cutRunRecorded(t, "op-cut"))
		return f, r, closeF
	}
	held := func(t *testing.T, f *fragFixture, r repairs.Row) repairs.Row {
		t.Helper()
		rows := interruptedRows(f.plan(t, "op-plan2"))
		require.NotEmpty(t, rows)
		for _, row := range rows {
			if strings.HasPrefix(row.RowID, r.RowID) {
				return row
			}
		}
		t.Fatalf("no held row for %s: %+v", r.RowID, rows)
		return repairs.Row{}
	}

	t.Run("survivor merged into X by another fixer: finish into X", func(t *testing.T) {
		f, r, closeF := cut40(t)
		defer closeF()
		x := f.newLiveBook(t, "x")
		f.otherFixerRetiresSurvivor(t, r.Proposed["survivor"], x)
		row := held(t, f, r)
		require.Contains(t, row.SkipReason, actFinish(x))
		require.NotContains(t, row.SkipReason, "revert")
		require.NotContains(t, row.SkipReason, "into "+r.Proposed["survivor"], "never names the retired survivor as the target")
		f.finishInto(t, r.BookIDs, x)
		requireCleared(t, f.plan(t, "op-plan3"), r.RowID)
		require.LessOrEqual(t, f.multiHolders(t, r), 1)
	})

	t.Run("survivor merged into X by a dedup merge: finish into X", func(t *testing.T) {
		f, r, closeF := cut40(t)
		defer closeF()
		survivor := r.Proposed["survivor"]
		x := f.newLiveBook(t, "x")
		f.organized(t, x)
		_, err := merge.NewService(f.s).MergeBooks([]string{survivor, x}, x)
		require.NoError(t, err)
		sb, err := f.s.GetBookByID(survivor)
		require.NoError(t, err)
		require.True(t, sb.IsSoftDeleted())
		row := held(t, f, r)
		require.Contains(t, row.SkipReason, actFinish(x), "the dedup merge's winner is found through the version group")
		require.NotContains(t, row.SkipReason, "deleted")
		f.finishInto(t, r.BookIDs, x)
		requireCleared(t, f.plan(t, "op-plan3"), r.RowID)
	})

	t.Run("survivor deleted outright: restore it", func(t *testing.T) {
		f, r, closeF := cut40(t)
		defer closeF()
		survivor := r.Proposed["survivor"]
		yes := true
		now := time.Now().UTC()
		_, err := f.s.ModifyBook(survivor, func(b *database.Book) error {
			b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
			return nil
		})
		require.NoError(t, err)
		row := held(t, f, r)
		require.Contains(t, row.SkipReason, "was deleted")
		require.Contains(t, row.SkipReason, actRestore(survivor))
		_, err = f.s.ModifyBook(survivor, func(b *database.Book) error {
			b.MarkedForDeletion, b.MarkedForDeletionAt = nil, nil
			return nil
		})
		require.NoError(t, err)
		f.applyAllOver(t, r, "op-plan3")
		requireCleared(t, f.plan(t, "op-plan4"), r.RowID)
		for _, id := range r.BookIDs {
			b, err := f.s.GetBookByID(id)
			require.NoError(t, err)
			require.Equal(t, id != survivor, b.IsSoftDeleted(), "the continuation finished: only the survivor is live (%s)", id)
		}
		require.Equal(t, 1, f.multiHolders(t, r))
	})

	t.Run("a planned file on an outside live book: move it back, finish", func(t *testing.T) {
		f, r, closeF := cut40(t)
		defer closeF()
		survivor := r.Proposed["survivor"]
		z := f.newLiveBook(t, "z")
		rows, err := f.s.GetBookFiles(survivor)
		require.NoError(t, err)
		var st fragGroupState
		require.NoError(t, json.Unmarshal(r.State, &st))
		var moved string
		for _, br := range rows {
			if br.ID != st.Files[survivor] {
				moved = br.ID
				break
			}
		}
		require.NotEmpty(t, moved, "event 40 has moved member rows onto the survivor")
		require.NoError(t, f.s.MoveBookFilesToBook([]string{moved}, survivor, z))
		row := held(t, f, r)
		require.Contains(t, row.SkipReason, "outside the run")
		require.Contains(t, row.SkipReason, z)
		require.Contains(t, row.SkipReason, actFinishOutside(survivor))
		require.NoError(t, f.s.MoveBookFilesToBook([]string{moved}, z, survivor))
		f.finishInto(t, r.BookIDs, survivor)
		requireCleared(t, f.plan(t, "op-plan3"), r.RowID)
	})

	changed := func(t *testing.T) (*fragFixture, repairs.Row, func()) {
		f, r, closeF := cut40(t)
		for _, id := range r.BookIDs {
			b, err := f.s.GetBookByID(id)
			require.NoError(t, err)
			if id != r.Proposed["survivor"] && !b.IsSoftDeleted() {
				f.organized(t, id) // an outside change the run did not make
				break
			}
		}
		return f, r, closeF
	}
	t.Run("live survivor, the run's row changed: revert", func(t *testing.T) {
		f, r, closeF := changed(t)
		defer closeF()
		row := held(t, f, r)
		require.Contains(t, row.SkipReason, actRevert([]string{"op-cut"}))
		_, err := audiobooks.NewRevertService(f.s).RevertOperation("op-cut")
		require.NoError(t, err)
		require.True(t, f.recordReverted(t, "op-cut"), "a full revert takes the plan record with it")
		require.Empty(t, interruptedRows(f.plan(t, "op-plan3")))
		f.applyAllOver(t, r, "op-plan5")
		require.LessOrEqual(t, f.multiHolders(t, r), 1)
	})
	t.Run("live survivor, the run's row changed: finish into the survivor", func(t *testing.T) {
		f, r, closeF := changed(t)
		defer closeF()
		row := held(t, f, r)
		require.Contains(t, row.SkipReason, actFinish(r.Proposed["survivor"]))
		f.finishInto(t, r.BookIDs, r.Proposed["survivor"])
		requireCleared(t, f.plan(t, "op-plan3"), r.RowID)
	})

	t.Run("unreadable plan record: revert", func(t *testing.T) {
		f := newFragFixture(t)
		planned, survivor, others, otherRows := f.looseCut(t)
		plan := planned.Detail.(*fragGroupPlan)
		w := f.fragWriter(t, "op-cut")
		rec, err := json.Marshal(fragPlanRecord{RowID: planned.RowID, Fingerprint: planned.Fingerprint, Dir: plan.Dir, Key: plan.Key,
			Survivor: survivor, BookIDs: planned.BookIDs, Proposed: planned.Proposed, PlannedAt: time.Now().UTC(), State: json.RawMessage(`{"files":5}`)})
		require.NoError(t, err)
		require.NoError(t, w.Journal(survivor, undo.ChangeTypeRepairPlanRecord, fragRecordField(planned.RowID), "", string(rec)))
		f.moveAndRetire(t, w, fragFixerID, survivor, others, otherRows, 1)
		row := held(t, f, planned)
		require.Contains(t, row.SkipReason, actRevert([]string{"op-cut"}))
		_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-cut")
		require.NoError(t, err)
		require.Empty(t, interruptedRows(f.plan(t, "op-plan3")))
	})

	t.Run("two survivors in one folder: finish into one of them", func(t *testing.T) {
		f := newFragFixture(t)
		planned, survivor, others, otherRows := f.looseCut(t)
		w := f.fragWriter(t, "op-cut")
		f.journalPlanRecord(t, w, planned)
		f.moveAndRetire(t, w, fragFixerID, survivor, others[:1], otherRows[:1], 0)
		f.forgeRecord(t, planned, "op-cut2", others[1], time.Now().UTC())
		rows := interruptedRows(f.plan(t, "op-plan2"))
		require.Len(t, rows, 2, "one held row per interrupted run, each with its own id")
		require.NotEqual(t, rows[0].RowID, rows[1].RowID)
		for _, row := range rows {
			require.Contains(t, row.SkipReason, survivor)
			require.Contains(t, row.SkipReason, others[1])
		}
		f.finishInto(t, planned.BookIDs, survivor)
		requireCleared(t, f.plan(t, "op-plan3"), planned.RowID)
	})

	t.Run("two plans on one survivor: finish into it", func(t *testing.T) {
		f := newFragFixture(t)
		planned, survivor, others, otherRows := f.looseCut(t)
		w := f.fragWriter(t, "op-cut")
		f.journalPlanRecord(t, w, planned)
		f.moveAndRetire(t, w, fragFixerID, survivor, others[:1], otherRows[:1], 0)
		f.forgeRecord(t, planned, "op-cut2", survivor, time.Now().UTC().Add(time.Minute))
		rows := interruptedRows(f.plan(t, "op-plan2"))
		require.Len(t, rows, 2)
		for _, row := range rows {
			require.Contains(t, row.SkipReason, actFinish(survivor))
		}
		f.finishInto(t, planned.BookIDs, survivor)
		requireCleared(t, f.plan(t, "op-plan3"), planned.RowID)
	})

	unrecorded := func(t *testing.T) (*fragFixture, []string, string) {
		f := newFragFixture(t)
		ids := f.looseGroup(t, "lib/Walk", "Chap", 8, func(int) bool { return true })
		r := rowWithBooks(t, f.plan(t, "op-plan"), ids)
		plan := r.Detail.(*fragGroupPlan)
		f.applyOp("op-cut", fragFixerID)
		w := f.fragWriter(t, "op-cut")
		moved := 0
		for _, m := range plan.Members {
			if m.Frag.Book.ID == plan.SurvivorID || moved >= 2 {
				continue
			}
			moved++
			require.NoError(t, w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID))
			_, err := retireInto(context.Background(), f.p, f.s, w, time.Now, fragFixerID, m.Frag.Book.ID, plan.SurvivorID,
				&merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
			require.NoError(t, err)
		}
		return f, ids, plan.SurvivorID
	}
	t.Run("a run with no plan record: revert it", func(t *testing.T) {
		f, ids, holder := unrecorded(t)
		rows := interruptedRows(f.plan(t, "op-plan2"))
		require.Len(t, rows, 1)
		require.Contains(t, rows[0].SkipReason, actRevert([]string{"op-cut"}))
		require.Contains(t, rows[0].SkipReason, holder)
		_, err := audiobooks.NewRevertService(f.s).RevertOperation("op-cut")
		require.NoError(t, err)
		res := f.plan(t, "op-plan3")
		require.Empty(t, interruptedRows(res))
		require.Len(t, applicableRowsWith(res, ids), 1, "the folder plans afresh as one row")
	})
	t.Run("a run with no plan record: finish into the holder", func(t *testing.T) {
		f, ids, holder := unrecorded(t)
		rows := interruptedRows(f.plan(t, "op-plan2"))
		require.Len(t, rows, 1)
		require.Contains(t, rows[0].SkipReason, "merge this row's books into "+holder)
		f.finishInto(t, rows[0].BookIDs, holder)
		require.Empty(t, interruptedRows(f.plan(t, "op-plan3")))
		_ = ids
	})
}
