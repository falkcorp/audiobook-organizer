// file: internal/metafetch/catalog_stage_test.go
// version: 1.9.0
// guid: 8a2d5f61-4c93-4e0b-b7d8-3f6a1e9c5207
// last-edited: 2026-10-12

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Tests of the local catalog blocking stage's wiring into the search. The
// matcher is a fake (the real one, internal/catalog.MatchBook, is a layer
// above this package; its own tests and the end-to-end scenario live there).
// Every fixture is synthetic.

type fakeCatalogMatcher struct {
	mu     sync.Mutex
	asked  []metadata.CatalogQuestion
	answer metadata.CatalogMatch
	err    error
	calls  atomic.Int64
	// onMatch runs inside MatchBook, before it answers (a test cancels the
	// search's context there, as a client disconnect would mid-lookup).
	onMatch func()
	// panicWith, when set, is panicked with from MatchBook.
	panicWith any
}

func (f *fakeCatalogMatcher) MatchBook(_ context.Context, q metadata.CatalogQuestion) (metadata.CatalogMatch, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.asked = append(f.asked, q)
	f.mu.Unlock()
	if f.onMatch != nil {
		f.onMatch()
	}
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.answer, f.err
}

// withCatalogStage sets metadata_fetch.catalog_stage for one test. Call it
// AFTER building the harness: fanoutHarness resets metadata_fetch to its
// defaults (stage off).
func withCatalogStage(t *testing.T, on bool) {
	t.Helper()
	prev := config.AppConfig.MetadataFetch
	t.Cleanup(func() { config.AppConfig.MetadataFetch = prev })
	config.AppConfig.MetadataFetch.CatalogStage = on
}

const (
	stageTitle  = "The Synthetic Lantern Keeper"
	stageAuthor = "Author 07"
)

func lanternAnswer(asin string) metadata.BookMetadata {
	return metadata.BookMetadata{Title: stageTitle, Author: stageAuthor, ASIN: asin, CoverURL: "c", Narrator: "Narrator 03", DurationSec: 36000}
}

// stageHarness serves one book, titled stageTitle and credited to stageAuthor
// (then a second credit), through a single Audible fake that answers with
// providerAnswers.
func stageHarness(t *testing.T, providerAnswers ...metadata.BookMetadata) (*Service, *fakeProvider) {
	t.Helper()
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata { return providerAnswers }}
	id1, id2, id3 := 1, 2, 3
	book := &database.Book{ID: "b1", Title: stageTitle, AuthorID: &id1}
	svc := fanoutHarness(t, book, audible)
	mock := svc.db.(*database.MockStore)
	names := map[int]string{1: stageAuthor, 2: "Author 08", 3: "Unknown Author"}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) { return &database.Author{ID: id, Name: names[id]}, nil }
	mock.GetBookAuthorsFunc = func(string) ([]database.BookAuthor, error) {
		// Out of position order on purpose: the live list sorts by position.
		return []database.BookAuthor{{BookID: "b1", AuthorID: id3, Position: 2}, {BookID: "b1", AuthorID: id2, Position: 1}, {BookID: "b1", AuthorID: id1, Position: 0}}, nil
	}
	return svc, audible
}

func searchStage(t *testing.T, svc *Service, ctx context.Context, opts SearchOptions) *SearchMetadataResponse {
	t.Helper()
	resp, err := svc.searchMetadataForBook(ctx, nil, "b1", "", "", "", "", opts)
	require.NoError(t, err)
	return resp
}

// catalogAnswer is what the fake matcher returns: one candidate that is also
// the provider's (same ASIN) and one only the catalog has.
func catalogAnswer() metadata.CatalogMatch {
	dup := lanternAnswer("B0PROV0001")
	only := metadata.BookMetadata{Title: stageTitle + " Returns", Author: stageAuthor, ASIN: "B0CATA0002", CoverURL: "c", Narrator: "Narrator 03", DurationSec: 36000}
	return metadata.CatalogMatch{
		Candidates: []metadata.BookMetadata{dup, only},
		Origins: []metadata.CatalogOrigin{
			{Stage: metadata.CatalogStageAuthor, CreditPosition: 0},
			{Stage: metadata.CatalogStageAuthor, CreditPosition: 1, ManualOnly: true},
		},
		Stage: metadata.CatalogStageAuthor, OversizeBlocks: 2, HomonymSkipped: 1,
	}
}

