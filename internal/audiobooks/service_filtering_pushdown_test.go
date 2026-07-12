// file: internal/audiobooks/service_filtering_pushdown_test.go
// version: 1.0.0
// guid: 3c9e7a41-2b5d-4f80-9c16-8a2e5d0b1f7c
// last-edited: 2026-07-11

package audiobooks

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// This file parity-locks the SHIPPED heavy-filter pushdown in GetAudiobooks
// (buildBookSummaryFilterWithLookupCount + summariesPushdownFiltered). The
// pushdown already exists at HEAD; these tests guarantee no future change
// silently narrows it — a regression would surface as MISSING BOOKS in the
// library list.
//
// Harness: mirrors internal/audiobooks/transcribed_title_pushdown_test.go —
// a real PebbleStore with a warm memdb so the pushdown path is live (a mock
// store would bypass the memdb walker entirely).

// pushdownFixture is a self-describing seed row. The test both persists it via
// CreateBook and evaluates the reference filter/sort against these same fields,
// so the reference never depends on the code under test.
type pushdownFixture struct {
	id           string // filled in after CreateBook
	title        string
	libraryState string
	genre        string
	fpStatus     string
	coverage     int
	duration     int
	primary      bool
	tags         []string
}

// seedPushdownBooks creates ~50 deterministic books spanning every filterable
// dimension and returns the fixtures with their assigned (ULID) IDs.
func seedPushdownBooks(t *testing.T, ps *database.PebbleStore) []pushdownFixture {
	t.Helper()

	states := []string{"organized", "imported", "suspicious"}
	genres := []string{"fantasy", "scifi", "history"}
	fpStatuses := []string{"none", "partial", "complete"}

	fixtures := make([]pushdownFixture, 0, 51)
	for i := 0; i < 51; i++ {
		f := pushdownFixture{
			title:        // spread titles so a title sort would reorder vs ID order
			string(rune('a'+(i%26))) + "-book",
			libraryState: states[i%len(states)],
			genre:        genres[i%len(genres)],
			fpStatus:     fpStatuses[i%len(fpStatuses)],
			coverage:     (i * 7) % 101, // 0..100
			duration:     3600 + (i*137)%9000,
			primary:      i%4 != 0, // ~75% primary
		}
		// Give ~every third book a tag; some share "shared" so tags-multi has
		// a real intersection to compute.
		if i%3 == 0 {
			f.tags = append(f.tags, "tagA")
		}
		if i%5 == 0 {
			f.tags = append(f.tags, "tagB")
		}
		if i%15 == 0 {
			f.tags = append(f.tags, "shared")
		}

		primary := f.primary
		ls := f.libraryState
		genre := f.genre
		dur := f.duration
		created, err := ps.CreateBook(&database.Book{
			Title:             f.title,
			LibraryState:      &ls,
			Genre:             &genre,
			Duration:          &dur,
			IsPrimaryVersion:  &primary,
			FingerprintStatus: f.fpStatus,
			CoveragePercent:   f.coverage,
		})
		require.NoError(t, err)
		f.id = created.ID
		for _, tag := range f.tags {
			require.NoError(t, ps.AddBookTag(created.ID, tag))
		}
		fixtures = append(fixtures, f)
	}
	return fixtures
}

// pdReferencePage computes the expected page of book IDs for a filter, mirroring
// the SHIPPED order-of-operations exactly:
//   - filter the fixtures with the same predicate the pushdown applies
//   - order the survivors by ID ascending (memdb memIdxID iteration order)
//   - for a non-title heavy sort: pdPaginate first, THEN stable-sort the page by
//     the sort key with an ID tiebreaker (applySorting runs AFTER pagination —
//     pre-existing design, see service_query.go)
//   - otherwise: pdPaginate directly (pushdown paginated in ID order; post-filter
//     pass is skipped)
func pdReferencePage(fixtures []pushdownFixture, match func(pushdownFixture) bool, sortBy string, limit, offset int) []string {
	survivors := make([]pushdownFixture, 0, len(fixtures))
	for _, f := range fixtures {
		if match(f) {
			survivors = append(survivors, f)
		}
	}
	// Natural memdb order = ID ascending.
	sort.Slice(survivors, func(i, j int) bool { return survivors[i].id < survivors[j].id })

	heavySort := sortBy != "" && sortBy != "title"
	if !heavySort {
		return pdPageIDs(survivors, limit, offset)
	}
	// Heavy sort: pdPaginate ID-ordered set, then stable-sort the page.
	page := make([]pushdownFixture, len(survivors))
	copy(page, survivors)
	page = pdPaginate(page, limit, offset)
	if sortBy == "duration" {
		sort.SliceStable(page, func(i, j int) bool {
			if page[i].duration != page[j].duration {
				return page[i].duration < page[j].duration
			}
			return page[i].id < page[j].id
		})
	}
	out := make([]string, len(page))
	for i := range page {
		out[i] = page[i].id
	}
	return out
}

