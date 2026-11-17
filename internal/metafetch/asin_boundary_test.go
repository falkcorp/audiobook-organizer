// file: internal/metafetch/asin_boundary_test.go
// version: 1.1.0
// guid: c3a7f1e8-52d4-4b09-9e61-8d0b4a27f5c3
// last-edited: 2026-11-16

package metafetch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The 2026-11-16 candidate-fetch run failed 814 of 814 books because every
// ten-character WORD of a title was looked up as an ASIN. These pin the
// boundary: what is asked of a provider, what is remembered, and that the
// identity hash did not move.

const (
	batASIN     = "B0GOTHAM99" // fake, in the ASIN grammar
	batBadASIN  = `{"error":{"code":"BAD_REQUEST","message":"Bad ASIN"}}`
	batTitle    = "Gotham Batmobiles Year One"
	batAuthor   = "Bruce Wayne"
	batWordOnly = "Batmobiles"
)

// asinRecorder is an asinLookupOverride that records the (provider, asin)
// pairs that reached it and answers with fn.
type asinRecorder struct {
	mu   sync.Mutex
	seen [][2]string
	fn   func(provider, asin string) (*metadata.BookMetadata, error)
}

func (r *asinRecorder) lookup(_ context.Context, provider, asin string) (*metadata.BookMetadata, error) {
	r.mu.Lock()
	r.seen = append(r.seen, [2]string{provider, asin})
	r.mu.Unlock()
	if r.fn == nil {
		return nil, nil
	}
	return r.fn(provider, asin)
}

func (r *asinRecorder) asins() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seen))
	for _, s := range r.seen {
		out = append(out, s[1])
	}
	return out
}

// batmanSearch runs one search for a book titled title whose stored ASIN is
// storedASIN ("" for none), through an Audible title source that answers
// a runtime of its own, so the only ASIN lookup a search can make is the
// book's (the pooled result needs no runtime enrichment).
func batmanSearch(t *testing.T, title, query, storedASIN string, rec *asinRecorder) (*Service, *database.Book) {
	t.Helper()
	book := &database.Book{ID: "b1", Title: title}
	if storedASIN != "" {
		a := storedASIN
		book.ASIN = &a
	}
	d := 36000
	book.Duration = &d
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible",
		answer: func(string, string) []metadata.BookMetadata {
			return []metadata.BookMetadata{{Title: "Year One", Author: batAuthor, ASIN: "B0GOTHAM98", DurationSec: 36000}}
		}}
	audnexus := &fakeProvider{id: metadata.SourceIDAudnexus, name: "Audnexus (Audible)"}
	svc := fanoutHarness(t, book, audible, audnexus)
	svc.asinLookupOverride = rec.lookup
	_, err := svc.SearchMetadataForBookWithOptions("b1", query, batAuthor, "", "", SearchOptions{})
	require.NoError(t, err)
	return svc, book
}

// A title word is never an ASIN: the search asks no provider for it.
func TestSearch_TitleWordIsNeverLookedUpAsAnASIN(t *testing.T) {
	for _, tc := range []struct{ name, title, query string }{
		{"word inside the title", batTitle, batTitle},
		{"the word is the whole query", batTitle, batWordOnly},
		{"upper-case word", "GOTHAMCITY Knights", "GOTHAMCITY Knights"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &asinRecorder{}
			batmanSearch(t, tc.title, tc.query, "", rec)
			assert.Empty(t, rec.asins(), "no ASIN lookup may be sent for a word (the 800-book failure)")
		})
	}
}

// A real ASIN inside the query is still looked up, as written.
func TestSearch_AnASINInTheQueryIsStillLookedUp(t *testing.T) {
	rec := &asinRecorder{}
	batmanSearch(t, batTitle, "Year One "+batASIN, "", rec)
	require.NotEmpty(t, rec.asins())
	for _, a := range rec.asins() {
		assert.Equal(t, batASIN, a)
	}
}

