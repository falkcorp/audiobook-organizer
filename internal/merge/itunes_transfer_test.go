// file: internal/merge/itunes_transfer_test.go
// version: 1.0.0
// guid: 7d3f2a90-5c1e-4b86-9a47-e0b8c4d1f259
// last-edited: 2026-10-17

package merge

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func i64Ptr(i int64) *int64   { return &i }

// Nil fields on the keeper are filled from the loser; fields the keeper
// already has are never overwritten (first-win).
func TestTransferITunesMetadataFirstWin_FillsNilKeepsExisting(t *testing.T) {
	added := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	played := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	keep := &database.Book{
		ID:                 "KEEP",
		ITunesPersistentID: strPtr("PID-KEEP"),
		ITunesRating:       intPtr(60),
	}
	from := &database.Book{
		ID:                 "FROM",
		ITunesPersistentID: strPtr("PID-FROM"),
		ITunesPlayCount:    intPtr(7),
		ITunesRating:       intPtr(100),
		ITunesDateAdded:    &added,
		ITunesLastPlayed:   &played,
		ITunesBookmark:     i64Ptr(4200),
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
	keep := &database.Book{ITunesPersistentID: strPtr("")}
	TransferITunesMetadataFirstWin(keep, &database.Book{ITunesPersistentID: strPtr("PID-FROM")})
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
		store := setupTestStore(t)
		added := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
		played := time.Date(2022, 8, 9, 10, 11, 12, 0, time.UTC)
		winner := seedGuardBook(t, store, "Carry Winner", "m4b", true)
		loser := seedGuardBook(t, store, "Carry Loser", "mp3", true)

		l, err := store.GetBookByID(loser.ID)
		require.NoError(t, err)
		l.ITunesPersistentID = strPtr("PID-LOSER")
		l.ITunesPlayCount = intPtr(9)
		l.ITunesRating = intPtr(80)
		l.ITunesDateAdded = &added
		l.ITunesLastPlayed = &played
		l.ITunesBookmark = i64Ptr(1234)
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
			continue
		}
		require.NotNil(t, got.ITunesPersistentID)
		assert.Equal(t, "PID-LOSER", *got.ITunesPersistentID)
		require.NotNil(t, got.ITunesPlayCount)
		assert.Equal(t, 9, *got.ITunesPlayCount)
		require.NotNil(t, got.ITunesRating)
		assert.Equal(t, 80, *got.ITunesRating)
		require.NotNil(t, got.ITunesDateAdded)
		assert.True(t, added.Equal(*got.ITunesDateAdded))
		require.NotNil(t, got.ITunesLastPlayed)
		assert.True(t, played.Equal(*got.ITunesLastPlayed))
		require.NotNil(t, got.ITunesBookmark)
		assert.Equal(t, int64(1234), *got.ITunesBookmark)
	}
}
