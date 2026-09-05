// file: internal/server/handlers/abs/search_author_test.go
// version: 1.1.0
// guid: 6c1f0d2e-7b3a-4c5d-9e8f-2a1b3c4d5e6f
// last-edited: 2026-09-05

package abs_test

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// firstSeries returns the id and name of the first series the library lists.
func firstSeries(t *testing.T, h *harness, tok string) (string, string) {
	t.Helper()
	_, listed := h.do(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/series?limit=1&page=0",
		headers: bearer(tok),
	})
	results := listed["results"].([]any)
	if len(results) == 0 {
		t.Fatal("oracle library has no series")
	}
	s := results[0].(map[string]any)
	return s["id"].(string), s["name"].(string)
}

func search(t *testing.T, h *harness, tok, q string) map[string]any {
	t.Helper()
	code, body := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/search?q=" + url.QueryEscape(q),
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("search %q = %d, want 200; body=%#v", q, code, body)
	}
	return body.(map[string]any)
}

// 🔴 A SERIES SEARCH HIT MUST CARRY ITS BOOKS. The client draws the tile from
// the books' covers; an empty array is the black tile the user reported, and
// it still opens the series because the id was right.
func TestSearch_SeriesHitCarriesBooksAndNestedSeries(t *testing.T) {
	seed := absSeedTwoSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	login := h.login(t, "oracle", "pw-pw-pw-pw")
	tok := str(t, userObj(t, login), "accessToken")
	seriesID, seriesName := firstSeries(t, h, tok)

	body := search(t, h, tok, seriesName)
	hits := body["series"].([]any)
	if len(hits) == 0 {
		t.Fatalf("search %q returned no series hit", seriesName)
	}
	hit := hits[0].(map[string]any)
	if hit["id"] != seriesID {
		t.Fatalf("hit id = %v, want %v", hit["id"], seriesID)
	}
	books, _ := hit["books"].([]any)
	if len(books) == 0 {
		t.Fatalf("series hit has no books; the tile would be black: %#v", hit)
	}
	item := books[0].(map[string]any)
	media, _ := item["media"].(map[string]any)
	if _, ok := media["coverPath"]; !ok {
		t.Fatalf("series hit book has no media.coverPath: %#v", item)
	}
	nested, _ := hit["series"].(map[string]any)
	if nested["id"] != seriesID || nested["name"] != seriesName {
		t.Fatalf("nested series object = %#v, want id %v name %q", nested, seriesID, seriesName)
	}
	if _, hasBooks := nested["books"]; hasBooks {
		t.Error("nested series object should not repeat the books array")
	}
}

// 🔴 THE AUTHOR PAGE IS BUILT FROM ?include=items,series. Without the
// expansions the page rendered nothing off a book.
func TestAuthorDetail_IncludeItemsAndSeries(t *testing.T) {
	h, _, tok := newBrowseHarness(t)
	_, listed := h.do(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/authors",
		headers: bearer(tok),
	})
	authors := listed["authors"].([]any)
	if len(authors) == 0 {
		t.Fatal("oracle library has no authors")
	}
	author := authors[0].(map[string]any)
	id := author["id"].(string)
	numBooks := int(author["numBooks"].(float64))

	code, raw := h.doAny(t, request{
		method: http.MethodGet, path: "/api/authors/" + id + "?include=items,series", headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("author detail = %d, want 200; body=%#v", code, raw)
	}
	body := raw.(map[string]any)
	if body["id"] != id || body["name"] != author["name"] {
		t.Fatalf("detail %#v does not match listed author %#v", body, author)
	}
	items, ok := body["libraryItems"].([]any)
	if !ok {
		t.Fatalf("include=items but no libraryItems array: %#v", body)
	}
	if len(items) != numBooks {
		t.Fatalf("libraryItems = %d, want the tile's numBooks %d", len(items), numBooks)
	}
	if _, ok := body["series"].([]any); !ok {
		t.Fatalf("include=series but no series array: %#v", body)
	}

	// Without include, the expansions are absent — the bare row real ABS serves.
	_, bare := h.do(t, request{method: http.MethodGet, path: "/api/authors/" + id, headers: bearer(tok)})
	if _, present := bare["libraryItems"]; present {
		t.Errorf("bare author detail should not carry libraryItems: %#v", bare)
	}
	if _, present := bare["series"]; present {
		t.Errorf("bare author detail should not carry series: %#v", bare)
	}
}

// A repeated query inside absSearchCacheTTL is answered from the cached
// document: the store is searched once. After the TTL it is searched again,
// and a different query is never served from another query's document.
func TestSearch_RepeatWithinTTLIsCached(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	base := time.Unix(1_800_000_000, 0)
	now := base
	h.handler.SetClock(func() time.Time { return now })

	first := search(t, h, tok, "Odyssey")
	if got := seed.lib.searchCalls(); got != 1 {
		t.Fatalf("first search hit the store %d times, want 1", got)
	}
	// Case and surrounding space fold into the same key.
	second := search(t, h, tok, " odyssey")
	if got := seed.lib.searchCalls(); got != 1 {
		t.Fatalf("repeat within TTL hit the store again (%d calls); not cached", got)
	}
	if len(first["book"].([]any)) != len(second["book"].([]any)) {
		t.Fatalf("cached document differs from the original: %d vs %d books",
			len(first["book"].([]any)), len(second["book"].([]any)))
	}

	search(t, h, tok, "different query")
	if got := seed.lib.searchCalls(); got != 2 {
		t.Fatalf("a different query must not be served from another query's cache (%d calls)", got)
	}

	now = base.Add(3 * time.Minute)
	search(t, h, tok, "odyssey")
	if got := seed.lib.searchCalls(); got != 3 {
		t.Fatalf("after the TTL the query should be recomputed (%d calls, want 3)", got)
	}
}

