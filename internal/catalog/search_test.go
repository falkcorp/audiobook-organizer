// file: internal/catalog/search_test.go
// version: 1.0.0
// guid: 3f0c9a51-6b7e-4d2a-9c18-5e4f7a2b8d63
// last-edited: 2026-10-07

package catalog

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func titled(asin, title string, authors ...string) metadata.CatalogProduct {
	p := metadata.CatalogProduct{ASIN: asin, Title: title, Language: "english", RuntimeMin: 600,
		ReleaseDate: "2019-04-02", CoverURL: "https://example.invalid/" + asin + ".jpg",
		Narrators: []metadata.CatalogContributor{{Name: "Sample Reader"}},
		Series:    []metadata.CatalogProductSeries{{Title: "Sample Saga", Sequence: "2"}}}
	for _, a := range authors {
		p.Authors = append(p.Authors, metadata.CatalogContributor{Name: a})
	}
	return p
}

func seedCatalog(t *testing.T, st *database.CatalogStore, harvest string, prods ...metadata.CatalogProduct) []string {
	t.Helper()
	items := make([]database.CatalogUpsert, len(prods))
	for i, p := range prods {
		items[i] = database.CatalogUpsert{Entry: BuildEntry(p, metadata.SourceIDAudible, "us")}
	}
	res, err := st.UpsertEntries(items, HarvestKey(harvest))
	require.NoError(t, err)
	return res.IDs
}

func asinsOf(rs []metadata.BookMetadata) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ASIN
	}
	return out
}

// An author-only search returns every live entry credited to the author,
// whatever its title; a partial title narrows it; a part of the name finds
// the author too; a co-authored entry appears once; a stale entry and other
// authors' entries never.
func TestSearch_AuthorAndPartialTitle(t *testing.T) {
	st, _ := openCatalog(t)
	ids := seedCatalog(t, st, "Joseph Phelps",
		titled("B0PHELPS01", "The First Sample", "Joseph Phelps"),
		titled("B0PHELPS02", "Second Sample Story", "Joseph Phelps"),
		titled("B0PHELPS03", "Another Tale", "Joseph Phelps", "Ann Coauthor"),
		titled("B0PHELPS04", "Gone From Listing", "Joseph Phelps"),
	)
	seedCatalog(t, st, "Other Writer", titled("B0OTHER001", "The First Sample", "Other Writer"))
	// A complete re-harvest that no longer lists the fourth title marks it stale.
	seen := map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}
	_, err := st.MarkUnseen(HarvestKey("Joseph Phelps"), seen)
	require.NoError(t, err)

	got, err := Search(st, "joseph phelps", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"B0PHELPS01", "B0PHELPS02", "B0PHELPS03"}, asinsOf(got))

	got, err = Search(st, "Phelps", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"B0PHELPS01", "B0PHELPS02", "B0PHELPS03"}, asinsOf(got), "a part of the name matches")

	got, err = Search(st, "Joseph Phelps", "sample")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"B0PHELPS01", "B0PHELPS02"}, asinsOf(got), "partial title, case-insensitive")

	got, err = Search(st, "Ann Coauthor", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"B0PHELPS03"}, asinsOf(got))

	got, err = Search(st, "jo", "")
	require.NoError(t, err)
	assert.Empty(t, got, "a two-letter fragment searches nothing")

	// The entry comes back with what a candidate shows.
	got, err = Search(st, "Joseph Phelps", "Another")
	require.NoError(t, err)
	require.Len(t, got, 1)
	m := got[0]
	assert.Equal(t, "Joseph Phelps, Ann Coauthor", m.Author)
	assert.Equal(t, "Sample Reader", m.Narrator)
	assert.Equal(t, "Sample Saga", m.Series)
	assert.Equal(t, "2", m.SeriesPosition)
	assert.Equal(t, 2019, m.PublishYear)
	assert.Equal(t, 36000, m.DurationSec)
	assert.NotEmpty(t, m.CoverURL)
}

type listingLister struct {
	pages map[int]metadata.AuthorPage
	asked []int
}

func (l *listingLister) ProviderID() string { return metadata.SourceIDAudible }
func (l *listingLister) ListByAuthor(_ context.Context, _ string, page, _ int) (metadata.AuthorPage, error) {
	l.asked = append(l.asked, page)
	return l.pages[page], nil
}
func (l *listingLister) LookupProduct(context.Context, string) (*metadata.CatalogProduct, error) {
	return nil, nil
}

// The live listing walks pages until the provider's total, keeps the
// configured language and the partial title.
func TestLiveAuthorListing_PagesLanguageAndTitle(t *testing.T) {
	german := titled("B0DE000001", "Ein Sample", "Joseph Phelps")
	german.Language = "german"
	l := &listingLister{pages: map[int]metadata.AuthorPage{
		0: {Products: []metadata.CatalogProduct{titled("B0PHELPS01", "The First Sample", "Joseph Phelps"), german}, TotalResults: 3},
		1: {Products: []metadata.CatalogProduct{titled("B0PHELPS02", "Another Tale", "Joseph Phelps")}, TotalResults: 3},
	}}
	got, err := LiveAuthorListing(context.Background(), l, "Joseph Phelps", "", "english", 4)
	require.NoError(t, err)
	assert.Equal(t, []string{"B0PHELPS01", "B0PHELPS02"}, asinsOf(got))
	assert.Equal(t, []int{0, 1}, l.asked, "stops at the provider's total")

	l.asked = nil
	got, err = LiveAuthorListing(context.Background(), l, "Joseph Phelps", "tale", "english", 4)
	require.NoError(t, err)
	assert.Equal(t, []string{"B0PHELPS02"}, asinsOf(got))
}
