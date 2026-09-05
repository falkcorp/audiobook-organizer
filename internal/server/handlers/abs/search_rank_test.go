// file: internal/server/handlers/abs/search_rank_test.go
// version: 1.0.0
// guid: 3d9b7e1a-5c2f-4a8e-b6d1-9f0e2c4a7b3d
// last-edited: 2026-09-05

package abs

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// An exact name outranks a prefix, which outranks a substring — and the cap is
// applied AFTER ranking, so the series the user typed is never cut for
// twenty-five series that merely contain the word.
func TestRankSeriesMatches_ExactThenPrefixThenContains(t *testing.T) {
	all := []database.Series{
		{ID: 1, Name: "A Hunter's Moon"},
		{ID: 2, Name: "Bounty Hunter"},
		{ID: 3, Name: "Hunter"},
		{ID: 4, Name: "Hunter Killer"},
		{ID: 5, Name: "Zebra"},
	}
	got := rankSeriesMatches(all, "hunter", 3)
	want := []int{3, 4, 1}
	if len(got) != len(want) {
		t.Fatalf("got %d matches, want %d: %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("rank %d = %q (id %d), want id %d", i, got[i].Name, got[i].ID, id)
		}
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
