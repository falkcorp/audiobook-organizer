// file: internal/merge/itunes_transfer_test.go
// version: 1.0.2
// guid: 7d3f2a90-5c1e-4b86-9a47-e0b8c4d1f259
// last-edited: 2026-10-20

package merge

import (
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Nil fields on the keeper are filled from the loser; fields the keeper
// already has are never overwritten (first-win).
func TestTransferITunesMetadataFirstWin_FillsNilKeepsExisting(t *testing.T) {
	added := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	played := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	keep := &database.Book{
		ID:                 "KEEP",
		ITunesPersistentID: new("PID-KEEP"),
		ITunesRating:       new(60),
	}
	from := &database.Book{
		ID:                 "FROM",
		ITunesPersistentID: new("PID-FROM"),
		ITunesPlayCount:    new(7),
		ITunesRating:       new(100),
		ITunesDateAdded:    &added,
		ITunesLastPlayed:   &played,
		ITunesBookmark:     new(int64(4200)),
	}

	TransferITunesMetadataFirstWin(keep, from)

	assert.Equal(t, "PID-KEEP", *keep.ITunesPersistentID, "existing PID must not be overwritten")
	assert.Equal(t, 60, *keep.ITunesRating, "existing rating must not be overwritten")
	require.NotNil(t, keep.ITunesPlayCount)
	assert.Equal(t, 7, *keep.ITunesPlayCount)
	require.NotNil(t, keep.ITunesDateAdded)
	assert.True(t, added.Equal(*keep.ITunesDateAdded))
	require.NotNil(t, keep.ITunesLastPlayed)
	assert.True(t, played.Equal(*keep.ITunesLastPlayed))
	require.NotNil(t, keep.ITunesBookmark)
	assert.Equal(t, int64(4200), *keep.ITunesBookmark)
}

// An empty-string PID on the keeper counts as unset; nil arguments are no-ops.
func TestTransferITunesMetadataFirstWin_EmptyPIDAndNilArgs(t *testing.T) {
	keep := &database.Book{ITunesPersistentID: new("")}
	TransferITunesMetadataFirstWin(keep, &database.Book{ITunesPersistentID: new("PID-FROM")})
	assert.Equal(t, "PID-FROM", *keep.ITunesPersistentID)

	assert.NotPanics(t, func() {
		TransferITunesMetadataFirstWin(nil, keep)
		TransferITunesMetadataFirstWin(keep, nil)
	})
}

// MergeOptions.CarryITunesFields copies the loser's Book-level iTunes stats
// onto the survivor inside the merge write; without it they stay behind.
func TestService_MergeBooksWithOptions_CarryITunesFields(t *testing.T) {
	for _, carry := range []bool{true, false} {
		t.Run(fmt.Sprintf("carry=%v", carry), func(t *testing.T) {
			store := setupTestStore(t)
			added := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
			played := time.Date(2022, 8, 9, 10, 11, 12, 0, time.UTC)
			winner := seedGuardBook(t, store, "Carry Winner", "m4b", true)
			w, err := store.GetBookByID(winner.ID)
			require.NoError(t, err)
			w.ITunesRating = new(55)
			_, err = store.UpdateBook(w.ID, w)
			require.NoError(t, err)
			loser := seedGuardBook(t, store, "Carry Loser", "mp3", true)

			l, err := store.GetBookByID(loser.ID)
			require.NoError(t, err)
			l.ITunesPersistentID = new("PID-LOSER")
			l.ITunesPlayCount = new(9)
			l.ITunesRating = new(80)
			l.ITunesDateAdded = &added
			l.ITunesLastPlayed = &played
			l.ITunesBookmark = new(int64(1234))
			_, err = store.UpdateBook(l.ID, l)
			require.NoError(t, err)

			_, err = NewService(store).MergeBooksWithOptions(
				[]string{winner.ID, loser.ID}, winner.ID, MergeOptions{CarryITunesFields: carry})
			require.NoError(t, err)

			got, err := store.GetBookByID(winner.ID)
			require.NoError(t, err)
			if !carry {
				assert.Nil(t, got.ITunesPersistentID, "no carry requested: stats stay on the loser")
				assert.Nil(t, got.ITunesPlayCount)
				return
			}
			require.NotNil(t, got.ITunesPersistentID)
			assert.Equal(t, "PID-LOSER", *got.ITunesPersistentID)
			require.NotNil(t, got.ITunesPlayCount)
			assert.Equal(t, 9, *got.ITunesPlayCount)
			require.NotNil(t, got.ITunesRating)
			assert.Equal(t, 55, *got.ITunesRating, "winner's pre-existing rating survives (first-win)")
			require.NotNil(t, got.ITunesDateAdded)
			assert.True(t, added.Equal(*got.ITunesDateAdded))
			require.NotNil(t, got.ITunesLastPlayed)
			assert.True(t, played.Equal(*got.ITunesLastPlayed))
			require.NotNil(t, got.ITunesBookmark)
			assert.Equal(t, int64(1234), *got.ITunesBookmark)
		})
	}
}
