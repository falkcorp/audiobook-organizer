// file: internal/reconcile/merge_locks_test.go
// version: 1.0.0
// guid: 5a8c2f41-9e7d-4b36-8c1a-d2f6e0b4a793
// last-edited: 2026-09-02

package reconcile

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func lockedReader(bookID string, keys ...string) *database.MockStore {
	return &database.MockStore{
		GetMetadataFieldStatesFunc: func(id string) ([]database.MetadataFieldState, error) {
			if id != bookID {
				return nil, nil
			}
			rows := make([]database.MetadataFieldState, 0, len(keys))
			for _, k := range keys {
				rows = append(rows, database.MetadataFieldState{BookID: id, Field: k, OverrideLocked: true})
			}
			return rows, nil
		},
	}
}

// The winner has a narrator the user cleared and LOCKED blank; the loser still
// carries the stale narrator. The merge fills empty fields from the loser --
// which is exactly how a locked blank gets resurrected. Narrator must stay nil;
// the unlocked publisher must still be copied and be the only field reported.
func TestMergeBookMetadataRespectingLocks_LockedBlankStaysBlank(t *testing.T) {
	winner := &database.Book{ID: "w", Title: "Winner"}
	loser := &database.Book{ID: "l", Title: "Loser", Narrator: new("Stale Narrator"), Publisher: new("Real Publisher")}

	merged, err := mergeBookMetadataRespectingLocks(lockedReader("w", database.FieldKeyNarrator), winner, loser)
	require.NoError(t, err)
	require.Nil(t, winner.Narrator, "the user locked the narrator blank; the loser's must not land")
	require.NotNil(t, winner.Publisher, "publisher is unlocked and must still merge")
	require.Equal(t, "Real Publisher", *winner.Publisher)
	require.Equal(t, []string{"publisher"}, merged, "the restored field must not be reported as merged")
}

// Lock reads fail closed: nothing merges and the winner is untouched.
func TestMergeBookMetadataRespectingLocks_LockReadErrorMergesNothing(t *testing.T) {
	winner := &database.Book{ID: "w", Title: "Winner"}
	loser := &database.Book{ID: "l", Narrator: new("N"), Publisher: new("P")}
	store := &database.MockStore{
		GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) {
			return nil, errors.New("pebble: closed")
		},
	}

	merged, err := mergeBookMetadataRespectingLocks(store, winner, loser)
	require.ErrorIs(t, err, database.ErrFieldLocksUnavailable)
	require.Nil(t, merged)
	require.Nil(t, winner.Narrator)
	require.Nil(t, winner.Publisher)
}

// MergeBookMetadata's own field names double as the lock vocabulary for every
// lockable column it touches, or the subtraction above silently reports a
// restored field as merged.
func TestMergeBookMetadata_LockableNamesAreTheVocabulary(t *testing.T) {
	dst := &database.Book{ID: "d"}
	src := &database.Book{
		ID:                   "s",
		Narrator:             new("n"),
		Description:          new("d"),
		Language:             new("en"),
		Publisher:            new("p"),
		AudiobookReleaseYear: new(2001),
		ISBN10:               new("0123456789"),
		ISBN13:               new("9780123456789"),
		ASIN:                 new("B000000000"),
	}
	merged := MergeBookMetadata(dst, src)
	for _, want := range []string{
		database.FieldKeyNarrator, database.FieldKeyDescription, database.FieldKeyLanguage,
		database.FieldKeyPublisher, database.FieldKeyAudiobookReleaseYear,
		database.FieldKeyISBN10, database.FieldKeyISBN13, database.FieldKeyASIN,
	} {
		require.Contains(t, merged, want)
	}
}
