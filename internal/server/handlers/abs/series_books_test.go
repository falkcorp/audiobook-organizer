// file: internal/server/handlers/abs/series_books_test.go
// version: 1.1.0
// guid: 7f2c5d81-3ab9-4e60-9c47-58d3e0b6a214
// last-edited: 2026-08-16

package abs_test

import (
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ── the series list must carry ITS OWN books ────────────────────────────────
//
// 🔴 WHAT WAS BROKEN. LibrarySeries hardcoded `"books": []any{}` and
// `"totalDuration": 0` on every row. Measured on production 2026-08-13 before the
// fix: books == [] on 14,625 of 14,625 series and totalDuration == 0 on 14,625 of
// 14,625, while numBooks was correctly populated on 14,295. The app showed a
// series claiming to hold no books while displaying a count.
//
// 🔴 WHY "NON-EMPTY" IS NOT THE ASSERTION. The symptom reported from the app was
// *random books for every series*. A test that only checked len(books) > 0 would
// pass against a handler that gives every series the same wrong list — i.e.
// against the very bug. So this seeds TWO series and asserts each returns exactly
// its own book ids and nothing else. Cross-contamination is the failure mode with
// teeth; emptiness is the one that is easy to see.
func absSeedTwoSeries(t *testing.T) *oracleSeed {
	t.Helper()
	seed := seedOracleLibrary(t)

	intp := func(i int) *int { return &i }
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }

	seed.lib.series[10] = &database.Series{ID: 10, Name: "Odyssey Cycle"}
	seed.lib.series[20] = &database.Series{ID: 20, Name: "Unrelated Saga"}

	// Books must look organized + primary or absItemFilterBase excludes them and
	// the series lists come back empty for a reason unrelated to the fix.
	add := func(id, title string, seriesID, seq, dur int) {
		seed.lib.addBook(&database.Book{
			ID: id, Title: title,
			SeriesID: intp(seriesID), SeriesSequence: intp(seq),
			Duration:     intp(dur),
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
		}, nil, nil)
	}
	// Deliberately inserted out of sequence order, so the ordering assertion below
	// is testing the sort rather than the insertion order.
	add("s10-b2", "Odyssey Book Two", 10, 2, 3600)
	add("s10-b1", "Odyssey Book One", 10, 1, 1800)
	add("s20-b1", "Saga Book One", 20, 1, 600)
	return seed
}

func absSeriesRows(t *testing.T, h *harness, tok string) map[string]map[string]any {
	t.Helper()
	code, body := h.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + h.libraryID() + "/series",
		headers: bearer(tok),
	})
	if code != http.StatusOK {
		t.Fatalf("GET series = %d, want 200", code)
	}
	m, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body is %T, want object", body)
	}
	rows := map[string]map[string]any{}
	results, _ := m["results"].([]any)
	for _, r := range results {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		rows[name] = row
	}
	return rows
}

// absBookTitles reads each book's title from media.metadata.title.
//
// It used to read a top-level "title". That field existed only because this
// route emitted a six-field ad-hoc book object instead of an ABS LibraryItem,
// which is why no ABS client could decode the series list and the app showed
// "No Series Found" while this test passed. The title now lives where ABS puts
// it, so reading it from there is also an assertion that the shape is right:
// if the response regresses to the flat projection, every title comes back
// empty and the callers below fail.
func absBookTitles(row map[string]any) []string {
	books, _ := row["books"].([]any)
	out := make([]string, 0, len(books))
	for _, b := range books {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		media, _ := m["media"].(map[string]any)
		meta, _ := media["metadata"].(map[string]any)
		title, _ := meta["title"].(string)
		out = append(out, title)
	}
	return out
}

// absBookIsLibraryItem reports whether a series book carries the fields an ABS
// client requires of a LibraryItem. This is the shape check the old test could
// not make, because the old shape had none of them.
func absBookIsLibraryItem(b any) bool {
	m, ok := b.(map[string]any)
	if !ok {
		return false
	}
	for _, k := range []string{"id", "libraryId", "mediaType", "media", "path"} {
		if _, present := m[k]; !present {
			return false
		}
	}
	media, _ := m["media"].(map[string]any)
	_, hasMeta := media["metadata"]
	return hasMeta
}

