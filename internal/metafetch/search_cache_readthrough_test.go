// file: internal/metafetch/search_cache_readthrough_test.go
// version: 1.0.0
// guid: 7de1cf53-bf29-420d-bfad-f87e0fe4db96
// last-edited: 2026-10-07

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

const readThroughASIN = "B000OWNED1"

// flakyProvider is a fakeProvider whose title searches fail while fail is set.
type flakyProvider struct {
	*fakeProvider
	fail atomic.Bool
}

func (f *flakyProvider) SearchByTitle(ctx context.Context, title string) ([]metadata.BookMetadata, error) {
	if f.fail.Load() {
		f.ask(title, "")
		return nil, errors.New("503 from the provider")
	}
	return f.fakeProvider.SearchByTitle(ctx, title)
}

func (f *flakyProvider) SearchByTitleAndAuthor(ctx context.Context, title, author string) ([]metadata.BookMetadata, error) {
	if f.fail.Load() {
		f.ask(title, author)
		return nil, errors.New("503 from the provider")
	}
	return f.fakeProvider.SearchByTitleAndAuthor(ctx, title, author)
}

// readThroughHarness serves a 10h book that carries its own ASIN, searched
// through an Audible title source whose answers lack that ASIN's runtime, so
// every search also runs the own-ASIN lookup. asinCalls counts the lookups
// that reached the (overridden) provider; asinAnswer decides what they return.
func readThroughHarness(t *testing.T) (svc *Service, book *database.Book, audible *flakyProvider, asinCalls *atomic.Int32, asinAnswer *atomic.Value) {
	t.Helper()
	own := readThroughASIN
	book = reacherBook(36000)
	book.ASIN = &own
	audible = &flakyProvider{fakeProvider: &fakeProvider{id: metadata.SourceIDAudible, name: "Audible",
		answer: func(title, _ string) []metadata.BookMetadata {
			return []metadata.BookMetadata{{Title: "A Wanted Man", Author: "Lee Child", ASIN: "B000OTHER9"}}
		}}}
	audnexus := &fakeProvider{id: metadata.SourceIDAudnexus, name: "Audnexus (Audible)"}
	svc = fanoutHarness(t, book, audible, audnexus)
	asinCalls = &atomic.Int32{}
	asinAnswer = &atomic.Value{}
	asinAnswer.Store(asinResult{rec: &metadata.BookMetadata{Title: "A Wanted Man", Author: "Lee Child", ASIN: own, DurationSec: 36000}})
	svc.asinLookupOverride = func(_ context.Context, _ string, asin string) (*metadata.BookMetadata, error) {
		asinCalls.Add(1)
		r := asinAnswer.Load().(asinResult)
		if r.rec == nil {
			return nil, r.err
		}
		cp := *r.rec
		return &cp, r.err
	}
	return svc, book, audible, asinCalls, asinAnswer
}

type asinResult struct {
	rec *metadata.BookMetadata
	err error
}

func search(t *testing.T, svc *Service, query string, opts SearchOptions) *SearchMetadataResponse {
	t.Helper()
	resp, err := svc.SearchMetadataForBookWithOptions("b1", query, "Lee Child", "", "", opts)
	require.NoError(t, err)
	return resp
}

// The owner's question, as a test: a second identical search makes ZERO
// provider calls -- no title search and no ASIN lookup. Before the ASIN
// lookups were cached, every repeat still re-asked Audible/Audnexus.
func TestSearchCache_RepeatSearchMakesNoProviderCalls(t *testing.T) {
	svc, _, audible, asinCalls, _ := readThroughHarness(t)

	first := search(t, svc, reacherTitle, SearchOptions{})
	require.Positive(t, audible.callCount(), "the first search asks the provider")
	require.Positive(t, asinCalls.Load(), "the first search looks the book's ASIN up")
	titleBefore, asinBefore := audible.callCount(), asinCalls.Load()

	second := search(t, svc, reacherTitle, SearchOptions{})
	assert.Equal(t, titleBefore, audible.callCount(), "a repeat search must not re-ask the title source")
	assert.Equal(t, asinBefore, asinCalls.Load(), "a repeat search must not re-look-up the ASIN")
	require.Len(t, second.Results, len(first.Results), "the replay returns the same candidates")
	for i := range first.Results {
		assert.Equal(t, first.Results[i].ASIN, second.Results[i].ASIN)
	}
}

