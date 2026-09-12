// file: internal/audiobooks/isprimary_nil_agreement_test.go
// version: 1.3.0
// guid: 69cb8a54-5f1e-4d77-a32a-38f6fe11cc10
// last-edited: 2026-09-12

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/require"
)

// This file pins the ONE canonical rule for a nil Book.IsPrimaryVersion —
// nil counts as PRIMARY — across the three sites that independently decide it
// for the SAME book on the SAME request:
//
//	site 1  the store-side pushdown filter (memdb's effectiveBoolFieldIndex
//	        {Default: true} / pebble_store.go's IsPrimaryVersion filter),
//	        reached by the generic library list;
//	site 2  the in-Go post-filter in service_query.go's GetAudiobooksWithTotal,
//	        reached by the author branch (which pushes nothing down);
//	site 3  the serialized `is_primary_version` field in the JSON body.
//
// The assertions below deliberately do NOT go end-to-end and check "the right
// books came back". They extract the effective boolean EACH SITE COMPUTED for
// one nil-flagged book and compare the three to each other. That distinction
// matters: an end-to-end listing assertion passes as long as the filter is
// self-consistent, which it already was — the defect was that site 3 disagreed
// with sites 1 and 2 about a book all three had just handled.
//
// Why a real PebbleStore and not a mock: a mock that does not implement
// filteredSummaryStore makes summariesPushdown fall back to the unfiltered
// GetAllBookSummaries with didPushdown=false, so the query silently exercises
// site 2 while claiming to test site 1. primaryFlagFixture asserts which path
// actually ran rather than trusting the label.

// primaryFlagFixture seeds three books that DISAGREE under the two candidate
// nil rules: an explicit true, an explicit false, and a nil flag. A fixture
// missing the nil row would pass under either rule and prove nothing; a fixture
// missing the explicit-false row could not catch a fix that made every book
// primary.
type primaryFlagFixture struct {
	store    *database.PebbleStore
	authorID int
	// label -> book ID
	ids map[string]string
}

func seedPrimaryFlagFixture(t *testing.T) primaryFlagFixture {
	t.Helper()

	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	author, err := ps.CreateAuthor("Nil Flag Author")
	require.NoError(t, err)

	fx := primaryFlagFixture{store: ps, authorID: author.ID, ids: map[string]string{}}
	for _, seed := range []struct {
		label string
		flag  *bool
	}{
		{"explicit-true", new(true)},
		{"explicit-false", new(false)},
		{"nil-flag", nil},
	} {
		created, cErr := ps.CreateBook(&database.Book{
			Title:            seed.label,
			AuthorID:         &author.ID,
			IsPrimaryVersion: seed.flag,
		})
		require.NoError(t, cErr)
		fx.ids[seed.label] = created.ID

		// Guard the fixture itself: a nil flag must still be nil on disk, or
		// the whole test is vacuous. CreateBook defaulting it to true would
		// make every assertion below pass for the wrong reason.
		readBack, rErr := ps.GetBookByID(created.ID)
		require.NoError(t, rErr)
		if seed.flag == nil {
			require.Nil(t, readBack.IsPrimaryVersion,
				"fixture invalid: CreateBook defaulted the nil flag, so nothing here tests nil semantics")
		} else {
			require.NotNil(t, readBack.IsPrimaryVersion)
			require.Equal(t, *seed.flag, *readBack.IsPrimaryVersion)
		}
	}
	return fx
}

// effectiveAtFilterSite derives what a filtering site decided about one book by
// asking it BOTH questions. A site that considers the book primary returns it
// for is_primary_version=true and withholds it for =false; a site that
// considers it non-primary does the reverse. Answering the same for both (or
// neither) means the site is incoherent, which is reported rather than
// silently collapsed into a boolean.
func effectiveAtFilterSite(t *testing.T, site string, run func(want bool) []database.Book, bookID string) bool {
	t.Helper()

	inTrue := containsBook(run(true), bookID)
	inFalse := containsBook(run(false), bookID)

	switch {
	case inTrue && !inFalse:
		return true
	case !inTrue && inFalse:
		return false
	default:
		t.Fatalf("site %q is incoherent for book %s: is_primary_version=true returned it=%v, =false returned it=%v",
			site, bookID, inTrue, inFalse)
		return false
	}
}

