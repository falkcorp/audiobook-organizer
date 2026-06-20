// file: internal/itunes/service/fs_regroup_test.go
// version: 1.0.0
// guid: 9f1c3a72-5e84-4b60-a3d7-2c8e0f6b1d49
// last-edited: 2026-06-20

package itunesservice

import (
	"fmt"
	"testing"
)

const root = "/mnt/bigdata/books/audiobook-organizer"

// chapter builds a shattered single-file chapter book under
// `<root>/<author>/<book> - <book>/<book> - <n>/<file>`.
func chapter(id, title string, authorID int, asin, author, book string, n int) FSBook {
	return FSBook{
		ID:        id,
		Title:     title,
		AuthorID:  authorID,
		ASIN:      asin,
		FilePath:  fmt.Sprintf("%s/%s/%s - %s/%s - %d/%d.mp3", root, author, book, book, book, n, 47),
		FileCount: 1,
		IsPrimary: true,
	}
}

func TestGroupShatteredBooks_CageOfSouls(t *testing.T) {
	var books []FSBook
	for i := 1; i <= 47; i++ {
		books = append(books, chapter(
			fmt.Sprintf("b%02d", i), "Cage of Souls", 7, "B0BTDSTWG9",
			"Adrian Tchaikovsky", "Cage of Souls", i))
	}
	got := GroupShatteredBooks(books)
	if len(got) != 1 {
		t.Fatalf("want 1 target, got %d", len(got))
	}
	tg := got[0]
	if len(tg.Members) != 47 {
		t.Errorf("want 47 members, got %d", len(tg.Members))
	}
	if tg.Title != "Cage of Souls" || tg.AuthorID != 7 || tg.ASIN != "B0BTDSTWG9" {
		t.Errorf("identity wrong: %+v", tg)
	}
	if !tg.Cohesive {
		t.Errorf("expected cohesive group")
	}
	// ordered by chapter number
	for i, m := range tg.Members {
		if chapterNumOf(m.FilePath) != i+1 {
			t.Errorf("member %d out of order: chapter %d", i, chapterNumOf(m.FilePath))
		}
	}
}

func TestGroupShatteredBooks_LoneSingleFileNotShattered(t *testing.T) {
	// one chapter dir alone under a folder is not a shattered set
	books := []FSBook{chapter("x", "Solo", 1, "", "A", "Solo", 1)}
	if got := GroupShatteredBooks(books); len(got) != 0 {
		t.Errorf("want 0 targets for a lone single-file book, got %d", len(got))
	}
}

func TestGroupShatteredBooks_MultiFileBookSkipped(t *testing.T) {
	b1 := chapter("m1", "RealBook", 2, "", "Auth", "RealBook", 1)
	b1.FileCount = 12 // already a real multi-file audiobook
	b2 := chapter("m2", "RealBook", 2, "", "Auth", "RealBook", 2)
	b2.FileCount = 12
	if got := GroupShatteredBooks([]FSBook{b1, b2}); len(got) != 0 {
		t.Errorf("multi-file books must be skipped, got %d targets", len(got))
	}
}

func TestGroupShatteredBooks_MixedTitlesFlaggedNotCohesive(t *testing.T) {
	// two distinct books accidentally under one grandparent → flagged, not silently merged
	books := []FSBook{
		chapter("a1", "Book One", 5, "", "Auth", "Mixed", 1),
		chapter("a2", "Book One", 5, "", "Auth", "Mixed", 2),
		chapter("a3", "Book Two", 5, "", "Auth", "Mixed", 3),
	}
	got := GroupShatteredBooks(books)
	if len(got) != 1 {
		t.Fatalf("want 1 (folder) target, got %d", len(got))
	}
	if got[0].Cohesive {
		t.Errorf("mixed-title folder should be flagged non-cohesive")
	}
	if len(got[0].DistinctTitles) != 2 {
		t.Errorf("want 2 distinct titles, got %v", got[0].DistinctTitles)
	}
	if got[0].Title != "Book One" { // majority wins
		t.Errorf("consensus title should be majority 'Book One', got %q", got[0].Title)
	}
}

func TestGroupShatteredBooks_ASINMinorityIgnored(t *testing.T) {
	// a single stray mis-enriched ASIN must not dictate identity
	books := []FSBook{
		chapter("c1", "T", 3, "GOOD", "Auth", "T", 1),
		chapter("c2", "T", 3, "GOOD", "Auth", "T", 2),
		chapter("c3", "T", 3, "STRAY", "Auth", "T", 3),
	}
	got := GroupShatteredBooks(books)
	if len(got) != 1 || got[0].ASIN != "GOOD" {
		t.Errorf("want dominant ASIN GOOD, got %+v", got)
	}
}

func TestGroupShatteredBooks_NonPrimaryIgnored(t *testing.T) {
	b1 := chapter("p1", "VG", 1, "", "Auth", "VG", 1)
	b2 := chapter("p2", "VG", 1, "", "Auth", "VG", 2)
	b2.IsPrimary = false
	got := GroupShatteredBooks([]FSBook{b1, b2})
	if len(got) != 0 { // only one primary remains → not a shattered set
		t.Errorf("non-primary members must be ignored, got %d targets", len(got))
	}
}
