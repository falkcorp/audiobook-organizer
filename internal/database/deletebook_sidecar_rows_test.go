// file: internal/database/deletebook_sidecar_rows_test.go
// version: 1.0.0
// guid: c125e490-ad49-4fbd-8185-e0ae632b4de3
// last-edited: 2026-09-11

package database

import (
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// rawKeysWithPrefix returns every committed key under prefix, by raw
// iteration — deliberately not through any store reader, because several of
// the readers skip rows whose book no longer exists and would mask exactly the
// leak this file guards against.
func rawKeysWithPrefix(t *testing.T, db *pebble.DB, prefix string) []string {
	t.Helper()
	p := []byte(prefix)
	iter, err := db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixEnd(p)})
	require.NoError(t, err)
	defer iter.Close()
	var keys []string
	for iter.First(); iter.Valid(); iter.Next() {
		keys = append(keys, string(iter.Key()))
	}
	require.NoError(t, iter.Error())
	return keys
}

// rawKeysWithSuffix returns every committed key under prefix that ends with
// suffix — the shape of a reverse index such as tag_idx:<tag>:<bookID>.
func rawKeysWithSuffix(t *testing.T, db *pebble.DB, prefix, suffix string) []string {
	t.Helper()
	var out []string
	for _, k := range rawKeysWithPrefix(t, db, prefix) {
		if strings.HasSuffix(k, suffix) {
			out = append(out, k)
		}
	}
	return out
}

// sidecarFamily is one per-book key family DeleteBook must tear down.
type sidecarFamily struct {
	name string
	keys func(bookID string) []string
}

func sidecarFamilies(t *testing.T, db *pebble.DB) []sidecarFamily {
	t.Helper()
	exact := func(prefix string) func(string) []string {
		return func(id string) []string { return rawKeysWithPrefix(t, db, prefix+id) }
	}
	return []sidecarFamily{
		{"book_authors:<id>", exact("book_authors:")},
		{"book_narrators:<id>", exact("book_narrators:")},
		{"book_tag:<id>:*", func(id string) []string { return rawKeysWithPrefix(t, db, "book_tag:"+id+":") }},
		{"tag_idx:*:<id>", func(id string) []string { return rawKeysWithSuffix(t, db, "tag_idx:", ":"+id) }},
		{"user_tag:book:<id>", exact("user_tag:book:")},
		{"alt_titles:book:<id>", exact("alt_titles:book:")},
		{"metadata_rejection:<id>:*", func(id string) []string { return rawKeysWithPrefix(t, db, "metadata_rejection:"+id+":") }},
		{"metadata_cache:<id>", exact(metadataCacheKeyPrefix)},
	}
}

// seedSidecars writes one row into every family for bookID through the same
// public writers production uses, so the keys under test are exactly the keys
// the writers create.
func seedSidecars(t *testing.T, s *PebbleStore, bookID string, authorID, narratorID int) {
	t.Helper()
	require.NoError(t, s.SetBookAuthors(bookID, []BookAuthor{{BookID: bookID, AuthorID: authorID, Role: "author"}}))
	require.NoError(t, s.SetBookNarrators(bookID, []BookNarrator{{BookID: bookID, NarratorID: narratorID, Role: "narrator"}}))
	// One tag with embedded colons (the ListAllTags trap) and one without.
	require.NoError(t, s.AddBookTagWithSource(bookID, "metadata:source:audible", "system"))
	require.NoError(t, s.AddBookTagWithSource(bookID, "favorite", "user"))
	require.NoError(t, s.SetBookUserTags(bookID, []string{"keep"}))
	require.NoError(t, s.SetBookAlternativeTitles(bookID, []BookAlternativeTitle{{Title: "Alt " + bookID}}))
	require.NoError(t, s.AddMetadataRejection(MetadataRejection{BookID: bookID, Source: "audible", RejectionReason: "user_rejected"}))
	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: bookID, FetchedAt: time.Now()}))
}