func TestLibrarySeries_BooksAreTheSeriesOwnBooksInSequenceOrder(t *testing.T) {
	seed := absSeedTwoSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	rows := absSeriesRows(t, h, tok)

	odyssey, ok := rows["Odyssey Cycle"]
	if !ok {
		t.Fatalf("series list has no 'Odyssey Cycle'; got %v", absSeriesKeys(rows))
	}
	saga, ok := rows["Unrelated Saga"]
	if !ok {
		t.Fatalf("series list has no 'Unrelated Saga'; got %v", absSeriesKeys(rows))
	}

	// Value-asserted AND order-asserted. A series listed out of reading order is
	// barely more useful than one listed not at all.
	gotOdyssey := absBookTitles(odyssey)
	wantOdyssey := []string{"Odyssey Book One", "Odyssey Book Two"}
	if len(gotOdyssey) != len(wantOdyssey) {
		t.Fatalf("Odyssey Cycle has %d books %v, want %d %v\n"+
			"this is the defect that shipped: books was hardcoded [] on 14,625/14,625 series",
			len(gotOdyssey), gotOdyssey, len(wantOdyssey), wantOdyssey)
	}
	for i := range wantOdyssey {
		if gotOdyssey[i] != wantOdyssey[i] {
			t.Fatalf("Odyssey Cycle books = %v, want %v (sequence order)", gotOdyssey, wantOdyssey)
		}
	}

	// 🔴 THE CONTAMINATION CHECK. The reported symptom was random books per
	// series, so proving each list is correct means proving the OTHER series'
	// books are absent — not merely that this one is non-empty.
	for _, title := range gotOdyssey {
		if title == "Saga Book One" {
			t.Fatalf("Odyssey Cycle contains a book from Unrelated Saga: %v", gotOdyssey)
		}
	}
	if got := absBookTitles(saga); len(got) != 1 || got[0] != "Saga Book One" {
		t.Fatalf("Unrelated Saga books = %v, want exactly [Saga Book One]", got)
	}

	// totalDuration must be the SUM of the series' own books, as an int.
	// 1800 + 3600 = 5400. A float here red-screens the series tile in Dart
	// (`42.0 as int?` throws during widget build) — §1.7.3 item 5.
	dur, ok := odyssey["totalDuration"].(float64) // JSON numbers decode as float64
	if !ok {
		t.Fatalf("totalDuration is %T, want a number", odyssey["totalDuration"])
	}
	if int(dur) != 5400 {
		t.Fatalf("Odyssey Cycle totalDuration = %d, want 5400 (1800+3600)\n"+
			"it was hardcoded 0 on every series in production", int(dur))
	}
	if dur != float64(int(dur)) {
		t.Fatalf("totalDuration = %v is not an integer value; Dart throws on `42.0 as int?`", dur)
	}
}

func absSeriesKeys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestLibrarySeries_BooksAreLibraryItems pins the 2026-08-16 fix.
//
// The series list returned HTTP 200 with 50 plausible rows and the app's Series
// tab showed "No Series Found". The books were not LibraryItem objects: they
// carried six ad-hoc fields (id, libraryItemId, libraryId, title, sequence,
// duration) and none of media, media.metadata, mediaType, coverPath or path.
//
// The control that identified it is the playlists route, which the same client
// renders correctly and which embeds a full libraryItem via minifiedItem. A
// typed client decodes books: [LibraryItem] as a unit, so one undecodable entry
// discards the entire response — which is why 23 of 50 series having real books
// still put zero on screen.
//
// Asserting the FIELDS rather than a rendered title is the point: a test that
// only checked titles passed against the broken shape for as long as it shipped.
func TestLibrarySeries_BooksAreLibraryItems(t *testing.T) {
	seed := absSeedTwoSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	odyssey, ok := absSeriesRows(t, h, tok)["Odyssey Cycle"]
	if !ok {
		t.Fatal("series list has no 'Odyssey Cycle'")
	}

	books, _ := odyssey["books"].([]any)
	if len(books) == 0 {
		t.Fatal("Odyssey Cycle served no books")
	}
	for i, b := range books {
		if !absBookIsLibraryItem(b) {
			t.Errorf("book %d is not a decodable ABS LibraryItem: %#v\n"+
				"an ABS client decodes books as [LibraryItem] and drops the WHOLE "+
				"response on one bad entry — this is why no series rendered", i, b)
		}
	}
}

// TestLibrarySeries_NumBooksMatchesTheBooksServed pins the self-consistency
// rule. Measured on production 2026-08-16, 9 of 50 series on page 0 reported
// numBooks >= 1 while carrying books: [] and totalDuration: 0, because books
// with no resolvable sync id are dropped after the count is taken.
func TestLibrarySeries_NumBooksMatchesTheBooksServed(t *testing.T) {
	seed := absSeedTwoSeries(t)
	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()))
	h.seedUser(t, "u1", "oracle", "", "pw-pw-pw-pw")
	tok := str(t, userObj(t, h.login(t, "oracle", "pw-pw-pw-pw")), "accessToken")

	for name, row := range absSeriesRows(t, h, tok) {
		books, _ := row["books"].([]any)
		num, _ := row["numBooks"].(float64)
		if int(num) != len(books) {
			t.Errorf("%s: numBooks=%d but %d books served — a row that reports a "+
				"count it cannot list forces the client to guess which to believe",
				name, int(num), len(books))
		}
	}
}