func containsBook(books []database.Book, id string) bool {
	for i := range books {
		if books[i].ID == id {
			return true
		}
	}
	return false
}

// effectiveAtSerializationSite reads the boolean a CLIENT would read: the
// is_primary_version key of the marshalled response row. database.Book tags the
// field `json:"is_primary_version,omitempty"`, so a nil *bool omits the key
// entirely — the second return value reports whether the key was present at
// all, which is the actual pre-fix symptom (absent, not null).
func effectiveAtSerializationSite(t *testing.T, books []database.Book, id string) (value bool, present bool) {
	t.Helper()

	for i := range books {
		if books[i].ID != id {
			continue
		}
		raw, err := json.Marshal(books[i])
		require.NoError(t, err)
		var body map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &body))
		field, ok := body["is_primary_version"]
		if !ok {
			return false, false
		}
		var v bool
		require.NoError(t, json.Unmarshal(field, &v))
		return v, true
	}
	t.Fatalf("book %s was not in the response, so its serialized field cannot be read", id)
	return false, false
}

// TestIsPrimaryVersion_NilAgreesAcrossFilterAndSerialization is the headline
// pin: for one nil-flagged, ungrouped book, all three sites must compute the
// same effective boolean, and that boolean must be true.
func TestIsPrimaryVersion_NilAgreesAcrossFilterAndSerialization(t *testing.T) {
	fx := seedPrimaryFlagFixture(t)
	nilID := fx.ids["nil-flag"]

	// --- site 1: the store-side pushdown filter -------------------------
	// pushdownSpyStore (service_filtering_pushdown_test.go) shadows both
	// summary getters, so the assertion below is about which one actually ran,
	// not about which one we assume ran.
	spy := &pushdownSpyStore{PebbleStore: fx.store}
	listByFlag := func(want bool) []database.Book {
		spy.reset()
		// A fresh service per call: the generation-keyed list cache would
		// otherwise serve call 2 from call 1 and the spy would record nothing.
		svc := NewAudiobookService(spy)
		books, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
			ListFilters{IsPrimaryVersion: &want})
		require.NoError(t, err)
		requireWentThroughPrimaryPushdown(t, spy, want)
		return books
	}
	pushdownEff := effectiveAtFilterSite(t, "store pushdown filter", listByFlag, nilID)

	// --- site 2: the in-Go post-filter ----------------------------------
	// The author branch fetches via GetBooksByAuthorIDCore and pushes nothing
	// down, so f.IsPrimaryVersion is resolved by the post-filter block in
	// GetAudiobooksWithTotal.
	postSvc := NewAudiobookService(fx.store)
	byAuthor := func(want bool) []database.Book {
		books, err := postSvc.GetAudiobooks(context.Background(), 100, 0, "", &fx.authorID, nil,
			ListFilters{IsPrimaryVersion: &want})
		require.NoError(t, err)
		return books
	}
	postFilterEff := effectiveAtFilterSite(t, "in-Go post-filter", byAuthor, nilID)

	// --- site 3: the serialized field -----------------------------------
	// Read the row out of whichever arm site 2 put it in, so a site-2
	// regression surfaces as the explicit site-disagreement failure below
	// rather than as "the book wasn't in the response".
	serializedEff, present := effectiveAtSerializationSite(t, byAuthor(postFilterEff), nilID)

	require.True(t, present,
		"is_primary_version key is absent from a row that is_primary_version=true just returned; "+
			"a client reading the field disagrees with the filter that selected it")
	require.Equal(t, pushdownEff, postFilterEff,
		"pushdown filter and in-Go post-filter disagree about the same nil-flagged book")
	require.Equal(t, pushdownEff, serializedEff,
		"the serialized is_primary_version disagrees with the filter that returned the row")
	require.True(t, pushdownEff,
		"canonical rule is nil-counts-as-primary; all three sites must resolve nil to true")
}