// An identifier pasted as the whole query ("Search again") still gets its
// direct lookup, in either case and in the ten-digit form; a one-word title
// does not.
func TestSearch_WholeQueryThatIsAnASINIsStillLookedUp(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{batASIN, batASIN},
		{"b0gotham99", batASIN},
		{"0123456789", "0123456789"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := &asinRecorder{}
			batmanSearch(t, batTitle, tc.query, "", rec)
			require.NotEmpty(t, rec.asins())
			for _, a := range rec.asins() {
				assert.Equal(t, tc.want, a)
			}
		})
	}
	for _, word := range []string{"Batmobiles", "BATMOBILES", "batmobiles"} {
		t.Run(word, func(t *testing.T) {
			rec := &asinRecorder{}
			batmanSearch(t, batTitle, word, "", rec)
			assert.Empty(t, rec.asins())
		})
	}
}

// The book's stored ASIN is an identifier, so its case is normalised before it
// is sent (Audnexus 400s a lower-case ASIN); a stored value that is not an
// ASIN is not sent at all.
func TestSearch_StoredASINIsNormalisedOrNotSent(t *testing.T) {
	t.Run("lower case", func(t *testing.T) {
		rec := &asinRecorder{}
		batmanSearch(t, batTitle, batTitle, " b0gotham99 ", rec)
		require.NotEmpty(t, rec.asins())
		for _, a := range rec.asins() {
			assert.Equal(t, batASIN, a)
		}
	})
	t.Run("a stored word", func(t *testing.T) {
		rec := &asinRecorder{}
		batmanSearch(t, batTitle, batTitle, batWordOnly, rec)
		assert.Empty(t, rec.asins())
	})
}

// The identity hash must not move: looksLikeASIN still decides which stored
// ASINs count toward the fingerprint, so a stored value that was hashed before
// is hashed now, and every cached row stays valid (an identity_stale storm
// otherwise). The lookup grammar is applied at the request, not here.
func TestSearchFingerprint_StillHashesAnyTenCharacterStoredASIN(t *testing.T) {
	svc := &Service{}
	for _, stored := range []string{batASIN, "b0gotham99", batWordOnly} {
		book := &database.Book{ID: "b1", Title: batTitle, ASIN: &stored}
		in := svc.resolveSearchInputs(book, batTitle, batAuthor, "")
		assert.Equal(t, stored, in.asin, "the stored value %q is part of the identity as before", stored)
		withASIN := in.fingerprint(batTitle)

		in.asin = ""
		assert.NotEqual(t, withASIN, in.fingerprint(batTitle), "%q still changes the fingerprint", stored)
	}
	// And a nine-character value still does not.
	short := "B0GOTHAM9"
	book := &database.Book{ID: "b1", Title: batTitle, ASIN: &short}
	assert.Empty(t, svc.resolveSearchInputs(book, batTitle, batAuthor, "").asin)
}

// cachedLookupASIN is the single door to the providers: a value outside the
// grammar is answered "nothing" without a request, a row or an error.
func TestCachedLookupASIN_InvalidASINAsksNothingAndStoresNothing(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	rec := &asinRecorder{}
	svc.asinLookupOverride = rec.lookup
	p := fanoutParams{ctx: context.Background(), bookID: "book-a", identity: "v1:book-a"}

	for _, bad := range []string{batWordOnly, "batmobiles", "B0GOTHAM9", "", "B0GOTHAM99!"} {
		got, err := svc.cachedLookupASIN(p, metadata.SourceIDAudnexus, bad)
		require.NoError(t, err, "%q", bad)
		assert.Nil(t, got, "%q", bad)
	}
	assert.Empty(t, rec.asins(), "no request for an impossible ASIN")
	assert.Empty(t, kv.keys(database.PCachePrefix), "no negative row keyed by garbage")
}

// A lower-case ASIN is asked, and cached, under its canonical form.
func TestCachedLookupASIN_CanonicalFormIsAskedAndKeyed(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	rec := &asinRecorder{fn: func(_, a string) (*metadata.BookMetadata, error) {
		return &metadata.BookMetadata{Title: "Year One", ASIN: a}, nil
	}}
	svc.asinLookupOverride = rec.lookup
	p := fanoutParams{ctx: context.Background(), bookID: "book-a", identity: "v1:book-a"}

	got, err := svc.cachedLookupASIN(p, metadata.SourceIDAudnexus, "b0gotham99")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []string{batASIN}, rec.asins())
	// The upper-case spelling is the same question: replayed, not re-asked.
	_, err = svc.cachedLookupASIN(p, metadata.SourceIDAudnexus, batASIN)
	require.NoError(t, err)
	assert.Equal(t, []string{batASIN}, rec.asins(), "one spelling, one live call")
	assert.Len(t, kv.keys(database.PCachePrefix), 1)
}

