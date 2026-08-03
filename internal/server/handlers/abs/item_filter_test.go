// file: internal/server/handlers/abs/item_filter_test.go
// version: 1.0.0
// guid: 1e63a04f-8c27-4d1b-95a0-2f78c6b3e419
// last-edited: 2026-08-03

package abs_test

import (
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The app showed 44,888 items where the owner has ~16,000, because
// GET /api/libraries/:id/items counted and listed EVERY book row. The library's own
// counts cache reports the real split:
//
//	total_books=44888  organized_books=16491  unorganized_books=23928
//
// The extra ~28k are raw imports, iTunes-tree copies, and alternate versions of books
// already in the list. These tests seed exactly those shapes and assert each one is
// excluded — asserting on the handler's OUTPUT rather than on which filter struct it
// happened to build, so the coverage survives a refactor of the filter plumbing.

// seedNoise adds one book of each shape that must NOT appear in the library list, and
// returns how many were added.
func seedNoise(t *testing.T, w *writeHarness) int {
	t.Helper()
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	now := timeNowForSeed()

	// An alternate version of a book already in the list.
	w.seed.lib.addBook(&database.Book{
		ID: "01ALTVERSIONBOOK000000000", Title: "The Odyssey",
		FilePath: "/library/Homer/The Odyssey (Alt)/alt.m4b", Format: "m4b",
		LibraryState: strp("organized"), IsPrimaryVersion: boolp(false),
		CreatedAt: &now, UpdatedAt: &now,
	}, nil, nil)

	// A raw iTunes-tree copy: primary, but not organized into the library.
	w.seed.lib.addBook(&database.Book{
		ID: "01UNORGANIZEDBOOK00000000", Title: "Unorganized Import",
		FilePath: "/iTunes Media/Audiobooks/raw.mp3", Format: "mp3",
		LibraryState: strp("organized_source"), IsPrimaryVersion: boolp(true),
		CreatedAt: &now, UpdatedAt: &now,
	}, nil, nil)

	// A quarantined file — cannot be played, so listing it only yields a failure.
	quarantined := now
	w.seed.lib.addBook(&database.Book{
		ID: "01QUARANTINEDBOOK00000000", Title: "Quarantined Book",
		FilePath: "/library/Broken/broken.m4b", Format: "m4b",
		LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
		QuarantinedAt: &quarantined, QuarantineReason: strp("unreadable"),
		CreatedAt: &now, UpdatedAt: &now,
	}, nil, nil)

	return 3
}

// libraryItemTitles returns the titles on one page of the library list.
func libraryItemTitles(t *testing.T, w *writeHarness, query string) ([]string, int) {
	t.Helper()
	code, body, raw := w.req(t, http.MethodGet,
		"/api/libraries/"+w.libraryID()+"/items"+query, nil)
	if code != http.StatusOK {
		t.Fatalf("items%s = %d %s", query, code, raw)
	}
	total := int(requireNum(t, body, "total"))
	titles := []string{}
	for _, entry := range requireArray(t, body, "results") {
		item, _ := entry.(map[string]any)
		if item == nil {
			continue
		}
		media, _ := item["media"].(map[string]any)
		if media == nil {
			continue
		}
		meta, _ := media["metadata"].(map[string]any)
		if meta == nil {
			continue
		}
		if title, _ := meta["title"].(string); title != "" {
			titles = append(titles, title)
		}
	}
	return titles, total
}

// 🔴 TestLibraryItems_ExcludesAlternateUnorganizedAndQuarantined is the regression for
// "why do I have 44,888 books when I should have 16,000".
func TestLibraryItems_ExcludesAlternateUnorganizedAndQuarantined(t *testing.T) {
	w := newWriteHarness(t)
	baseline, baseTotal := libraryItemTitles(t, w, "?limit=100&page=0")
	seedNoise(t, w)

	titles, total := libraryItemTitles(t, w, "?limit=100&page=0")

	if total != baseTotal {
		t.Errorf("total = %d after seeding 3 excluded books, want %d unchanged", total, baseTotal)
	}
	if len(titles) != len(baseline) {
		t.Errorf("result count = %d, want %d — an excluded book leaked into the list", len(titles), len(baseline))
	}
	for _, unwanted := range []string{"Unorganized Import", "Quarantined Book"} {
		if contains(titles, unwanted) {
			t.Errorf("%q appears in the library list but must be filtered out: %v", unwanted, titles)
		}
	}
	// The alternate version shares its title with the primary, so assert by COUNT:
	// "The Odyssey" must not gain an extra entry.
	var odyssey int
	for _, tt := range titles {
		if tt == "The Odyssey" {
			odyssey++
		}
	}
	var baseOdyssey int
	for _, tt := range baseline {
		if tt == "The Odyssey" {
			baseOdyssey++
		}
	}
	if odyssey != baseOdyssey {
		t.Errorf("The Odyssey appears %d times, want %d — the alternate version was not collapsed",
			odyssey, baseOdyssey)
	}
}

// 🔴 TestLibraryItems_TotalIsTheFilteredCount. Reporting the unfiltered total makes the
// client page forever into empty results, because it uses `total` to decide whether
// another page exists.
func TestLibraryItems_TotalIsTheFilteredCount(t *testing.T) {
	w := newWriteHarness(t)
	seedNoise(t, w)

	titles, total := libraryItemTitles(t, w, "?limit=100&page=0")
	if total != len(titles) {
		t.Fatalf("total = %d but the page holds %d items and there is only one page — "+
			"total is counting rows the list excludes", total, len(titles))
	}
}

// TestLibraryItems_CountIsCached proves the full-library count scan is not repeated on
// every request. Before caching, latency was a flat ~2s on EVERY page because
// CountAllBooks json.Unmarshal'd all 44,888 books per call.
func TestLibraryItems_CountIsCached(t *testing.T) {
	w := newWriteHarness(t)
	if _, _ = libraryItemTitles(t, w, "?limit=10&page=0"); true {
		// primed
	}
	before := w.seed.lib.countCalls()
	for range 5 {
		libraryItemTitles(t, w, "?limit=10&page=0")
	}
	if after := w.seed.lib.countCalls(); after != before {
		t.Fatalf("count was recomputed %d times across 5 cached requests — "+
			"each one is a full-library scan", after-before)
	}
}

// TestLibraryItems_SortIsHonoured. The client always sends
// sort=media.metadata.title and we previously IGNORED it, so the library was never
// actually title-sorted.
func TestLibraryItems_SortIsHonoured(t *testing.T) {
	w := newWriteHarness(t)

	asc, _ := libraryItemTitles(t, w, "?limit=100&page=0&sort=media.metadata.title")
	desc, _ := libraryItemTitles(t, w, "?limit=100&page=0&sort=media.metadata.title&desc=1")
	if len(asc) < 2 {
		t.Skipf("need >=2 items to observe ordering, have %d", len(asc))
	}
	for i := 1; i < len(asc); i++ {
		if asc[i-1] > asc[i] {
			t.Fatalf("ascending sort not applied: %v", asc)
		}
	}
	if len(desc) == len(asc) && asc[0] == desc[0] && asc[0] != asc[len(asc)-1] {
		t.Fatalf("?desc=1 did not reverse the order: asc=%v desc=%v", asc, desc)
	}
}