// With the switch off the matcher is never asked and the response is the one
// a service without a matcher gives, call for call.
func TestCatalogStageOffMatchesToday(t *testing.T) {
	plain, plainProv := stageHarness(t, lanternAnswer("B0PROV0001"))
	want := searchStage(t, plain, context.Background(), SearchOptions{})

	off, offProv := stageHarness(t, lanternAnswer("B0PROV0001"))
	fm := &fakeCatalogMatcher{answer: catalogAnswer()}
	off.SetCatalogMatcher(fm)
	got := searchStage(t, off, context.Background(), SearchOptions{})

	assert.Zero(t, fm.calls.Load(), "the stage is off: the matcher is not asked")
	assert.Equal(t, plainProv.callCount(), offProv.callCount())
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	assert.JSONEq(t, string(wantJSON), string(gotJSON), "response identical to a service with no matcher")
	for _, c := range got.Results {
		assert.NotEqual(t, metadata.CatalogSourceName, c.Source)
	}

	// On, but no matcher installed: the same.
	bare, bareProv := stageHarness(t, lanternAnswer("B0PROV0001"))
	withCatalogStage(t, true)
	got = searchStage(t, bare, context.Background(), SearchOptions{})
	gotJSON, _ = json.Marshal(got)
	assert.JSONEq(t, string(wantJSON), string(gotJSON))
	assert.Equal(t, plainProv.callCount(), bareProv.callCount())
}

// With the switch on the catalog's candidates join the pool as review-only
// candidates tagged with their stage, merged after the providers' (a
// duplicate ASIN stays the provider's), and the provider fan-out makes the
// same calls it made without the stage.
func TestCatalogStageOn_AddsReviewOnlyCandidatesAfterProviders(t *testing.T) {
	offSvc, offProv := stageHarness(t, lanternAnswer("B0PROV0001"))
	_ = searchStage(t, offSvc, context.Background(), SearchOptions{})
	baseline := offProv.callCount()

	svc, prov := stageHarness(t, lanternAnswer("B0PROV0001"))
	withCatalogStage(t, true)
	fm := &fakeCatalogMatcher{answer: catalogAnswer()}
	svc.SetCatalogMatcher(fm)
	stats := &CatalogStageStats{}
	resp := searchStage(t, svc, WithCatalogStageStats(context.Background(), stats), SearchOptions{})

	require.EqualValues(t, 1, fm.calls.Load())
	q := fm.asked[0]
	assert.Equal(t, []string{stageAuthor, "Author 08", ""}, q.Credits, "the live credits in position order; the placeholder keeps its place, as a credit naming no block")
	assert.Contains(t, q.Titles, stageTitle)
	assert.Equal(t, baseline, prov.callCount(), "the stage removes no provider call and adds none")

	var catalog, providers []MetadataCandidate
	for _, c := range resp.Results {
		if c.Source == metadata.CatalogSourceName {
			catalog = append(catalog, c)
		} else {
			providers = append(providers, c)
		}
	}
	require.Len(t, catalog, 1, "the duplicate ASIN is the provider's, not the catalog's")
	assert.Equal(t, "B0CATA0002", catalog[0].ASIN)
	assert.Equal(t, metadata.CatalogStageAuthor, catalog[0].Stage)
	assert.Equal(t, 1, catalog[0].StageCreditPosition)
	assert.True(t, catalog[0].ManualOnly)
	assert.True(t, catalog[0].FromCatalog)
	assert.True(t, IsReviewOnlyCandidateSource(catalog[0].Source))
	require.Len(t, providers, 1)
	assert.Equal(t, "B0PROV0001", providers[0].ASIN)
	assert.Empty(t, providers[0].Stage)

	assert.NotContains(t, resp.SourcesTried, metadata.CatalogSourceName)
	assert.NotContains(t, resp.SourcesAnswered, metadata.CatalogSourceName)
	assert.NotContains(t, resp.SourcesAsked, metadata.CatalogSourceName)

	assert.EqualValues(t, 2, stats.CatalogMatched.Load(), "the stage found both, before the pool dropped the provider's duplicate")
	assert.EqualValues(t, 1, stats.CatalogCandidates.Load())
	assert.EqualValues(t, 1, stats.ProviderCandidates.Load())
	assert.EqualValues(t, 1, stats.BooksWithCatalog.Load())
	assert.EqualValues(t, 2, stats.OversizeBlocks.Load())
	assert.EqualValues(t, 1, stats.HomonymSkipped.Load())
}

