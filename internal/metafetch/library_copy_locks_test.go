// file: internal/metafetch/library_copy_locks_test.go
// version: 1.0.0
// guid: 9c2d6b03-7f18-4a54-b3e6-5d0a91c7e482
// last-edited: 2026-09-02

package metafetch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The library copy is a SEPARATE book row with its own id, so it carries its
// own lock rows. A user who edited the copy locked the copy. Copying the
// original's columns over it wholesale is a write to the copy like any other,
// and before 2026-09-02 it went through no guard at all: a fetch the
// original's locks refused could still land on the copy by the back door.
func libCopySyncFixture(states func(string) ([]database.MetadataFieldState, error)) (*database.MockStore, **database.Book, *int, *int) {
	var written *database.Book
	authorWrites, narratorWrites := 0, 0
	store := &database.MockStore{
		GetMetadataFieldStatesFunc: states,
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			clone := *b
			written = &clone
			return b, nil
		},
		GetBookAuthorsFunc: func(string) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "orig", AuthorID: 7, Role: "author"}}, nil
		},
		SetBookAuthorsFunc: func(string, []database.BookAuthor) error { authorWrites++; return nil },
		GetBookNarratorsFunc: func(string) ([]database.BookNarrator, error) {
			return []database.BookNarrator{{BookID: "orig", NarratorID: 3, Role: "narrator"}}, nil
		},
		SetBookNarratorsFunc: func(string, []database.BookNarrator) error { narratorWrites++; return nil },
	}
	return store, &written, &authorWrites, &narratorWrites
}

func libCopyPair() (*database.Book, *database.Book) {
	fetchedNarrator := "Fetched Narrator"
	fetchedPub := "Fetched Press"
	myNarrator := "My Narrator"
	original := &database.Book{
		ID: "orig", Title: "Fetched Title",
		Narrator: &fetchedNarrator, Publisher: &fetchedPub,
	}
	libCopy := &database.Book{
		ID: "copy", Title: "My Title", Narrator: &myNarrator,
	}
	return original, libCopy
}

func TestSyncMetadataToLibraryCopy_HonorsTheCopysOwnLocks(t *testing.T) {
	store, written, authorWrites, narratorWrites := libCopySyncFixture(
		func(bookID string) ([]database.MetadataFieldState, error) {
			if bookID != "copy" {
				return nil, nil
			}
			return []database.MetadataFieldState{
				{BookID: bookID, Field: database.FieldKeyTitle, OverrideLocked: true},
				{BookID: bookID, Field: database.FieldKeyNarrator, OverrideLocked: true},
			}, nil
		})
	svc := NewService(store)
	original, libCopy := libCopyPair()

	svc.syncMetadataToLibraryCopy(original, libCopy)

	require.NotNil(t, *written, "the unlocked fields must still be synced")
	assert.Equal(t, "My Title", (*written).Title, "the copy's locked title was overwritten")
	require.NotNil(t, (*written).Narrator)
	assert.Equal(t, "My Narrator", *(*written).Narrator, "the copy's locked narrator was overwritten")
	require.NotNil(t, (*written).Publisher)
	assert.Equal(t, "Fetched Press", *(*written).Publisher, "an unlocked field must still sync")

	// The join tables are the same fields by another name.
	assert.Equal(t, 0, *narratorWrites, "narrator is locked on the copy; the join table must not be rewritten")
	assert.Equal(t, 1, *authorWrites, "author is not locked; its join table must still sync")
}

func TestSyncMetadataToLibraryCopy_UnlockedCopySyncsEverything(t *testing.T) {
	store, written, authorWrites, narratorWrites := libCopySyncFixture(nil)
	store.GetUserPreferenceFunc = func(string) (*database.UserPreference, error) { return nil, nil }
	svc := NewService(store)
	original, libCopy := libCopyPair()

	svc.syncMetadataToLibraryCopy(original, libCopy)

	require.NotNil(t, *written)
	assert.Equal(t, "Fetched Title", (*written).Title,
		"the fixture cannot observe the lock if an unlocked copy does not sync")
	assert.Equal(t, 1, *authorWrites)
	assert.Equal(t, 1, *narratorWrites)
}

func TestSyncMetadataToLibraryCopy_LockReadErrorSyncsNothing(t *testing.T) {
	store, written, authorWrites, narratorWrites := libCopySyncFixture(
		func(string) ([]database.MetadataFieldState, error) { return nil, errors.New("pebble: closed") })
	svc := NewService(store)
	original, libCopy := libCopyPair()

	svc.syncMetadataToLibraryCopy(original, libCopy)

	assert.Nil(t, *written, "fail closed: an unreadable lock set must not overwrite the copy")
	assert.Equal(t, 0, *authorWrites)
	assert.Equal(t, 0, *narratorWrites)
}