// TestIsPrimaryVersion_ExplicitFalseStillExcluded is the anti-over-suppression
// pin. The fix resolves an UNKNOWN flag to primary; it must not make an
// explicitly-demoted book primary, and the DTO normalization must not flatten
// every row to true. Without this, "write true everywhere" would satisfy
// TestIsPrimaryVersion_NilAgreesAcrossFilterAndSerialization.
//
// The explicit-false assertions run through the library/pushdown path, not the
// author path. Measured while writing this test: the author branch's getter
// (MemStore.getBooksByAuthorID with primaryOnly=true — memdb_reads.go) drops
// explicitly non-primary books BEFORE the post-filter sees them, so an
// author-scoped is_primary_version=false returns nothing and the book is
// invisible to both arms. That is pre-existing, deliberate listing-view
// behaviour and is out of scope here; it is recorded because it is exactly the
// shape of thing that makes a filter test pass for the wrong reason.
func TestIsPrimaryVersion_ExplicitFalseStillExcluded(t *testing.T) {
	fx := seedPrimaryFlagFixture(t)
	falseID := fx.ids["explicit-false"]
	trueID := fx.ids["explicit-true"]

	spy := &pushdownSpyStore{PebbleStore: fx.store}
	listByFlag := func(want bool) []database.Book {
		spy.reset()
		svc := NewAudiobookService(spy)
		books, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
			ListFilters{IsPrimaryVersion: &want})
		require.NoError(t, err)
		requireWentThroughPrimaryPushdown(t, spy, want)
		return books
	}

	require.False(t, effectiveAtFilterSite(t, "store pushdown filter", listByFlag, falseID),
		"an explicitly non-primary book must stay excluded from is_primary_version=true")
	require.True(t, effectiveAtFilterSite(t, "store pushdown filter", listByFlag, trueID),
		"an explicitly primary book must stay included in is_primary_version=true")

	require.ElementsMatch(t,
		[]string{trueID, fx.ids["nil-flag"]},
		idsOf(listByFlag(true)),
		"is_primary_version=true is exactly {explicit true, nil} — no more, no less")

	falseEff, present := effectiveAtSerializationSite(t, listByFlag(false), falseID)
	require.True(t, present, "is_primary_version must be emitted, not omitted")
	require.False(t, falseEff,
		"normalization must preserve an explicit false, not rewrite every row to true")

	// The in-Go post-filter must reach the same verdict on the explicit rows it
	// CAN see through the author path.
	postSvc := NewAudiobookService(fx.store)
	primary := true
	byAuthor, err := postSvc.GetAudiobooks(context.Background(), 100, 0, "", &fx.authorID, nil,
		ListFilters{IsPrimaryVersion: &primary})
	require.NoError(t, err)
	require.False(t, containsBook(byAuthor, falseID),
		"the in-Go post-filter must also keep excluding the explicitly non-primary book")
	require.True(t, containsBook(byAuthor, trueID))
}

// TestIsPrimaryVersion_SerializationAgreesOnCacheHit pins the cache-hit half of
// the listing path. GetAudiobooksWithTotal returns cached pages from an early
// `return cached` that never reaches the tail normalization, so normalizing
// only at the tail would answer the same request one way on a miss and another
// on a hit. A single-request test cannot see that.
func TestIsPrimaryVersion_SerializationAgreesOnCacheHit(t *testing.T) {
	fx := seedPrimaryFlagFixture(t)
	nilID := fx.ids["nil-flag"]

	svc := NewAudiobookService(fx.store)
	primary := true

	miss, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
		ListFilters{IsPrimaryVersion: &primary})
	require.NoError(t, err)
	hit, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
		ListFilters{IsPrimaryVersion: &primary})
	require.NoError(t, err)

	missEff, missPresent := effectiveAtSerializationSite(t, miss, nilID)
	hitEff, hitPresent := effectiveAtSerializationSite(t, hit, nilID)

	require.True(t, missPresent, "cache MISS omitted is_primary_version")
	require.True(t, hitPresent, "cache HIT omitted is_primary_version")
	require.Equal(t, missEff, hitEff,
		"the same request serialized is_primary_version differently on a cache hit than on a miss")
	require.True(t, hitEff, "nil resolves to true on both cache paths")
}

