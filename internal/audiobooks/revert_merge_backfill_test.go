// file: internal/audiobooks/revert_merge_backfill_test.go
// version: 1.0.0
// guid: 5b41849c-b912-4800-9ead-9c3ea4beb456
// last-edited: 2026-10-18

package audiobooks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	_ "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs" // registers backfill-merge-undo-pointers
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// End-to-end for maintenance job backfill-merge-undo-pointers: a merge whose
// journal has no live pointer (written before the merge cores journaled one,
// simulated here by voiding the pointer the core wrote) is undone by
// reverting the operation id the backfill gives it.

type backfillReporter struct{}

func (backfillReporter) SetTotal(int)                {}
func (backfillReporter) Increment()                  {}
func (backfillReporter) Log(string, string, *string) {}

// runPointerBackfill runs the job under opID and returns its results by
// journal id.
func runPointerBackfill(t *testing.T, store database.Store, opID string, dryRun bool) map[string]merge.PointerBackfillItem {
	t.Helper()
	job, err := maintenance.Get("backfill-merge-undo-pointers")
	require.NoError(t, err)
	ctx := maintenance.WithOperationID(context.Background(), opID)
	require.NoError(t, job.Run(ctx, store, backfillReporter{}, dryRun))
	results, err := store.GetOperationResults(opID)
	require.NoError(t, err)
	out := map[string]merge.PointerBackfillItem{}
	for _, r := range results {
		var item merge.PointerBackfillItem
		require.NoError(t, json.Unmarshal([]byte(r.ResultJSON), &item))
		require.Equal(t, item.Verdict, r.Status)
		require.Equal(t, item.SurvivorID, r.BookID)
		out[item.JournalID] = item
	}
	return out
}

// voidPointers makes opID's pointer rows of changeType look like they were
// never written: what a journal from before the chokepoint pointers has.
func voidPointers(t *testing.T, store database.Store, opID, changeType string) {
	t.Helper()
	rows := pointerRows(t, store, opID, changeType)
	require.NotEmpty(t, rows)
	for _, r := range rows {
		v := *r
		now := time.Now()
		v.Voided, v.RevertedAt = true, &now
		require.NoError(t, store.CreateOperationChange(&v))
	}
}

// livePointersOn returns the live merge journal pointers on bookID.
func livePointersOn(t *testing.T, store database.Store, bookID string) []*database.OperationChange {
	t.Helper()
	rows, err := store.GetBookChanges(bookID)
	require.NoError(t, err)
	var out []*database.OperationChange
	for _, r := range rows {
		if !r.Voided && (r.ChangeType == undo.ChangeTypeMergeCombineJournal || r.ChangeType == undo.ChangeTypeMergeSiblingJournal) {
			out = append(out, r)
		}
	}
	return out
}

