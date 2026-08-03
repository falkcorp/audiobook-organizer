// file: internal/server/handlers/abs/narrator_tiers_test.go
// version: 1.0.0
// guid: 8b2f6d14-3c07-49a5-b6e8-1f90a7d25c38
// last-edited: 2026-08-03

package abs_test

import (
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// 🔴 TestNarratorsTab_UsesAllThreeTiers is the regression for the near-empty
// Narrators tab.
//
// Narrator data lives in three places and resolveNarrators reads them in order:
// the BookNarrator junction, Book.NarratorsJSON, then the legacy Book.Narrator
// column. Book DETAIL has always done this.
//
// The Narrators TAB was built from the junction alone, and for organized books
// the junction is nearly empty — so the tab showed 8 names for a library where
// roughly 9,500 of 16,491 visible books have a narrator stored (57.5% of a
// 120-book prod sample). The tab and the book page must agree.
func TestNarratorsTab_UsesAllThreeTiers(t *testing.T) {
	w := newWriteHarness(t)
	strp := func(s string) *string { return &s }
	boolp := func(b bool) *bool { return &b }
	now := timeNowForSeed()

	mk := func(id, title string) *database.Book {
		return &database.Book{
			ID: id, Title: title,
			FilePath: "/library/" + id + ".m4b", Format: "m4b",
			LibraryState: strp("organized"), IsPrimaryVersion: boolp(true),
			CreatedAt: &now, UpdatedAt: &now,
		}
	}

	// Tier 1 — the junction. Already worked.
	junctionBook := mk("01NARRJUNCTION0000000000", "Junction Book")
	w.seed.lib.addBook(junctionBook, nil, nil)
	w.seed.lib.attachNarrators(junctionBook.ID, "Junction Narrator")

	// Tier 2 — NarratorsJSON, no junction row.
	jsonBook := mk("01NARRJSONBOOK0000000000", "JSON Book")
	jsonBook.NarratorsJSON = strp(`["Json Narrator"]`)
	w.seed.lib.addBook(jsonBook, nil, nil)

	// Tier 3 — the legacy single-string column, no junction row, no JSON.
	colBook := mk("01NARRCOLUMNBOOK00000000", "Column Book")
	colBook.Narrator = strp("Column Narrator")
	w.seed.lib.addBook(colBook, nil, nil)

	_, body, _ := w.req(t, http.MethodGet, "/api/libraries/"+w.libraryID()+"/narrators", nil)

	got := map[string]bool{}
	for _, entry := range requireArray(t, body, "narrators") {
		if n, _ := entry.(map[string]any); n != nil {
			if name, _ := n["name"].(string); name != "" {
				got[name] = true
			}
		}
	}

	for _, want := range []string{"Junction Narrator", "Json Narrator", "Column Narrator"} {
		if !got[want] {
			t.Errorf("narrator %q missing from the Narrators tab — got %v", want, keysOf(got))
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
