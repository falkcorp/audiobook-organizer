// file: internal/database/memdb_reads_test.go
// version: 1.9.1
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000007
// last-edited: 2026-10-19

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

// seed inserts the given objects into the appropriate memdb tables.
// Used to set up deterministic state for the read query tests.
func seedMemStore(t *testing.T, m *MemStore, books []Book, files []BookFile, authors []Author, series []Series) {
	t.Helper()
	txn := m.db.Txn(true)
	defer txn.Commit()
	for i := range books {
		b := books[i]
		if err := txn.Insert(memTableBooks, &b); err != nil {
			t.Fatalf("seed book: %v", err)
		}
	}
	for i := range files {
		f := files[i]
		if err := txn.Insert(memTableBookFiles, &f); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}
	for i := range authors {
		a := authors[i]
		if err := txn.Insert(memTableAuthors, &a); err != nil {
			t.Fatalf("seed author: %v", err)
		}
	}
	for i := range series {
		s := series[i]
		if err := txn.Insert(memTableSeries, &s); err != nil {
			t.Fatalf("seed series: %v", err)
		}
	}
}

func TestMemStore_GetAllSeriesBookCounts(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}

	books := []Book{
		// Primary, not deleted — counted
		{ID: "b1", Title: "B1", SeriesID: new(10), IsPrimaryVersion: new(true)},
		// Primary, not deleted — counted (same series)
		{ID: "b2", Title: "B2", SeriesID: new(10), IsPrimaryVersion: new(true)},
		// Different series, counted
		{ID: "b3", Title: "B3", SeriesID: new(20), IsPrimaryVersion: new(true)},
		// Marked for deletion — skipped
		{ID: "b4", Title: "B4", SeriesID: new(10), IsPrimaryVersion: new(true), MarkedForDeletion: new(true)},
		// Not primary — skipped (effectiveBoolFieldIndex stores false here)
		{ID: "b5", Title: "B5", SeriesID: new(10), IsPrimaryVersion: new(false)},
		// nil SeriesID — skipped
		{ID: "b6", Title: "B6", IsPrimaryVersion: new(true)},
		// nil IsPrimaryVersion → effectively true, counted
		{ID: "b7", Title: "B7", SeriesID: new(20)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	got, err := m.GetAllSeriesBookCounts()
	if err != nil {
		t.Fatalf("GetAllSeriesBookCounts: %v", err)
	}
	want := map[int]int{10: 2, 20: 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts mismatch: got %v, want %v", got, want)
	}
}

func TestMemStore_GetAllAuthorBookCounts(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := []Book{
		{ID: "b1", Title: "A", AuthorID: new(1), IsPrimaryVersion: new(true)},
		{ID: "b2", Title: "B", AuthorID: new(1), IsPrimaryVersion: new(true)},
		{ID: "b3", Title: "C", AuthorID: new(2), IsPrimaryVersion: new(true)},
		{ID: "b4", Title: "D", AuthorID: new(1), MarkedForDeletion: new(true), IsPrimaryVersion: new(true)},
		{ID: "b5", Title: "E", AuthorID: new(1), IsPrimaryVersion: new(false)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	got, err := m.GetAllAuthorBookCounts()
	if err != nil {
		t.Fatalf("GetAllAuthorBookCounts: %v", err)
	}
	want := map[int]int{1: 2, 2: 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts mismatch: got %v, want %v", got, want)
	}
}

func TestMemStore_GetAllSeriesFileCounts(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := []Book{
		{ID: "b1", Title: "B1", SeriesID: new(10), IsPrimaryVersion: new(true)},
		{ID: "b2", Title: "B2", SeriesID: new(10), IsPrimaryVersion: new(true)},
		{ID: "b3", Title: "B3", SeriesID: new(20), IsPrimaryVersion: new(true)},
		{ID: "b4", Title: "B4", SeriesID: new(10), IsPrimaryVersion: new(false)}, // skipped
	}
	files := []BookFile{
		{ID: "f1", BookID: "b1", Missing: false},
		{ID: "f2", BookID: "b1", Missing: false},
		{ID: "f3", BookID: "b2", Missing: false},
		{ID: "f4", BookID: "b3", Missing: false},
		{ID: "f5", BookID: "b3", Missing: true},      // missing — skipped
		{ID: "f6", BookID: "b4", Missing: false},     // non-primary book — skipped
		{ID: "f7", BookID: "orphan", Missing: false}, // unknown book — skipped
	}
	seedMemStore(t, m, books, files, nil, nil)

	got, err := m.GetAllSeriesFileCounts()
	if err != nil {
		t.Fatalf("GetAllSeriesFileCounts: %v", err)
	}
	want := map[int]int{10: 3, 20: 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("counts mismatch: got %v, want %v", got, want)
	}
}

func TestMemStore_GetBooksBySeriesIDCore(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := []Book{
		{ID: "b1", Title: "Book One", SeriesID: new(10), SeriesSequence: new(1), IsPrimaryVersion: new(true)},
		{ID: "b2", Title: "Book Three", SeriesID: new(10), SeriesSequence: new(3), IsPrimaryVersion: new(true)},
		{ID: "b3", Title: "Book Two", SeriesID: new(10), SeriesSequence: new(2), IsPrimaryVersion: new(true)},
		{ID: "b4", Title: "Other Series", SeriesID: new(20), IsPrimaryVersion: new(true)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	got, err := m.GetBooksBySeriesIDCore(10, 10, 0)
	if err != nil {
		t.Fatalf("GetBooksBySeriesIDCore: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 books, got %d", len(got))
	}
	titles := []string{got[0].Title, got[1].Title, got[2].Title}
	wantTitles := []string{"Book One", "Book Two", "Book Three"}
	if !reflect.DeepEqual(titles, wantTitles) {
		t.Errorf("order wrong: got %v, want %v", titles, wantTitles)
	}
}

func TestMemStore_GetBooksByAuthorID(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := []Book{
		{ID: "b1", Title: "Zebra", AuthorID: new(1), IsPrimaryVersion: new(true)},
		{ID: "b2", Title: "Alpha", AuthorID: new(1), IsPrimaryVersion: new(true)},
		{ID: "b3", Title: "Other", AuthorID: new(2), IsPrimaryVersion: new(true)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	got, err := m.GetBooksByAuthorID(1, 10, 0)
	if err != nil {
		t.Fatalf("GetBooksByAuthorID: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 books, got %d", len(got))
	}
	// GetBooksByAuthorID intentionally does NOT sort (matches Pebble path
	// and avoids per-call sort cost on hot read path). Assert the set
	// without ordering.
	seen := map[string]bool{}
	for _, b := range got {
		seen[b.Title] = true
	}
	if !seen["Alpha"] || !seen["Zebra"] {
		t.Errorf("expected {Alpha, Zebra}, got %v", got)
	}
}

func TestMemStore_GetAllBooksCore_Filters(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := []Book{
		{ID: "b1", Title: "A", IsPrimaryVersion: new(true), AuthorID: new(1), MarkedForDeletion: new(false)},
		{ID: "b2", Title: "B", IsPrimaryVersion: new(true), AuthorID: new(2), MarkedForDeletion: new(false)},
		{ID: "b3", Title: "C", IsPrimaryVersion: new(false), AuthorID: new(1), MarkedForDeletion: new(false)},
		{ID: "b4", Title: "D", IsPrimaryVersion: new(true), AuthorID: new(1), MarkedForDeletion: new(true)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	// author_id=1 && is_primary && !deleted → only b1
	got, err := m.GetAllBooksCore(100, 0, map[string]any{
		"author_id":           1,
		"is_primary_version":  true,
		"marked_for_deletion": false,
	})
	if err != nil {
		t.Fatalf("GetAllBooksCore: %v", err)
	}
	if len(got) != 1 || got[0].ID != "b1" {
		ids := []string{}
		for _, b := range got {
			ids = append(ids, b.ID)
		}
		t.Errorf("expected [b1], got %v", ids)
	}
}

// GetAllBooksCore must treat limit <= 0 as UNBOUNDED (return every row), while a
// positive limit truncates. Whole-library maintenance ops rely on the limit==0
// form to avoid silently processing only a subset (see the July-16 truncation
// fixes: OptimizeDatabase, enrichImportedBooks, runMetadataRefreshScan,
// quarantineKnownBadFiles). This test locks that contract so a future change to
// the getter can't reintroduce a silent cap on limit==0.
func TestMemStore_GetAllBooksCore_LimitZeroIsUnbounded(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := make([]Book, 0, 5)
	for i := range 5 {
		books = append(books, Book{
			ID:                fmt.Sprintf("b%d", i),
			Title:             fmt.Sprintf("Book %d", i),
			IsPrimaryVersion:  new(true),
			MarkedForDeletion: new(false),
		})
	}
	seedMemStore(t, m, books, nil, nil, nil)

	// Positive limit below the row count truncates.
	got, err := m.GetAllBooksCore(3, 0, nil)
	if err != nil {
		t.Fatalf("GetAllBooksCore(3,0): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("limit=3 returned %d rows, want 3 (truncated)", len(got))
	}

	// limit == 0 returns everything.
	all, err := m.GetAllBooksCore(0, 0, nil)
	if err != nil {
		t.Fatalf("GetAllBooksCore(0,0): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("limit=0 returned %d rows, want all 5 (unbounded)", len(all))
	}

	// Negative limit is also unbounded.
	allNeg, err := m.GetAllBooksCore(-1, 0, nil)
	if err != nil {
		t.Fatalf("GetAllBooksCore(-1,0): %v", err)
	}
	if len(allNeg) != 5 {
		t.Fatalf("limit=-1 returned %d rows, want all 5 (unbounded)", len(allNeg))
	}
}

func TestMemStore_CountFiles(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	trueVal := true
	books := []Book{
		{ID: "b1", IsPrimaryVersion: &trueVal},
	}
	files := []BookFile{
		{ID: "f1", BookID: "b1", Missing: false},
		{ID: "f2", BookID: "b1", Missing: false},
		{ID: "f3", BookID: "b2", Missing: true}, // b2 has no book entry → not counted
	}
	seedMemStore(t, m, books, files, nil, nil)

	got, err := m.CountFiles()
	if err != nil {
		t.Fatalf("CountFiles: %v", err)
	}
	if got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestMemStore_GetBookFilesNeedingDelugeImportCore(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	now := time.Now()
	files := []BookFile{
		// Match: has DelugeHash, not yet imported.
		{ID: "f1", BookID: "b1", DelugeHash: "aaa"},
		// Match: has DelugeHash, not yet imported.
		{ID: "f2", BookID: "b1", DelugeHash: "bbb"},
		// Skip: DelugeHash present but already imported.
		{ID: "f3", BookID: "b2", DelugeHash: "ccc", ImportedFromDelugeAt: &now},
		// Skip: no DelugeHash (not in the sparse index at all).
		{ID: "f4", BookID: "b3"},
		// Skip: empty DelugeHash, with ImportedFromDelugeAt nil — must not
		// leak into results just because the post-filter passes.
		{ID: "f5", BookID: "b4", DelugeHash: ""},
	}
	seedMemStore(t, m, nil, files, nil, nil)

	got, err := m.GetBookFilesNeedingDelugeImportCore()
	if err != nil {
		t.Fatalf("GetBookFilesNeedingDelugeImportCore: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, f := range got {
		gotIDs[f.ID] = true
	}
	if len(gotIDs) != 2 || !gotIDs["f1"] || !gotIDs["f2"] {
		ids := []string{}
		for _, f := range got {
			ids = append(ids, f.ID)
		}
		t.Errorf("expected [f1 f2], got %v", ids)
	}
}

func TestMemStore_GetAllAuthorsSeriesImportPaths_Sort(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	authors := []Author{
		{ID: 3, Name: "Zoe"},
		{ID: 1, Name: "Alice"},
		{ID: 2, Name: "Bob"},
	}
	series := []Series{
		{ID: 1, Name: "Z Series"},
		{ID: 2, Name: "A Series"},
	}
	seedMemStore(t, m, nil, nil, authors, series)

	gotAuthors, err := m.GetAllAuthors()
	if err != nil {
		t.Fatalf("GetAllAuthors: %v", err)
	}
	if gotAuthors[0].Name != "Alice" || gotAuthors[2].Name != "Zoe" {
		t.Errorf("authors not sorted: %v", gotAuthors)
	}

	gotSeries, err := m.GetAllSeries()
	if err != nil {
		t.Fatalf("GetAllSeries: %v", err)
	}
	if gotSeries[0].Name != "A Series" || gotSeries[1].Name != "Z Series" {
		t.Errorf("series not sorted: %v", gotSeries)
	}
}

func TestMemStore_ListSoftDeletedBooks(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	now := time.Now()
	older := now.Add(-30 * 24 * time.Hour)
	recent := now.Add(-1 * time.Hour)
	tOlder := older
	tRecent := recent

	books := []Book{
		// alive — skipped
		{ID: "b1", Title: "Alive"},
		// soft-deleted (recent)
		{ID: "b2", Title: "Recently deleted", MarkedForDeletion: new(true), MarkedForDeletionAt: &tRecent},
		// soft-deleted (old)
		{ID: "b3", Title: "Old deleted", MarkedForDeletion: new(true), MarkedForDeletionAt: &tOlder},
		// soft-deleted, no timestamp — included, sorts last
		{ID: "b4", Title: "Timeless deleted", MarkedForDeletion: new(true)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	got, err := m.ListSoftDeletedBooks(100, 0, nil)
	if err != nil {
		t.Fatalf("ListSoftDeletedBooks: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d soft-deleted, want 3 (ids=%v)", len(got), idList(got))
	}
	// Sorted: recent first, then older, then nil last.
	wantOrder := []string{"b2", "b3", "b4"}
	if !reflect.DeepEqual(idList(got), wantOrder) {
		t.Errorf("order mismatch: got %v want %v", idList(got), wantOrder)
	}

	// Age filter: olderThan=14 days ago → only b3 qualifies (b2 deleted 1h ago is too recent).
	cutoff := now.Add(-14 * 24 * time.Hour)
	got, err = m.ListSoftDeletedBooks(100, 0, &cutoff)
	if err != nil {
		t.Fatalf("ListSoftDeletedBooks filtered: %v", err)
	}
	// b4 has nil MarkedForDeletionAt → not filtered out (matches Pebble semantics).
	wantIDs := map[string]bool{"b3": true, "b4": true}
	for _, b := range got {
		if !wantIDs[b.ID] {
			t.Errorf("unexpected book in filtered result: %s", b.ID)
		}
	}

	// Pagination
	page, err := m.ListSoftDeletedBooks(1, 1, nil)
	if err != nil {
		t.Fatalf("paginated: %v", err)
	}
	if len(page) != 1 || page[0].ID != "b3" {
		t.Errorf("page got %v, want [b3]", idList(page))
	}
}

func TestMemStore_CountBooksByPathPrefix(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := []Book{
		{ID: "b1", FilePath: "/mnt/books/a/one.m4b", SourceImportPath: new("/mnt/books/a")},
		{ID: "b2", FilePath: "/mnt/books/a/two.m4b", SourceImportPath: new("/mnt/books/a")},
		{ID: "b3", FilePath: "/mnt/books/b/three.m4b", SourceImportPath: new("/mnt/books/b")},
		// no SourceImportPath → falls back to FilePath
		{ID: "b4", FilePath: "/mnt/books/a/four.m4b"},
		// deleted — excluded
		{ID: "b5", FilePath: "/mnt/books/a/five.m4b", SourceImportPath: new("/mnt/books/a"), MarkedForDeletion: new(true)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	cases := []struct {
		prefix string
		want   int
	}{
		{"/mnt/books/a", 3}, // b1, b2 via SourceImportPath; b4 via FilePath
		{"/mnt/books/b", 1},
		{"/mnt/books", 4},
		{"", 0},
	}
	for _, tc := range cases {
		got, err := m.CountBooksByPathPrefix(tc.prefix)
		if err != nil {
			t.Fatalf("CountBooksByPathPrefix(%q): %v", tc.prefix, err)
		}
		if got != tc.want {
			t.Errorf("CountBooksByPathPrefix(%q) = %d, want %d", tc.prefix, got, tc.want)
		}
	}
}

func TestMemStore_ComputeLibraryStats(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	imp := []ImportPath{
		{ID: 1, Name: "Drop A", Path: "/inbox/a"},
		{ID: 2, Name: "Drop B", Path: "/inbox/b"},
	}
	books := []Book{
		// organized (under root)
		{ID: "b1", Title: "Org1", FilePath: "/library/x.m4b", IsPrimaryVersion: new(true),
			Duration: new(3600), FileSize: new(int64(100)), Codec: new("aac"), LibraryState: new("organized")},
		// unorganized under import path A
		{ID: "b2", Title: "Inbox1", FilePath: "/inbox/a/x.m4b", IsPrimaryVersion: new(true),
			Duration: new(7200), FileSize: new(int64(200)), Codec: new("aac")},
		// unorganized under import path B
		{ID: "b3", Title: "Inbox2", FilePath: "/inbox/b/y.m4b", IsPrimaryVersion: new(true),
			FileSize: new(int64(50))},
		// non-primary version — counted in totals, NOT in organized/unorganized
		{ID: "b4", Title: "Variant", FilePath: "/library/x-alt.m4b", IsPrimaryVersion: new(false),
			FileSize: new(int64(10))},
		// deleted — fully excluded
		{ID: "b5", Title: "Gone", FilePath: "/library/gone.m4b", IsPrimaryVersion: new(true),
			MarkedForDeletion: new(true), FileSize: new(int64(999))},
	}
	files := []BookFile{
		// b1: 2 files, one fingerprinted (AcoustIDSeg0, the primary/non-fallback
		// path) and one not → partially fingerprinted. BOTH are Missing (bytes gone)
		// → b1 is broken, and must count ONCE toward BrokenFiles even though two of
		// its files are missing. Marking Missing does not change the file count or
		// the fingerprint tally — the row still exists; only its bytes are gone.
		{ID: "f1", BookID: "b1", AcoustIDSeg0: "seg0-data", Missing: true},
		{ID: "f2", BookID: "b1", Missing: true},
		// b2: 1 file, fingerprinted via the memdb-safe duration fallback proxy
		// (AcoustIDSeg0 stripped, AcoustIDFingerprintDurationSec retained) →
		// fully fingerprinted. Also Missing → b2 is broken.
		{ID: "f3", BookID: "b2", AcoustIDFingerprintDurationSec: 42.0, Missing: true},
		// b4 is NON-primary; its Missing file must NOT count toward BrokenFiles —
		// the stats pass tallies files only for primary books.
		{ID: "f4", BookID: "b4", Missing: true},
		// b3 has no file rows → counted as 1, unfingerprinted, and NOT broken.
	}
	authors := []Author{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}
	series := []Series{{ID: 1, Name: "S1"}}
	seedMemStore(t, m, books, files, authors, series)

	stats, err := m.ComputeLibraryStats("/library", imp)
	if err != nil {
		t.Fatalf("ComputeLibraryStats: %v", err)
	}

	if stats.TotalBooks != 4 {
		t.Errorf("TotalBooks = %d, want 4 (excludes deleted)", stats.TotalBooks)
	}
	if stats.OrganizedBooks != 1 {
		t.Errorf("OrganizedBooks = %d, want 1", stats.OrganizedBooks)
	}
	if stats.UnorganizedBooks != 2 {
		t.Errorf("UnorganizedBooks = %d, want 2", stats.UnorganizedBooks)
	}
	if stats.OrganizedSize != 100 {
		t.Errorf("OrganizedSize = %d, want 100", stats.OrganizedSize)
	}
	if stats.UnorganizedSize != 250 {
		t.Errorf("UnorganizedSize = %d, want 250", stats.UnorganizedSize)
	}
	if stats.TotalSize != 360 { // 100+200+50+10
		t.Errorf("TotalSize = %d, want 360", stats.TotalSize)
	}
	if stats.TotalDuration != 10800 {
		t.Errorf("TotalDuration = %d, want 10800", stats.TotalDuration)
	}
	// b1: 2 files, b2: 1 file, b3: 0 → 1 sentinel. b4 not primary so skipped.
	if stats.TotalFiles != 4 {
		t.Errorf("TotalFiles = %d, want 4 (2+1+1)", stats.TotalFiles)
	}
	if stats.BooksByImportPath[1] != 1 || stats.BooksByImportPath[2] != 1 {
		t.Errorf("BooksByImportPath = %v, want {1:1, 2:1}", stats.BooksByImportPath)
	}
	if stats.SizeByImportPath[1] != 200 || stats.SizeByImportPath[2] != 50 {
		t.Errorf("SizeByImportPath = %v, want {1:200, 2:50}", stats.SizeByImportPath)
	}
	if stats.TotalAuthors != 2 {
		t.Errorf("TotalAuthors = %d, want 2", stats.TotalAuthors)
	}
	if stats.TotalSeries != 1 {
		t.Errorf("TotalSeries = %d, want 1", stats.TotalSeries)
	}
	if stats.StateDistribution["organized"] != 1 {
		t.Errorf("StateDistribution[organized] = %d, want 1", stats.StateDistribution["organized"])
	}
	if stats.FormatDistribution["aac"] != 2 {
		t.Errorf("FormatDistribution[aac] = %d, want 2", stats.FormatDistribution["aac"])
	}
	if stats.ComputedAt.IsZero() {
		t.Error("ComputedAt not set")
	}
	// Fingerprint coverage: b1 partial (1/2 files), b2 complete (1/1 via the
	// duration fallback), b3 unfingerprinted (0 files). b4 is non-primary and
	// excluded from classification.
	if stats.FingerprintedBooks != 1 {
		t.Errorf("FingerprintedBooks = %d, want 1 (b2)", stats.FingerprintedBooks)
	}
	if stats.PartiallyFingerprintedBooks != 1 {
		t.Errorf("PartiallyFingerprintedBooks = %d, want 1 (b1)", stats.PartiallyFingerprintedBooks)
	}
	if stats.UnfingerprintedBooks != 1 {
		t.Errorf("UnfingerprintedBooks = %d, want 1 (b3)", stats.UnfingerprintedBooks)
	}
	if stats.FingerprintCoveragePercent != 25 {
		t.Errorf("FingerprintCoveragePercent = %d, want 25 (1/4 books)", stats.FingerprintCoveragePercent)
	}
	// BrokenFiles = distinct PRIMARY books with ≥1 book_file whose bytes are gone
	// (Missing==true): b1 (two missing files, counted once) and b2. b3 has no file
	// rows so it cannot be broken; b4's missing file is excluded as non-primary.
	// This is the counter the dashboard's "Broken Files" tile reads — proving it
	// is derived from the Missing flag, not the dead book_file_errors_by_book: index.
	if stats.BrokenFiles != 2 {
		t.Errorf("BrokenFiles = %d, want 2 (b1, b2; b4 non-primary excluded, b3 has no files)", stats.BrokenFiles)
	}
}

func idList(books []Book) []string {
	out := make([]string, len(books))
	for i, b := range books {
		out[i] = b.ID
	}
	return out
}

// countPrimaryFixture is the synthetic library the CountPrimaryBooks tests
// share. Exactly four rows are live primaries (explicit true or unset), and
// exactly two of those have a number-leading title.
func countPrimaryFixture() []*Book {
	yes, no := true, false
	return []*Book{
		{ID: "cp-01", Title: "Alpha Tale", FilePath: "/l/cp-01.m4b", Format: "m4b"},                                                    // primary (unset)
		{ID: "cp-02", Title: "Beta Story", FilePath: "/l/cp-02.m4b", Format: "m4b", IsPrimaryVersion: &yes},                            // primary
		{ID: "cp-03", Title: "7 Sample Stories", FilePath: "/l/cp-03.m4b", Format: "m4b", IsPrimaryVersion: &yes},                      // primary, number-leading
		{ID: "cp-04", Title: "42 Answers", FilePath: "/l/cp-04.m4b", Format: "m4b"},                                                    // primary (unset), number-leading
		{ID: "cp-06", Title: "3 Duplicate Edition", FilePath: "/l/cp-06.m4b", Format: "m4b", IsPrimaryVersion: &no},                    // non-primary
		{ID: "cp-07", Title: "Delta Alt Edition", FilePath: "/l/cp-07.m4b", Format: "m4b", IsPrimaryVersion: &no},                      // non-primary
		{ID: "cp-08", Title: "Gamma Removed", FilePath: "/l/cp-08.m4b", Format: "m4b", MarkedForDeletion: &yes},                        // soft-deleted
		{ID: "cp-09", Title: "9 Removed Volume", FilePath: "/l/cp-09.m4b", Format: "m4b", MarkedForDeletion: &yes},                     // soft-deleted, number-leading
		{ID: "cp-10", Title: "5 Removed Alt", FilePath: "/l/cp-10.m4b", Format: "m4b", IsPrimaryVersion: &no, MarkedForDeletion: &yes}, // both
	}
}

// openWarmedCountStore writes the fixture to a real on-disk Pebble store,
// closes it, and reopens the directory, so the books reach memdb the way they
// do in production: WarmFromPebble decodes each row and stripBookForMemdb
// projects it. Inserting rows into memdb directly would prove nothing about
// whether the strip keeps the fields the count reads.
func openWarmedCountStore(t *testing.T) *PebbleStore {
	t.Helper()
	dir := t.TempDir()
	seed, err := NewPebbleStore(dir)
	require.NoError(t, err)
	seed.WaitForWarmup()
	for _, b := range countPrimaryFixture() {
		_, err := seed.CreateBook(b)
		require.NoError(t, err)
	}
	require.NoError(t, seed.Close())

	p, err := NewPebbleStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "memdb must publish after the reopen; the warm path is what is under test")
	return p
}

// invalidatePrimaryCount drops CountPrimaryBooks' TTL cache so the next call
// recomputes.
func invalidatePrimaryCount(p *PebbleStore) {
	p.primaryCountMu.Lock()
	p.primaryCountValid = false
	p.primaryCountMu.Unlock()
}

func TestMemStoreCountPrimaryBooksMatchesPebbleScan(t *testing.T) {
	p := openWarmedCountStore(t)

	memCount, memLeading, err := p.mem().CountPrimaryBooks()
	require.NoError(t, err)
	scanCount, scanLeading, err := p.countPrimaryBooksScan()
	require.NoError(t, err)

	require.Equal(t, 4, scanCount, "fixture sanity: four live primaries")
	require.Equal(t, 2, scanLeading, "fixture sanity: two number-leading primaries")
	require.Equal(t, scanCount, memCount, "memdb primary count must equal the Pebble scan")
	require.Equal(t, scanLeading, memLeading, "memdb number-leading count must equal the Pebble scan")
}

// A memdb known to be missing book rows must refuse, never answer short.
func TestMemStoreCountPrimaryBooksRefusesWhenShort(t *testing.T) {
	p := openWarmedCountStore(t)
	p.mem().recordLostRows(memTableBooks, 1)

	_, _, err := p.mem().CountPrimaryBooks()
	require.ErrorIs(t, err, ErrMemdbIncomplete)
}

// countFallbackLine scrapes the memdb fallback counter for the CountPrimaryBooks
// site from a private meter provider.
func countFallbackCounter(t *testing.T, p *PebbleStore) func() string {
	t.Helper()
	reg := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	require.NoError(t, err)
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	p.SetMeterProvider(mp)
	return func() string {
		rec := httptest.NewRecorder()
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		for l := range strings.SplitSeq(rec.Body.String(), "\n") {
			if strings.HasPrefix(l, "audiobook_organizer_memdb_fallback_reads_total{") &&
				strings.Contains(l, `site="CountPrimaryBooks"`) && strings.Contains(l, `outcome="fallback"`) {
				return l
			}
		}
		return ""
	}
}

// writeRawPrimaryBook adds one live primary book to Pebble ONLY, bypassing the
// memdb write-through, so the two backends disagree by exactly one and a test
// can tell which one answered.
func writeRawPrimaryBook(t *testing.T, p *PebbleStore) {
	t.Helper()
	b := &Book{ID: "cp-raw", Title: "Epsilon Raw", FilePath: "/l/cp-raw.m4b", Format: "m4b"}
	data, err := json.Marshal(stripBookSigForRow(b))
	require.NoError(t, err)
	require.NoError(t, p.db.Set([]byte("book:cp-raw"), data, pebble.Sync))
}

func TestCountPrimaryBooksFallsBackBeforeWarm(t *testing.T) {
	p := openWarmedCountStore(t)
	scrape := countFallbackCounter(t, p)
	writeRawPrimaryBook(t, p) // Pebble sees 5 live primaries, memdb still 4

	// memdb unpublished: the Pebble scan answers, and the miss is counted.
	m := p.mem()
	p.memPtr.Store(nil)
	got, err := p.CountPrimaryBooks()
	require.NoError(t, err)
	require.Equal(t, 5, got, "unpublished memdb must be answered by the Pebble scan")
	line := scrape()
	require.NotEmpty(t, line, "the CountPrimaryBooks fallback must be recorded")
	require.True(t, strings.HasSuffix(line, " 1"), "want exactly one fallback read, got %q", line)

	// memdb published: it answers (the raw row is invisible to it), and no
	// further fallback is recorded.
	p.memPtr.Store(m)
	invalidatePrimaryCount(p)
	got, err = p.CountPrimaryBooks()
	require.NoError(t, err)
	require.Equal(t, 4, got, "published memdb must answer, not the Pebble scan")
	require.True(t, strings.HasSuffix(scrape(), " 1"), "a published memdb must not record a fallback, got %q", scrape())
}

// ErrMemdbIncomplete falls through to the authoritative scan instead of
// serving a short count as a fact for the cache TTL.
func TestCountPrimaryBooksFallsBackWhenMemdbShort(t *testing.T) {
	p := openWarmedCountStore(t)
	scrape := countFallbackCounter(t, p)
	writeRawPrimaryBook(t, p)
	p.mem().recordLostRows(memTableBooks, 1)

	got, err := p.CountPrimaryBooks()
	require.NoError(t, err)
	require.Equal(t, 5, got, "a short memdb must be answered by the Pebble scan")
	require.True(t, strings.HasSuffix(scrape(), " 1"), "the short-memdb fall-through must be recorded, got %q", scrape())
}

// shortMemdbLogRecords returns the "memdb is missing rows" records, by level.
func shortMemdbLogRecords(rec *logtest.Recorder) (errors, debugs int) {
	for _, r := range rec.Records() {
		if !strings.Contains(r.Message, "memdb is missing rows") {
			continue
		}
		switch r.Level {
		case slog.LevelError:
			errors++
		case slog.LevelDebug:
			debugs++
		}
	}
	return errors, debugs
}

// The short-memdb condition lasts until restart and CountPrimaryBooks recomputes
// every TTL window (the 5s metrics tick), so only the first occurrence may be
// an ERROR. When memdb answers again the next episode must log at ERROR once
// more, not stay muted forever.
func TestCountPrimaryBooksShortMemdbLogsErrorOnce(t *testing.T) {
	p := openWarmedCountStore(t)
	rec := logtest.CaptureRecords(t) // not parallel: swaps slog.Default

	recompute := func() {
		t.Helper()
		invalidatePrimaryCount(p)
		_, err := p.CountPrimaryBooks()
		require.NoError(t, err)
	}
	clearLostRows := func() {
		p.mem().lostMu.Lock()
		p.mem().lostRows = nil
		p.mem().lostMu.Unlock()
	}

	p.mem().recordLostRows(memTableBooks, 1)
	for range 3 {
		recompute()
	}
	errs, debugs := shortMemdbLogRecords(rec)
	require.Equal(t, 1, errs, "three recomputes against a short memdb must log ERROR once")
	require.Equal(t, 2, debugs, "the repeats must still be recorded, at Debug")

	// memdb complete again: no short log, and the flag resets.
	clearLostRows()
	recompute()
	errs, debugs = shortMemdbLogRecords(rec)
	require.Equal(t, 1, errs)
	require.Equal(t, 2, debugs)

	// A second episode is a new ERROR.
	p.mem().recordLostRows(memTableBooks, 1)
	recompute()
	errs, _ = shortMemdbLogRecords(rec)
	require.Equal(t, 2, errs, "a new short episode after memdb recovered must log ERROR again")
}
