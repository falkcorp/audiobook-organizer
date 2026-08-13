// file: internal/server/handlers/abs/item_filter_series_test.go
// version: 1.0.0
// guid: 2b6e91d4-70c3-4a58-b1f9-4d82e75c3a06
// last-edited: 2026-08-13

package abs_test

import (
	"encoding/base64"
	"net/http"
	"testing"
)

// ── ?filter= must actually filter ───────────────────────────────────────────
//
// 🔴 THE DEFECT. Nothing read the filter parameter, so every drill-down answered
// with the WHOLE LIBRARY. Proven on production 2026-08-13 with three requests to
// /items differing only in the filter — none, a real series, and a fabricated
// series id — all returning total=34280 with the same first title.
//
// That is the reported "shows random books for every series, and the books it
// shows are random too".
//
// FORMAT CONFIRMED BY OBSERVATION, not inferred: the server's own log carries a
// real request from the app, filter=series.MTQ3OTI0&page=0&limit=100, where
// MTQ3OTI0 is base64 "147924" — a live series id. No fixture shows a filter at
// all, so the log was the only oracle.

func absFilterToken(group, value string) string {
	return group + "." + base64.StdEncoding.EncodeToString([]byte(value))
}

func absItemsFiltered(t *testing.T, h *harness, tok, filter string) map[string]any {
	t.Helper()
	path := "/api/libraries/" + h.libraryID() + "/items?limit=50&page=0"
	if filter != "" {
		path += "&filter=" + filter
	}
	code, body := h.doAny(t, request{method: http.MethodGet, path: path, headers: bearer(tok)})
	if code != http.StatusOK {
		t.Fatalf("GET items(filter=%q) = %d, want 200 — a 4xx reads to the client as "+
			"\"endpoint unsupported\" and disables browsing", filter, code)
	}
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body is %T, want an object", body)
	}
	return m
}

func absItemTitles(m map[string]any) []string {
	results, _ := m["results"].([]any)
	out := make([]string, 0, len(results))
	for _, r := range results {
		item, _ := r.(map[string]any)
		media, _ := item["media"].(map[string]any)
		meta, _ := media["metadata"].(map[string]any)
		title, _ := meta["title"].(string)
		out = append(out, title)
	}
	return out
}

func TestLibraryItems_SeriesFilterReturnsOnlyThatSeries(t *testing.T) {
	seed := absSeedTwoSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	unfiltered := absItemsFiltered(t, h, tok, "")
	all := absItemTitles(unfiltered)
	if len(all) < 3 {
		t.Fatalf("unfiltered library has %d items (%v); the comparison below is "+
			"meaningless unless it holds more books than one series", len(all), all)
	}

	got := absItemsFiltered(t, h, tok, absFilterToken("series", "10"))
	titles := absItemTitles(got)

	// 🔴 THE ASSERTION IS EXCLUSION, NOT INCLUSION. Returning the whole library
	// also "includes" the series' books — that IS the bug. What distinguishes a
	// working filter is that everything else is ABSENT.
	want := map[string]bool{"Odyssey Book One": true, "Odyssey Book Two": true}
	for _, title := range titles {
		if !want[title] {
			t.Fatalf("series filter returned %q, which is not in series 10: %v\n"+
				"an unfiltered response contains the series' books too — exclusion is "+
				"what proves the filter ran", title, titles)
		}
	}
	if len(titles) != 2 {
		t.Fatalf("series filter returned %d items %v, want exactly 2", len(titles), titles)
	}
	// Order is the series' reading order, same as the series tile.
	if titles[0] != "Odyssey Book One" || titles[1] != "Odyssey Book Two" {
		t.Fatalf("series filter order = %v, want sequence order", titles)
	}
	// total must be the FILTERED size. Reporting the library total makes the client
	// page forever into empty results.
	if total, _ := got["total"].(float64); int(total) != 2 {
		t.Fatalf("total = %v, want 2 (the filtered set, not the library)", got["total"])
	}
	if int(unfiltered["total"].(float64)) == 2 {
		t.Fatal("the unfiltered total is also 2 — this fixture cannot distinguish a " +
			"working filter from a broken one")
	}
}

// 🔴 THE FABRICATED-ID CASE, which is what proved the bug on production. A filter
// naming something that CANNOT exist must return nothing. Returning the library
// here is the exact observed defect, and it is the cheapest possible probe: a real
// id returning everything can be excused as an over-broad match, a fake one cannot.
func TestLibraryItems_UnmatchableFilterReturnsEmptyNotTheWholeLibrary(t *testing.T) {
	seed := absSeedTwoSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	for _, tc := range []struct {
		name   string
		filter string
	}{
		{"series id that does not exist", absFilterToken("series", "999999")},
		{"series value that is not a number", absFilterToken("series", "nonexistent-series-id-9999")},
		{"a filter group we do not implement", absFilterToken("genres", "Sci-Fi")},
		{"a token that is not valid base64", "series.!!!!not-base64!!!!"},
		{"a token with no group separator", "seriesMTQ3OTI0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := absItemsFiltered(t, h, tok, tc.filter)
			titles := absItemTitles(got)
			if len(titles) != 0 {
				t.Fatalf("filter %q returned %d items %v, want 0\n"+
					"an unrecognised filter that falls through to the unfiltered list is "+
					"the production defect: wrong data that looks like real data is worse "+
					"than no data", tc.filter, len(titles), titles)
			}
			if total, _ := got["total"].(float64); int(total) != 0 {
				t.Fatalf("filter %q reported total=%v, want 0", tc.filter, got["total"])
			}
		})
	}
}
