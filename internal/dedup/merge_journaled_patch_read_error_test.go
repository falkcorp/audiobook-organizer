// file: internal/dedup/merge_journaled_patch_read_error_test.go
// version: 1.0.0
// guid: 2f6a9c40-d7b1-4e83-95a2-0c8e1b4d7f36
// last-edited: 2026-11-17

package dedup

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// armedSnapshotListStore fails GetBookSnapshots for one book once armed, so a test
// can let the baseline reads before the merge succeed and fail the reads the
// post-merge patch makes.
type armedSnapshotListStore struct {
	database.Store
	failID string
	armed  bool
	failed int
}

func (a *armedSnapshotListStore) Unwrap() database.Store { return a.Store }
func (a *armedSnapshotListStore) GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error) {
	if a.armed && id == a.failID {
		a.failed++
		return nil, errors.New("injected: snapshot list unreadable after the merge")
	}
	return a.Store.GetBookSnapshots(id, limit)
}

// A snapshot list that cannot be read when the post-merge patch asks for it is not
// "no snapshot". Recording it as 0 would FINALIZE the row: a finalized row pins
// exactly what it names and a 0 names nothing, so the floor that protected the
// merge's pre-merge snapshot would be dropped, and an undo would skip that side
// and still mark the entry undone. The row is left provisional instead, which
// keeps the floor and makes UnmergeAuto refuse it by name.
func TestMergeBooksJournaled_UnreadablePatchSnapshotsLeaveTheRowProvisionalAndPinned(t *testing.T) {
	for _, side := range []string{"winner", "loser"} {
		t.Run(side, func(t *testing.T) {
			f := vptest.New(t)
			engine, es := engineOn(t, f, f.S)
			k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
			l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
			baseline := newestBefore(t, f.S, []string{k, l})
			failID := map[string]string{"winner": k, "loser": l}[side]
			store := &armedSnapshotListStore{Store: f.S, failID: failID}
			engine.bookStore = store
			engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
				key, err := es.PutAutoMergeJournalEntry(e)
				if e.Provisional {
					store.armed = true // every provisional row is written: the merge is next
				}
				return key, err
			}

			_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
			require.NoError(t, err, "the merge itself is done; a patch that cannot run is logged, not returned")
			require.Positive(t, store.failed, "the injected read error was hit")
			require.Len(t, keys, 1)
			e, gerr := es.GetAutoMergeJournalEntry(keys[0])
			require.NoError(t, gerr)
			require.True(t, e.Provisional, "the row is NOT finalized with a 0 that came from an error")
			require.Zero(t, e.AbortedAt, "and not closed: the merge wrote")
			require.Zero(t, e.WinnerPreMergeTS)
			require.Zero(t, e.LoserPreMergeTS)
			require.True(t, e.BaselinesRecorded)
			require.ErrorContains(t, engine.UnmergeAuto(keys[0]), "never finalized",
				"the undo refuses by name instead of half-applying")
			lb, gerr := f.S.GetBookByID(l)
			require.NoError(t, gerr)
			require.True(t, lb.IsSoftDeleted(), "the refusal changed nothing")

			// The pin the row held is still there: a later prune keeps each book's
			// pre-merge snapshot, which is exactly what the patch would have named.
			store.armed = false
			for _, id := range []string{k, l} {
				want := firstSnapshotAbove(t, f.S, id, baseline[id])
				editTitle(t, f.S, id, 3)
				require.Contains(t, pinnedAfterPrune(t, f.S, id, 1), want, "%s lost its pre-merge snapshot", id)
			}
		})
	}
}

// A loser whose list cannot be read leaves only ITS row provisional. The other
// rows of the merge are finalized and undoable.
func TestMergeBooksJournaled_UnreadableLoserSnapshotsSkipOnlyThatRow(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l1 := f.Book(t, vptest.Spec{ID: "robin", Group: "G1", Primary: "true"})
	l2 := f.Book(t, vptest.Spec{ID: "nightwing", Group: "G2", Primary: "true"})
	store := &armedSnapshotListStore{Store: f.S, failID: l2}
	engine.bookStore = store
	provisionalWrites := 0
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		key, err := es.PutAutoMergeJournalEntry(e)
		if e.Provisional {
			provisionalWrites++
			if provisionalWrites == 2 {
				store.armed = true
			}
		}
		return key, err
	}

	_, keys, err := engine.MergeBooksJournaled(0, []string{l1, l2, k}, k, "t")
	require.NoError(t, err)
	require.Len(t, keys, 2)
	byLoser := map[string]*database.AutoMergeJournalEntry{}
	for _, key := range keys {
		e, gerr := es.GetAutoMergeJournalEntry(key)
		require.NoError(t, gerr)
		byLoser[e.LoserID] = e
	}
	require.False(t, byLoser[l1].Provisional, "the readable loser's row is finalized")
	require.NotZero(t, byLoser[l1].LoserPreMergeTS)
	require.NotZero(t, byLoser[l1].WinnerPreMergeTS)
	require.True(t, byLoser[l2].Provisional, "the unreadable loser's row stays provisional")
	require.Zero(t, byLoser[l2].LoserPreMergeTS)
	require.NoError(t, engine.UnmergeAuto(byLoser[l1].Key), "the finalized row still undoes")
}