// A fallback provider's own search (OnlySources naming only Open Library /
// Google Books) does not run the stage; a chain re-ask does.
func TestCatalogStage_SkippedForFallbackOnlySearches(t *testing.T) {
	ol := &fakeProvider{id: metadata.SourceIDOpenLibrary, name: "Open Library"}
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible"}
	id1 := 1
	book := &database.Book{ID: "b1", Title: stageTitle, AuthorID: &id1}
	svc := fanoutHarness(t, book, audible, ol)
	svc.db.(*database.MockStore).GetAuthorByIDFunc = func(id int) (*database.Author, error) { return &database.Author{ID: id, Name: stageAuthor}, nil }
	withCatalogStage(t, true)
	fm := &fakeCatalogMatcher{}
	svc.SetCatalogMatcher(fm)

	searchStage(t, svc, context.Background(), SearchOptions{OnlySources: []string{"Open Library"}})
	assert.Zero(t, fm.calls.Load(), "a fallback-only search")
	searchStage(t, svc, context.Background(), SearchOptions{OnlySources: []string{"Audible"}})
	assert.EqualValues(t, 1, fm.calls.Load(), "a chain re-ask")
	searchStage(t, svc, context.Background(), SearchOptions{})
	assert.EqualValues(t, 2, fm.calls.Load(), "an unrestricted search")
}

// The stage only adds candidates: a matcher error is logged and the search is
// the provider's alone.
func TestCatalogStage_MatcherErrorIsNotFatal(t *testing.T) {
	svc, _ := stageHarness(t, lanternAnswer("B0PROV0001"))
	withCatalogStage(t, true)
	svc.SetCatalogMatcher(&fakeCatalogMatcher{err: errors.New("catalog store unreadable")})
	resp := searchStage(t, svc, context.Background(), SearchOptions{})
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "B0PROV0001", resp.Results[0].ASIN)
}

// A book with no ASIN and no credit has nothing to ask: the matcher is not.
func TestCatalogStage_NoQuestionNoCall(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible"}
	svc := fanoutHarness(t, &database.Book{ID: "b1", Title: stageTitle}, audible)
	withCatalogStage(t, true)
	fm := &fakeCatalogMatcher{}
	svc.SetCatalogMatcher(fm)
	searchStage(t, svc, context.Background(), SearchOptions{})
	assert.Zero(t, fm.calls.Load())

	asin := "B0OWNASIN1"
	svc = fanoutHarness(t, &database.Book{ID: "b1", Title: stageTitle, ASIN: &asin}, audible)
	withCatalogStage(t, true) // the harness reset it
	svc.SetCatalogMatcher(fm)
	searchStage(t, svc, context.Background(), SearchOptions{})
	require.EqualValues(t, 1, fm.calls.Load(), "the book's own ASIN is a question without a credit")
	assert.Equal(t, asin, fm.asked[0].ASIN)
}