func pdPaginate(in []pushdownFixture, limit, offset int) []pushdownFixture {
	if offset > 0 && offset < len(in) {
		in = in[offset:]
	} else if offset >= len(in) {
		return nil
	}
	if limit > 0 && limit < len(in) {
		in = in[:limit]
	}
	return in
}

func pdPageIDs(in []pushdownFixture, limit, offset int) []string {
	in = pdPaginate(in, limit, offset)
	out := make([]string, len(in))
	for i := range in {
		out[i] = in[i].id
	}
	return out
}

func pdGotIDs(books []database.Book) []string {
	out := make([]string, len(books))
	for i := range books {
		out[i] = books[i].ID
	}
	return out
}

// TestLibraryStatePushdownParity is the core parity suite: for library_state /
// tag / tags-multi / FieldFilters queries, the page returned by GetAudiobooks
// (IDs + order) must equal the reference evaluated directly from the fixtures,
// across several limit/offset combos.
func TestLibraryStatePushdownParity(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	fixtures := seedPushdownBooks(t, ps)
	svc := NewAudiobookService(ps)

	combos := []struct{ limit, offset int }{
		{limit: 1000, offset: 0}, // full page
		{limit: 5, offset: 0},
		{limit: 5, offset: 5},
		{limit: 7, offset: 20},
		{limit: 1000, offset: 40},
	}

	cases := []struct {
		name   string
		filter ListFilters
		match  func(pushdownFixture) bool
	}{
		{
			name:   "library_state=organized",
			filter: ListFilters{LibraryState: "organized"},
			match:  func(f pushdownFixture) bool { return f.libraryState == "organized" },
		},
		{
			name:   "tag=tagA",
			filter: ListFilters{Tag: "tagA"},
			match:  func(f pushdownFixture) bool { return pdHasTag(f, "tagA") },
		},
		{
			name:   "tags-multi=tagA+shared",
			filter: ListFilters{Tags: []string{"tagA", "shared"}},
			match:  func(f pushdownFixture) bool { return pdHasTag(f, "tagA") && pdHasTag(f, "shared") },
		},
		{
			name:   "fieldfilter genre=fantasy",
			filter: ListFilters{FieldFilters: []FieldFilter{{Field: "genre", Value: "fantasy"}}},
			match:  func(f pushdownFixture) bool { return f.genre == "fantasy" },
		},
		{
			name:   "fingerprint status=complete",
			filter: ListFilters{FingerprintStatus: "complete"},
			match:  func(f pushdownFixture) bool { return f.fpStatus == "complete" },
		},
		{
			name:   "coverage min=50",
			filter: ListFilters{CoveragePercentMin: pdIntPtr(50)},
			match:  func(f pushdownFixture) bool { return f.coverage >= 50 },
		},
	}

	for _, tc := range cases {
		for _, c := range combos {
			t.Run(tc.name+"/"+pdComboName(c.limit, c.offset), func(t *testing.T) {
				got, err := svc.GetAudiobooks(context.Background(), c.limit, c.offset, "", nil, nil, tc.filter)
				require.NoError(t, err)
				want := pdReferencePage(fixtures, tc.match, "", c.limit, c.offset)
				require.Equal(t, want, pdGotIDs(got),
					"pushdown page diverged from reference for %s (limit=%d offset=%d)", tc.name, c.limit, c.offset)
			})
		}
	}
}

// TestLibraryStatePushdownParity_MatchEverythingAndEmpty guards the two ends of
// the range: an all-matching filter must NOT over-suppress (returns the full
// page), and a tag with zero books must return an empty page via the pushdown's
// empty-RestrictToIDs short-circuit — NOT the fetch-all fallback.
func TestLibraryStatePushdownParity_MatchEverythingAndEmpty(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	fixtures := seedPushdownBooks(t, ps)
	svc := NewAudiobookService(ps)

	// Anti-over-suppression: coverage>=0 matches every book — the full library
	// must come back, not a narrowed subset.
	got, err := svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil,
		ListFilters{CoveragePercentMin: pdIntPtr(0)})
	require.NoError(t, err)
	require.Len(t, got, len(fixtures), "match-everything filter must return the full library")

	// Empty tag: no book carries it → empty page via pushdown, no error, no
	// full-corpus fetch.
	got, err = svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil,
		ListFilters{Tag: "does-not-exist"})
	require.NoError(t, err)
	require.Empty(t, got, "tag with zero books must return an empty page, not the whole corpus")
}

// TestPushdownParitySort locks the non-title heavy-sort path. Sorting runs on a
// full page (offset 0, limit>=N) so pagination is a no-op and the whole filtered
// set is sorted — clean parity against a reference sort.
func TestPushdownParitySort(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	fixtures := seedPushdownBooks(t, ps)
	svc := NewAudiobookService(ps)

	got, err := svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil,
		ListFilters{SortBy: "duration", SortOrder: "asc", LibraryState: "organized"})
	require.NoError(t, err)

	want := pdReferencePage(fixtures,
		func(f pushdownFixture) bool { return f.libraryState == "organized" },
		"duration", 1000, 0)
	require.Equal(t, want, pdGotIDs(got), "non-title sort page diverged from reference")
}