// TestIsPrimaryVersion_SerializationAgreesOnCacheHit_NoPushdown exercises the
// degraded no-pushdown read path.
//
// HISTORY (2026-08-23): this test used to be described as "the cache test that
// can actually FAIL if the normalization before listCache.Set is removed". That
// is no longer true and the claim was removed rather than left to mislead. The
// Set is now gated on didPushdown, so this arm does not cache at all, and the
// pre-cache normalization is unobservable everywhere (see the note in
// service_query.go). What this test pins NOW is the membership invariant the
// gate exists for: two identical requests must return the same set of books,
// and the store must be hit twice because nothing was cached.
//
// With a conforming store the cached slice and the slice returned at the tail
// are the SAME backing array, so normalizing only at the tail happens to fix
// the cached page too — by aliasing, not by design. Take the pushdown away and
// the aliasing goes with it: summariesPushdown falls back to the unfiltered
// GetAllBookSummaries with didPushdown=false, the page that gets CACHED is the
// pre-post-filter projection, and the post-filter then builds a NEW slice which
// is what the tail normalizes. The cache is left holding raw rows, and the next
// request for the same page serves them from the early `return cached`.
//
// This is the degraded read path — a store without memdb, which production
// really does run through during the ~2 minute startup warmup — so it is a live
// path, not a mock-only curiosity.
func TestIsPrimaryVersion_SerializationAgreesOnCacheHit_NoPushdown(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	// MockStore does not declare HonorsEveryBookSummaryFilter, so it cannot
	// satisfy filteredSummaryStore and the query is forced down the
	// unfiltered-fetch + in-Go post-filter path.
	_, conforms := database.AsCapability[filteredSummaryStore](mockStore)
	require.False(t, conforms,
		"fixture invalid: this store pushes the filter down, so the cache would alias the returned slice and the test could not fail")

	summaries := []database.BookSummary{
		{ID: "explicit-true", Title: "a", IsPrimaryVersion: new(true)},
		{ID: "explicit-false", Title: "b", IsPrimaryVersion: new(false)},
		{ID: "nil-flag", Title: "c", IsPrimaryVersion: nil},
	}
	// Exactly once: the second GetAudiobooks call must be served by the list
	// cache, which is the whole point of the assertion.
	// TWICE, and that is the assertion. The no-pushdown arm no longer
	// writes to listCache at all (service_query.go gates the Set on
	// didPushdown), so the second request cannot be served from cache and
	// must go back to the store. If this ever drops to Once again, the
	// unfiltered page is being cached under a filtered key once more.
	mockStore.EXPECT().GetAllBookSummaries(100, 0).Return(summaries, nil).Times(2)

	svc := NewAudiobookService(mockStore)
	primary := true

	miss, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
		ListFilters{IsPrimaryVersion: &primary})
	require.NoError(t, err)
	hit, err := svc.GetAudiobooks(context.Background(), 100, 0, "", nil, nil,
		ListFilters{IsPrimaryVersion: &primary})
	require.NoError(t, err)

	require.ElementsMatch(t, []string{"explicit-true", "nil-flag"}, idsOf(miss),
		"the in-Go post-filter must select exactly {explicit true, nil}")

	// NOW ASSERTED — this was the membership bug, and it is fixed.
	// Before the fix, listCache.Set ran unconditionally right after
	// bookSummariesToBooks while didPushdown was not consulted until
	// several lines later, so the UNFILTERED projection was cached under a
	// key encoding p=true and every later request for that key was served
	// rows the filter excludes. Measured at the time:
	//
	//	listA (miss): {"explicit-true", "nil-flag"}
	//	listB (hit):  {"explicit-true", "explicit-false", "nil-flag"}
	//
	// It mis-served explicit-false rows as much as nil ones, so it was a
	// membership defect, not a nil-semantics one.
	require.ElementsMatch(t, idsOf(miss), idsOf(hit),
		"second identical request returned a different set of books than the first")
	require.NotContains(t, idsOf(hit), "explicit-false",
		"a book the filter explicitly excludes was served to the second request")

	missEff, missPresent := effectiveAtSerializationSite(t, miss, "nil-flag")
	hitEff, hitPresent := effectiveAtSerializationSite(t, hit, "nil-flag")

	require.True(t, missPresent, "cache MISS omitted is_primary_version")
	require.True(t, hitPresent,
		"cache HIT omitted is_primary_version: the page was cached before it was normalized")
	require.Equal(t, missEff, hitEff,
		"the same request serialized is_primary_version differently on a cache hit than on a miss")
	require.True(t, hitEff, "nil resolves to true on both cache paths")
}

