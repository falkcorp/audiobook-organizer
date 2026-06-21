// file: internal/itunes/service/fs_regroup_test.go
// version: 2.0.0
// guid: 9f1c3a72-5e84-4b60-a3d7-2c8e0f6b1d49
// last-edited: 2026-06-20

package itunesservice

import (
	"fmt"
	"testing"
)

const root = "/mnt/bigdata/books/audiobook-organizer"

// chapter builds a true shattered chapter book:
// `<root>/<author>/<book> - <book>/<book> - <n>/<file>` (book folder named after the book).
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

// seriesVolume builds a series VOLUME stored as a single file in a numbered folder
// directly under the author dir: `<root>/<author>/<series> - <n>/<file>`. The parent
// folder (the author) is NOT named after the series, so these must never be merged.
func seriesVolume(id, author, series string, n int) FSBook {
	return FSBook{
		ID:        id,
		Title:     fmt.Sprintf("%s %d", series, n),
		AuthorID:  1,
		FilePath:  fmt.Sprintf("%s/%s/%s - %d/book.m4b", root, author, series, n),
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
	got, st := GroupShatteredBooks(books)
	if len(got) != 1 {
		t.Fatalf("want 1 target, got %d", len(got))
	}
	tg := got[0]
	if len(tg.Members) != 47 {
		t.Errorf("want 47 members, got %d", len(tg.Members))
	}
	if tg.Title != "Cage of Souls" || tg.AuthorID != 7 || tg.ASIN != "B0BTDSTWG9" {
		t.Errorf("identity wrong: title=%q author=%d asin=%q", tg.Title, tg.AuthorID, tg.ASIN)
	}
	if !tg.Cohesive {
		t.Errorf("expected cohesive group")
	}
	for i, m := range tg.Members {
		_, _, num, _ := chapterParts(m.FilePath)
		if num != i+1 {
			t.Errorf("member %d out of order: chapter %d", i, num)
		}
	}
	if st.GroupedRecords != 47 || st.ShatteredBooks != 1 {
		t.Errorf("stats: %+v", st)
	}
}

// Title comes from the folder prefix even when Book.Title is empty (the common case
// for the biggest shattered books like Elantris).
func TestGroupShatteredBooks_EmptyTitleUsesPrefix(t *testing.T) {
	var books []FSBook
	for i := 1; i <= 12; i++ {
		books = append(books, chapter(fmt.Sprintf("e%02d", i), "", 3, "", "Brandon Sanderson", "Elantris", i))
	}
	got, _ := GroupShatteredBooks(books)
	if len(got) != 1 || got[0].Title != "Elantris" {
		t.Fatalf("want title from prefix 'Elantris', got %+v", got)
	}
}

func TestGroupShatteredBooks_SeriesVolumesNotMerged(t *testing.T) {
	// Mistborn 1/2/3 as single files under the author dir must NOT collapse to one book.
	books := []FSBook{
		seriesVolume("m1", "Brandon Sanderson", "Mistborn", 1),
		seriesVolume("m2", "Brandon Sanderson", "Mistborn", 2),
		seriesVolume("m3", "Brandon Sanderson", "Mistborn", 3),
	}
	got, st := GroupShatteredBooks(books)
	if len(got) != 0 {
		t.Fatalf("series volumes must not be merged, got %d targets", len(got))
	}
	if st.PrefixNotInParent == 0 {
		t.Errorf("expected prefix-not-in-parent guard to fire; stats %+v", st)
	}
}

func TestGroupShatteredBooks_FlatDumpNotMerged(t *testing.T) {
	// abooks/<Book> - N/file: different books under one flat dir, parent "abooks" is not
	// named after any book → not grouped (would be a false merge).
	mk := func(id, book string, n int) FSBook {
		return FSBook{ID: id, Title: book, AuthorID: 0,
			FilePath: fmt.Sprintf("%s/abooks/%s - %d/f.mp3", root, book, n), FileCount: 1, IsPrimary: true}
	}
	books := []FSBook{
		mk("a1", "Throne of Jade", 1), mk("a2", "Throne of Jade", 2),
		mk("b1", "His Majesty's Dragon", 1), mk("b2", "His Majesty's Dragon", 2),
	}
	got, st := GroupShatteredBooks(books)
	if len(got) != 0 {
		t.Fatalf("flat-dump books must not be merged, got %d targets", len(got))
	}
	if st.PrefixNotInParent != 2 {
		t.Errorf("expected 2 prefix-not-in-parent groups, stats %+v", st)
	}
}

func TestGroupShatteredBooks_LoneSingleFileNotShattered(t *testing.T) {
	books := []FSBook{chapter("x", "Solo", 1, "", "A", "Solo", 1)}
	got, st := GroupShatteredBooks(books)
	if len(got) != 0 {
		t.Errorf("want 0 targets for a lone single-file book, got %d", len(got))
	}
	if st.SingletonGroups != 1 {
		t.Errorf("expected 1 singleton group, stats %+v", st)
	}
}

func TestGroupShatteredBooks_MultiFileBookSkipped(t *testing.T) {
	b1 := chapter("m1", "RealBook", 2, "", "Auth", "RealBook", 1)
	b1.FileCount = 12
	b2 := chapter("m2", "RealBook", 2, "", "Auth", "RealBook", 2)
	b2.FileCount = 12
	got, st := GroupShatteredBooks([]FSBook{b1, b2})
	if len(got) != 0 {
		t.Errorf("multi-file books must be skipped, got %d targets", len(got))
	}
	if st.MultiFile != 2 {
		t.Errorf("expected 2 multi-file skips, stats %+v", st)
	}
}

func TestGroupShatteredBooks_ASINMinorityIgnored(t *testing.T) {
	books := []FSBook{
		chapter("c1", "T", 3, "GOOD", "Auth", "T", 1),
		chapter("c2", "T", 3, "GOOD", "Auth", "T", 2),
		chapter("c3", "T", 3, "STRAY", "Auth", "T", 3),
	}
	got, _ := GroupShatteredBooks(books)
	if len(got) != 1 || got[0].ASIN != "GOOD" {
		t.Errorf("want dominant ASIN GOOD, got %+v", got)
	}
}

func TestGroupShatteredBooks_SurvivorIsRichest(t *testing.T) {
	b1 := chapter("s1", "T", 1, "", "Auth", "T", 1)
	b2 := chapter("s2", "T", 1, "", "Auth", "T", 2)
	b3 := chapter("s3", "T", 1, "", "Auth", "T", 3)
	b2.EnrichScore = 5
	b3.EnrichScore = 2
	got, _ := GroupShatteredBooks([]FSBook{b1, b2, b3})
	if len(got) != 1 || got[0].SurvivorID != "s2" {
		t.Errorf("survivor should be richest s2, got %+v", got)
	}
}

func TestGroupShatteredBooks_NonPrimaryIgnored(t *testing.T) {
	b1 := chapter("p1", "VG", 1, "", "Auth", "VG", 1)
	b2 := chapter("p2", "VG", 1, "", "Auth", "VG", 2)
	b2.IsPrimary = false
	got, st := GroupShatteredBooks([]FSBook{b1, b2})
	if len(got) != 0 {
		t.Errorf("non-primary members must be ignored, got %d targets", len(got))
	}
	if st.NonPrimary != 1 {
		t.Errorf("expected 1 non-primary skip, stats %+v", st)
	}
}
