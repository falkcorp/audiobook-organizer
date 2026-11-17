// file: internal/itunes/service/importer_enrich_retired_test.go
// version: 1.0.0
// guid: e1cf22ab-aed6-46a8-b67a-9f9695f4ac01
// last-edited: 2026-11-17

package itunesservice

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

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
	fetcher.failErr = fmt.Errorf("book %q: %w", "Synthetic Title", metafetch.ErrBookRetired)
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
