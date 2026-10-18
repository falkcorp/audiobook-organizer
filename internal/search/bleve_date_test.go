// file: internal/search/bleve_date_test.go
// version: 1.0.0
// guid: e5b81c3a-2d97-4f06-8a14-9c07d3f1b6e2
// last-edited: 2026-10-18

package search

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// dateTestNow is the fixed instant the date index tests resolve relative terms
// against.
var dateTestNow = time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)

func dayAgo(n int) *time.Time {
	t := dateTestNow.AddDate(0, 0, -n)
	return &t
}

// dateSearch runs q against idx with the fixed now and returns the sorted ids.
func dateSearch(t *testing.T, idx *BleveIndex, q string) []string {
	t.Helper()
	ast, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("parse %q: %v", q, err)
	}
	bq, _, err := TranslateAt(ast, dateTestNow)
	if err != nil {
		t.Fatalf("translate %q: %v", q, err)
	}
	hits, _, err := idx.SearchNative(bq, 0, 100)
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	ids := hitIDs(hits)
	sort.Strings(ids)
	return ids
}

func seedDateIndex(t *testing.T) *BleveIndex {
	t.Helper()
	idx := openTestIndex(t)
	for _, d := range []BookDocument{
		{BookID: "b1", Title: "Recent One", DateAdded: dayAgo(3), DateModified: dayAgo(1)},
		{BookID: "b2", Title: "Last Week Edge", DateAdded: dayAgo(6), DateModified: dayAgo(5)},
		{BookID: "b3", Title: "Old One", DateAdded: dayAgo(40), DateModified: dayAgo(40)},
		{BookID: "b4", Title: "Ancient One", DateAdded: dayAgo(400), DateModified: dayAgo(2)},
		{BookID: "b5", Title: "No Dates At All"},
	} {
		if err := idx.IndexBook(d); err != nil {
			t.Fatalf("index %s: %v", d.BookID, err)
		}
	}
	return idx
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBleveIndex_DateAddedRelative is the translator/index half of the D76
// acceptance: date_added:>-7d finds the books added in the last seven days and
// not older ones, nor a book that carries no date.
func TestBleveIndex_DateAddedRelative(t *testing.T) {
	idx := seedDateIndex(t)

	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"date_added:>-7d", []string{"b1", "b2"}},
		{"date_added:<-7d", []string{"b3", "b4"}},
		{"date_added:>-1y", []string{"b1", "b2", "b3"}},
		{"date_added:-7d..now", []string{"b1", "b2"}},
		{"added:>-7d", []string{"b1", "b2"}},
		{"date_modified:>-3d", []string{"b1", "b4"}},
		{"modified:>-3d", []string{"b1", "b4"}},
		{"date_modified:-10d..-4d", []string{"b2"}},
		// Absolute dates keep working, day granularity on equality.
		{"date_added:>2026-10-01", []string{"b1", "b2"}},
		{"date_added:2026-10-15", []string{"b1"}}, // dateTestNow-3d is 2026-10-15 12:00
		{"date_added:2026-10-14", []string(nil)},
		// A negation keeps the book with no date.
		{"-date_added:>-7d", []string{"b3", "b4", "b5"}},
		// Combined with free text.
		{"date_added:>-7d Recent", []string{"b1"}},
	} {
		got := dateSearch(t, idx, tc.q)
		if !sameStrings(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestTranslate_DateTermErrorsAreReported(t *testing.T) {
	for q, want := range map[string]string{
		"date_added:>30d":     "-30d",
		"date_added:>-30x":    "invalid date",
		"date_added:garbage":  "invalid date",
		"date_added:-1y..30d": "-30d",
		"modified:<yesterday": "invalid date",
	} {
		ast, err := ParseQuery(q)
		if err != nil {
			t.Fatalf("parse %q: %v", q, err)
		}
		_, _, terr := TranslateAt(ast, dateTestNow)
		if terr == nil || !strings.Contains(terr.Error(), want) {
			t.Errorf("Translate(%q) error = %v, want one containing %q", q, terr, want)
		}
	}
}

// The date fields are known to the index: an unknown field under a NOT would
// otherwise answer the whole library (see field_routing.go).
func TestIsIndexedField_DateFields(t *testing.T) {
	for _, f := range []string{"date_added", "added", "date_modified", "modified", "last_played"} {
		if !IsIndexedField(f) {
			t.Errorf("IsIndexedField(%q) = false", f)
		}
	}
	if IsIndexedField("itunes_last_played") {
		t.Error("itunes_last_played is sort-only and not indexed")
	}
}

// The projection fills the dates from the Book row and leaves them empty when the
// row has none, so a book with no timestamp is not indexed at year 1.
func TestBookToDocCore_Dates(t *testing.T) {
	created := time.Date(2026, 9, 1, 8, 0, 0, 0, time.FixedZone("x", 3600))
	updated := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	doc := bookToDocCore(&database.Book{ID: "b1", Title: "T", CreatedAt: &created, UpdatedAt: &updated})
	if doc.DateAdded == nil || !doc.DateAdded.Equal(created) || doc.DateAdded.Location() != time.UTC {
		t.Errorf("DateAdded = %v, want %v in UTC", doc.DateAdded, created)
	}
	if doc.DateModified == nil || !doc.DateModified.Equal(updated) {
		t.Errorf("DateModified = %v, want %v", doc.DateModified, updated)
	}
	empty := bookToDocCore(&database.Book{ID: "b2", Title: "T"})
	if empty.DateAdded != nil || empty.DateModified != nil {
		t.Errorf("a book with no timestamps must carry no dates, got %v / %v", empty.DateAdded, empty.DateModified)
	}
	var zero time.Time
	z := bookToDocCore(&database.Book{ID: "b3", Title: "T", CreatedAt: &zero, UpdatedAt: &zero})
	if z.DateAdded != nil || z.DateModified != nil {
		t.Errorf("a zero timestamp must carry no date, got %v / %v", z.DateAdded, z.DateModified)
	}
}

// ── mapping version (the one-time reindex) ──────────────────────────────────

// TestBookMappingVersionBumpedForDates pins the bump that adding the date fields
// requires, and that an index built by the previous version is thrown away and
// rebuilt rather than kept with the old mapping (bleve uses the STORED mapping,
// so without the recreate date_added:... would match nothing in production while
// every other test here passed). The rebuild is a one-time full reindex at the
// first start after deploy.
func TestBookMappingVersionBumpedForDates(t *testing.T) {
	if bookMappingVersion != "4" {
		t.Fatalf("bookMappingVersion = %q, want %q (date_added/date_modified added)", bookMappingVersion, "4")
	}

	path := filepath.Join(t.TempDir(), "library.bleve")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedOne(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// What production holds today: an index stamped with the previous version.
	if err := os.WriteFile(mappingMarkerPath(path), []byte("3\n"), 0o644); err != nil {
		t.Fatalf("write v3 marker: %v", err)
	}

	second := indexAt(t, path)
	if !second.RecreatedForMappingChange() {
		t.Fatal("a version 3 index was kept: date fields would never be indexed in production")
	}
	if !second.Rebuilding() {
		t.Error("the recreated index must report Rebuilding until the reindex drains")
	}
	if got := docCount(t, second); got != 0 {
		t.Errorf("recreated index should be empty, DocCount = %d", got)
	}
	if got := readMappingMarker(path); got != "4" {
		t.Errorf("marker after recreate = %q, want 4", got)
	}
}
