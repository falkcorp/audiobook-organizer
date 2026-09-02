// file: internal/merge/combine_override_locks_test.go
// version: 1.0.0
// guid: 0f4a7e91-58c6-4b23-9d07-3e1b6c8a5d24
// last-edited: 2026-09-02

package merge

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// A CombineOverride is the user telling us, in the combine dialog, which
// title/author/narrator the survivor should carry. Until 2026-09-02 it wrote
// those columns and recorded no lock row, so the next metadata fetch was free
// to replace the values the user had just chosen by hand.
func TestCombineBooks_OverrideRecordsUserLocks(t *testing.T) {
	store := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Auto Title", Format: "mp3", FilePath: "/tmp/c-survivor.mp3"}
	shell := &database.Book{ID: ulid.Make().String(), Title: "Shell", Format: "mp3", FilePath: "/tmp/c-shell.mp3"}
	_, err := store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(shell)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, shell.ID}, survivor.ID, &CombineOverride{
		Title:    "The Title I Chose",
		Author:   "Isaac Asimov",
		Narrator: "Scott Brick",
	})
	require.NoError(t, err)

	fresh, err := store.GetBookByID(survivor.ID)
	require.NoError(t, err)
	require.Equal(t, "The Title I Chose", fresh.Title)

	locks, err := database.LoadFieldLocks(store, survivor.ID)
	require.NoError(t, err)
	for _, key := range []string{
		database.FieldKeyTitle, database.FieldKeyNarrator, database.FieldKeyAuthorName,
	} {
		require.Truef(t, locks.Locked(key),
			"%s is not locked after the user chose it in the combine dialog; "+
				"the next fetch would overwrite it", key)
	}

	// The stored override must be the user's value, not a placeholder.
	states, err := store.GetMetadataFieldStates(survivor.ID)
	require.NoError(t, err)
	byField := map[string]database.MetadataFieldState{}
	for _, st := range states {
		byField[st.Field] = st
	}
	require.NotNil(t, byField[database.FieldKeyTitle].OverrideValue)
	require.Equal(t, `"The Title I Chose"`, *byField[database.FieldKeyTitle].OverrideValue)
	require.NotNil(t, byField[database.FieldKeyAuthorName].OverrideValue)
	require.Equal(t, `"Isaac Asimov"`, *byField[database.FieldKeyAuthorName].OverrideValue)
}

// The discriminating negative: a combine with NO override is not a user
// statement about metadata, so it must lock nothing. Without this, a guard
// that locked every survivor unconditionally would pass the test above.
func TestCombineBooks_NoOverrideLocksNothing(t *testing.T) {
	store := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Auto Title", Format: "mp3", FilePath: "/tmp/c2-survivor.mp3"}
	shell := &database.Book{ID: ulid.Make().String(), Title: "Shell", Format: "mp3", FilePath: "/tmp/c2-shell.mp3"}
	_, err := store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(shell)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, shell.ID}, survivor.ID, nil)
	require.NoError(t, err)

	locks, err := database.LoadFieldLocks(store, survivor.ID)
	require.NoError(t, err)
	require.False(t, locks.Any(), "a combine with no user override must lock nothing")
}
