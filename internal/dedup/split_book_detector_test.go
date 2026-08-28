// file: internal/dedup/split_book_detector_test.go
// version: 1.2.0
// guid: 1f7e3b4c-c2a9-4c0e-9c83-1d5b6f0a8e74
// last-edited: 2026-08-28

package dedup

import (
	"fmt"
	"testing"
)

func intp(v int) *int { return &v }

// makeSlim is a tiny helper that builds a slim book with the given path
// under a shared author/series.
func makeSlim(id, title, path string, author, series *int) splitBookSlim {
	return splitBookSlim{
		ID:       id,
		Title:    title,
		FilePath: path,
		AuthorID: author,
		SeriesID: series,
	}
}

func TestDetect_ParentFlat_Tarkin(t *testing.T) {
	// 5 chapter files in the same parent dir — classic flat split-book.
	var books []splitBookSlim
	author := intp(42)
	for i := 1; i <= 5; i++ {
		path := fmt.Sprintf("/lib/Star Wars/Tarkin/%02d.mp3", i)
		books = append(books, makeSlim(
			fmt.Sprintf("01HZZZZZ%08d", i),
			"Tarkin",
			path,
			author, nil,
		))
	}
	got := detectFromSlim(books, func(id int) string { return "James Luceno" })
	if len(got) != 1 {
		t.Fatalf("want 1 candidate, got %d (%+v)", len(got), got)
	}
	c := got[0]
	if c.Shape != "parent" {
		t.Errorf("shape: want parent, got %q", c.Shape)
	}
	if c.SuggestedTitle != "Tarkin" {
		t.Errorf("title: want Tarkin, got %q", c.SuggestedTitle)
	}
	if c.SuggestedAuthor != "James Luceno" {
		t.Errorf("author: want James Luceno, got %q", c.SuggestedAuthor)
	}
	if len(c.BookIDs) != 5 {
		t.Errorf("want 5 book IDs, got %d", len(c.BookIDs))
	}
	if c.BookIDs[0] != "01HZZZZZ00000001" {
		t.Errorf("keep-ID should be earliest ULID, got %q", c.BookIDs[0])
	}
}

func TestDetect_Grandparent_RogueSubdir(t *testing.T) {
	// Each chapter file in its OWN subdir under the grandparent.
	// Parent groups would be size 1; grandparent recovers cluster.
	var books []splitBookSlim
	author := intp(7)
	for i := 1; i <= 5; i++ {
		path := fmt.Sprintf("/lib/Author/Tarkin/%d/chapter%02d.mp3", i, i)
		books = append(books, makeSlim(
			fmt.Sprintf("01HXXXXX%08d", i),
			"Tarkin",
			path,
			author, nil,
		))
	}
	got := detectFromSlim(books, func(id int) string { return "" })
	if len(got) != 1 {
		t.Fatalf("want 1 candidate, got %d (%+v)", len(got), got)
	}
	if got[0].Shape != "grandparent" {
		t.Errorf("shape: want grandparent, got %q", got[0].Shape)
	}
	if got[0].ParentFolder != "/lib/Author/Tarkin" {
		t.Errorf("parent folder: want /lib/Author/Tarkin, got %q", got[0].ParentFolder)
	}
}

func TestDetect_TooSmall(t *testing.T) {
	// 2 chapters is below the min-group-size of 3.
	author := intp(1)
	books := []splitBookSlim{
		makeSlim("a", "Title", "/lib/A/Title/01.mp3", author, nil),
		makeSlim("b", "Title", "/lib/A/Title/02.mp3", author, nil),
	}
	got := detectFromSlim(books, nil)
	if len(got) != 0 {
		t.Fatalf("want 0 candidates, got %d", len(got))
	}
}

func TestDetect_AuthorMismatch_Disqualifies(t *testing.T) {
	a, b := intp(1), intp(2)
	books := []splitBookSlim{
		makeSlim("a", "T", "/lib/X/T/01.mp3", a, nil),
		makeSlim("b", "T", "/lib/X/T/02.mp3", a, nil),
		makeSlim("c", "T", "/lib/X/T/03.mp3", b, nil),
	}
	got := detectFromSlim(books, nil)
	if len(got) != 0 {
		t.Fatalf("want 0 candidates, got %d (%+v)", len(got), got)
	}
}

func TestDetect_NumberedSiblingFilenamesIgnorePollutedMetadata(t *testing.T) {
	// These are the historical failure shape: each numbered track became a
	// separate book and the title/author/series IDs no longer agree. The shared
	// parent and filename stem are enough for a PREVIEW candidate, not a merge.
	books := []splitBookSlim{
		{ID: "a", Title: "01 - The Book", FilePath: "/lib/A/The Book/01 - The Book.mp3", OriginalFilename: "01 - The Book.mp3", AuthorID: intp(1), SeriesID: intp(10)},
		{ID: "b", Title: "02 - The Book", FilePath: "/lib/A/The Book/02 - The Book.mp3", OriginalFilename: "02 - The Book.mp3", AuthorID: intp(2), SeriesID: nil},
		{ID: "c", Title: "03 - The Book", FilePath: "/lib/A/The Book/03 - The Book.mp3", OriginalFilename: "03 - The Book.mp3", AuthorID: nil, SeriesID: intp(30)},
	}
	got := detectFromSlim(books, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 relaxed preview candidate, got %d (%+v)", len(got), got)
	}
	if got[0].Shape != "parent-relaxed" {
		t.Errorf("shape: want parent-relaxed, got %q", got[0].Shape)
	}
	if got[0].SequentialPattern == "" {
		t.Error("want filename-stem evidence")
	}
	if got[0].SuggestedTitle != "The Book" {
		t.Errorf("suggested title: want human-readable filename stem, got %q", got[0].SuggestedTitle)
	}
}

