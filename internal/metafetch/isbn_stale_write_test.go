// file: internal/metafetch/isbn_stale_write_test.go
// version: 1.0.0
// guid: 7e41c0b2-93d5-4a6f-b8e2-15c9d4f0a371
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/require"
)

// isbnHookSource answers every search with the same results and runs
// hooks[n] on the n-th search call, i.e. while EnrichBookISBN is between its
// read of the book and its write -- the provider round-trip window.
type isbnHookSource struct {
	name    string
	results []metadata.BookMetadata
	calls   int
	hooks   []func()
}

func (s *isbnHookSource) fire() {
	if s.calls < len(s.hooks) && s.hooks[s.calls] != nil {
		s.hooks[s.calls]()
	}
	s.calls++
}

func (s *isbnHookSource) Name() string { return s.name }
func (s *isbnHookSource) SearchByTitle(_ context.Context, _ string) ([]metadata.BookMetadata, error) {
	s.fire()
	return s.results, nil
}
func (s *isbnHookSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	s.fire()
	return s.results, nil
}

// A field another writer commits while the enrichment waits on a provider
// must survive the enrichment's own write. The old code wrote back the whole
// row it read before the provider calls, reverting both edits below.
func TestEnrichBookISBN_KeepsFieldsWrittenDuringProviderCalls(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	book, err := store.CreateBook(&database.Book{
		Title: "Neuromancer", FilePath: "/library/neuromancer.m4b", Format: "m4b",
		Description: new("old description"), Publisher: new("Old Publisher"),
	})
	require.NoError(t, err)

	set := func(apply func(*database.Book)) func() {
		return func() {
			_, err := store.ModifyBook(book.ID, func(b *database.Book) error { apply(b); return nil })
			require.NoError(t, err)
		}
	}
	src := &isbnHookSource{name: "Audible", results: isbnHit(), hooks: []func(){
		set(func(b *database.Book) { b.Description = new("new description") }),
		set(func(b *database.Book) { b.Publisher = new("New Publisher") }),
	}}
	svc := NewISBNService(store, []metadata.MetadataSource{src})

	found, err := svc.EnrichBookISBN(context.Background(), book.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.GreaterOrEqual(t, src.calls, 2, "both hooks must have fired")

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ISBN13)
	require.Equal(t, "9780441569595", *got.ISBN13)
	require.NotNil(t, got.ASIN)
	require.Equal(t, "B000SEGUDE", *got.ASIN)
	require.NotNil(t, got.Description)
	require.Equal(t, "new description", *got.Description, "concurrent description edit reverted")
	require.NotNil(t, got.Publisher)
	require.Equal(t, "New Publisher", *got.Publisher, "concurrent publisher edit reverted")
}

// An identifier another writer filled while the providers were searched is
// not overwritten with the provider's value.
func TestEnrichBookISBN_LeavesIdentifierFilledDuringSearch(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	book, err := store.CreateBook(&database.Book{Title: "Neuromancer", FilePath: "/library/n2.m4b", Format: "m4b"})
	require.NoError(t, err)

	src := &isbnHookSource{name: "Audible", results: isbnHit(), hooks: []func(){
		func() {
			_, err := store.ModifyBook(book.ID, func(b *database.Book) error { b.ISBN13 = new("9999999999999"); return nil })
			require.NoError(t, err)
		},
	}}
	svc := NewISBNService(store, []metadata.MetadataSource{src})
	_, err = svc.EnrichBookISBN(context.Background(), book.ID)
	require.NoError(t, err)

	got, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ISBN13)
	require.Equal(t, "9999999999999", *got.ISBN13)
}
