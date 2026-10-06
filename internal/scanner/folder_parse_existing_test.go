// file: internal/scanner/folder_parse_existing_test.go
// version: 1.1.0
// guid: dc6ba60e-80dc-4e3a-97c9-2b0fcadcf86c
// last-edited: 2026-10-06

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Owner, 2026-10-05: the new folder parse is for searches and NEW imports
// only. A rescan of an existing row must not rewrite its title, author or
// series with folder-parse values; a value from a tag still lands, and a new
// row is created from the folder parse as-is.
func TestSaveBookToDatabase_FolderParseNeverRewritesExistingRows(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})
	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })
	rootDir := t.TempDir()
	config.AppConfig.RootDir = rootDir

	write := func(name string) string {
		t.Helper()
		p := filepath.Join(rootDir, name)
		if err := os.WriteFile(p, []byte("content of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	read := func(path string) (*database.Book, string, string) {
		t.Helper()
		b, err := store.GetBookByFilePath(path)
		if err != nil || b == nil {
			t.Fatalf("GetBookByFilePath(%s): %v", path, err)
		}
		author, series := "", ""
		if b.AuthorID != nil {
			if a, _ := store.GetAuthorByID(*b.AuthorID); a != nil {
				author = a.Name
			}
		}
		if b.SeriesID != nil {
			if s, _ := store.GetSeriesByID(*b.SeriesID); s != nil {
				series = s.Name
			}
		}
		return b, author, series
	}

	// An existing row, imported earlier.
	path := write("existing.m4b")
	if err := saveBookToDatabase(context.Background(), &Book{FilePath: path, Title: "Stored Title", Author: "Mara Quill",
		Series: "Stored Series", Position: 2, Narrator: "Stored Narrator", Format: ".m4b", Duration: 100}); err != nil {
		t.Fatal(err)
	}

	counts := func() (int, int, int) {
		t.Helper()
		a, err := store.GetAllAuthors()
		if err != nil {
			t.Fatal(err)
		}
		se, err := store.GetAllSeries()
		if err != nil {
			t.Fatal(err)
		}
		w, err := store.GetAllWorks()
		if err != nil {
			t.Fatal(err)
		}
		return len(a), len(se), len(w)
	}
	before, _, _ := read(path)
	if before.Narrator == nil || *before.Narrator != "Stored Narrator" {
		t.Fatalf("setup: narrator %v", before.Narrator)
	}
	a0, s0, w0 := counts()

	// Rescan: the folder parse now reads other values for every field.
	rescan := &Book{FilePath: path, Title: "Reparsed Title", Author: "Dorian Vex", Series: "Reparsed Series", Position: 5,
		Narrator: "Reparsed Narrator", Format: ".m4b", Duration: 100,
		folderParse: folderParsed{Title: "Reparsed Title", Author: "Dorian Vex", Series: "Reparsed Series", Narrator: "Reparsed Narrator"}}
	if err := saveBookToDatabase(context.Background(), rescan); err != nil {
		t.Fatal(err)
	}
	b, author, series := read(path)
	if b.Title != "Stored Title" || author != "Mara Quill" || series != "Stored Series" {
		t.Fatalf("existing row rewritten by the folder parse: title=%q author=%q series=%q", b.Title, author, series)
	}
	if b.SeriesSequence == nil || *b.SeriesSequence != 2 {
		t.Fatalf("series position rewritten: %v", b.SeriesSequence)
	}
	if b.Narrator == nil || *b.Narrator != "Stored Narrator" {
		t.Fatalf("narrator rewritten: %v", b.Narrator)
	}
	if !eqStr(b.WorkID, before.WorkID) {
		t.Fatalf("work changed: %v -> %v", before.WorkID, b.WorkID)
	}
	if a1, s1, w1 := counts(); a1 != a0 || s1 != s0 || w1 != w0 {
		t.Fatalf("rescan created rows from the folder parse: authors %d->%d series %d->%d works %d->%d", a0, a1, s0, s1, w0, w1)
	}

	// A title-only folder value still holds the position read off it.
	posOnly := &Book{FilePath: path, Title: "Reparsed Title 9", Author: "Mara Quill", Position: 9, Format: ".m4b", Duration: 100,
		folderParse: folderParsed{Title: "Reparsed Title 9"}}
	if err := saveBookToDatabase(context.Background(), posOnly); err != nil {
		t.Fatal(err)
	}
	if b, _, _ = read(path); b.Title != "Stored Title" || b.SeriesSequence == nil || *b.SeriesSequence != 2 {
		t.Fatalf("title-derived position rewritten: title=%q seq=%v", b.Title, b.SeriesSequence)
	}

	// The same values from a TAG (no folder source) still land, as before.
	tagged := &Book{FilePath: path, Title: "Tagged Title", Author: "Mara Quill", Format: ".m4b", Duration: 100}
	if err := saveBookToDatabase(context.Background(), tagged); err != nil {
		t.Fatal(err)
	}
	if b, _, _ = read(path); b.Title != "Tagged Title" {
		t.Fatalf("a tag title must still update an existing row, got %q", b.Title)
	}

	// A new import takes the folder parse.
	fresh := write("fresh.m4b")
	if err := saveBookToDatabase(context.Background(), &Book{FilePath: fresh, Title: "Reparsed Title", Author: "Dorian Vex",
		Series: "Reparsed Series", Position: 5, Format: ".m4b", Duration: 100,
		folderParse: folderParsed{Title: "Reparsed Title", Author: "Dorian Vex", Series: "Reparsed Series"}}); err != nil {
		t.Fatal(err)
	}
	if b, author, series = read(fresh); b.Title != "Reparsed Title" || author != "Dorian Vex" || series != "Reparsed Series" {
		t.Fatalf("new import: title=%q author=%q series=%q, want the folder parse", b.Title, author, series)
	}
}
