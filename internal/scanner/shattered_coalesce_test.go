// file: internal/scanner/shattered_coalesce_test.go
// version: 1.1.0
// guid: 4c7e9a02-8d31-4b65-9f80-1a3c6d2e7b59
// last-edited: 2026-07-01

package scanner

import (
	"context"
	"reflect"
	"testing"
)

func sf(path string) Book { return Book{FilePath: path} }

// Shattered chapters (sibling "<prefix> - N" dirs under a book-named folder)
// collapse into ONE multi-file book with ordered segments.
func TestCoalesce_ShatteredMergesToOneBook(t *testing.T) {
	base := "/lib/Adrian Tchaikovsky/Cage of Souls - Cage of Souls"
	in := []Book{
		sf(base + "/Cage of Souls - 1/01.mp3"),
		sf(base + "/Cage of Souls - 2/01.mp3"),
		sf(base + "/Cage of Souls - 3/01.mp3"),
	}
	out := coalesceShatteredSiblings(context.Background(), in)
	if len(out) != 1 {
		t.Fatalf("got %d books, want 1: %+v", len(out), out)
	}
	want := []string{
		base + "/Cage of Souls - 1/01.mp3",
		base + "/Cage of Souls - 2/01.mp3",
		base + "/Cage of Souls - 3/01.mp3",
	}
	if !reflect.DeepEqual(out[0].SegmentFiles, want) {
		t.Errorf("segments = %v, want %v", out[0].SegmentFiles, want)
	}
}

// Segments must be ordered by NUMERIC chapter number, not lexical.
func TestCoalesce_OrdersChaptersNumerically(t *testing.T) {
	base := "/lib/A/Elantris - Elantris"
	in := []Book{
		sf(base + "/Elantris - 11/f.mp3"),
		sf(base + "/Elantris - 2/f.mp3"),
		sf(base + "/Elantris - 1/f.mp3"),
		sf(base + "/Elantris - 10/f.mp3"),
	}
	out := coalesceShatteredSiblings(context.Background(), in)
	if len(out) != 1 {
		t.Fatalf("got %d books, want 1", len(out))
	}
	want := []string{
		base + "/Elantris - 1/f.mp3",
		base + "/Elantris - 2/f.mp3",
		base + "/Elantris - 10/f.mp3",
		base + "/Elantris - 11/f.mp3",
	}
	if !reflect.DeepEqual(out[0].SegmentFiles, want) {
		t.Errorf("segments = %v, want numeric order %v", out[0].SegmentFiles, want)
	}
}

// Flat dumps (`abooks/<Book> - N/`) — prefix NOT a substring of the parent folder
// — must NOT be merged (the books are unrelated, sharing only a dump directory).
func TestCoalesce_FlatDumpNotMerged(t *testing.T) {
	in := []Book{
		sf("/mnt/bigdata/books/abooks/Throne of Jade - 1/f.mp3"),
		sf("/mnt/bigdata/books/abooks/Throne of Jade - 2/f.mp3"),
	}
	out := coalesceShatteredSiblings(context.Background(), in)
	if len(out) != 2 {
		t.Errorf("flat dump merged (got %d, want 2 untouched): %+v", len(out), out)
	}
}

// Series volumes stored as `Author/Series - N/file` — parent is the author dir,
// not a book-named folder — must NOT be merged (would destroy distinct volumes).
func TestCoalesce_SeriesVolumesNotMerged(t *testing.T) {
	in := []Book{
		sf("/lib/Brandon Sanderson/Stormlight - 1/book.m4b"),
		sf("/lib/Brandon Sanderson/Stormlight - 2/book.m4b"),
		sf("/lib/Brandon Sanderson/Stormlight - 3/book.m4b"),
	}
	out := coalesceShatteredSiblings(context.Background(), in)
	if len(out) != 3 {
		t.Errorf("series volumes merged (got %d, want 3 untouched): %+v", len(out), out)
	}
}

// A box set with two DISTINCT books under one folder must coalesce into TWO
// books (each book's own chapters), never cross-merge into one.
func TestCoalesce_BoxSetDistinctPrefixesSeparate(t *testing.T) {
	base := "/lib/Author/Apex Ascended - Apex Ascended"
	in := []Book{
		sf(base + "/Apex Ascended - 1/f.mp3"),
		sf(base + "/Apex Ascended - 2/f.mp3"),
		// a different book that happens to share the grandparent but a distinct prefix
		sf("/lib/Author/Other Book - Other Book/Other Book - 1/f.mp3"),
		sf("/lib/Author/Other Book - Other Book/Other Book - 2/f.mp3"),
	}
	out := coalesceShatteredSiblings(context.Background(), in)
	if len(out) != 2 {
		t.Fatalf("got %d books, want 2 (one per distinct prefix): %+v", len(out), out)
	}
	for _, b := range out {
		if len(b.SegmentFiles) != 2 {
			t.Errorf("book %q has %d segments, want 2", b.FilePath, len(b.SegmentFiles))
		}
	}
}

// A lone chapter (single member of a group) is not a shattered book.
func TestCoalesce_LoneChapterNotMerged(t *testing.T) {
	in := []Book{sf("/lib/A/Solo - Solo/Solo - 1/f.mp3")}
	out := coalesceShatteredSiblings(context.Background(), in)
	if len(out) != 1 || len(out[0].SegmentFiles) != 0 {
		t.Errorf("lone chapter altered: %+v", out)
	}
}

// Already-multi-file books pass through untouched; unrelated books are preserved.
func TestCoalesce_PassThroughNonCandidates(t *testing.T) {
	multi := Book{FilePath: "/lib/A/Book/01.mp3", SegmentFiles: []string{"/lib/A/Book/01.mp3", "/lib/A/Book/02.mp3"}}
	other := sf("/lib/A/Standalone/whole.m4b")
	in := []Book{multi, other}
	out := coalesceShatteredSiblings(context.Background(), in)
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
}