// Genres come from the cached /filterdata document. Two DIFFERENT searches
// must not each pay the full-keyspace genre scan: on production that scan
// was 4.8s of every 7.5s search.
func TestSearch_GenresComeFromFilterData(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	search(t, h, tok, "odyssey")
	search(t, h, tok, "another")
	if got := seed.lib.genreScanCalls(); got != 1 {
		t.Fatalf("two searches ran the genre scan %d times, want 1 (one filterdata build)", got)
	}
}

// 🔴 A DEGRADED SEARCH DOCUMENT IS NEVER CACHED. When an optional source
// fails, the request is still served (with that list empty) but the next
// request must rebuild, so the phone does not retry into the same quietly
// empty answer for two minutes.
func TestSearch_DegradedDocumentIsNotCached(t *testing.T) {
	seed := absSeedTwoSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	login := h.login(t, "oracle", "pw-pw-pw-pw")
	tok := str(t, userObj(t, login), "accessToken")
	_, seriesName := firstSeries(t, h, tok)

	seed.lib.setSeriesCountsErr(errors.New("counts unavailable"))
	body := search(t, h, tok, seriesName)
	if len(body["series"].([]any)) == 0 {
		t.Fatal("a degraded search should still serve the series it could build")
	}
	search(t, h, tok, seriesName)
	if got := seed.lib.searchCalls(); got != 2 {
		t.Fatalf("degraded document was replayed from cache (%d store searches, want 2)", got)
	}

	seed.lib.setSeriesCountsErr(nil)
	search(t, h, tok, seriesName)
	search(t, h, tok, seriesName)
	if got := seed.lib.searchCalls(); got != 3 {
		t.Fatalf("once the sources are healthy the document should be cached again (%d store searches, want 3)", got)
	}
}

// 🔴 A HYDRATION FAILURE IS A 500, NOT AN EMPTY LIST. The same body carries
// numBooks from the index; "12 books" beside "libraryItems: []" is the empty
// author page this route exists to fix, with no error to show for it.
func TestAuthorDetail_HydrationFailureIsAnError(t *testing.T) {
	h, seed, tok := newBrowseHarness(t)
	_, listed := h.do(t, request{
		method: http.MethodGet, path: "/api/libraries/" + h.libraryID() + "/authors", headers: bearer(tok),
	})
	author := listed["authors"].([]any)[0].(map[string]any)
	id := author["id"].(string)

	seed.lib.setBooksByIDsErr(errors.New("pebble: closed"))
	code, body := h.doAny(t, request{
		method: http.MethodGet, path: "/api/authors/" + id + "?include=items", headers: bearer(tok),
	})
	if code != http.StatusInternalServerError {
		t.Fatalf("author detail with a failing store = %d, want 500; body=%#v", code, body)
	}
	// Without the include nothing is hydrated, so the bare row still serves.
	code, _ = h.doAny(t, request{method: http.MethodGet, path: "/api/authors/" + id, headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("bare author detail should not depend on hydration; got %d", code)
	}
}

// A series row no book references is not a search result. The oracle seed
// holds "Odyssey Cycle" (id 10, with books); an orphan duplicate "Odyssey
// Cycle" (id 30, no books) is added beside it — the production shape where
// 16 of 25 "primal hunter" hits were empty duplicates rendering as black
// tiles. Search returns only the populated row, and returns it first.
func TestSearch_EmptyDuplicateSeriesIsNotAHit(t *testing.T) {
	seed := absSeedTwoSeries(t)
	seed.lib.series[30] = &database.Series{ID: 30, Name: "Odyssey Cycle"}
	seed.lib.series[31] = &database.Series{ID: 31, Name: "Odyssey Cycle 2: Nothing"}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	login := h.login(t, "oracle", "pw-pw-pw-pw")
	tok := str(t, userObj(t, login), "accessToken")

	hits := search(t, h, tok, "odyssey")["series"].([]any)
	if len(hits) != 1 {
		ids := []any{}
		for _, x := range hits {
			ids = append(ids, x.(map[string]any)["id"])
		}
		t.Fatalf("search returned %d series %v, want only the populated id 10", len(hits), ids)
	}
	hit := hits[0].(map[string]any)
	if hit["id"] != "10" || len(hit["books"].([]any)) == 0 {
		t.Fatalf("hit = id %v with %d books, want id 10 with books", hit["id"], len(hit["books"].([]any)))
	}
}

// When the count source fails the filter cannot run: the empty rows are served
// (a degraded document, not cached) rather than every series vanishing.
func TestSearch_CountsFailureServesUnfilteredSeriesUncached(t *testing.T) {
	seed := absSeedTwoSeries(t)
	seed.lib.series[30] = &database.Series{ID: 30, Name: "Odyssey Cycle"}
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	login := h.login(t, "oracle", "pw-pw-pw-pw")
	tok := str(t, userObj(t, login), "accessToken")

	seed.lib.setSeriesCountsErr(errors.New("counts index unavailable"))
	if n := len(search(t, h, tok, "odyssey")["series"].([]any)); n != 2 {
		t.Fatalf("degraded search returned %d series, want both (unfiltered)", n)
	}
	seed.lib.setSeriesCountsErr(nil)
	if n := len(search(t, h, tok, "odyssey")["series"].([]any)); n != 1 {
		t.Fatalf("after recovery search returned %d series, want 1 — the degraded document was cached", n)
	}
}
