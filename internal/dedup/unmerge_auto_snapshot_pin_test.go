// file: internal/dedup/unmerge_auto_snapshot_pin_test.go
// version: 1.3.0
// guid: 9c4e7a21-5b83-4d06-a1f9-3e2d8b6c0f47
// last-edited: 2026-11-17

package dedup

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// pinEngine is vpEngine with the embedding store on the SAME Pebble DB as the
// book store, which is the production layout (database.NewEmbeddingStoreFromStore
// is the only production constructor). vpEngine and setupRealStoreEngine open a
// second DB for the embedding store, so the auto-merge journal in them is
// invisible to anything that reads it through the book store's DB: a snapshot
// prune cannot be tested there.
func pinEngine(t *testing.T) (*Engine, *vptest.Fixture, *database.EmbeddingStore) {
	t.Helper()
	f := vptest.New(t)
	prev := config.Get().RootDir
	config.SetForTest(func(c *config.Config) { c.RootDir = f.Root })
	t.Cleanup(func() { config.SetForTest(func(c *config.Config) { c.RootDir = prev }) })
	es := database.NewEmbeddingStoreFromStore(f.S)
	require.NotNil(t, es)
	engine, err := NewEngine(es, f.S, nil, nil, merge.NewService(f.S), unified.DefaultScoreConfig())
	require.NoError(t, err)
	return engine, f, es
}

// editTitle writes n distinct titles to one book. Every write to a book row
// leaves one book_ver snapshot of the row it replaced.
func editTitle(t *testing.T, s *database.PebbleStore, id string, n int) {
	t.Helper()
	for i := range n {
		_, err := s.ModifyBook(id, func(b *database.Book) error {
			b.Title = fmt.Sprintf("The Long Halloween, printing %d", i)
			return nil
		})
		require.NoError(t, err)
	}
}

// mergedPair merges loser l into survivor k through the journaled path and
// returns the finalized journal entry.
func mergedPair(t *testing.T, engine *Engine, es *database.EmbeddingStore, l, k string) *database.AutoMergeJournalEntry {
	t.Helper()
	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "dedup:merge-source:test")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	entry, err := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.False(t, entry.Provisional)
	require.NotZero(t, entry.WinnerPreMergeTS, "the merge must have recorded the survivor's pre-merge snapshot")
	require.NotZero(t, entry.LoserPreMergeTS, "the merge must have recorded the loser's pre-merge snapshot")
	return entry
}

// The defect: an automatic merge's undo restores both books from the saved
// snapshots its journal row names. The snapshot prune kept only the newest N
// per book and exempted nothing, so N+1 later edits and one prune left the undo
// with nothing to restore. Here the merge is undone AFTER the survivor and the
// loser were each edited past the keep window and pruned, and the undo must
// still restore both books to what they were before the merge.
func TestUnmergeAuto_UndoSurvivesAPruneAfterLaterEdits(t *testing.T) {
	engine, f, es := pinEngine(t)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	before, err := f.S.GetBookByID(k)
	require.NoError(t, err)
	entry := mergedPair(t, engine, es, l, k)

	const keep = 10
	editTitle(t, f.S, k, keep+2)
	editTitle(t, f.S, l, keep+2)
	for _, id := range []string{k, l} {
		_, err := f.S.PruneBookSnapshots(id, keep)
		require.NoError(t, err)
	}

	require.NoError(t, engine.UnmergeAuto(entry.Key),
		"a merge a journal row still points at must stay undoable after a prune")
	winner, err := f.S.GetBookByID(k)
	require.NoError(t, err)
	require.Equal(t, before.Title, winner.Title, "the survivor must be restored to its pre-merge row")
	loser, err := f.S.GetBookByID(l)
	require.NoError(t, err)
	require.False(t, loser.IsSoftDeleted(), "the loser must be brought back from the trash")
	after, err := es.GetAutoMergeJournalEntry(entry.Key)
	require.NoError(t, err)
	require.NotZero(t, after.UndoneAt)
}

// If a snapshot is gone anyway (a prune that ran before the pin existed, a
// manual delete), the undo must say so and write nothing. Before this fix it
// put the loser's sibling and the loser back first, then failed on the
// survivor with "version not found" and left the entry open: a half-applied
// undo that no retry can finish.
func TestUnmergeAuto_MissingSnapshotRefusesBeforeAnyWrite(t *testing.T) {
	engine, f, es := pinEngine(t)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	s := f.Book(t, vptest.Spec{ID: "nightwing", Group: "G", Primary: "false"})
	entry := mergedPair(t, engine, es, l, k)
	require.Equal(t, "H", f.GroupOf(t, s), "the merge carries the loser's sibling into the survivor's group")

	lostKey := fmt.Sprintf("book_ver:%s:%d", k, entry.WinnerPreMergeTS)
	require.NoError(t, f.S.DeleteRaw(lostKey))

	err := engine.UnmergeAuto(entry.Key)
	require.Error(t, err)
	require.ErrorContains(t, err, lostKey, "the error must name the missing snapshot")
	require.ErrorContains(t, err, "nothing reverted")

	loser, gerr := f.S.GetBookByID(l)
	require.NoError(t, gerr)
	require.True(t, loser.IsSoftDeleted(), "a refused undo must leave the loser in the trash")
	require.Equal(t, "H", f.GroupOf(t, s), "a refused undo must not move the sibling back")
	after, gerr := es.GetAutoMergeJournalEntry(entry.Key)
	require.NoError(t, gerr)
	require.Zero(t, after.UndoneAt)
}

// The provisional entry (written before the merge, finalized after) records each
// book's newest snapshot at that moment and that a baseline was recorded, so the
// prune can pin the one snapshot the patch will name. Here the finalizing write
// fails, so the entry stays provisional and its baselines are visible.
func TestMergeBooksJournaled_ProvisionalEntryRecordsEachBooksBaselineSnapshot(t *testing.T) {
	engine, f, es := pinEngine(t)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	editTitle(t, f.S, k, 2)
	editTitle(t, f.S, l, 3)
	wantK, err := engine.newestSnapshotNanos(k)
	require.NoError(t, err)
	wantL, err := engine.newestSnapshotNanos(l)
	require.NoError(t, err)
	require.NotZero(t, wantK)
	require.NotZero(t, wantL)

	calls := 0
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		calls++
		if calls > 1 {
			return "", fmt.Errorf("injected patch failure")
		}
		return es.PutAutoMergeJournalEntry(e)
	}
	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err)
	entry, err := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.True(t, entry.Provisional)
	require.True(t, entry.BaselinesRecorded)
	require.Equal(t, wantK, entry.WinnerBaselineTS)
	require.Equal(t, wantL, entry.LoserBaselineTS)

	// The prune, through the same store the merge ran on, keeps exactly the
	// snapshot the patch would have named for each book (the first above its
	// baseline) and nothing else of what the merge wrote.
	for id, floor := range map[string]int64{k: wantK, l: wantL} {
		want, werr := engine.preMergeSnapshotNanos(id, floor)
		require.NoError(t, werr)
		require.NotZero(t, want, "the merge wrote at least one snapshot of %s", id)
		editTitle(t, f.S, id, 4)
		_, err := f.S.PruneBookSnapshots(id, 1)
		require.NoError(t, err)
		snaps, err := f.S.GetBookSnapshots(id, 0)
		require.NoError(t, err)
		var got []int64
		for _, sn := range snaps {
			got = append(got, sn.Timestamp.UnixNano())
		}
		require.Contains(t, got, want, "book %s: the snapshot the patch names must survive the prune", id)
		require.Len(t, got, 2, "book %s: that one and the newest, nothing else", id)
	}
}