// TestIsPrimaryVersion_CountAgreesWithListing pins the COUNT side of the
// nil-counts-as-primary rule. The listing tests above never call
// CountAudiobooksFiltered, and it decides the same question for the same
// books at three sites of its own:
//
//	count site A  the store's CountBookSummariesFiltered (countingFilteredStore);
//	count site B  countSummariesPushdownFiltered's summary fallback loop
//	              (service_filtering.go), reached by a store that does not
//	              conform to countingFilteredStore/filteredSummaryStore;
//	count site C  the materialize-and-count loop at the tail of
//	              CountAudiobooksFiltered (service_query.go), reached ONLY when
//	              buildBookSummaryFilter returns pushdownOK=false.
//
// Site C is the one nothing pinned before this test (measured 2026-09-12:
// rewriting its EffectiveIsPrimaryVersion call as `!= nil && *` left every
// test in the package passing). A non-conforming store does NOT reach it — it
// still gets pushdownOK=true and lands in site B. At HEAD the only
// pushdownOK=false return is a GetBooksByTag error inside the tag loop, and
// the site C loop then calls GetBooksByTag again and returns the error if it
// fails a second time. So site C runs only when a tag lookup fails once and
// then succeeds; service_query.go calls it "unreachable in practice" and keeps
// it as a defensive fallback. flakyTagStore produces exactly that sequence and
// counts the lookups so the subtest proves it ran site C rather than assuming it.
//
// Each subtest compares the count to the length of a listing for the SAME
// filters, and also pins the listing's membership, so "both sides agree on a
// wrong answer" (or on zero) cannot pass.
func TestIsPrimaryVersion_CountAgreesWithListing(t *testing.T) {
	wantSets := func(fx primaryFlagFixture, want bool) []string {
		if want {
			return []string{fx.ids["explicit-true"], fx.ids["nil-flag"]}
		}
		return []string{fx.ids["explicit-false"]}
	}

	t.Run("store count pushdown", func(t *testing.T) {
		fx := seedPrimaryFlagFixture(t)
		_, conforms := database.AsCapability[countingFilteredStore](fx.store)
		require.True(t, conforms,
			"fixture invalid: PebbleStore no longer conforms to countingFilteredStore, so this subtest is not exercising the store count pushdown")

		for _, want := range []bool{true, false} {
			filters := ListFilters{IsPrimaryVersion: &want}
			listed, err := NewAudiobookService(fx.store).GetAudiobooks(context.Background(), 100, 0, "", nil, nil, filters)
			require.NoError(t, err)
			require.ElementsMatch(t, wantSets(fx, want), idsOf(listed),
				"listing for is_primary_version=%v is not {explicit, nil-as-primary}", want)

			count, err := NewAudiobookService(fx.store).CountAudiobooksFiltered(context.Background(), filters)
			require.NoError(t, err)
			require.Equal(t, len(listed), count,
				"store count pushdown disagrees with the listing for is_primary_version=%v", want)
		}
	})

	t.Run("summary fallback without pushdown", func(t *testing.T) {
		mockStore := mocks.NewMockStore(t)
		_, conformsCount := database.AsCapability[countingFilteredStore](mockStore)
		_, conformsList := database.AsCapability[filteredSummaryStore](mockStore)
		require.False(t, conformsCount || conformsList,
			"fixture invalid: the mock pushes the filter down, so the in-Go summary fallback never runs")

		summaries := []database.BookSummary{
			{ID: "explicit-true", Title: "a", IsPrimaryVersion: new(true)},
			{ID: "explicit-false", Title: "b", IsPrimaryVersion: new(false)},
			{ID: "nil-flag", Title: "c", IsPrimaryVersion: nil},
		}
		// Listing pages with (100, 0); the count fetches everything with (0, 0).
		// One call each per value of want.
		mockStore.EXPECT().GetAllBookSummaries(100, 0).Return(summaries, nil).Times(2)
		mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil).Times(2)

		for _, want := range []bool{true, false} {
			filters := ListFilters{IsPrimaryVersion: &want}
			listed, err := NewAudiobookService(mockStore).GetAudiobooks(context.Background(), 100, 0, "", nil, nil, filters)
			require.NoError(t, err)
			expected := []string{"explicit-false"}
			if want {
				expected = []string{"explicit-true", "nil-flag"}
			}
			require.ElementsMatch(t, expected, idsOf(listed))

			count, err := NewAudiobookService(mockStore).CountAudiobooksFiltered(context.Background(), filters)
			require.NoError(t, err)
			require.Equal(t, len(listed), count,
				"summary-fallback count disagrees with the listing for is_primary_version=%v", want)
		}
	})

	t.Run("materialize-and-count fallback", func(t *testing.T) {
		fx := seedPrimaryFlagFixture(t)
		// Tag ALL three books so the tag intersection keeps every row and the
		// count is decided by the primary rule alone.
		const tag = "count-nil-primary"
		for _, id := range fx.ids {
			require.NoError(t, fx.store.AddBookTag(id, tag))
		}
		flaky := &flakyTagStore{PebbleStore: fx.store}

		for _, want := range []bool{true, false} {
			filters := ListFilters{IsPrimaryVersion: &want, Tag: tag}
			// The listing runs over the plain store in its own service, so it
			// cannot consume the armed failure meant for the count.
			listed, err := NewAudiobookService(fx.store).GetAudiobooks(context.Background(), 100, 0, "", nil, nil, filters)
			require.NoError(t, err)
			require.ElementsMatch(t, wantSets(fx, want), idsOf(listed),
				"listing for tag + is_primary_version=%v is not {explicit, nil-as-primary}", want)

			flaky.armOneFailure()
			count, err := NewAudiobookService(flaky).CountAudiobooksFiltered(context.Background(), filters)
			require.NoError(t, err)
			require.Equal(t, 2, flaky.tagLookups(),
				"fixture invalid: expected one failed tag lookup (pushdownOK=false) then one successful lookup in the "+
					"materialize-and-count fallback; any other number means that loop did not run")
			require.Equal(t, len(listed), count,
				"materialize-and-count fallback disagrees with the listing for is_primary_version=%v "+
					"(nil must count as primary there too)", want)
		}
	})
}