// The search author stands in only when the book has no usable live credit,
// and never goes in front of real ones: it would change which block is tried
// first and every reported credit position.
func TestCatalogCredits_SearchAuthorOnlyWhenNoLiveCredit(t *testing.T) {
	svc, _ := stageHarness(t)
	book := &database.Book{ID: "b1", Title: stageTitle}
	id1 := 1
	book.AuthorID = &id1
	got := svc.catalogCredits(book, "Typed Author")
	assert.Equal(t, []string{stageAuthor, "Author 08", ""}, got, "live credits win over the search author")

	mock := svc.db.(*database.MockStore)
	mock.GetBookAuthorsFunc = func(string) ([]database.BookAuthor, error) { return nil, nil }
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) { return &database.Author{ID: id, Name: "Unknown Author"}, nil }
	assert.Equal(t, []string{"Typed Author"}, svc.catalogCredits(book, "Typed Author"), "a placeholder-only list is no credit")
	assert.Nil(t, svc.catalogCredits(&database.Book{ID: "b2"}, ""), "no credit, no search author")
}

// A provider outage is not made a settled look by catalog candidates: with
// every provider erroring, a search that holds only the catalog's candidates
// is still ErrNoSourceAnswered, writes nothing, and leaves the book's earlier
// provider candidates exactly as they were.
func TestCatalogStage_ProviderOutageWithCatalogMatchIsNotCached(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: stageTitle, FilePath: "/lib/x/lantern.m4b"})
	require.NoError(t, err)
	a, err := f.store.CreateAuthor(stageAuthor)
	require.NoError(t, err)
	require.NoError(t, f.store.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: a.ID, Role: "author"}}))
	old := time.Now().Add(-2 * database.MetadataCacheTTL).UTC().Truncate(time.Second)
	prior := json.RawMessage(`{"title":"` + stageTitle + `","author":"` + stageAuthor + `","asin":"B0PROV0001","source":"Audible","score":0.97}`)
	require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: b.ID, FetchedAt: old, SourceHash: "h", SearchFingerprint: "f", Candidates: []json.RawMessage{prior},
	}))

	f.mfs.SetOverrideSources([]metadata.MetadataSource{
		&verdictSource{name: "SrcA", byAuthorErr: errors.New("503"), byTitleErr: errors.New("503")},
		&verdictSource{name: "SrcB", byAuthorErr: metadata.ErrProviderThrottled, byTitleErr: metadata.ErrProviderThrottled},
	})
	fm := &fakeCatalogMatcher{answer: catalogAnswer()}
	f.mfs.SetCatalogMatcher(fm)
	withCatalogStage(t, true)

	_, resp, err := f.mfs.FetchAndCacheWithResponse(context.Background(), nil, b.ID, b.Title, "", "", "", SearchOptions{})
	require.ErrorIs(t, err, ErrNoSourceAnswered)
	require.EqualValues(t, 1, fm.calls.Load(), "fixture: the stage ran and found candidates")
	require.NotNil(t, resp)
	hasCatalog := false
	for _, c := range resp.Results {
		hasCatalog = hasCatalog || c.Source == metadata.CatalogSourceName
	}
	require.True(t, hasCatalog, "fixture: the response did hold catalog candidates")

	stored, gerr := f.store.GetMetadataCache(b.ID)
	require.NoError(t, gerr)
	require.NotNil(t, stored)
	require.True(t, stored.FetchedAt.Equal(old), "the outage dated the row (%v)", stored.FetchedAt)
	require.Len(t, stored.Candidates, 1)
	require.JSONEq(t, string(prior), string(stored.Candidates[0]), "the earlier provider candidate was replaced")
}

// A plain replace with no caller ranker stores a review-only candidate (the
// local catalog's, Open Library's, Google Books') after a provider's, whatever
// the search ordered, so slot 0 -- which the review lane and bulk apply read --
// is the provider's.
func TestRankCandidatesByTier_NilRankPutsReviewOnlyLast(t *testing.T) {
	in := []MetadataCandidate{
		{ASIN: "B0CATA0002", Source: metadata.CatalogSourceName, Score: 0.99},
		{ASIN: "B0PROV0001", Source: "Audible", Score: 0.80},
		{ASIN: "B0PROV0002", Source: "Audible", Score: 0.70},
	}
	got := rankCandidatesByTier(in, nil)
	var order []string
	for _, c := range got {
		order = append(order, c.ASIN)
	}
	assert.Equal(t, []string{"B0PROV0001", "B0PROV0002", "B0CATA0002"}, order, "provider candidates first, in the search's own order")
	assert.Equal(t, "B0CATA0002", in[0].ASIN, "the caller's slice is not reordered")
}