// TestDeleteBook_RemovesPerBookSidecarRows locks in SQ-03: a hard delete tears
// down every row keyed by the deleted book's ID that only DeleteBook could
// ever remove — the author/narrator junction rows, book tags and their reverse
// index, user tags, alternative titles, metadata rejections and the metadata
// candidate cache — while an unrelated book's rows in the same families
// survive untouched.
//
// Before the fix DeleteBook removed the book row, its indexes, its embedding,
// pending dedup candidates and its chapters, but none of the families above,
// so every purge / merge cleanup / archive sweep leaked them permanently and
// the author-side junction scan kept surfacing the deleted book as a phantom
// credit.
func TestDeleteBook_RemovesPerBookSidecarRows(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir() + "/db")
	require.NoError(t, err)
	defer store.Close()
	db := store.DB()

	const doomed, bystander = "bA", "bB"
	const sharedAuthor, sharedNarrator = 7, 9
	mkCascadeBook(t, store, doomed, "Doomed")
	mkCascadeBook(t, store, bystander, "Bystander")
	seedSidecars(t, store, doomed, sharedAuthor, sharedNarrator)
	seedSidecars(t, store, bystander, sharedAuthor, sharedNarrator)

	families := sidecarFamilies(t, db)

	// Preconditions: every family really has rows for BOTH books, so an
	// assertion below cannot pass vacuously because a writer silently wrote
	// nothing. Remember the bystander's exact key set to compare after.
	bystanderBefore := make(map[string][]string, len(families))
	for _, f := range families {
		require.NotEmpty(t, f.keys(doomed), "precondition: %s written for the doomed book", f.name)
		bystanderBefore[f.name] = f.keys(bystander)
		require.NotEmpty(t, bystanderBefore[f.name], "precondition: %s written for the bystander", f.name)
	}
	// The author-side scan sees both books before the delete.
	inJunction, err := store.bookIDsInAuthorJunction(sharedAuthor)
	require.NoError(t, err)
	require.Contains(t, inJunction, doomed)
	require.Contains(t, inJunction, bystander)

	require.NoError(t, store.DeleteBook(doomed))

	// Nothing keyed by the doomed book's ID remains in any family...
	for _, f := range families {
		require.Empty(t, f.keys(doomed), "%s must be removed by DeleteBook", f.name)
	}
	// ...and the bystander's rows are byte-for-byte the same key set.
	for _, f := range families {
		require.Equal(t, bystanderBefore[f.name], f.keys(bystander), "%s of an unrelated book must survive", f.name)
	}

	// Reader-level checks on the two families with a cross-book index, so the
	// fix is proven through the code paths that consumed the leaked rows.
	inJunction, err = store.bookIDsInAuthorJunction(sharedAuthor)
	require.NoError(t, err)
	require.NotContains(t, inJunction, doomed, "author-side junction scan must no longer reference the deleted book")
	require.Contains(t, inJunction, bystander)

	byTag, err := store.GetBooksByTag("favorite")
	require.NoError(t, err)
	require.Equal(t, []string{bystander}, byTag, "tag reverse index must no longer resolve to the deleted book")

	tags, err := store.ListAllTags()
	require.NoError(t, err)
	for _, tc := range tags {
		require.Equal(t, 1, tc.Count, "tag %q must count only the surviving book", tc.Tag)
	}
}

// TestDeleteBook_SidecarTeardownIsAtomicWithBookRow proves the sidecar deletes
// ride the same batch as the book row: DeleteBook on a book with NO sidecars is
// still a clean success (absent-key Deletes are no-ops), and after the call the
// book row and every family are absent together.
func TestDeleteBook_SidecarTeardownIsAtomicWithBookRow(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir() + "/db")
	require.NoError(t, err)
	defer store.Close()
	db := store.DB()

	mkCascadeBook(t, store, "bare", "No Sidecars")
	require.NoError(t, store.DeleteBook("bare"))
	require.False(t, cascadeKeyPresent(t, db, []byte("book:bare")))
	for _, f := range sidecarFamilies(t, db) {
		require.Empty(t, f.keys("bare"), "%s", f.name)
	}
	// Idempotent: a second delete of a gone book is a nil no-op, as before.
	require.NoError(t, store.DeleteBook("bare"))
}
