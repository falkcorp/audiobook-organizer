// file: internal/server/handlers/abs/authors_cache_test.go
// version: 1.0.0
// guid: 6b90d248-1f37-4c05-a3e8-71d24f9068ba
// last-edited: 2026-08-03

package abs_test

import (
	"net/http"
	"testing"
)

// AudioBooth pages authors 100 at a time and its jump-to-letter feature keeps
// loading pages until the target letter appears (AuthorsPageModel: itemsPerPage=100,
// hasMorePages = currentPage*itemsPerPage < total). With ~9,200 authors, jumping to
// "Z" is ~93 consecutive requests.
//
// Each of those used to call GetAllAuthorBookCounts — by its own description a "Full
// Pebble book scan combined with junction table scan", walking all 44,888 books. 93
// full library scans is why reaching the end of the alphabet took ~37 seconds.

// 🔴 TestAuthors_ListBuiltOncePerTTL is the regression. The value under test is the
// NUMBER OF REBUILDS across a burst of page requests, because that is what the
// jump-to-letter interaction actually generates.
func TestAuthors_ListBuiltOncePerTTL(t *testing.T) {
	w := newWriteHarness(t)

	// Prime, then simulate the client walking pages toward a letter.
	w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/authors?limit=100&page=0", nil)
	before := w.seed.lib.authorCountCalls()

	for page := 1; page <= 20; page++ {
		code, _, raw := w.req(t, http.MethodGet,
			"/api/libraries/"+w.libraryID()+"/authors?limit=100&page="+itoa(page), nil)
		if code != http.StatusOK {
			t.Fatalf("page %d = %d %s", page, code, raw)
		}
	}

	if after := w.seed.lib.authorCountCalls(); after != before {
		t.Fatalf("author list rebuilt %d times across 20 paged requests — each rebuild is a "+
			"full library scan, and the client issues up to 93 of these in a row", after-before)
	}
}

// TestAuthors_CachedListStillServesCorrectPages — a cache that returns stale or
// wrongly-sliced pages would be worse than the slow path it replaces.
func TestAuthors_CachedListStillServesCorrectPages(t *testing.T) {
	w := newWriteHarness(t)

	_, all, _ := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/authors", nil)
	full := requireArray(t, all, "authors")
	if len(full) < 2 {
		t.Skipf("need >=2 authors, have %d", len(full))
	}

	// Page through with limit=1 and confirm we see each author exactly once, in order.
	var seen []string
	for page := 0; page < len(full); page++ {
		_, body, _ := w.req(t, http.MethodGet,
			"/api/libraries/"+w.libraryID()+"/authors?limit=1&page="+itoa(page), nil)
		results := requireArray(t, body, "results")
		if len(results) != 1 {
			t.Fatalf("page %d returned %d results, want 1", page, len(results))
		}
		item, _ := results[0].(map[string]any)
		name, _ := item["name"].(string)
		seen = append(seen, name)
		if got := int(requireNum(t, body, "total")); got != len(full) {
			t.Fatalf("page %d total = %d, want %d", page, got, len(full))
		}
	}
	for i, entry := range full {
		want, _ := entry.(map[string]any)
		if seen[i] != want["name"] {
			t.Fatalf("paged order diverged at %d: got %q want %v", i, seen[i], want["name"])
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