func TestBackfillMergeUndoPointers_CombineJournals(t *testing.T) {
	store := dbtest.NewStore(t)
	ms := merge.NewService(store)

	// Historical: a combine whose journal has no live pointer.
	hist := seedCombineFixture(t, store)
	before := snapLibrary(t, store, hist)
	histRes, err := ms.CombineBooks([]string{hist.keep, hist.a, hist.b}, hist.keep, nil)
	require.NoError(t, err)
	voidPointers(t, store, histRes.OperationID, undo.ChangeTypeMergeCombineJournal)
	require.Empty(t, livePointersOn(t, store, hist.keep))

	// Current: a combine whose chokepoint pointer is live.
	cur := seedCombineFixture(t, store)
	curRes, err := ms.CombineBooks([]string{cur.keep, cur.a, cur.b}, cur.keep, nil)
	require.NoError(t, err)

	// Refused: the pointer could not be written, so the journal was emptied
	// to Applied-with-nothing. No undo would do anything with it.
	ref := seedCombineFixture(t, store)
	_, err = merge.NewService(noLedgerStore{Store: store}).CombineBooks([]string{ref.keep, ref.a, ref.b}, ref.keep, nil)
	require.Error(t, err)

	journals, err := ms.ListCombineJournals(0)
	require.NoError(t, err)
	require.Len(t, journals, 3)
	var refusedID string
	for _, j := range journals {
		if j.SurvivorID == ref.keep {
			refusedID = j.ID
		}
	}
	require.NotEmpty(t, refusedID)

	// Preview: names the historical journal only, writes no pointer.
	preview := runPointerBackfill(t, store, "op-backfill-preview", true)
	require.Len(t, preview, 1, "%+v", preview)
	require.Equal(t, merge.PointerBackfillWouldWrite, preview[histRes.JournalID].Verdict)
	require.Empty(t, preview[histRes.JournalID].OperationID)
	require.Empty(t, livePointersOn(t, store, hist.keep), "a preview writes no pointer")
	require.Empty(t, livePointersOn(t, store, ref.keep))

	// Apply: one pointer, under a fresh id that is not the backfill's own.
	applied := runPointerBackfill(t, store, "op-backfill-apply", false)
	require.Len(t, applied, 1, "%+v", applied)
	got := applied[histRes.JournalID]
	require.Equal(t, merge.PointerBackfillWritten, got.Verdict)
	require.NotEmpty(t, got.OperationID)
	require.NotEqual(t, "op-backfill-apply", got.OperationID)
	require.NotEqual(t, histRes.OperationID, got.OperationID)
	require.Empty(t, pointerRows(t, store, "op-backfill-apply", undo.ChangeTypeMergeCombineJournal),
		"nothing is journaled under the backfill's own id, so reverting it undoes no combine")
	ptr := pointerRows(t, store, got.OperationID, undo.ChangeTypeMergeCombineJournal)
	require.Len(t, ptr, 1)
	require.Equal(t, histRes.JournalID, ptr[0].NewValue)
	require.Equal(t, hist.keep, ptr[0].BookID)
	require.Len(t, livePointersOn(t, store, cur.keep), 1, "the live chokepoint pointer is not duplicated")
	require.Empty(t, livePointersOn(t, store, ref.keep), "the refused combine's empty journal gets no pointer")

	// A re-run writes nothing more.
	require.Empty(t, runPointerBackfill(t, store, "op-backfill-rerun", false))
	require.Len(t, livePointersOn(t, store, hist.keep), 1)

	// The backfilled id reverts the historical combine; the current one is
	// untouched.
	revertAndRequireRestored(t, store, got.OperationID, hist, before)
	requireCombined(t, store, cur)
	_, err = NewRevertService(store).RevertOperation(curRes.OperationID)
	require.NoError(t, err)

	// Undone journals are not undoable, so a later run leaves them alone.
	require.Empty(t, runPointerBackfill(t, store, "op-backfill-after-undo", true))
}

func TestBackfillMergeUndoPointers_SiblingJournal(t *testing.T) {
	f, k, l, ls := siblingFixture(t)
	res, err := merge.NewService(f.S).MergeBooks([]string{l, k}, k)
	require.NoError(t, err)
	require.Equal(t, "H", f.GroupOf(t, ls))
	voidPointers(t, f.S, res.SiblingOperationID, undo.ChangeTypeMergeSiblingJournal)

	preview := runPointerBackfill(t, f.S, "op-sib-preview", true)
	require.Equal(t, merge.PointerBackfillWouldWrite, preview[res.SiblingJournalID].Verdict)
	require.Empty(t, livePointersOn(t, f.S, k))

	applied := runPointerBackfill(t, f.S, "op-sib-apply", false)
	got := applied[res.SiblingJournalID]
	require.Equal(t, merge.PointerBackfillWritten, got.Verdict)
	require.Equal(t, merge.SiblingJournalPointerType, got.ChangeType)
	require.Equal(t, k, got.SurvivorID)

	rv, err := NewRevertService(f.S).RevertOperation(got.OperationID)
	require.NoError(t, err)
	require.Equal(t, 1, rv.RestoredTypes[undo.ChangeTypeMergeSiblingJournal])
	require.Equal(t, "G", f.GroupOf(t, ls), "the sibling is back in its group")
	f.RequireSinglePrimary(t, "G", ls)
}
