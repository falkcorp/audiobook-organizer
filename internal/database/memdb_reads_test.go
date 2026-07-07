// file: internal/database/memdb_reads_test.go
// version: 1.4.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000007
// last-edited: 2026-07-07

package database

import (
	"reflect"
	"testing"
	"time"
)

// Local ptr helpers — names suffixed _mem to avoid conflict with poc_chai_test.go.
func ptrBool_mem(b bool) *bool      { return &b }
func ptrInt_mem(i int) *int         { return &i }
func ptrInt64_mem(i int64) *int64   { return &i } //nolint:unused // kept for future tests
func ptrString_mem(s string) *string { return &s } //nolint:unused // kept for future tests

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
		{ID: "b1", Title: "B1", SeriesID: ptrInt_mem(10), IsPrimaryVersion: ptrBool_mem(true)},
		// Primary, not deleted — counted (same series)
		{ID: "b2", Title: "B2", SeriesID: ptrInt_mem(10), IsPrimaryVersion: ptrBool_mem(true)},
		// Different series, counted
		{ID: "b3", Title: "B3", SeriesID: ptrInt_mem(20), IsPrimaryVersion: ptrBool_mem(true)},
		// Marked for deletion — skipped
		{ID: "b4", Title: "B4", SeriesID: ptrInt_mem(10), IsPrimaryVersion: ptrBool_mem(true), MarkedForDeletion: ptrBool_mem(true)},
		// Not primary — skipped (effectiveBoolFieldIndex stores false here)
		{ID: "b5", Title: "B5", SeriesID: ptrInt_mem(10), IsPrimaryVersion: ptrBool_mem(false)},
		// nil SeriesID — skipped
		{ID: "b6", Title: "B6", IsPrimaryVersion: ptrBool_mem(true)},
		// nil IsPrimaryVersion → effectively true, counted
		{ID: "b7", Title: "B7", SeriesID: ptrInt_mem(20)},
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
		{ID: "b1", Title: "A", AuthorID: ptrInt_mem(1), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b2", Title: "B", AuthorID: ptrInt_mem(1), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b3", Title: "C", AuthorID: ptrInt_mem(2), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b4", Title: "D", AuthorID: ptrInt_mem(1), MarkedForDeletion: ptrBool_mem(true), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b5", Title: "E", AuthorID: ptrInt_mem(1), IsPrimaryVersion: ptrBool_mem(false)},
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
		{ID: "b1", Title: "B1", SeriesID: ptrInt_mem(10), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b2", Title: "B2", SeriesID: ptrInt_mem(10), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b3", Title: "B3", SeriesID: ptrInt_mem(20), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b4", Title: "B4", SeriesID: ptrInt_mem(10), IsPrimaryVersion: ptrBool_mem(false)}, // skipped
	}
	files := []BookFile{
		{ID: "f1", BookID: "b1", Missing: false},
		{ID: "f2", BookID: "b1", Missing: false},
		{ID: "f3", BookID: "b2", Missing: false},
		{ID: "f4", BookID: "b3", Missing: false},
		{ID: "f5", BookID: "b3", Missing: true}, // missing — skipped
		{ID: "f6", BookID: "b4", Missing: false}, // non-primary book — skipped
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
		{ID: "b1", Title: "Book One", SeriesID: ptrInt_mem(10), SeriesSequence: ptrInt_mem(1), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b2", Title: "Book Three", SeriesID: ptrInt_mem(10), SeriesSequence: ptrInt_mem(3), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b3", Title: "Book Two", SeriesID: ptrInt_mem(10), SeriesSequence: ptrInt_mem(2), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b4", Title: "Other Series", SeriesID: ptrInt_mem(20), IsPrimaryVersion: ptrBool_mem(true)},
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
		{ID: "b1", Title: "Zebra", AuthorID: ptrInt_mem(1), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b2", Title: "Alpha", AuthorID: ptrInt_mem(1), IsPrimaryVersion: ptrBool_mem(true)},
		{ID: "b3", Title: "Other", AuthorID: ptrInt_mem(2), IsPrimaryVersion: ptrBool_mem(true)},
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
		{ID: "b1", Title: "A", IsPrimaryVersion: ptrBool_mem(true), AuthorID: ptrInt_mem(1), MarkedForDeletion: ptrBool_mem(false)},
		{ID: "b2", Title: "B", IsPrimaryVersion: ptrBool_mem(true), AuthorID: ptrInt_mem(2), MarkedForDeletion: ptrBool_mem(false)},
		{ID: "b3", Title: "C", IsPrimaryVersion: ptrBool_mem(false), AuthorID: ptrInt_mem(1), MarkedForDeletion: ptrBool_mem(false)},
		{ID: "b4", Title: "D", IsPrimaryVersion: ptrBool_mem(true), AuthorID: ptrInt_mem(1), MarkedForDeletion: ptrBool_mem(true)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	// author_id=1 && is_primary && !deleted → only b1
	got, err := m.GetAllBooksCore(100, 0, map[string]interface{}{
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

func TestMemStore_GetBookFilesNeedingDelugeImport(t *testing.T) {
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

	got, err := m.GetBookFilesNeedingDelugeImport()
	if err != nil {
		t.Fatalf("GetBookFilesNeedingDelugeImport: %v", err)
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
		{ID: "b2", Title: "Recently deleted", MarkedForDeletion: ptrBool_mem(true), MarkedForDeletionAt: &tRecent},
		// soft-deleted (old)
		{ID: "b3", Title: "Old deleted", MarkedForDeletion: ptrBool_mem(true), MarkedForDeletionAt: &tOlder},
		// soft-deleted, no timestamp — included, sorts last
		{ID: "b4", Title: "Timeless deleted", MarkedForDeletion: ptrBool_mem(true)},
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
		{ID: "b1", FilePath: "/mnt/books/a/one.m4b", SourceImportPath: ptrString_mem("/mnt/books/a")},
		{ID: "b2", FilePath: "/mnt/books/a/two.m4b", SourceImportPath: ptrString_mem("/mnt/books/a")},
		{ID: "b3", FilePath: "/mnt/books/b/three.m4b", SourceImportPath: ptrString_mem("/mnt/books/b")},
		// no SourceImportPath → falls back to FilePath
		{ID: "b4", FilePath: "/mnt/books/a/four.m4b"},
		// deleted — excluded
		{ID: "b5", FilePath: "/mnt/books/a/five.m4b", SourceImportPath: ptrString_mem("/mnt/books/a"), MarkedForDeletion: ptrBool_mem(true)},
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
		{ID: "b1", Title: "Org1", FilePath: "/library/x.m4b", IsPrimaryVersion: ptrBool_mem(true),
			Duration: ptrInt_mem(3600), FileSize: ptrInt64_mem(100), Codec: ptrString_mem("aac"), LibraryState: ptrString_mem("organized")},
		// unorganized under import path A
		{ID: "b2", Title: "Inbox1", FilePath: "/inbox/a/x.m4b", IsPrimaryVersion: ptrBool_mem(true),
			Duration: ptrInt_mem(7200), FileSize: ptrInt64_mem(200), Codec: ptrString_mem("aac")},
		// unorganized under import path B
		{ID: "b3", Title: "Inbox2", FilePath: "/inbox/b/y.m4b", IsPrimaryVersion: ptrBool_mem(true),
			FileSize: ptrInt64_mem(50)},
		// non-primary version — counted in totals, NOT in organized/unorganized
		{ID: "b4", Title: "Variant", FilePath: "/library/x-alt.m4b", IsPrimaryVersion: ptrBool_mem(false),
			FileSize: ptrInt64_mem(10)},
		// deleted — fully excluded
		{ID: "b5", Title: "Gone", FilePath: "/library/gone.m4b", IsPrimaryVersion: ptrBool_mem(true),
			MarkedForDeletion: ptrBool_mem(true), FileSize: ptrInt64_mem(999)},
	}
	files := []BookFile{
		// b1: 2 files, one fingerprinted (AcoustIDSeg0, the primary/non-fallback
		// path) and one not → partially fingerprinted.
		{ID: "f1", BookID: "b1", AcoustIDSeg0: "seg0-data"},
		{ID: "f2", BookID: "b1"},
		// b2: 1 file, fingerprinted via the memdb-safe duration fallback proxy
		// (AcoustIDSeg0 stripped, AcoustIDFingerprintDurationSec retained) →
		// fully fingerprinted.
		{ID: "f3", BookID: "b2", AcoustIDFingerprintDurationSec: 42.0},
		// b3 has no file rows → counted as 1, and as unfingerprinted.
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
}

func idList(books []Book) []string {
	out := make([]string, len(books))
	for i, b := range books {
		out[i] = b.ID
	}
	return out
}
