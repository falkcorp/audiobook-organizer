// file: internal/dedup/unmerge_auto_flag_holder_test.go
// version: 1.0.0
// guid: c6c97550-3cc8-40ed-a690-45cfa6b45942
// last-edited: 2026-10-05

package dedup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func fhSetAudio(t *testing.T, f *vptest.Fixture, id, format string, kbps int) {
	t.Helper()
	_, err := f.S.ModifyBook(id, func(b *database.Book) error {
		b.Format = format
		b.Bitrate = &kbps
		return nil
	})
	require.NoError(t, err)
}

func fhSeedProgress(t *testing.T, f *vptest.Fixture, bookID string) *database.User {
	t.Helper()
	u, err := f.S.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.S.SetUserBookState(&database.UserBookState{
		UserID: u.ID, BookID: bookID, Status: database.UserBookStatusInProgress,
		ProgressPct: 45, LastActivityAt: time.Now(),
	}))
	require.NoError(t, f.S.SetUserPosition(u.ID, bookID, "seg", 45))
	_, err = database.AsSyncIdentityStore(f.S).MintOrGetSyncID(bookID)
	require.NoError(t, err)
	return u
}

func fhProgress(t *testing.T, f *vptest.Fixture, userID, bookID string) int {
	t.Helper()
	st, err := f.S.GetUserBookState(userID, bookID)
	require.NoError(t, err)
	pos, err := f.S.ListUserPositionsForBook(userID, bookID)
	require.NoError(t, err)
	if st == nil || len(pos) == 0 {
		return 0
	}
	return st.ProgressPct
}

func fhCurrentBook(t *testing.T, f *vptest.Fixture, bookID string) string {
	t.Helper()
	ids := database.AsSyncIdentityStore(f.S)
	sid, has, err := ids.GetSyncIDForBook(bookID)
	require.NoError(t, err)
	require.True(t, has)
	item, err := ids.ResolveSyncItem(sid)
	require.NoError(t, err)
	require.NotNil(t, item)
	return item.CurrentBookID
}

// Item 1 of the #3762 re-review: a member of the reused group holds the flag
// and no sibling moves. The merge still writes a sibling-move journal (it
// records the loser's state follow onto that member), so the loser's entry
// must name it, and UnmergeAuto must put the state back on the loser and
// clear its redirect.
func TestUnmergeAuto_ReusedMemberHolderReturnsState(t *testing.T) {
	engine, f, _, es := vpEngine(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
	m := f.Book(t, vptest.Spec{ID: "m", Group: "H", Primary: "false"})
	l := f.Book(t, vptest.Spec{ID: "l", State: "imported"})
	fhSetAudio(t, f, k, "m4b", 128)
	fhSetAudio(t, f, m, "mp3", 64)
	fhSetAudio(t, f, l, "mp3", 64)
	u := fhSeedProgress(t, f, l)

	res, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, "", "dedup:merge-source:test")
	require.NoError(t, err)
	require.Equal(t, k, res.PrimaryID)
	require.Equal(t, m, res.GroupPrimaryID, "the organized member holds the flag")
	require.Empty(t, res.MovedSiblings)
	require.NotEmpty(t, res.SiblingJournalID)
	require.Equal(t, 45, fhProgress(t, f, u.ID, m))
	require.Equal(t, m, fhCurrentBook(t, f, l))

	require.Len(t, keys, 1)
	entry, err := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.Equal(t, res.SiblingJournalID, entry.SiblingJournalID, "the entry names the journal holding the follow")

	require.NoError(t, engine.UnmergeAuto(keys[0]))
	require.Equal(t, 45, fhProgress(t, f, u.ID, l), "the state is back on the loser")
	require.Zero(t, fhProgress(t, f, u.ID, m), "and off the flag holder")
	require.Equal(t, l, fhCurrentBook(t, f, l), "the redirect is cleared")
}

// Item 1, second shape: the flag holder moved in with ANOTHER loser. The
// loser no sibling left with still gets the journal on its entry, and its
// UnmergeAuto returns its state.
func TestUnmergeAuto_HolderMovedWithAnotherLoserReturnsState(t *testing.T) {
	engine, f, _, es := vpEngine(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
	l1 := f.Book(t, vptest.Spec{ID: "l1", Group: "G1", Primary: "false", State: "imported"})
	s1 := f.Book(t, vptest.Spec{ID: "s1", Group: "G1", Primary: "true"})
	l2 := f.Book(t, vptest.Spec{ID: "l2", State: "imported"})
	fhSetAudio(t, f, k, "m4b", 128)
	for _, id := range []string{l1, s1, l2} {
		fhSetAudio(t, f, id, "mp3", 64)
	}
	u := fhSeedProgress(t, f, l2)

	res, keys, err := engine.MergeBooksJournaled(0, []string{l1, l2, k}, "", "dedup:merge-source:test")
	require.NoError(t, err)
	require.Equal(t, k, res.PrimaryID)
	require.Equal(t, s1, res.GroupPrimaryID)
	require.Equal(t, 45, fhProgress(t, f, u.ID, s1))

	keyOf := map[string]string{}
	for _, key := range keys {
		e, err := es.GetAutoMergeJournalEntry(key)
		require.NoError(t, err)
		keyOf[e.LoserID] = key
		require.Equal(t, res.SiblingJournalID, e.SiblingJournalID, "loser %s", e.LoserID)
	}
	require.NoError(t, engine.UnmergeAuto(keyOf[l2]))
	require.Equal(t, 45, fhProgress(t, f, u.ID, l2))
	require.Equal(t, l2, fhCurrentBook(t, f, l2))
}
