// file: internal/itunes/service/importer_enrich_retired_test.go
// version: 1.0.3
// guid: e1cf22ab-aed6-46a8-b67a-9f9695f4ac01
// last-edited: 2026-11-17

package itunesservice

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The import's enrichment is an unattended caller of the metadata fetch: it
// states that (metafetch.Unattended), and a book the fetch refuses as retired
// is a skip. A skip is not a provider failure, so it must not feed the
// rate-limit breaker: a run over more retired books than the breaker's
// threshold still reaches every book.
func TestEnrichImportedBooks_RetiredBooksAreSkipsAndDoNotTripTheBreaker(t *testing.T) {
	const totalBooks = 50 // far above enrichBreakerThreshold

	_, store := buildEnrichFixture(t, totalBooks)
	fetcher := newFakeMetadataFetcher(func(string) bool { return true })
	fetcher.failErr = fmt.Errorf("book %q: %w", "The Long Halloween", metafetch.ErrBookRetired)
	imp := &Importer{store: store, mfs: fetcher, enrichConcurrencyOverride: 4}

	imp.enrichImportedBooks(context.Background(), fixtureBookIDs(totalBooks), &itunesImportStatus{}, logger.New("test-enrich-retired"))

	require.Equal(t, totalBooks, fetcher.callCount(),
		"a retired book is not a provider failure: it must not trip the breaker and cancel the rest of the run")
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	for i, in := range fetcher.intents {
		require.Equal(t, metafetch.Unattended, in, "call %d: the import picks no book one by one", i)
	}
}

// Control: the same run where every failure is an ordinary provider miss does
// trip the breaker, so the test above passes because of the retired
// classification and not because the fixture can never trip it.
func TestEnrichImportedBooks_ControlProviderMissesStillTripTheBreaker(t *testing.T) {
	const totalBooks = 50

	_, store := buildEnrichFixture(t, totalBooks)
	fetcher := newFakeMetadataFetcher(func(string) bool { return true })
	imp := &Importer{store: store, mfs: fetcher, enrichConcurrencyOverride: 4}

	imp.enrichImportedBooks(context.Background(), fixtureBookIDs(totalBooks), &itunesImportStatus{}, logger.New("test-enrich-miss"))

	require.Less(t, fetcher.callCount(), totalBooks/2)
}

// retireThenSucceedFetcher answers like a fetch that committed while the book
// was live, then lets a merge or trash land before it returns: the window
// between the guarded commit inside the fetch and the importer's own author
// join.
type retireThenSucceedFetcher struct {
	retire   func(id string)
	authorID int
}

func (f *retireThenSucceedFetcher) FetchMetadataForBook(_ context.Context, id string, _ metafetch.FetchIntent) (*metafetch.FetchMetadataResponse, error) {
	if f.retire != nil {
		f.retire(id)
	}
	return &metafetch.FetchMetadataResponse{
		Message: "metadata fetched and applied",
		Book:    &database.Book{ID: id, AuthorID: &f.authorID},
		Source:  "fake",
	}, nil
}

// enrichJoinFixture is one imported book with a single credit, the artist iTunes
// supplied (the enrichment replaces a join of one), and the author the fetch
// found.
func enrichJoinFixture(t *testing.T) (store *database.PebbleStore, bookID string, placeholderID, foundID int) {
	t.Helper()
	store = newRegressionStore(t)
	placeholder, err := store.CreateAuthor("Lucius Fox")
	require.NoError(t, err)
	found, err := store.CreateAuthor("Alfred Pennyworth")
	require.NoError(t, err)
	imported, src := "imported", "/mnt/itunes/Library.xml"
	b, err := store.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: "/lib/a/The Long Halloween.m4b",
		LibraryState: &imported, ITunesImportSource: &src})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: placeholder.ID, Role: "author", Position: 0}}))
	return store, b.ID, placeholder.ID, found.ID
}

func enrichedJoin(t *testing.T, store *database.PebbleStore, id string) []int {
	t.Helper()
	credits, err := store.GetBookAuthors(id)
	require.NoError(t, err)
	var ids []int
	for _, c := range credits {
		ids = append(ids, c.AuthorID)
	}
	return ids
}

// The fetch's guarded commit refuses a retired book, but the author join the
// importer writes AFTER it was unguarded: a book merged away or trashed in
// between had its credits replaced.
func TestEnrichImportedBooks_AuthorJoinIsNotWrittenOntoABookRetiredAfterTheCommit(t *testing.T) {
	for name, retire := range map[string]func(*database.Book){
		"soft_deleted": func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes },
		"merged_into":  func(b *database.Book) { into := "survivor-book-id"; b.MergedIntoBookID = &into },
	} {
		t.Run(name, func(t *testing.T) {
			store, id, placeholderID, foundID := enrichJoinFixture(t)
			fetcher := &retireThenSucceedFetcher{authorID: foundID, retire: func(id string) {
				_, err := store.ModifyBook(id, func(b *database.Book) error { retire(b); return nil })
				require.NoError(t, err)
			}}
			imp := &Importer{store: store, mfs: fetcher, enrichConcurrencyOverride: 1}
			var out strings.Builder // one worker, so no concurrent writes
			log := logger.FromSlog(slog.New(slog.NewTextHandler(&out, nil)))

			imp.enrichImportedBooks(context.Background(), []string{id}, &itunesImportStatus{}, log)

			require.Equal(t, []int{placeholderID}, enrichedJoin(t, store, id),
				"a retired book's author credits must be left as they were")
			// The run counts it as a skip, not as an enriched book.
			require.Contains(t, out.String(), "0 books enriched, 1 retired books skipped")
		})
	}
}

// Control: the same run on a live book replaces the placeholder credit, so the
// assertion above is the refusal and not a join the enrichment never writes.
func TestEnrichImportedBooks_ControlLiveBookGetsTheFetchedAuthorJoin(t *testing.T) {
	store, id, _, foundID := enrichJoinFixture(t)
	imp := &Importer{store: store, mfs: &retireThenSucceedFetcher{authorID: foundID}, enrichConcurrencyOverride: 1}

	imp.enrichImportedBooks(context.Background(), []string{id}, &itunesImportStatus{}, logger.New("test-enrich-join-live"))

	require.Equal(t, []int{foundID}, enrichedJoin(t, store, id))
}
