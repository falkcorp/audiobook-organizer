// file: internal/audiobooks/isprimary_nil_agreement_test.go
// version: 1.0.0
// guid: 69cb8a54-5f1e-4d77-a32a-38f6fe11cc10
// last-edited: 2026-08-23

package audiobooks

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
		{"explicit-true", boolPtr(true)},
		{"explicit-false", boolPtr(false)},
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
	serializedEff, present := effectiveAtSerializationSite(t, byAuthor(true), nilID)

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