var errFlakyTagLookup = errors.New("flakyTagStore: armed tag lookup failure")

// flakyTagStore fails the next GetBooksByTag call after armOneFailure, then
// delegates. The failure is armed per call rather than as a one-shot global so
// no other request in the test can consume it.
type flakyTagStore struct {
	*database.PebbleStore
	mu       sync.Mutex
	failNext bool
	calls    int
}

func (s *flakyTagStore) armOneFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = true
	s.calls = 0
}

func (s *flakyTagStore) tagLookups() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *flakyTagStore) GetBooksByTag(tag string) ([]string, error) {
	s.mu.Lock()
	s.calls++
	fail := s.failNext
	s.failNext = false
	s.mu.Unlock()
	if fail {
		return nil, errFlakyTagLookup
	}
	return s.PebbleStore.GetBooksByTag(tag)
}

// requireWentThroughPrimaryPushdown is the anti-mislabelling guard for site 1.
// pushdownSpyStore's own assertWentThroughPushdown demands a Predicate, which
// only the heavy-filter arm builds; the light is_primary_version arm carries the
// flag on BookSummaryFilter.IsPrimaryVersion instead, so it needs its own check.
func requireWentThroughPrimaryPushdown(t *testing.T, spy *pushdownSpyStore, want bool) {
	t.Helper()
	spy.mu.Lock()
	defer spy.mu.Unlock()
	require.Zero(t, spy.unfilteredCalls,
		"query fell through to the unfiltered fetch-all path, so it exercised the in-Go post-filter, not the store pushdown")
	require.NotEmpty(t, spy.filteredCalls, "query never called the filtered summary pushdown")
	for _, f := range spy.filteredCalls {
		require.NotNil(t, f.IsPrimaryVersion,
			"the pushed-down filter did not carry IsPrimaryVersion")
		require.Equal(t, want, *f.IsPrimaryVersion)
	}
}

func idsOf(books []database.Book) []string {
	out := make([]string, 0, len(books))
	for i := range books {
		out = append(out, books[i].ID)
	}
	return out
}
