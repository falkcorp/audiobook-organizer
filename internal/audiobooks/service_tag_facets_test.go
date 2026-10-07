// file: internal/audiobooks/service_tag_facets_test.go
// version: 1.0.0
// guid: 8c4d2e7f-1a9b-4f36-b5d0-6e3a9c7b2f18
// last-edited: 2026-10-06

package audiobooks

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// referenceTagCounts counts tags over the fixtures pick admits — the answer a
// scoped facet must give, computed without the code under test.
func referenceTagCounts(fixtures []pushdownFixture, pick func(pushdownFixture) bool) (map[string]int, int) {
	counts := map[string]int{}
	total := 0
	for _, f := range fixtures {
		if !pick(f) {
			continue
		}
		total++
		for _, t := range f.tags {
			// The store normalizes tags on write (util.NormalizeString).
			counts[util.NormalizeString(t)]++
		}
	}
	return counts, total
}

func facetMap(f ScopedTagFacets) map[string]int {
	m := map[string]int{}
	for _, t := range f.Tags {
		m[t.Tag] = t.Count
	}
	return m
}

func newScopedFacetsFixture(t *testing.T) (*AudiobookService, *database.PebbleStore, []pushdownFixture) {
	t.Helper()
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	fixtures := seedPushdownBooks(t, ps)
	return NewAudiobookService(ps), ps, fixtures
}

// The chips must count ONLY the books the list request matches. Every case is
// chosen so the scoped answer differs from the library-wide one: a facet that
// ignored the filter would fail on the counts, not just on the total.
func TestScopedTagFacets_CountOnlyMatchingBooks(t *testing.T) {
	svc, _, fixtures := newScopedFacetsFixture(t)
	yes := true

	cases := []struct {
		name   string
		search string
		f      ListFilters
		pick   func(pushdownFixture) bool
	}{
		{
			name: "primary only (default library view)",
			f:    ListFilters{IsPrimaryVersion: &yes},
			pick: func(f pushdownFixture) bool { return f.primary },
		},
		{
			name: "library_state AND primary",
			f:    ListFilters{IsPrimaryVersion: &yes, LibraryState: "organized"},
			pick: func(f pushdownFixture) bool { return f.primary && f.libraryState == "organized" },
		},
		{
			name: "tags[] narrows: co-occurring tags only",
			f:    ListFilters{Tags: []string{"tagA"}},
			pick: func(f pushdownFixture) bool { return pdHasTag(f, "tagA") },
		},
		{
			name: "field filter genre",
			f:    ListFilters{FieldFilters: []FieldFilter{{Field: "genre", Value: "scifi"}}},
			pick: func(f pushdownFixture) bool { return f.genre == "scifi" },
		},
		{
			name:   "search (substring engine, no index wired)",
			search: "b-book",
			pick:   func(f pushdownFixture) bool { return f.title == "b-book" },
		},
	}

	for _, seekThreshold := range []int{1 << 20, 0} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/threshold=%d", tc.name, seekThreshold), func(t *testing.T) {
				old := tagCountSeekThreshold
				tagCountSeekThreshold = seekThreshold // 0 forces the tag_idx scan path
				t.Cleanup(func() { tagCountSeekThreshold = old })

				got, err := svc.ScopedTagFacets(context.Background(), tc.search, nil, nil, tc.f)
				require.NoError(t, err)
				wantCounts, wantTotal := referenceTagCounts(fixtures, tc.pick)
				require.Equal(t, wantTotal, got.Total, "match-set size")
				require.Equal(t, wantCounts, facetMap(got), "tag counts over the match set")

				// Ordered count desc, then tag asc.
				for i := 1; i < len(got.Tags); i++ {
					a, b := got.Tags[i-1], got.Tags[i]
					require.True(t, a.Count > b.Count || (a.Count == b.Count && a.Tag < b.Tag), "order at %d: %v %v", i, a, b)
				}
			})
		}
	}
}

// A cached entry is keyed by the store change-log generation: a tag write
// recorded in the log must be visible on the next request, and an unchanged
// generation must be served from the cache.
func TestScopedTagFacets_CacheInvalidatesOnRecordedChange(t *testing.T) {
	svc, ps, fixtures := newScopedFacetsFixture(t)
	changes := searchcache.NewChangeLog(64)
	svc.SetSearchResultCache(searchcache.New(changes, searchcache.Config{}))
	f := ListFilters{LibraryState: "imported"}

	first, err := svc.ScopedTagFacets(context.Background(), "", nil, nil, f)
	require.NoError(t, err)

	var target string
	for _, fx := range fixtures {
		if fx.libraryState == "imported" && !pdHasTag(fx, "tagA") {
			target = fx.id
			break
		}
	}
	require.NotEmpty(t, target)
	require.NoError(t, ps.AddBookTag(target, "tagA"))

	// Generation unchanged: the cached answer is served.
	same, err := svc.ScopedTagFacets(context.Background(), "", nil, nil, f)
	require.NoError(t, err)
	require.Equal(t, facetMap(first)["taga"], facetMap(same)["taga"])

	// The store observer records tag writes in production; simulate it.
	changes.Record(target)
	after, err := svc.ScopedTagFacets(context.Background(), "", nil, nil, f)
	require.NoError(t, err)
	require.Equal(t, facetMap(first)["taga"]+1, facetMap(after)["taga"])
}

// Requests whose result depends on state the change log does not track are
// never cached (same fail-closed set as searchCacheKey).
func TestScopedTagFacetsKey_FailsClosed(t *testing.T) {
	svc := NewAudiobookService(nil)
	svc.SetSearchResultCache(searchcache.New(searchcache.NewChangeLog(8), searchcache.Config{}))
	minCov := 10
	for name, f := range map[string]ListFilters{
		"per-user":    {PerUserFilters: []FieldFilter{{Field: "read_status", Value: "finished"}}},
		"restrict":    {RestrictToIDs: map[string]struct{}{}},
		"fingerprint": {FingerprintStatus: "none"},
		"coverage":    {CoveragePercentMin: &minCov},
	} {
		_, ok := svc.scopedTagFacetsKey("", nil, nil, f)
		require.False(t, ok, name)
	}
	_, ok := svc.scopedTagFacetsKey("read_status:finished", nil, nil, ListFilters{})
	require.False(t, ok, "per-user DSL in search")

	// Sort never changes membership, so it never splits an entry.
	a, ok1 := svc.scopedTagFacetsKey("x", nil, nil, ListFilters{SortBy: "title"})
	b, ok2 := svc.scopedTagFacetsKey("x", nil, nil, ListFilters{SortBy: "year", SortOrder: "desc"})
	require.True(t, ok1 && ok2)
	require.Equal(t, a, b)
}