// Catalog candidates are not a provider's answer to the cache: when every
// provider answers "nothing" and the catalog matches, the book's earlier
// provider candidates are kept with their own age, the empty look is recorded,
// and the catalog's candidates join after them (the 2026-09-08 empty-refetch
// rule, through the catalog's side door).
func TestCatalogStage_ProvidersEmptyKeepsEarlierProviderCandidates(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: stageTitle, FilePath: "/lib/x/lantern.m4b"})
	require.NoError(t, err)
	a, err := f.store.CreateAuthor(stageAuthor)
	require.NoError(t, err)
	require.NoError(t, f.store.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: a.ID, Role: "author"}}))

	src := &verdictSource{name: "SrcA", results: []metadata.BookMetadata{lanternAnswer("B0PROV0001")}}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{src})
	first, err := f.mfs.FetchAndCacheLimited(context.Background(), nil, b.ID, b.Title, "", "", "", SearchOptions{BypassFetchCache: true})
	require.NoError(t, err)
	require.Len(t, first.Candidates, 1)

	src.results = nil // the provider now has nothing
	fm := &fakeCatalogMatcher{answer: catalogAnswer()}
	f.mfs.SetCatalogMatcher(fm)
	withCatalogStage(t, true)
	_, resp, err := f.mfs.FetchAndCacheWithResponse(context.Background(), nil, b.ID, b.Title, "", "", "", SearchOptions{BypassFetchCache: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, fm.calls.Load())
	require.NotEmpty(t, resp.Results, "fixture: the catalog's candidates are in the response")

	stored, gerr := f.store.GetMetadataCache(b.ID)
	require.NoError(t, gerr)
	require.NotNil(t, stored)
	var sources []string
	for _, raw := range stored.Candidates {
		var c MetadataCandidate
		require.NoError(t, json.Unmarshal(raw, &c))
		sources = append(sources, c.Source+"/"+c.ASIN)
	}
	require.NotEmpty(t, sources)
	assert.Equal(t, "SrcA/B0PROV0001", sources[0], "the earlier provider candidate stays in slot 0")
	assert.Contains(t, sources, metadata.CatalogSourceName+"/B0CATA0002")
	assert.True(t, stored.FetchedAt.Equal(first.FetchedAt), "FetchedAt dates the provider candidates (%v, was %v)", stored.FetchedAt, first.FetchedAt)
	require.NotNil(t, stored.LastEmptyFetchAt, "the empty look is recorded")
	assert.Contains(t, stored.EmptyAnswers, "SrcA")
}

// With no earlier row, a providers-empty search still records the empty look
// and stores the catalog's candidates after nothing.
func TestCatalogStage_ProvidersEmptyNoPriorRowStoresCatalogCandidates(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: stageTitle, FilePath: "/lib/x/lantern.m4b"})
	require.NoError(t, err)
	a, err := f.store.CreateAuthor(stageAuthor)
	require.NoError(t, err)
	require.NoError(t, f.store.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: a.ID, Role: "author"}}))
	f.mfs.SetOverrideSources([]metadata.MetadataSource{&verdictSource{name: "SrcA"}})
	f.mfs.SetCatalogMatcher(&fakeCatalogMatcher{answer: catalogAnswer()})
	withCatalogStage(t, true)

	entry, err := f.mfs.FetchAndCacheLimited(context.Background(), nil, b.ID, b.Title, "", "", "", SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, entry.Candidates)
	require.NotNil(t, entry.LastEmptyFetchAt)
	assert.Contains(t, entry.EmptyAnswers, "SrcA")
}

// A review-only refusal can now be the local catalog's doing, so the
// user-facing text names it instead of listing only the two web sources.
func TestReviewOnlyRefusalMessageNamesTheCatalog(t *testing.T) {
	assert.Contains(t, ErrReviewOnlyCandidatesNotApplied.Error(), "the local catalog")
}

