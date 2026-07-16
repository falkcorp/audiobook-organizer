// file: internal/audiobooks/service_mutation_author_sync_test.go
// version: 1.0.0
// guid: 3f9a0c71-6d24-4e83-b1a7-5c8e9f0a2d13
// last-edited: 2026-07-16

package audiobooks

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// TestUpdateAudiobook_SyncsDenormalizedAuthorOnIDChange is a regression guard
// for the stale denormalized-author write-back bug: changing a book's author by
// author_id (not author_name) updated Book.AuthorID but left the embedded
// Book.Author display struct pointing at the OLD author. UpdateBook's
// preserve-on-nil guard could not fix it (the stale struct is non-nil), so the
// stale name was persisted, and every read path
// (resolveAuthorAndSeriesNames / EnrichAudiobooksWithNames) prefers the
// embedded object when non-nil — so the wrong author name was displayed
// indefinitely. The fix syncs the embedded object to the resolved ID BEFORE
// persisting. This test re-reads the row straight from the store (not the
// returned pointer, which the response-enrichment block fixes regardless) so it
// asserts on what was actually written.
func TestUpdateAudiobook_SyncsDenormalizedAuthorOnIDChange(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)

	oldAuthor, err := ps.CreateAuthor("Old Author")
	require.NoError(t, err)
	newAuthor, err := ps.CreateAuthor("New Author")
	require.NoError(t, err)

	// Seed a book whose embedded Author matches its AuthorID (the correct
	// starting state produced by any normal write).
	book, err := ps.CreateBook(&database.Book{
		Title:    "Test Book",
		AuthorID: &oldAuthor.ID,
		Author:   &database.Author{ID: oldAuthor.ID, Name: "Old Author"},
	})
	require.NoError(t, err)

	svc := NewAudiobookService(ps)

	// Change the author via author_id only — the exact reported path.
	_, err = svc.UpdateAudiobook(context.Background(), book.ID, &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{
			Book: &database.Book{AuthorID: &newAuthor.ID},
		},
	})
	require.NoError(t, err)

	// Re-read from the store to inspect what was actually persisted.
	reread, err := ps.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, reread)

	require.NotNil(t, reread.AuthorID)
	require.Equal(t, newAuthor.ID, *reread.AuthorID, "AuthorID should be updated to the new author")

	// The denormalized display object must track the FK, not lag behind it.
	require.NotNil(t, reread.Author, "embedded Author should be populated")
	require.Equal(t, newAuthor.ID, reread.Author.ID,
		"persisted Author.ID is stale — still points at the old author")
	require.Equal(t, "New Author", reread.Author.Name,
		"persisted Author.Name is stale — read paths would display the old author name")
}
