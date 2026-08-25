// file: internal/audiobooks/restrict_to_ids_test.go
// version: 1.0.0
// guid: 7d2f5b91-3ac6-4e18-b520-9f4e6c8a1d33
// last-edited: 2026-08-25

package audiobooks

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// ListFilters.RestrictToIDs exists so the has_file_errors and quick-query fast
// paths can narrow the library to an ID set WITHOUT discarding the rest of the
// request. Those paths used to answer from the ID slice alone, hand-paginating
// it and reporting its length as the count, which silently dropped the search,
// the filters JSON, author_id, series_id and the sort that arrived on the same
// request.
//
// The discriminating property is INTERSECTION. A fixture where every
// restricted book also matches the other filter is green under the bug — the
// restriction alone and the intersection are the same set, so the assertion
// cannot tell "ANDed correctly" from "other filter ignored". Every case below
// therefore uses a restriction that only PARTIALLY overlaps its filter, and
// asserts the result is a proper subset of BOTH.

// pdRestrictSet builds an ID set from the fixtures matching pick. It always
// returns a non-nil map so the nil/empty distinction stays under test control.
func pdRestrictSet(fixtures []pushdownFixture, pick func(int, pushdownFixture) bool) map[string]struct{} {
	set := make(map[string]struct{})
	for i, f := range fixtures {
		if pick(i, f) {
			set[f.id] = struct{}{}
		}
	}
	return set
}

func TestRestrictToIDsIsANDedWithEveryOtherFilter(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	fixtures := seedPushdownBooks(t, ps)
	svc := NewAudiobookService(ps)

	// Every other fixture, chosen independently of library_state/tag/genre so
	// the overlap with each filter below is partial by construction.
	restrict := pdRestrictSet(fixtures, func(i int, _ pushdownFixture) bool { return i%2 == 0 })
	inRestrict := func(f pushdownFixture) bool { _, ok := restrict[f.id]; return ok }

	cases := []struct {
		name   string
		filter ListFilters
		match  func(pushdownFixture) bool
	}{
		{
			name:   "restrict alone",
			filter: ListFilters{RestrictToIDs: restrict},
			match:  inRestrict,
		},
		{
			name:   "restrict AND library_state",
			filter: ListFilters{RestrictToIDs: restrict, LibraryState: "organized"},
			match: func(f pushdownFixture) bool {
				return inRestrict(f) && f.libraryState == "organized"
			},
		},
		{
			name:   "restrict AND tag",
			filter: ListFilters{RestrictToIDs: restrict, Tag: "tagA"},
			match: func(f pushdownFixture) bool {
				return inRestrict(f) && pdHasTag(f, "tagA")
			},
		},
		{
			name:   "restrict AND field filter",
			filter: ListFilters{RestrictToIDs: restrict, FieldFilters: []FieldFilter{{Field: "genre", Value: "fantasy"}}},
			match: func(f pushdownFixture) bool {
				return inRestrict(f) && f.genre == "fantasy"
			},
		},
		{
			name:   "restrict AND sort",
			filter: ListFilters{RestrictToIDs: restrict, SortBy: "duration", SortOrder: "asc"},
			match:  inRestrict,
		},
	}

	for _, tc := range cases {
		// Guard the guard: if a case's expected set were equal to the
		// restriction or to the whole library, the case could pass while the
		// other half of the predicate was ignored. Assert the overlap really
		// is partial before trusting the comparison.
		want := pdReferencePage(fixtures, tc.match, "", 1000, 0)
		if tc.name != "restrict alone" && tc.name != "restrict AND sort" {
			require.Less(t, len(want), len(restrict),
				"%s: expected set must be a PROPER subset of the restriction, "+
					"otherwise this case cannot detect the other filter being dropped", tc.name)
		}
		require.NotEmpty(t, want, "%s: expected set must be non-empty", tc.name)
		require.Less(t, len(want), len(fixtures),
			"%s: expected set must be a proper subset of the library", tc.name)

		for _, c := range []struct{ limit, offset int }{{1000, 0}, {5, 0}, {5, 5}, {7, 3}} {
			t.Run(tc.name+"/"+pdComboName(c.limit, c.offset), func(t *testing.T) {
				sortBy := ""
				if tc.filter.SortBy != "" {
					sortBy = tc.filter.SortBy
				}
				gotBooks, total, err := svc.GetAudiobooksWithTotal(
					context.Background(), c.limit, c.offset, "", nil, nil, tc.filter)
				require.NoError(t, err)

				wantPage := pdReferencePage(fixtures, tc.match, sortBy, c.limit, c.offset)
				require.Equal(t, wantPage, pdGotIDs(gotBooks),
					"%s: page diverged from the intersection (limit=%d offset=%d)",
					tc.name, c.limit, c.offset)

				// The count is half the bug: the old fast paths reported the
				// length of the unfiltered ID slice, so the total belonged to a
				// different query than the page. It must be derived from the
				// same filter.
				//
				// GetAudiobooksWithTotal returns -1 for "total not computed" on
				// the light/cached path, so the count contract is asserted
				// against CountAudiobooksFiltered — which is the path
				// buildAudiobookListResponse actually reports to the client —
				// and the inline total is only checked when it was computed.
				wantTotal := len(pdReferencePage(fixtures, tc.match, "", 100000, 0))
				counted, cErr := svc.CountAudiobooksFiltered(context.Background(), tc.filter)
				require.NoError(t, cErr)
				require.Equal(t, wantTotal, counted,
					"%s: count must count the intersection, not the restriction alone", tc.name)
				if total != -1 {
					require.Equal(t, wantTotal, total,
						"%s: computed total must agree with the count path", tc.name)
				}
			})
		}
	}
}

// TestRestrictToIDsNilAndEmptyDiffer pins the contract the store walkers rely
// on (memdb_summaries.go, pebble_store.go): nil means "no ID restriction" and
// empty means "no book is eligible". Collapsing the two — testing with len()
// instead of against nil — turns a narrowing request into a full-library
// response, which is strictly worse than the bug being fixed.
func TestRestrictToIDsNilAndEmptyDiffer(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()

	fixtures := seedPushdownBooks(t, ps)
	svc := NewAudiobookService(ps)

	nilGot, nilTotal, err := svc.GetAudiobooksWithTotal(
		context.Background(), 1000, 0, "", nil, nil, ListFilters{RestrictToIDs: nil})
	require.NoError(t, err)
	require.Len(t, nilGot, len(fixtures), "nil RestrictToIDs must mean NO restriction")
	nilCount, err := svc.CountAudiobooksFiltered(context.Background(), ListFilters{RestrictToIDs: nil})
	require.NoError(t, err)
	require.Equal(t, len(fixtures), nilCount, "nil RestrictToIDs must count the whole library")
	if nilTotal != -1 {
		require.Equal(t, len(fixtures), nilTotal)
	}

	emptyGot, emptyTotal, err := svc.GetAudiobooksWithTotal(
		context.Background(), 1000, 0, "", nil, nil,
		ListFilters{RestrictToIDs: map[string]struct{}{}})
	require.NoError(t, err)
	require.Empty(t, emptyGot, "non-nil empty RestrictToIDs must mean NO book is eligible")
	emptyCount, err := svc.CountAudiobooksFiltered(context.Background(),
		ListFilters{RestrictToIDs: map[string]struct{}{}})
	require.NoError(t, err)
	require.Equal(t, 0, emptyCount, "an empty restriction must count 0, not the library size")
	if emptyTotal != -1 {
		require.Equal(t, 0, emptyTotal)
	}
}
