// file: internal/metafetch/cleaned_hint_roundtrip_test.go
// version: 1.1.0
// guid: a90831ba-362e-4ef9-b611-020e8c9d8009
// last-edited: 2026-10-06

package metafetch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The batch fetch sends and hashes SearchAuthorHint(live author); the apply
// gate and the batch verdict recompute the hash from the book's author forms.
// When SearchAuthorHint rewrites a credit ("zzJane Example" -> "Jane
// Example") both sides must hash the same text, or every such
// book is identity_stale at apply and re-asked every batch run. Made-up
// authors with the 2026-10-05 no-match census's shapes.
func TestCleanedAuthorHint_RoundTripsThroughTheIdentityChecks(t *testing.T) {
	for i, author := range []string{
		"zzJane Example",
		"GraphicAudio [Jane Example]",
		"Some Title_copy1",
		"Some Long Book_10-02",
		"Jane Example", // unchanged by cleaning
	} {
		t.Run(author, func(t *testing.T) {
			f := newVerdictFixture(t)
			a, err := f.store.CreateAuthor(author)
			require.NoError(t, err, "fixture: author row %q", author)
			b, err := f.store.CreateBook(&database.Book{Title: "Census Book", FilePath: "/lib/rt/" + string(rune('a'+i)) + ".m4b", AuthorID: &a.ID})
			require.NoError(t, err)
			f.mfs.SetOverrideSources([]metadata.MetadataSource{&verdictSource{name: "Src",
				results: []metadata.BookMetadata{{Title: "Census Book", Author: "Someone"}}}})

			book := f.book(b.ID)
			live, err := database.LiveBookAuthorNames(f.store, book)
			require.NoError(t, err)
			require.NotEmpty(t, live)
			hint := SearchAuthorHint(live[0]) // what fetchCandidateForBook sends and hashes
			_, err = f.mfs.FetchAndCacheLimited(context.Background(), nil, b.ID, book.Title, hint, "", "", SearchOptions{})
			require.NoError(t, err)

			entry, verdict, _ := f.mfs.CachedBatchVerdict(f.book(b.ID), book.Title, hint)
			require.Equal(t, BatchVerdictFreshCandidates, verdict, "a row just fetched must be served, not re-asked")
			require.NoError(t, f.mfs.ValidateCachedIdentityForBook(entry, f.book(b.ID), live), "the apply gate must accept the row")
		})
	}
}

// A row main wrote before the hint was cleaned hashed the RAW author. It was
// fetched for the book's current author, so the apply gate still accepts it.
func TestCleanedAuthorHint_RowHashedWithTheRawAuthorStillValidates(t *testing.T) {
	f := newVerdictFixture(t)
	a, err := f.store.CreateAuthor("zzJane Example")
	require.NoError(t, err)
	b, err := f.store.CreateBook(&database.Book{Title: "Onward", FilePath: "/lib/rt/raw.m4b", AuthorID: &a.ID})
	require.NoError(t, err)
	book := f.book(b.ID)
	live, err := database.LiveBookAuthorNames(f.store, book)
	require.NoError(t, err)
	entry := &MetadataCandidateCache{BookID: b.ID, SourceHash: BatchSourceHash(b.ID, book.Title, live[0])}
	require.NoError(t, f.mfs.ValidateCachedIdentityForBook(entry, book, live))
}
