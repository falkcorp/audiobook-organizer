// file: internal/server/handlers/abs/series_pagination_test.go
// version: 1.0.0
// guid: 9c4a17e5-62b8-4d03-a7f1-3e58b0d94c27
// last-edited: 2026-08-13

package abs_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ── the series list must honour page/limit ──────────────────────────────────
//
// 🔴 WHAT WAS BROKEN. LibrarySeries accepted `page`, `limit` and `sort` and read
// none of them. Confirmed against production 2026-08-13: `?limit=100` and
// `?limit=500` both returned all 14,625 series, and the app's own
// `?limit=50&page=2&sort=name` got page 0, unsorted, every single time.
//
// 🔴 WHY IT STOPPED BEING COSMETIC. Populating `books` on each series row (the
// sibling fix) takes the unpaginated response from 3.36 MB to roughly 10.8 MB —
// 31,139 book rows — which is not a payload to hand a phone that asked for 50.
//
// 🔴 WHY THE FIXTURE HAS SEVEN SERIES. A fixture smaller than the page size makes
// every clamp dead code under a green suite: with 3 series and a limit of 10 there
// is only ever one page, so slicing, the end-clamp and the past-the-end case are
// all unexercised. Seven series at limit 2 gives four pages, the last one partial,
// plus a page past the end.

// absPgSeedSeries seeds seven series whose alphabetical order differs from their
// id order, so a handler that "paginates" by id rather than by the sorted order
// cannot pass by accident.
func absPgSeedSeries(t *testing.T) *oracleSeed {
	t.Helper()
	seed := seedOracleLibrary(t)

	intp := func(i int) *int { return &i }
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }

	// id order and name order are deliberately uncorrelated.
	names := map[int]string{
		70: "Alpha Chronicles",
		30: "Bravo Cycle",
		90: "Charlie Saga",
		10: "Delta Sequence",
		50: "Echo Arc",
		80: "Foxtrot Run",
		20: "Golf Trilogy",
	}
	i := 0
	for id, name := range names {
		seed.lib.series[id] = &database.Series{ID: id, Name: name}
		// One organized, primary book per series so absItemFilterBase does not
		// filter the series into emptiness for an unrelated reason.
		seed.lib.addBook(&database.Book{
			ID: fmt.Sprintf("pg-b%d", id), Title: fmt.Sprintf("Book of %s", name),
			SeriesID: intp(id), SeriesSequence: intp(1), Duration: intp(600),
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
		}, nil, nil)
		i++
	}
	return seed
}

// absPgHarness seeds the seven-series library and returns a logged-in harness.
func absPgHarness(t *testing.T) (*harness, string) {
	t.Helper()
	seed := absPgSeedSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	return h, tok
}

// absPgFetch returns (names in response order, total field) for one series request.
func absPgFetch(t *testing.T, h *harness, tok, query string) ([]string, int) {
	t.Helper()
	path := "/api/libraries/" + h.libraryID() + "/series"
	if query != "" {
		path += "?" + query
	}
	code, body := h.doAny(t, request{method: http.MethodGet, path: path, headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, code)
	}
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body is %T, want object", body)
	}
	total, ok := m["total"].(float64)
	if !ok {
		t.Fatalf("total is %T (%v), want a number", m["total"], m["total"])
	}
	results, _ := m["results"].([]any)
	names := make([]string, 0, len(results))
	for _, r := range results {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		names = append(names, name)
	}
	return names, int(total)
}

