// file: internal/server/handlers/abs/item_filter_sort_test.go
// version: 1.0.0
// guid: 6f2b9d41-3a58-4c07-8e13-5b9a2c6f0d87
// last-edited: 2026-08-25
//
// ── sorting inside a ?filter= drill-down ────────────────────────────────────
//
// filteredItems served the series/author/narrator views straight from a
// precomputed id slice and never read the `sort` query parameter, so every
// entry in the client's 14-item Sort By menu -- Title included -- silently
// returned the group's own order.
//
// The discriminating case is a SORTED, PAGINATED request. Ask for the whole
// group and a broken implementation returns the same books as a working one,
// merely in a different order, which is easy to squint past. Ask for one book
// of two, descending, and the two implementations return DIFFERENT BOOKS:
// slicing before sorting yields the group's first book, sorting before slicing
// yields its last.

package abs_test

import (
	"net/http"
	"strconv"
	"testing"
)

// absItemsSorted issues a filtered request with explicit sort/paging controls.
// absItemsFiltered pins limit=50, which cannot express the page-boundary case
// this file exists to test.
func absItemsSorted(t *testing.T, h *harness, tok, filter, sort string, desc bool, limit int) map[string]any {
	t.Helper()
	path := "/api/libraries/" + h.libraryID() + "/items?page=0"
	if limit > 0 {
		path += "&limit=" + strconv.Itoa(limit)
	}
	if filter != "" {
		path += "&filter=" + filter
	}
	if sort != "" {
		path += "&sort=" + sort
	}
	if desc {
		path += "&desc=1"
	}
	code, body := h.doAny(t, request{method: http.MethodGet, path: path, headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("GET items(filter=%q sort=%q desc=%v) = %d, want 200",
			filter, sort, desc, code)
	}
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body is %T, want an object", body)
	}
	return m
}

func absSortTestLogin(t *testing.T) (*harness, string) {
	t.Helper()
	seed := absSeedTwoSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")
	return h, tok
}

func TestLibraryItems_FilteredViewHonoursSort(t *testing.T) {
	h, tok := absSortTestLogin(t)
	series := absFilterToken("series", "10")

	// Baseline: no sort keeps the series' reading order, which is a better
	// default than any field and must not regress.
	got := absItemsSorted(t, h, tok, series, "", false, 50)
	if titles := absItemTitles(got); len(titles) != 2 ||
		titles[0] != "Odyssey Book One" || titles[1] != "Odyssey Book Two" {
		t.Fatalf("unsorted series view = %v, want series sequence order", absItemTitles(got))
	}

	// Descending title must REVERSE that. Sequence order and title-ascending
	// order coincide in this fixture, so only the descending case can tell a
	// sort that ran from one that was ignored.
	got = absItemsSorted(t, h, tok, series, "media.metadata.title", true, 50)
	titles := absItemTitles(got)
	if len(titles) != 2 {
		t.Fatalf("sorted series view returned %d items %v, want 2", len(titles), titles)
	}
	if titles[0] != "Odyssey Book Two" || titles[1] != "Odyssey Book One" {
		t.Fatalf("sort=title desc inside a series returned %v, want "+
			"[Odyssey Book Two, Odyssey Book One].\nThe series' own order is "+
			"[One, Two]; getting it back means the sort parameter was ignored.",
			titles)
	}
}

// 🔴 THE PAGE-BOUNDARY CASE. This is the one that distinguishes "sorted the
// whole filtered set, then paginated" from "paginated, then sorted the page" --
// the latter looks correct on a single-page response and is wrong everywhere
// else.
func TestLibraryItems_FilteredViewSortsBeforePaginating(t *testing.T) {
	h, tok := absSortTestLogin(t)
	series := absFilterToken("series", "10")

	got := absItemsSorted(t, h, tok, series, "media.metadata.title", true, 1)
	titles := absItemTitles(got)
	if len(titles) != 1 {
		t.Fatalf("limit=1 returned %d items %v, want exactly 1", len(titles), titles)
	}
	if titles[0] != "Odyssey Book Two" {
		t.Fatalf("first page of a title-descending series view = %q, want "+
			"%q.\nGetting %q means the page was cut from the series' own order "+
			"first and only then sorted, so the sort only ever reorders the rows "+
			"already on screen.",
			titles[0], "Odyssey Book Two", "Odyssey Book One")
	}

	// total stays the size of the FILTERED set regardless of sorting, or the
	// client pages forever into empty results.
	if total, _ := got["total"].(float64); int(total) != 2 {
		t.Fatalf("total = %v, want 2 (the filtered set, not the page)", got["total"])
	}
}

// A sort the drill-down cannot resolve must leave the group's order alone
// rather than emitting an arbitrary one.
func TestLibraryItems_FilteredViewIgnoresUnknownSort(t *testing.T) {
	h, tok := absSortTestLogin(t)
	series := absFilterToken("series", "10")

	got := absItemsSorted(t, h, tok, series, "media.metadata.nonsenseField", false, 50)
	titles := absItemTitles(got)
	if len(titles) != 2 || titles[0] != "Odyssey Book One" || titles[1] != "Odyssey Book Two" {
		t.Fatalf("unknown sort field returned %v, want the series' own order", titles)
	}
}