// The stage runs on its own goroutine beside the fan-out, where an escaped
// panic is not recovered by anything above it and would end the process. A
// matcher that panics is a stage with no answer: the search is the
// providers' alone, and the next search still runs the stage.
func TestCatalogStage_PanickingMatcherDoesNotCrashTheSearch(t *testing.T) {
	svc, _ := stageHarness(t, lanternAnswer("B0PROV0001"))
	withCatalogStage(t, true)
	fm := &fakeCatalogMatcher{panicWith: "synthetic matcher fault"}
	svc.SetCatalogMatcher(fm)

	resp := searchStage(t, svc, context.Background(), SearchOptions{})
	require.EqualValues(t, 1, fm.calls.Load(), "the stage was asked")
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "B0PROV0001", resp.Results[0].ASIN, "the providers' answer is untouched")
	assert.NotEqual(t, metadata.CatalogSourceName, resp.Results[0].Source)

	resp = searchStage(t, svc, context.Background(), SearchOptions{})
	assert.EqualValues(t, 2, fm.calls.Load(), "one fault does not switch the stage off")
	require.Len(t, resp.Results, 1)
}

// A search whose context ended while the matcher was working never merges the
// stage's answer, so its candidates are not counted as matches either.
func TestCatalogStage_AbortedSearchIsNotCounted(t *testing.T) {
	svc, _ := stageHarness(t, lanternAnswer("B0PROV0001"))
	withCatalogStage(t, true)
	book := &database.Book{ID: "b1", Title: stageTitle}
	run := func(cancelMidMatch bool) (*metadata.CatalogMatch, *CatalogStageStats) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fm := &fakeCatalogMatcher{answer: catalogAnswer()}
		if cancelMidMatch {
			fm.onMatch = cancel
		}
		svc.SetCatalogMatcher(fm)
		stats := &CatalogStageStats{}
		m := svc.runCatalogStage(WithCatalogStageStats(ctx, stats), book, searchInputs{}, stageTitle, stageTitle, "", "B0PROV0001", SearchOptions{})
		return m, stats
	}

	m, stats := run(false)
	require.NotNil(t, m)
	assert.EqualValues(t, 2, stats.CatalogMatched.Load(), "control: a finished lookup is counted")

	m, stats = run(true)
	assert.Nil(t, m)
	assert.Zero(t, stats.CatalogMatched.Load())
	assert.Zero(t, stats.OversizeBlocks.Load())
	assert.Zero(t, stats.HomonymSkipped.Load())
}