// The provider's 400 "Bad ASIN" is a permanent answer about that ASIN: it is
// returned as "nothing", written ONCE as a negative row, and replayed without
// a second request. It is not an error, so the book is not left unanswered.
func TestCachedLookupASIN_BadASIN400IsRememberedPerASIN(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	rec := &asinRecorder{fn: func(_, _ string) (*metadata.BookMetadata, error) {
		return nil, &metadata.ProviderStatusError{Provider: metadata.SourceIDAudnexus, Status: 400, Body: batBadASIN}
	}}
	svc.asinLookupOverride = rec.lookup
	p := fanoutParams{ctx: context.Background(), bookID: "book-a", identity: "v1:book-a"}

	got, err := svc.cachedLookupASIN(p, metadata.SourceIDAudnexus, batASIN)
	require.NoError(t, err, "a refused ASIN is an answer, not a failure")
	assert.Nil(t, got)
	assert.Len(t, kv.keys(database.PCachePrefix), 1, "one negative row")

	// A second book, the same ASIN: replayed.
	p2 := fanoutParams{ctx: context.Background(), bookID: "book-b", identity: "v1:book-b"}
	got, err = svc.cachedLookupASIN(p2, metadata.SourceIDAudnexus, batASIN)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Equal(t, []string{batASIN}, rec.asins(), "the refusal is remembered: 1 request for 2 books")

	// A different ASIN is a different question.
	_, err = svc.cachedLookupASIN(p, metadata.SourceIDAudnexus, "B0GOTHAM97")
	require.NoError(t, err)
	assert.Len(t, rec.asins(), 2)
}

// A 400 that is not the provider's own JSON (a proxy's error page) says
// nothing about the ASIN: it stays a failure and is never cached.
func TestCachedLookupASIN_NonJSON400StaysAFailure(t *testing.T) {
	withSharedCache(t, true)
	svc, kv := newPCacheService(t)
	var calls atomic.Int64
	svc.asinLookupOverride = func(context.Context, string, string) (*metadata.BookMetadata, error) {
		calls.Add(1)
		return nil, &metadata.ProviderStatusError{Provider: metadata.SourceIDAudnexus, Status: 400, Body: "<html>Bad Request</html>"}
	}
	p := fanoutParams{ctx: context.Background(), bookID: "book-a", identity: "v1:book-a"}
	_, err := svc.cachedLookupASIN(p, metadata.SourceIDAudnexus, batASIN)
	require.Error(t, err)
	_, err = svc.cachedLookupASIN(p, metadata.SourceIDAudnexus, batASIN)
	require.Error(t, err)
	assert.EqualValues(t, 2, calls.Load())
	assert.Empty(t, kv.keys(database.PCachePrefix))
}

// Only Audnexus's 400 is read as "Bad ASIN": the same status and body from any
// other provider is a failure, and neither it nor Audnexus's JSON 400 about
// something else is cached as a permanent answer.
func TestCachedLookupASIN_OnlyAudnexusBadASINIsAnAnswer(t *testing.T) {
	withSharedCache(t, true)
	for _, tc := range []struct {
		name, provider, body string
	}{
		{"audible", metadata.SourceIDAudible, batBadASIN},
		{"audnexus, other message", metadata.SourceIDAudnexus, `{"error":{"code":"BAD_REQUEST","message":"Bad region"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, kv := newPCacheService(t)
			var calls atomic.Int64
			svc.asinLookupOverride = func(context.Context, string, string) (*metadata.BookMetadata, error) {
				calls.Add(1)
				return nil, &metadata.ProviderStatusError{Provider: tc.provider, Status: 400, Body: tc.body}
			}
			p := fanoutParams{ctx: context.Background(), bookID: "book-a", identity: "v1:book-a"}
			_, err := svc.cachedLookupASIN(p, tc.provider, batASIN)
			require.Error(t, err)
			_, err = svc.cachedLookupASIN(p, tc.provider, batASIN)
			require.Error(t, err)
			assert.EqualValues(t, 2, calls.Load(), "not remembered")
			assert.Empty(t, kv.keys(database.PCachePrefix))
		})
	}
}