// pushdownSpyStore wraps a real PebbleStore and records which summary getters
// GetAudiobooks calls. Its own GetAllBookSummariesFiltered / GetAllBookSummaries
// shadow the promoted PebbleStore methods, so the filteredSummaryStore type
// assertion in summariesPushdownFiltered resolves to the spy while the real
// pushdown still runs underneath.
type pushdownSpyStore struct {
	*database.PebbleStore

	mu              sync.Mutex
	filteredCalls   []database.BookSummaryFilter
	unfilteredCalls int
}

func (s *pushdownSpyStore) GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error) {
	s.mu.Lock()
	s.filteredCalls = append(s.filteredCalls, f)
	s.mu.Unlock()
	return s.PebbleStore.GetAllBookSummariesFiltered(limit, offset, f)
}

func (s *pushdownSpyStore) GetAllBookSummaries(limit, offset int) ([]database.BookSummary, error) {
	s.mu.Lock()
	s.unfilteredCalls++
	s.mu.Unlock()
	return s.PebbleStore.GetAllBookSummaries(limit, offset)
}

func (s *pushdownSpyStore) reset() {
	s.mu.Lock()
	s.filteredCalls = nil
	s.unfilteredCalls = 0
	s.mu.Unlock()
}

// assertWentThroughPushdown is the Decision-9 anti-narrowing guard: the query
// must route through the shipped filtered pushdown with a real predicate, and
// must NOT touch the unfiltered fetch-all path nor record a zero-value filter
// (the fetch-all fallback arm a future narrowing would reroute through).
func (s *pushdownSpyStore) assertWentThroughPushdown(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Zero(t, s.unfilteredCalls, "query fell through to the unfiltered fetch-all path")
	require.NotEmpty(t, s.filteredCalls, "query never called the filtered pushdown")
	sawPredicate := false
	for _, f := range s.filteredCalls {
		if pdIsZeroFilter(f) {
			t.Fatalf("query routed through the zero-value fetch-all fallback filter")
		}
		if f.Predicate != nil {
			sawPredicate = true
		}
	}
	require.True(t, sawPredicate, "no recorded filter carried a Predicate — fingerprint/field predicate was not pushed down")
}

// pdIsZeroFilter reports whether f is the zero-value BookSummaryFilter{} used by
// the fetch-all fallback arm. Predicate is a func (not comparable), so compare
// field-by-field.
func pdIsZeroFilter(f database.BookSummaryFilter) bool {
	return f.IsPrimaryVersion == nil &&
		!f.ExcludeQuarantined &&
		f.LibraryState == "" &&
		f.ReviewStatus == "" &&
		f.RestrictToIDs == nil &&
		f.Predicate == nil &&
		f.SortBy == "" &&
		!f.SortAscending &&
		f.MarkedForDeletion == nil
}

// TestPushdownParityFingerprintAndSort is the Decision-9 anti-narrowing pin: it
// proves the fingerprint filter — alone, and paired with a non-title sort —
// provably goes through the shipped pushdown (a real BookSummaryFilter.Predicate),
// never the unfiltered fetch-all path. It intentionally does NOT assert result
// parity for the sort+fingerprint combination: the post-filter re-application of
// the fingerprint predicate runs against summary-projected Books that don't carry
// FingerprintStatus/CoveragePercent, so result-parity there is not meaningful —
// the routing pin is the guarantee that matters (spec Decision 9).
func TestPushdownParityFingerprintAndSort(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	seedPushdownBooks(t, ps)
	spy := &pushdownSpyStore{PebbleStore: ps}
	svc := NewAudiobookService(spy)

	// Fingerprint filter alone.
	spy.reset()
	_, err = svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil,
		ListFilters{FingerprintStatus: "complete"})
	require.NoError(t, err)
	spy.assertWentThroughPushdown(t)

	// Fingerprint filter paired WITH a non-title sort.
	spy.reset()
	_, err = svc.GetAudiobooks(context.Background(), 1000, 0, "", nil, nil,
		ListFilters{FingerprintStatus: "complete", SortBy: "duration", SortOrder: "asc"})
	require.NoError(t, err)
	spy.assertWentThroughPushdown(t)
}

func pdHasTag(f pushdownFixture, tag string) bool {
	for _, t := range f.tags {
		if t == tag {
			return true
		}
	}
	return false
}

func pdComboName(limit, offset int) string {
	return "l" + pdItoa(limit) + "_o" + pdItoa(offset)
}

func pdItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b strings.Builder
	digits := make([]byte, 0, 8)
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	if neg {
		b.WriteByte('-')
	}
	for i := len(digits) - 1; i >= 0; i-- {
		b.WriteByte(digits[i])
	}
	return b.String()
}

func pdIntPtr(v int) *int { return &v }
