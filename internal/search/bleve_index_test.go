// file: internal/search/bleve_index_test.go
// version: 1.1.0
// guid: 8e2c4a1d-5b9f-4f70-a7d6-2f8e0c1b9a58
// last-edited: 2026-07-10

package search

import (
	"path/filepath"
	"testing"
)

func openTestIndex(t *testing.T) *BleveIndex {
	t.Helper()
	idx, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

func TestBleveIndex_OpenAndClose(t *testing.T) {
	idx := openTestIndex(t)
	if err := idx.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	// Second close is safe.
	if err := idx.Close(); err != nil {
		t.Errorf("second close should be no-op, got: %v", err)
	}
}

func TestBleveIndex_IndexAndSearch(t *testing.T) {
	idx := openTestIndex(t)

	docs := []BookDocument{
		{BookID: "b1", Title: "The Way of Kings", Author: "Brandon Sanderson", Series: "Stormlight Archive", SeriesNumber: 1, Year: 2010, Format: "m4b"},
		{BookID: "b2", Title: "Words of Radiance", Author: "Brandon Sanderson", Series: "Stormlight Archive", SeriesNumber: 2, Year: 2014, Format: "m4b"},
		{BookID: "b3", Title: "The Fifth Season", Author: "N. K. Jemisin", Series: "Broken Earth", SeriesNumber: 1, Year: 2015, Format: "mp3"},
	}
	for _, d := range docs {
		if err := idx.IndexBook(d); err != nil {
			t.Fatalf("index %s: %v", d.BookID, err)
		}
	}

	got, err := idx.DocCount()
	if err != nil {
		t.Fatalf("doccount: %v", err)
	}
	if got != 3 {
		t.Errorf("DocCount = %d, want 3", got)
	}

	// Author match — should find both Sanderson books.
	hits, total, err := idx.Search("author:sanderson", 0, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (both Sanderson books)", total)
	}
	if len(hits) != 2 {
		t.Errorf("hits = %d, want 2", len(hits))
	}

	// Title stemming — "kings" should match "Kings" (English stemmer).
	hits, _, err = idx.Search("title:kings", 0, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].BookID != "b1" {
		t.Errorf("title:kings hits = %v, want [b1]", hitIDs(hits))
	}
}

func TestBleveIndex_Reindex(t *testing.T) {
	idx := openTestIndex(t)

	_ = idx.IndexBook(BookDocument{BookID: "b1", Title: "The Way of Kings", Author: "Sanderson"})
	// Re-index with new author.
	_ = idx.IndexBook(BookDocument{BookID: "b1", Title: "The Way of Kings", Author: "Someone Else"})

	hits, _, _ := idx.Search("author:sanderson", 0, 10)
	if len(hits) != 0 {
		t.Errorf("re-index should have replaced author; still found %d hits", len(hits))
	}
	hits, _, _ = idx.Search("author:someone", 0, 10)
	if len(hits) != 1 {
		t.Errorf("re-index should find new author; got %d hits", len(hits))
	}
}

func TestBleveIndex_Delete(t *testing.T) {
	idx := openTestIndex(t)

	_ = idx.IndexBook(BookDocument{BookID: "b1", Title: "Test One"})
	_ = idx.IndexBook(BookDocument{BookID: "b2", Title: "Test Two"})

	if err := idx.DeleteBook("b1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	c, _ := idx.DocCount()
	if c != 1 {
		t.Errorf("DocCount after delete = %d, want 1", c)
	}

	// Delete of absent ID is a no-op (not an error).
	if err := idx.DeleteBook("nonexistent"); err != nil {
		t.Errorf("delete of absent ID should not error, got: %v", err)
	}
}

func TestBleveIndex_ReopenPreservesDocs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bleve")
	idx, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := idx.IndexBook(BookDocument{BookID: "b1", Title: "Persistent"}); err != nil {
		t.Fatalf("index: %v", err)
	}
	_ = idx.Close()

	idx2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer idx2.Close()

	c, _ := idx2.DocCount()
	if c != 1 {
		t.Errorf("after reopen DocCount = %d, want 1", c)
	}
	hits, _, _ := idx2.Search("title:persistent", 0, 10)
	if len(hits) != 1 {
		t.Errorf("search after reopen returned %d hits, want 1", len(hits))
	}
}

// TestBleveIndex_FreeTextBoost_TitleRanksAboveDescription is an
// end-to-end pin for the free-text field-boost fan-out in
// translateFreeText: the same term appearing in doc a's title
// (boost 3.0) must outrank it appearing only in doc b's description
// (boost 0.5).
func TestBleveIndex_FreeTextBoost_TitleRanksAboveDescription(t *testing.T) {
	idx := openTestIndex(t)

	docs := []BookDocument{
		{BookID: "a", Title: "Wandering Wizard", Author: "Someone"},
		{BookID: "b", Title: "Unrelated Book", Description: "A wizard walks into a bar."},
	}
	for _, d := range docs {
		if err := idx.IndexBook(d); err != nil {
			t.Fatalf("index %s: %v", d.BookID, err)
		}
	}

	ast, err := ParseQuery("wizard")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bq, _, err := Translate(ast)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hits, total, err := idx.SearchNative(bq, 0, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (both a and b match \"wizard\")", total)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].BookID != "a" {
		t.Errorf("top hit = %s, want a (title boost 3.0 should outrank description boost 0.5); order = %v", hits[0].BookID, hitIDs(hits))
	}
}

// TestBleveIndex_FreeTextRecall_TagOnlyMatchStillReturned is the
// anti-over-suppression pin: a doc matching a free-text term ONLY
// via a non-boosted _all field (tags, not in textFieldBoosts) must
// still be returned via the unfielded fallback child.
func TestBleveIndex_FreeTextRecall_TagOnlyMatchStillReturned(t *testing.T) {
	idx := openTestIndex(t)

	// "zephyr" is unaffected by the English stemmer (stems to itself),
	// so it isolates the recall behavior under test from an unrelated
	// analyzer-mismatch quirk between the tags field's "standard"
	// analyzer and the _all composite field's default "en" analyzer.
	if err := idx.IndexBook(BookDocument{BookID: "c", Title: "Something Else", Tags: []string{"zephyr"}}); err != nil {
		t.Fatalf("index: %v", err)
	}

	ast, err := ParseQuery("zephyr")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bq, _, err := Translate(ast)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hits, total, err := idx.SearchNative(bq, 0, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 || len(hits) != 1 || hits[0].BookID != "c" {
		t.Errorf("tag-only match = %v (total %d), want [c]", hitIDs(hits), total)
	}
}

func hitIDs(hits []SearchResult) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.BookID
	}
	return out
}