func TestDetect_NumberedSiblingRejectsStandaloneNumericTitles(t *testing.T) {
	books := []splitBookSlim{
		{ID: "a", Title: "1984", FilePath: "/lib/A/1984/1984.m4b", OriginalFilename: "1984.m4b"},
		{ID: "b", Title: "86-Neon", FilePath: "/lib/A/1984/86-Neon.m4b", OriginalFilename: "86-Neon.m4b"},
		{ID: "c", Title: "2001", FilePath: "/lib/A/1984/2001.m4b", OriginalFilename: "2001.m4b"},
	}
	if got := detectFromSlim(books, nil); len(got) != 0 {
		t.Fatalf("standalone numeric titles must not form a candidate: %+v", got)
	}
}

func TestDetect_SeriesMismatch_Disqualifies(t *testing.T) {
	a := intp(1)
	s1, s2 := intp(10), intp(20)
	books := []splitBookSlim{
		makeSlim("a", "T", "/lib/X/T/01.mp3", a, s1),
		makeSlim("b", "T", "/lib/X/T/02.mp3", a, s1),
		makeSlim("c", "T", "/lib/X/T/03.mp3", a, s2),
	}
	got := detectFromSlim(books, nil)
	if len(got) != 0 {
		t.Fatalf("want 0 candidates, got %d (%+v)", len(got), got)
	}
}

func TestDetect_NonSequential_Disqualifies(t *testing.T) {
	// Numbers are 1, 50, 99 — huge gaps; not a chapter cluster.
	a := intp(1)
	books := []splitBookSlim{
		makeSlim("a", "T", "/lib/X/T/01.mp3", a, nil),
		makeSlim("b", "T", "/lib/X/T/50.mp3", a, nil),
		makeSlim("c", "T", "/lib/X/T/99.mp3", a, nil),
	}
	got := detectFromSlim(books, nil)
	if len(got) != 0 {
		t.Fatalf("want 0 candidates, got %d", len(got))
	}
}

func TestDetect_ParentBeatsGrandparent(t *testing.T) {
	// One flat parent cluster of 4 PLUS one rogue grandparent of 3
	// elsewhere — both should be emitted, and no book double-claimed.
	a := intp(1)
	var books []splitBookSlim
	// Flat cluster under /lib/A/Flat.
	for i := 1; i <= 4; i++ {
		books = append(books, makeSlim(
			fmt.Sprintf("flat-%02d", i),
			"Flat Book",
			fmt.Sprintf("/lib/A/Flat/%02d.mp3", i),
			a, nil,
		))
	}
	// Rogue subdir cluster under /lib/A/Rogue.
	for i := 1; i <= 3; i++ {
		books = append(books, makeSlim(
			fmt.Sprintf("rogue-%02d", i),
			"Rogue Book",
			fmt.Sprintf("/lib/A/Rogue/%d/ch%02d.mp3", i, i),
			a, nil,
		))
	}
	got := detectFromSlim(books, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %d (%+v)", len(got), got)
	}
	// Confirm no book ID appears in two candidates.
	seen := make(map[string]int)
	for _, c := range got {
		for _, id := range c.BookIDs {
			seen[id]++
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("book %s appears in %d candidates", id, n)
		}
	}
}

func TestStripChapterMarker(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Tarkin (3/85)", "Tarkin"},
		{"Tarkin 3/85", "Tarkin"},
		{"Tarkin - Chapter 3", "Tarkin"},
		{"Tarkin 03", "Tarkin"},
		{"Tarkin", "Tarkin"},
		{"   Tarkin (Chapter 1)  ", "Tarkin"},
	}
	for _, c := range cases {
		got := stripChapterMarker(c.in)
		if got != c.want {
			t.Errorf("stripChapterMarker(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSequentialRun(t *testing.T) {
	cases := []struct {
		in   []int
		want bool
	}{
		{[]int{1, 2, 3, 4, 5}, true},
		{[]int{1, 2, 3, 5}, true},     // gap=2 ok
		{[]int{1, 2, 3, 6}, false},    // gap=3 fails
		{[]int{1, 50, 99}, false},     // sparse coverage fails
		{[]int{5, 5, 5}, false},       // not enough unique
		{[]int{1, 2}, false},          // too few
		{[]int{10, 11, 12, 13}, true}, // shifted run ok
	}
	for _, c := range cases {
		got, _ := sequentialRun(c.in)
		if got != c.want {
			t.Errorf("sequentialRun(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
