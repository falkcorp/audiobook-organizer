// file: internal/search/index_builder_test.go
// version: 1.1.1
// guid: 9d8e2c1a-5b4f-4f70-a7c6-2d8e0f1b9a47

package search

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func TestBookToDoc_ResolvesRelations(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	author, _ := store.CreateAuthor("Brandon Sanderson")
	series, _ := store.CreateSeries("Stormlight Archive", &author.ID)
	seq := 1
	year := 2010
	lang := "en"
	book := &database.Book{
		ID: "b1", Title: "The Way of Kings",
		AuthorID: &author.ID, SeriesID: &series.ID,
		SeriesSequence:       &seq,
		Format:               "m4b",
		Language:             &lang,
		AudiobookReleaseYear: &year,
	}
	created, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	// Tag wiring is verified separately; PebbleStore tag methods have
	// their own semantics. Here we only verify the resolver doesn't
	// choke when tags are absent.

	doc := BookToDoc(store, created)

	if doc.BookID != "b1" {
		t.Errorf("BookID = %q", doc.BookID)
	}
	if doc.Title != "The Way of Kings" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.Author != "Brandon Sanderson" {
		t.Errorf("Author = %q, want resolved from AuthorID", doc.Author)
	}
	if doc.Series != "Stormlight Archive" {
		t.Errorf("Series = %q, want resolved from SeriesID", doc.Series)
	}
	if doc.SeriesNumber != 1 {
		t.Errorf("SeriesNumber = %d, want 1", doc.SeriesNumber)
	}
	if doc.Year != 2010 {
		t.Errorf("Year = %d, want 2010", doc.Year)
	}
	if doc.Format != "m4b" {
		t.Errorf("Format = %q", doc.Format)
	}
	if doc.Language != "en" {
		t.Errorf("Language = %q", doc.Language)
	}
	if doc.Type != BookDocType {
		t.Errorf("Type = %q", doc.Type)
	}
}

func TestTruncateForIndex(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 500, ""},
		{"under-limit", "hello", 500, "hello"},
		{"at-limit", strings.Repeat("a", 500), 500, strings.Repeat("a", 500)},
		{"truncate-ascii", strings.Repeat("a", 600), 500, strings.Repeat("a", 500)},
		{"no-limit", strings.Repeat("a", 600), 0, strings.Repeat("a", 600)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateForIndex(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("truncateForIndex(%q, %d) len=%d want len=%d",
					tc.name, tc.n, len(got), len(tc.want))
			}
		})
	}
}

func TestTruncateForIndex_MultiByteRunesNotSplit(t *testing.T) {
	// Each rune is 3 bytes (CJK). 600 runes = 1800 bytes; truncating
	// to 500 runes must yield exactly 1500 bytes and still be valid
	// UTF-8 — i.e. never cut a rune mid-byte.
	in := strings.Repeat("漢", 600)
	out := truncateForIndex(in, 500)
	if !utf8.ValidString(out) {
		t.Fatalf("truncated output is not valid UTF-8")
	}
	if rc := utf8.RuneCountInString(out); rc != 500 {
		t.Errorf("rune count = %d, want 500", rc)
	}
}

func TestBookToDoc_TruncatesDescription(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	long := strings.Repeat("x", 5000)
	book := &database.Book{
		ID: "b-desc", Title: "Long Description", Format: "mp3",
		Description: &long,
	}
	created, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	doc := BookToDoc(store, created)
	limit := descriptionLimit()
	if limit > 0 && utf8.RuneCountInString(doc.Description) > limit {
		t.Errorf("doc.Description rune count = %d, want <= %d",
			utf8.RuneCountInString(doc.Description), limit)
	}
}

func TestBookToDoc_MissingRelationsSafelySkipped(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Book with nil AuthorID/SeriesID.
	book, _ := store.CreateBook(&database.Book{
		ID: "b2", Title: "Orphan Book", Format: "mp3",
	})
	doc := BookToDoc(store, book)
	if doc.Author != "" || doc.Series != "" {
		t.Errorf("expected empty Author/Series, got %q / %q", doc.Author, doc.Series)
	}
	if doc.Title != "Orphan Book" {
		t.Errorf("Title = %q", doc.Title)
	}
}
