// file: internal/merge/merge_loser_redirect_test.go
// version: 1.0.0
// guid: 799aa880-0cbb-4b00-8dff-70479ab80a19
// last-edited: 2026-10-04

package merge

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// TestMergeBooks_LoserWithoutSyncIDGetsRedirect: a loser no client had seen
// (no syncID) still leaves a redirect to the winner, so ResolveSurvivor finds
// where it went. Before 2026-10-04 RecordSyncMerge recorded nothing for it,
// and the retired loser could not be told from a book deleted outright.
func TestMergeBooks_LoserWithoutSyncIDGetsRedirect(t *testing.T) {
	store := setupTestStore(t)
	ids := database.AsSyncIdentityStore(store)
	require.NotNil(t, ids)
	var books []string
	for _, title := range []string{"Loser", "Winner"} {
		b := &database.Book{ID: ulid.Make().String(), Title: title, Format: "mp3", FilePath: "/tmp/" + title + ".mp3"}
		_, err := store.CreateBook(b)
		require.NoError(t, err)
		_, has, err := ids.GetSyncIDForBook(b.ID)
		require.NoError(t, err)
		require.False(t, has, "no client has seen %s", title)
		books = append(books, b.ID)
	}
	loser, winner := books[0], books[1]

	res, err := NewService(store).MergeBooks([]string{loser, winner}, winner)
	require.NoError(t, err)
	require.Equal(t, winner, res.PrimaryID)

	_, has, err := ids.GetSyncIDForBook(loser)
	require.NoError(t, err)
	require.True(t, has, "the merge minted the loser's syncID")
	got, err := ResolveSurvivor(store, loser)
	require.NoError(t, err)
	require.Equal(t, winner, got, "the redirect leads from the retired loser to the winner")
}
