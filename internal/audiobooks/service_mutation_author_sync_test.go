// file: internal/audiobooks/service_mutation_author_sync_test.go
// version: 1.3.0
// guid: 3f9a0c71-6d24-4e83-b1a7-5c8e9f0a2d13
// last-edited: 2026-09-05

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

// TestUpdateAudiobook_SyncsJoinTableOnIDChange guards the download-fix Part 3
// corollary: the organizer now resolves an applied author from the book_authors
// JOIN table (durable) in preference to the scalar AuthorID (a rescan reverts
// it). The name-edit path always wrote both, but an author_id-only edit wrote
// only the scalar and left the join stale — so trusting the join would misfile
// the book under the OLD author on the next organize. The ID path must now write
// the join too. An ID-based set is single-author by definition, so the join
// becomes exactly that one author at Position 0.
func TestUpdateAudiobook_SyncsJoinTableOnIDChange(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)

	oldAuthor, err := ps.CreateAuthor("Old Author")
	require.NoError(t, err)
	oldCoauthor, err := ps.CreateAuthor("Old Coauthor")
	require.NoError(t, err)
	newAuthor, err := ps.CreateAuthor("New Author")
	require.NoError(t, err)

	book, err := ps.CreateBook(&database.Book{
		Title:    "Test Book",
		AuthorID: &oldAuthor.ID,
		Author:   &database.Author{ID: oldAuthor.ID, Name: "Old Author"},
	})
	require.NoError(t, err)
	// Seed a TWO-row join (primary + co-author). The seed must have more than one
	// row so this test can tell replace-semantics from an upsert: the comment on
	// the fix claims the join "becomes exactly that one author", which is only
	// true if the write REPLACES the whole set and drops the stale co-author. A
	// single-row seed would pass either way and would not pin the claim.
	require.NoError(t, ps.SetBookAuthors(book.ID, []database.BookAuthor{
		{BookID: book.ID, AuthorID: oldAuthor.ID, Role: "author", Position: 0},
		{BookID: book.ID, AuthorID: oldCoauthor.ID, Role: "co-author", Position: 1},
	}))

	svc := NewAudiobookService(ps)

	_, err = svc.UpdateAudiobook(context.Background(), book.ID, &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{
			Book: &database.Book{AuthorID: &newAuthor.ID},
		},
	})
	require.NoError(t, err)

	joins, err := ps.GetBookAuthors(book.ID)
	require.NoError(t, err)
	require.Len(t, joins, 1,
		"an ID-based author set is single-author and REPLACES the join; the stale co-author must be gone, not upserted alongside")
	require.Equal(t, newAuthor.ID, joins[0].AuthorID,
		"join still points at the OLD author — the organizer would misfile under it on the next scan")
}

// TestUpdateAudiobook_UnrelatedEditPreservesMultiAuthorJoin guards the other
// side of the same fix: the join sync fires ONLY when the client actually sends
// author_id. An unrelated edit (here, a title change) carries the pre-existing
// scalar AuthorID through the payload, but must NOT rewrite — and thereby
// collapse — a multi-author join down to a single author.
func TestUpdateAudiobook_UnrelatedEditPreservesMultiAuthorJoin(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)

	primary, err := ps.CreateAuthor("Primary Author")
	require.NoError(t, err)
	coauthor, err := ps.CreateAuthor("Co Author")
	require.NoError(t, err)

	book, err := ps.CreateBook(&database.Book{
		Title:    "Test Book",
		AuthorID: &primary.ID,
		Author:   &database.Author{ID: primary.ID, Name: "Primary Author"},
	})
	require.NoError(t, err)
	require.NoError(t, ps.SetBookAuthors(book.ID, []database.BookAuthor{
		{BookID: book.ID, AuthorID: primary.ID, Role: "author", Position: 0},
		{BookID: book.ID, AuthorID: coauthor.ID, Role: "co-author", Position: 1},
	}))

	svc := NewAudiobookService(ps)

	// Title-only edit: no author_id, no author_name.
	_, err = svc.UpdateAudiobook(context.Background(), book.ID, &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{
			Book: &database.Book{Title: "Renamed Book"},
		},
	})
	require.NoError(t, err)

	joins, err := ps.GetBookAuthors(book.ID)
	require.NoError(t, err)
	require.Len(t, joins, 2, "an unrelated edit must not collapse the multi-author join")
}

// TestUpdateAudiobook_SyncsDenormalizedSeriesOnIDChange is the Series twin of
// the author regression above — changing a book's series by series_id must keep
// the embedded Series display object in sync with the FK, not persist the old
// series' name.
func TestUpdateAudiobook_SyncsDenormalizedSeriesOnIDChange(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)

	oldSeries, err := ps.CreateSeries("Old Series", nil)
	require.NoError(t, err)
	newSeries, err := ps.CreateSeries("New Series", nil)
	require.NoError(t, err)

	book, err := ps.CreateBook(&database.Book{
		Title:    "Test Book",
		SeriesID: &oldSeries.ID,
		Series:   &database.Series{ID: oldSeries.ID, Name: "Old Series"},
	})
	require.NoError(t, err)

	svc := NewAudiobookService(ps)

	_, err = svc.UpdateAudiobook(context.Background(), book.ID, &UpdateAudiobookRequest{
		Updates: &AudiobookUpdate{
			Book: &database.Book{SeriesID: &newSeries.ID},
		},
	})
	require.NoError(t, err)

	reread, err := ps.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, reread)

	require.NotNil(t, reread.SeriesID)
	require.Equal(t, newSeries.ID, *reread.SeriesID, "SeriesID should be updated to the new series")

	require.NotNil(t, reread.Series, "embedded Series should be populated")
	require.Equal(t, newSeries.ID, reread.Series.ID,
		"persisted Series.ID is stale — still points at the old series")
	require.Equal(t, "New Series", reread.Series.Name,
		"persisted Series.Name is stale — read paths would display the old series name")
}
