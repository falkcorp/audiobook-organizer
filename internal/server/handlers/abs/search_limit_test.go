// file: internal/server/handlers/abs/search_limit_test.go
// version: 1.0.0
// guid: 10053598-c905-49f2-81c3-654a372db5b5
// last-edited: 2026-09-08
//
// ABS search takes a `limit` and does NOT paginate — there is no second page to
// recover a dropped hit from. So the load-bearing property is not "the list is
// short", it is "what falls off the end is the least relevant, never the
// arbitrary." These tests pin that.

package abs

import (
	"fmt"
	"testing"
)

func TestParseSearchLimit_DefaultsToTheABSDefault(t *testing.T) {
	for _, raw := range []string{"", "   ", "0", "-5", "abc"} {
		if got := parseSearchLimit(raw); got != searchResultLimit {
			t.Fatalf("parseSearchLimit(%q) = %d, want the default %d", raw, got, searchResultLimit)
		}
	}
}

func TestParseSearchLimit_ClampsInsteadOfRejecting(t *testing.T) {
	if got := parseSearchLimit("100000"); got != searchResultLimitMax {
		t.Fatalf("parseSearchLimit(100000) = %d, want it clamped to %d", got, searchResultLimitMax)
	}
	if got := parseSearchLimit("5"); got != 5 {
		t.Fatalf("parseSearchLimit(5) = %d, want 5 honoured verbatim", got)
	}
	// The previous hardcoded behaviour was 25 for books and series. Asking for
	// the ceiling must still reproduce it, so nothing reachable before this
	// change became unreachable after it.
	if got := parseSearchLimit("25"); got != 25 {
		t.Fatalf("parseSearchLimit(25) = %d, want 25 — the old ceiling must stay reachable", got)
	}
}

func TestSearchCacheKey_VariesByLimit(t *testing.T) {
	// Without the limit in the key, whichever caller arrived first would have
	// its document replayed to a caller asking for a different size, for the
	// whole 2-minute TTL.
	if searchCacheKey("lib", "dune", 12) == searchCacheKey("lib", "dune", 25) {
		t.Fatal("cache key ignores the limit; two different requests would share one document")
	}
	if searchCacheKey("lib", "Dune ", 12) != searchCacheKey("lib", "dune", 12) {
		t.Fatal("cache key stopped folding case/space; that fold is deliberate")
	}
}

// TestRankNameMatches_ExactMatchSurvivesTruncation is the important one. A naive
// `break` at the limit takes items in index order, so an exact match sitting
// late in the index is silently dropped in favour of arbitrary earlier
// substring hits — and with no pagination the user simply never sees it.
func TestRankNameMatches_ExactMatchSurvivesTruncation(t *testing.T) {
	names := make([]string, 0, 200)
	for i := range 199 {
		// Every one of these contains "hell" as a substring, so all of them
		// match, and all of them sort before the exact match by index.
		names = append(names, fmt.Sprintf("Michelle Filler %03d", i))
	}
	names = append(names, "Hell") // the exact match, dead last

	got := rankNameMatches(names, "hell", 3, func(s string) string { return s })

	if len(got) != 3 {
		t.Fatalf("returned %d items, want 3", len(got))
	}
	if got[0] != "Hell" {
		t.Fatalf("got[0] = %q, want the exact match %q ranked first — truncating an unranked list loses it", got[0], "Hell")
	}
}

func TestRankNameMatches_PrefixBeatsSubstring(t *testing.T) {
	names := []string{"Michelle Substring", "Hellboy Prefix", "Hell"}

	got := rankNameMatches(names, "hell", 3, func(s string) string { return s })

	want := []string{"Hell", "Hellboy Prefix", "Michelle Substring"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rank %d = %q, want %q (tiers: exact, prefix, substring)", i, got[i], want[i])
		}
	}
}

func TestRankNameMatches_DropsNonMatchesEntirely(t *testing.T) {
	got := rankNameMatches([]string{"Dune", "Neuromancer"}, "hell", 10, func(s string) string { return s })
	if len(got) != 0 {
		t.Fatalf("returned %v for a query nothing matches", got)
	}
}

func TestRankNameMatches_KeepsIndexOrderWithinATier(t *testing.T) {
	// Stability matters: two equally-relevant names must not reorder between
	// requests, or the list shuffles under the user as they type.
	names := []string{"Hell A", "Hell B", "Hell C"}
	got := rankNameMatches(names, "hell", 3, func(s string) string { return s })
	for i := range names {
		if got[i] != names[i] {
			t.Fatalf("tier order not stable: got %v, want %v", got, names)
		}
	}
}