// TestLibrarySeries_PagesPartitionTheSeriesSet asserts the property that actually
// matters: walking every page yields each series EXACTLY ONCE and misses none.
//
// Per-page length is the weak assertion — it passes against a handler that returns
// the same page forever, which is the bug being fixed. Partitioning does not.
func TestLibrarySeries_PagesPartitionTheSeriesSet(t *testing.T) {
	h, tok := absPgHarness(t)

	all, total := absPgFetch(t, h, tok, "")
	if total != 7 {
		t.Fatalf("unpaginated total = %d, want 7", total)
	}
	if len(all) != 7 {
		t.Fatalf("unpaginated returned %d series, want all 7 (an absent limit must not paginate)", len(all))
	}

	const limit = 2
	seen := map[string]int{}
	var walked []string
	for page := 0; page < 5; page++ { // 4 real pages + 1 past the end
		names, pageTotal := absPgFetch(t, h, tok, fmt.Sprintf("limit=%d&page=%d", limit, page))

		// Total must be the FULL count on every page. If it were len(results) the
		// client's `page*limit < total` test would stop it after page 0.
		if pageTotal != 7 {
			t.Errorf("page %d reported total = %d, want the full 7 — the client uses this to decide whether more pages exist", page, pageTotal)
		}
		if len(names) > limit {
			t.Errorf("page %d returned %d series, want at most limit=%d", page, len(names), limit)
		}
		for _, n := range names {
			seen[n]++
			walked = append(walked, n)
		}
	}

	if len(walked) != 7 {
		t.Errorf("walking all pages yielded %d series, want exactly 7 (got %v)", len(walked), walked)
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("series %q appeared on %d pages, want exactly 1 — pages must partition the set", name, n)
		}
	}
	for _, name := range all {
		if seen[name] == 0 {
			t.Errorf("series %q never appeared on any page — pages must cover the whole set", name)
		}
	}
}

// TestLibrarySeries_PageOrderIsTheSortedOrderNotInsertionOrder pins the order the
// pages are cut from. Paginating an unspecified order lets pages overlap and skip,
// so the sort is a prerequisite for the test above rather than a nicety.
func TestLibrarySeries_PageOrderIsTheSortedOrderNotInsertionOrder(t *testing.T) {
	h, tok := absPgHarness(t)

	want := []string{
		"Alpha Chronicles", "Bravo Cycle", "Charlie Saga", "Delta Sequence",
		"Echo Arc", "Foxtrot Run", "Golf Trilogy",
	}

	all, _ := absPgFetch(t, h, tok, "")
	if len(all) != len(want) {
		t.Fatalf("got %d series, want %d", len(all), len(want))
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("series order = %v,\n                  want %v", all, want)
		}
	}

	// The same order must survive being cut into pages: page 1 at limit 2 is the
	// third and fourth entries, not an arbitrary pair.
	page1, _ := absPgFetch(t, h, tok, "limit=2&page=1")
	if len(page1) != 2 || page1[0] != want[2] || page1[1] != want[3] {
		t.Errorf("page 1 (limit 2) = %v, want %v", page1, want[2:4])
	}
}

// TestLibrarySeries_LimitIsActuallyApplied is the direct regression for the
// production observation: limit=100 and limit=500 both returned all 14,625.
func TestLibrarySeries_LimitIsActuallyApplied(t *testing.T) {
	h, tok := absPgHarness(t)

	for _, tc := range []struct {
		query string
		want  int
		why   string
	}{
		{"limit=1", 1, "a limit below the set size must truncate"},
		{"limit=3", 3, "a limit below the set size must truncate"},
		{"limit=100", 7, "a limit above the set size returns what exists, not an error"},
		{"limit=0", 7, "limit=0 means unlimited — every non-app caller keeps today's response"},
		{"", 7, "an absent limit means unlimited"},
		{"limit=2&page=3", 1, "the final page is partial, not padded"},
		{"limit=2&page=99", 0, "past the end is an empty page, not a wrapped-around one"},
		{"limit=-5", 7, "a garbage limit falls back to unlimited rather than erroring"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			names, total := absPgFetch(t, h, tok, tc.query)
			if len(names) != tc.want {
				t.Errorf("%q returned %d series, want %d — %s (got %v)", tc.query, len(names), tc.want, tc.why, names)
			}
			if total != 7 {
				t.Errorf("%q reported total = %d, want the full 7", tc.query, total)
			}
		})
	}
}