// "Search again" with a changed query is a different question: it misses the
// cache and asks the provider.
func TestSearchCache_ChangedQueryAsksProvider(t *testing.T) {
	svc, _, audible, _, _ := readThroughHarness(t)
	search(t, svc, reacherTitle, SearchOptions{})
	before := audible.callCount()

	search(t, svc, "Never Go Back", SearchOptions{})
	assert.Greater(t, audible.callCount(), before, "a changed query must reach the provider")
}

// A forced refresh skips the READ (both title variants and the ASIN lookup)
// but still WRITES the fresh answer, replacing the row.
func TestSearchCache_ForcedRefreshBypassesReadButWrites(t *testing.T) {
	svc, book, audible, asinCalls, asinAnswer := readThroughHarness(t)
	search(t, svc, reacherTitle, SearchOptions{})
	titleBefore, asinBefore := audible.callCount(), asinCalls.Load()

	asinAnswer.Store(asinResult{rec: &metadata.BookMetadata{Title: "A Wanted Man (refreshed)", Author: "Lee Child", ASIN: readThroughASIN, DurationSec: 36000}})
	search(t, svc, reacherTitle, SearchOptions{BypassFetchCache: true})
	assert.Greater(t, audible.callCount(), titleBefore, "forced refresh re-asks the title source")
	assert.Greater(t, asinCalls.Load(), asinBefore, "forced refresh re-looks-up the ASIN")

	rec := cachedASINRow(t, svc, book, metadata.SourceIDAudible)
	require.NotNil(t, rec, "the forced answer is written")
	assert.Equal(t, "A Wanted Man (refreshed)", rec.Title, "the forced answer replaces the row")

	// And the next plain search replays the refreshed row without a call.
	asinAfter := asinCalls.Load()
	search(t, svc, reacherTitle, SearchOptions{})
	assert.Equal(t, asinAfter, asinCalls.Load())
}

// Errors are never cached: a failed title search or ASIN lookup leaves no row,
// so the next search asks again instead of replaying the failure.
func TestSearchCache_ErrorsAreNotCached(t *testing.T) {
	svc, book, audible, asinCalls, asinAnswer := readThroughHarness(t)
	audible.fail.Store(true)
	asinAnswer.Store(asinResult{err: errors.New("503 from the store")})
	_, _ = svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
	require.Positive(t, audible.callCount())
	assert.Nil(t, cachedASINRow(t, svc, book, metadata.SourceIDAudible), "a failed ASIN lookup writes no row")
	assert.Nil(t, cachedASINRow(t, svc, book, metadata.SourceIDAudnexus), "a failed ASIN lookup writes no row")

	titleBefore, asinBefore := audible.callCount(), asinCalls.Load()
	_, _ = svc.SearchMetadataForBookWithOptions("b1", reacherTitle, "Lee Child", "", "", SearchOptions{})
	assert.Greater(t, audible.callCount(), titleBefore, "a failed title search is asked again")
	assert.Greater(t, asinCalls.Load(), asinBefore, "a failed ASIN lookup is asked again")
}

// A forced refresh that comes back empty ("no such ASIN") must not erase the
// good row an earlier search cached -- the empty-refetch damage shape.
func TestSearchCache_EmptyForcedRefreshKeepsGoodRow(t *testing.T) {
	svc, book, _, _, asinAnswer := readThroughHarness(t)
	search(t, svc, reacherTitle, SearchOptions{})
	require.NotNil(t, cachedASINRow(t, svc, book, metadata.SourceIDAudible))

	asinAnswer.Store(asinResult{})
	search(t, svc, reacherTitle, SearchOptions{BypassFetchCache: true})
	rec := cachedASINRow(t, svc, book, metadata.SourceIDAudible)
	require.NotNil(t, rec, "an empty refetch must not erase the cached record")
	assert.Equal(t, "A Wanted Man", rec.Title)
}

func cachedASINRow(t *testing.T, svc *Service, book *database.Book, providerID string) *metadata.BookMetadata {
	t.Helper()
	entry, err := database.GetCachedMetadataFetch(svc.db, "b1", asinCacheSource(providerID, readThroughASIN), svc.fetchCacheIdentity(book))
	require.NoError(t, err)
	if entry == nil {
		return nil
	}
	var rs []metadata.BookMetadata
	require.NoError(t, json.Unmarshal(entry.Results, &rs))
	require.Len(t, rs, 1)
	return &rs[0]
}
