// file: internal/dedup/merge_journaled_siblings_test.go
// version: 1.1.0
// guid: 92334d16-d043-4457-bdc3-273468cb5afc
// last-edited: 2026-10-05

package dedup

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// seedGrouped creates a plausible book in group gid with the given flag
// pointer (nil stays nil).
func seedGrouped(t *testing.T, store database.Store, id, gid string, primary *bool) {
	t.Helper()
	b := arPlausibleBook(id, "Sibling Title")
	g := gid
	b.VersionGroupID = &g
	b.IsPrimaryVersion = primary
	_, err := store.CreateBook(b)
	require.NoError(t, err)
}

func groupAndFlag(t *testing.T, store database.Store, id string) (string, *bool, bool) {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b, "book %s", id)
	g := ""
	if b.VersionGroupID != nil {
		g = *b.VersionGroupID
	}
	return g, b.IsPrimaryVersion, b.IsSoftDeleted()
}

// A journaled merge K<-L carries L's siblings into K's group (merge.MergeBooks
// item 6) and records them on L's journal entry; UnmergeAuto puts each one back
// in its original group with its exact original flag pointer (nil included).
func TestUnmergeAuto_RestoresMovedSiblings(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)
	yes, no := true, false

	seedGrouped(t, store, "K", "H", &yes)
	seedGrouped(t, store, "L", "G", &no)
	seedGrouped(t, store, "LS", "G", &yes)
	seedGrouped(t, store, "LSNIL", "G", nil)

	res, keys, err := engine.MergeBooksJournaled(0, []string{"L", "K"}, "K", "dedup:merge-source:test")
	require.NoError(t, err)
	require.Equal(t, "H", res.VersionGroupID)
	require.Len(t, keys, 1)
	for _, id := range []string{"LS", "LSNIL"} {
		g, flag, deleted := groupAndFlag(t, store, id)
		require.Equal(t, "H", g, "sibling %s moved with the loser", id)
		require.Equal(t, &no, flag)
		require.False(t, deleted)
	}

	entry, err := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Equal(t, []database.AutoMergeJournalSibling{
		{BookID: "LS", FromGroupID: "G", IntoGroupID: "H", WasPrimary: &yes},
		{BookID: "LSNIL", FromGroupID: "G", IntoGroupID: "H", WasPrimary: nil},
	}, entry.Siblings)
	require.Equal(t, res.SiblingJournalID, entry.SiblingJournalID)
	require.Equal(t, "H", entry.IntoGroupID)
	require.False(t, entry.Provisional)

	require.NoError(t, engine.UnmergeAuto(keys[0]))

	g, flag, deleted := groupAndFlag(t, store, "LS")
	require.Equal(t, "G", g, "the undo puts the sibling back in its original group")
	require.Equal(t, &yes, flag)
	require.False(t, deleted)
	g, flag, _ = groupAndFlag(t, store, "LSNIL")
	require.Equal(t, "G", g)
	require.Nil(t, flag, "a nil flag is restored as nil, not false")
	g, _, deleted = groupAndFlag(t, store, "L")
	require.Equal(t, "G", g)
	require.False(t, deleted, "the loser is restored live")
	g, flag, _ = groupAndFlag(t, store, "K")
	require.Equal(t, "H", g)
	require.Equal(t, &yes, flag)

	// A second undo of the same entry is refused and moves nothing.
	err = engine.UnmergeAuto(keys[0])
	require.ErrorContains(t, err, "already undone")
	g, _, _ = groupAndFlag(t, store, "LS")
	require.Equal(t, "G", g)
}

// A sibling that moved to a third group after the merge is not pulled back by
// the undo; the undo reports it instead.
func TestUnmergeAuto_LeavesSiblingThatMovedOn(t *testing.T) {
	engine, store, _ := setupRealStoreEngine(t)
	yes, no := true, false

	seedGrouped(t, store, "K", "H", &yes)
	seedGrouped(t, store, "L", "G", &yes)
	seedGrouped(t, store, "LS", "G", &no)

	_, keys, err := engine.MergeBooksJournaled(0, []string{"L", "K"}, "K", "dedup:merge-source:test")
	require.NoError(t, err)
	require.Len(t, keys, 1)

	_, err = store.ModifyBook("LS", func(b *database.Book) error {
		other := "OTHER"
		b.VersionGroupID = &other
		return nil
	})
	require.NoError(t, err)

	err = engine.UnmergeAuto(keys[0])
	require.Error(t, err)
	require.Contains(t, err.Error(), "sibling LS")
	g, _, _ := groupAndFlag(t, store, "LS")
	require.Equal(t, "OTHER", g, "a later move is not undone")
}
