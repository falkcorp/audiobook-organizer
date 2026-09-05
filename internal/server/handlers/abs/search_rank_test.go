// file: internal/server/handlers/abs/search_rank_test.go
// version: 1.1.0
// guid: 3d9b7e1a-5c2f-4a8e-b6d1-9f0e2c4a7b3d
// last-edited: 2026-09-05

package abs

import (
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func idsOf(s []database.Series) []int {
	out := make([]int, 0, len(s))
	for _, x := range s {
		out = append(out, x.ID)
	}
	return out
}

// An exact name outranks a prefix, which outranks a substring — and the cap is
// applied AFTER ranking, so the series the user typed is never cut for
// twenty-five series that merely contain the word. With counts unknown (nil)
// nothing is filtered. Matching is article-insensitive, so "A Hunter's Moon"
// is a prefix match for "hunter", ahead of the substring match.
func TestRankSeriesMatches_ExactThenPrefixThenContains(t *testing.T) {
	all := []database.Series{
		{ID: 1, Name: "A Hunter's Moon"},
		{ID: 2, Name: "Bounty Hunter"},
		{ID: 3, Name: "Hunter"},
		{ID: 4, Name: "Hunter Killer"},
		{ID: 5, Name: "Zebra"},
	}
	got := idsOf(rankSeriesMatches(all, "hunter", nil, 3))
	if want := []int{3, 1, 4}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The production shape of 2026-09-05: many duplicate rows named for one series,
// most with no books. Empty rows are not results; among the populated ones the
// article-insensitive exact match wins, the one with the most books first, and
// a "Primal Hunter 4: ..." prefix match does not outrank "The Primal Hunter".
func TestRankSeriesMatches_DropsEmptyAndPrefersPopulatedExact(t *testing.T) {
	all := []database.Series{
		{ID: 206295, Name: "Primal Hunter 4: A LitRPG Adventure (The Primal Hunter)"},
		{ID: 211617, Name: "The Primal Hunter"},
		{ID: 220015, Name: "The Primal Hunter"},
		{ID: 145783, Name: "The Primal Hunter"},
		{ID: 193157, Name: "The Primal Hunter 10"},
		{ID: 201343, Name: "The Primal Hunter 11"},
	}
	counts := map[int]int{206295: 1, 211617: 1, 145783: 12, 193157: 1}
	got := idsOf(rankSeriesMatches(all, "primal hunter", counts, 25))
	if want := []int{145783, 211617, 206295, 193157}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v (empty 220015 and 201343 dropped)", got, want)
	}
	// The user typing the article changes nothing.
	if got2 := idsOf(rankSeriesMatches(all, "the primal hunter", counts, 25)); !slices.Equal(got2, got) {
		t.Fatalf("with article: got %v, want %v", got2, got)
	}
}

// The bound holds when every entry was built in the same instant.
func TestSearchStore_BoundHoldsUnderFrozenClock(t *testing.T) {
	frozen := time.Unix(1_800_000_000, 0)
	h := &Handler{now: func() time.Time { return frozen }}
	for i := 0; i < absSearchCacheMax+10; i++ {
		h.searchStore(searchCacheKey("lib", string(rune('a'+i%26))+string(rune('a'+i/26))), emptySearchResponse())
	}
	if n := len(h.searchCache); n > absSearchCacheMax {
		t.Fatalf("cache holds %d entries, bound is %d", n, absSearchCacheMax)
	}
}