func storedCandidates(t *testing.T, f *verdictFixture, bookID string) []MetadataCandidate {
	t.Helper()
	stored, err := f.store.GetMetadataCache(bookID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	out := make([]MetadataCandidate, 0, len(stored.Candidates))
	for _, raw := range stored.Candidates {
		var c MetadataCandidate
		require.NoError(t, json.Unmarshal(raw, &c))
		out = append(out, c)
	}
	return out
}

// Joining the catalog's candidates must not re-sort the providers' by score:
// the search put its own-ASIN match first (a lower score than its sibling's),
// and slot 0 is what the review lane shows and every bulk apply reads.
func TestCatalogStage_JoinKeepsProviderOrderInSlotZero(t *testing.T) {
	f := newVerdictFixture(t)
	resp := &SearchMetadataResponse{Results: []MetadataCandidate{
		{Source: "SrcA", Title: stageTitle, Author: stageAuthor, ASIN: "B0OWN00001", Score: 0.70},
		{Source: "SrcA", Title: stageTitle + " Returns", Author: stageAuthor, ASIN: "B0SIB00002", Score: 0.90},
		{Source: metadata.CatalogSourceName, Title: stageTitle + " Again", Author: stageAuthor, ASIN: "B0CAT00003", Score: 0.80, FromCatalog: true},
	}}
	f.mfs.cacheSearchResponse("bk1", stageTitle, stageAuthor, "", "", resp)

	got := storedCandidates(t, f, "bk1")
	require.Len(t, got, 3)
	assert.Equal(t, "B0OWN00001", got[0].ASIN, "the own-ASIN candidate keeps slot 0 despite the sibling's higher score")
	assert.Equal(t, "B0SIB00002", got[1].ASIN)
	assert.Equal(t, "B0CAT00003", got[2].ASIN, "the catalog's candidate goes after the providers'")
}

// A stored catalog copy is an older reading: the fresh one replaces it, so a
// ManualOnly tag the catalog now carries is not lost to the stale copy on a
// Source|Title|ASIN collision -- and the old copy does not linger beside it.
func TestCatalogStage_FreshCatalogRowReplacesStoredCopy(t *testing.T) {
	f := newVerdictFixture(t)
	cat := func(manualOnly bool, score float64) MetadataCandidate {
		return MetadataCandidate{Source: metadata.CatalogSourceName, Title: stageTitle, Author: stageAuthor, ASIN: "B0CAT00003",
			Score: score, ManualOnly: manualOnly, FromCatalog: true, Stage: metadata.CatalogStageAuthor}
	}
	first := &SearchMetadataResponse{Results: []MetadataCandidate{
		{Source: "SrcA", Title: stageTitle, Author: stageAuthor, ASIN: "B0PROV0001", Score: 0.9},
		cat(false, 0.95),
	}}
	f.mfs.cacheSearchResponse("bk2", stageTitle, stageAuthor, "", "", first)

	// Providers have nothing now; the catalog says the entry is manual-only.
	second := &SearchMetadataResponse{Results: []MetadataCandidate{cat(true, 0.6)}}
	f.mfs.cacheSearchResponse("bk2", stageTitle, stageAuthor, "", "", second)

	got := storedCandidates(t, f, "bk2")
	require.Len(t, got, 2, "the provider's earlier candidate and exactly one catalog row")
	assert.Equal(t, "B0PROV0001", got[0].ASIN)
	assert.Equal(t, metadata.CatalogSourceName, got[1].Source)
	assert.True(t, got[1].ManualOnly, "the fresh catalog row, not the stored copy")
	assert.InDelta(t, 0.6, got[1].Score, 1e-9)
}

func asinsOf(cs []MetadataCandidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ASIN
	}
	return out
}

func catRow(asin string) MetadataCandidate {
	return MetadataCandidate{Source: metadata.CatalogSourceName, Title: stageTitle + " " + asin, Author: stageAuthor, ASIN: asin, Score: 0.7, FromCatalog: true}
}

// The stage ASKED and the catalog has nothing now: the stored catalog rows are
// gone with it. A stage that was not asked (or failed) says nothing about them,
// so they stay.
func TestCatalogStage_AnsweredEmptyDropsStoredCatalogRows(t *testing.T) {
	seed := func(t *testing.T, id string) *verdictFixture {
		f := newVerdictFixture(t)
		f.mfs.cacheSearchResponse(id, stageTitle, stageAuthor, "", "", &SearchMetadataResponse{Results: []MetadataCandidate{
			{Source: "SrcA", Title: stageTitle, Author: stageAuthor, ASIN: "B0PROV0001", Score: 0.9},
			catRow("B0CAT00001"),
		}, catalogAnswered: true})
		require.Equal(t, []string{"B0PROV0001", "B0CAT00001"}, asinsOf(storedCandidates(t, f, id)), "fixture")
		return f
	}

	t.Run("asked, nothing found: rows dropped", func(t *testing.T) {
		f := seed(t, "bk3")
		f.mfs.cacheSearchResponse("bk3", stageTitle, stageAuthor, "", "", &SearchMetadataResponse{catalogAnswered: true})
		assert.Equal(t, []string{"B0PROV0001"}, asinsOf(storedCandidates(t, f, "bk3")))
	})
	t.Run("asked, nothing found, providers merged: rows dropped", func(t *testing.T) {
		f := seed(t, "bk4")
		f.mfs.cacheSearchResponse("bk4", stageTitle, stageAuthor, "", "", &SearchMetadataResponse{
			Results:         []MetadataCandidate{{Source: "SrcB", Title: stageTitle, Author: stageAuthor, ASIN: "B0PROV0002", Score: 0.8}},
			catalogAnswered: true, mergeCached: true,
		})
		got := asinsOf(storedCandidates(t, f, "bk4"))
		assert.ElementsMatch(t, []string{"B0PROV0001", "B0PROV0002"}, got)
	})
	t.Run("not asked: rows kept", func(t *testing.T) {
		f := seed(t, "bk5")
		f.mfs.cacheSearchResponse("bk5", stageTitle, stageAuthor, "", "", &SearchMetadataResponse{})
		assert.Equal(t, []string{"B0PROV0001", "B0CAT00001"}, asinsOf(storedCandidates(t, f, "bk5")))
	})
}

