// file: internal/metafetch/browse_search_test.go
// version: 1.0.0
// guid: 8e2d4b17-c95a-4f03-b6e1-2a7c9d0f5b48
// last-edited: 2026-10-07

package metafetch

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func browseFixture(t *testing.T) (*Service, string, *authorRecordingSource) {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	book, err := store.CreateBook(&database.Book{Title: "The First Sample", FilePath: "/library/x/The First Sample.m4b", Format: "m4b"})
	require.NoError(t, err)
	rec := &authorRecordingSource{}
	svc := NewService(store)
	svc.overrideSources = []metadata.MetadataSource{rec}
	return svc, book.ID, rec
}

func phelpsBook(asin, title string) metadata.BookMetadata {
	return metadata.BookMetadata{ASIN: asin, Title: title, Author: "Joseph Phelps", Narrator: "Sample Reader",
		CoverURL: "https://example.invalid/" + asin + ".jpg", DurationSec: 36000}
}

func resultASINs(rs []MetadataCandidate) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ASIN
	}
	return out
}

// Author only: every catalog entry for the author comes back -- titles that
// share no word with the book's title included -- and no provider is asked
// the book's own title (the defect: an empty title became the book's title,
// and every other book scored 0 and was dropped).
func TestBrowseSearch_AuthorOnlyReturnsEveryCatalogBook(t *testing.T) {
	svc, id, rec := browseFixture(t)
	var gotAuthor, gotTitle string
	src := BrowseSources{
		Catalog: func(author, title string) ([]metadata.BookMetadata, error) {
			gotAuthor, gotTitle = author, title
			return []metadata.BookMetadata{
				phelpsBook("B0PHELPS01", "The First Sample"),
				phelpsBook("B0PHELPS02", "Unrelated Words Entirely"),
				phelpsBook("B0PHELPS03", "Another Tale"),
			}, nil
		},
		AuthorListing: func(context.Context, string, string) ([]metadata.BookMetadata, error) {
			t.Fatal("the live listing is only for an author the catalog has nothing for")
			return nil, nil
		},
	}
	resp, err := svc.BrowseSearch(context.Background(), id, BrowseQuery{Author: "joseph phelps"}, src)
	require.NoError(t, err)
	assert.Equal(t, "joseph phelps", gotAuthor)
	assert.Empty(t, gotTitle)
	assert.ElementsMatch(t, []string{"B0PHELPS01", "B0PHELPS02", "B0PHELPS03"}, resultASINs(resp.Results))
	for _, c := range resp.Results {
		assert.True(t, c.FromCatalog)
		assert.Equal(t, "Audible", c.Source, "apply provenance keys on the provider name")
		assert.Positive(t, c.Score)
		require.NotNil(t, c.ScoreBreakdown)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Empty(t, rec.titles, "no provider title search for an author-only browse")
}

// The catalog has nothing for the author: the live author listing answers.
func TestBrowseSearch_EmptyCatalogFallsBackToLiveListing(t *testing.T) {
	svc, id, _ := browseFixture(t)
	called := false
	src := BrowseSources{
		Catalog: func(string, string) ([]metadata.BookMetadata, error) { return nil, nil },
		AuthorListing: func(_ context.Context, author, _ string) ([]metadata.BookMetadata, error) {
			called = true
			assert.Equal(t, "Joseph Phelps", author)
			return []metadata.BookMetadata{phelpsBook("B0PHELPS01", "A"), phelpsBook("B0PHELPS02", "B")}, nil
		},
	}
	resp, err := svc.BrowseSearch(context.Background(), id, BrowseQuery{Author: "Joseph Phelps"}, src)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Len(t, resp.Results, 2)
	assert.False(t, resp.Results[0].FromCatalog)
}

// Title + author: the catalog's matches and the providers' answers are
// pooled, deduplicated by ASIN (the catalog's copy kept), and ranked by score.
func TestBrowseSearch_TitleAndAuthorMergesAndDedupesByASIN(t *testing.T) {
	svc, id, _ := browseFixture(t)
	svc.overrideSources = []metadata.MetadataSource{fakeSource{name: "Audible", results: []metadata.BookMetadata{
		phelpsBook("B0PHELPS01", "The First Sample"), // also in the catalog
		phelpsBook("B0LIVEONLY", "The First Sample: Live Edition"),
	}}}
	src := BrowseSources{Catalog: func(_, title string) ([]metadata.BookMetadata, error) {
		assert.Equal(t, "first sample", title)
		return []metadata.BookMetadata{phelpsBook("B0PHELPS01", "The First Sample")}, nil
	}}
	resp, err := svc.BrowseSearch(context.Background(), id, BrowseQuery{Title: "first sample", Author: "Joseph Phelps"}, src)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"B0PHELPS01", "B0LIVEONLY"}, resultASINs(resp.Results))
	for _, c := range resp.Results {
		if c.ASIN == "B0PHELPS01" {
			assert.True(t, c.FromCatalog, "the catalog's copy is the one kept")
		}
	}
	assert.GreaterOrEqual(t, resp.Results[0].Score, resp.Results[1].Score)
}

// CatalogOnly asks no provider; neither title nor author is a 400-class error.
func TestBrowseSearch_CatalogOnlyAndEmpty(t *testing.T) {
	svc, id, rec := browseFixture(t)
	src := BrowseSources{
		Catalog: func(string, string) ([]metadata.BookMetadata, error) { return nil, nil },
		AuthorListing: func(context.Context, string, string) ([]metadata.BookMetadata, error) {
			t.Fatal("catalog_only must not ask the provider")
			return nil, nil
		},
	}
	resp, err := svc.BrowseSearch(context.Background(), id, BrowseQuery{Title: "x", Author: "Joseph Phelps", CatalogOnly: true}, src)
	require.NoError(t, err)
	assert.Empty(t, resp.Results)
	rec.mu.Lock()
	assert.Empty(t, rec.titles)
	rec.mu.Unlock()

	_, err = svc.BrowseSearch(context.Background(), id, BrowseQuery{}, src)
	assert.ErrorIs(t, err, ErrBrowseEmpty)
}