// The stored order is whatever the writer's ranker gave it; a catalog answer
// only adds rows behind it. Here the row was ranked by a ranker that puts the
// Google row first, and the re-search has no ranker (review-only rows last): a
// sort would pull the other row to slot 0.
func TestCatalogStage_JoinNeverReordersExistingRows(t *testing.T) {
	f := newVerdictFixture(t)
	googleFirst := func(c MetadataCandidate) int {
		if c.Source == "Google Books" {
			return 0
		}
		return 1
	}
	f.mfs.cacheSearchResponse("bk6", stageTitle, stageAuthor, "", "", &SearchMetadataResponse{
		Results: []MetadataCandidate{
			{Source: "Audible", Title: stageTitle, Author: stageAuthor, ASIN: "B0REF00001", Score: 0.9},
			{Source: "Google Books", Title: stageTitle, Author: stageAuthor, ASIN: "B0GBK00002", Score: 0.5},
		},
		mergeRank: googleFirst,
	})
	require.Equal(t, []string{"B0GBK00002", "B0REF00001"}, asinsOf(storedCandidates(t, f, "bk6")), "fixture")

	f.mfs.cacheSearchResponse("bk6", stageTitle, stageAuthor, "", "", &SearchMetadataResponse{
		Results:         []MetadataCandidate{catRow("B0CAT00003")},
		catalogAnswered: true,
	})
	assert.Equal(t, []string{"B0GBK00002", "B0REF00001", "B0CAT00003"}, asinsOf(storedCandidates(t, f, "bk6")),
		"slot 0 unchanged; the catalog row goes behind every row ranked at or below it")
}

// Stored catalog rows do not take part in a merge's top-N when the stage has
// answered: with a ranker that puts one provider tier after review-only rows,
// they would evict that provider's row.
func TestCatalogStage_MergeCachedStoredCatalogRowsDoNotEvictProviders(t *testing.T) {
	f := newVerdictFixture(t)
	rank := func(c MetadataCandidate) int {
		switch {
		case c.ASIN == "B0REJ00000":
			return 2
		case c.Source == metadata.CatalogSourceName:
			return 1
		}
		return 0
	}
	f.mfs.cacheSearchResponse("bk7", stageTitle, stageAuthor, "", "", &SearchMetadataResponse{
		Results:         []MetadataCandidate{catRow("B0CAT00001"), catRow("B0CAT00002")},
		catalogAnswered: true, mergeRank: rank,
	})

	fresh := []MetadataCandidate{{Source: "SrcA", Title: stageTitle, Author: stageAuthor, ASIN: "B0REJ00000", Score: 0.9}}
	for i := 1; i < metadataCacheTopN; i++ {
		fresh = append(fresh, MetadataCandidate{Source: "SrcA", Title: stageTitle, Author: stageAuthor, ASIN: fmt.Sprintf("B0PRV%05d", i), Score: 0.8})
	}
	f.mfs.cacheSearchResponse("bk7", stageTitle, stageAuthor, "", "", &SearchMetadataResponse{
		Results: fresh, mergeCached: true, mergeRank: rank, catalogAnswered: true,
	})
	got := asinsOf(storedCandidates(t, f, "bk7"))
	assert.Len(t, got, metadataCacheTopN)
	assert.Contains(t, got, "B0REJ00000", "the provider row survives the stale catalog rows")
	assert.NotContains(t, got, "B0CAT00001")
	assert.NotContains(t, got, "B0CAT00002")
}
