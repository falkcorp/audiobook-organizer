// file: internal/search/index_builder_relations_test.go
// version: 1.0.0
// guid: 1eddba97-38b6-4647-aaf6-6012f7a3c758
// last-edited: 2026-09-11

package search

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestBookToDocWithRelations_MatchesBookToDoc: the batch path must
// produce a document field-for-field identical to the per-book path for
// the same rows — with authors, series and tags present, and with each
// of them absent.
func TestBookToDocWithRelations_MatchesBookToDoc(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	author, _ := store.CreateAuthor("Relations Author")
	series, _ := store.CreateSeries("Relations Series", &author.ID)
	seq := 2
	books := []database.Book{
		{ID: "r1", Title: "Both", FilePath: "/tmp/r1", Format: "m4b", AuthorID: &author.ID, SeriesID: &series.ID, SeriesSequence: &seq},
		{ID: "r2", Title: "Author only", FilePath: "/tmp/r2", Format: "mp3", AuthorID: &author.ID},
		{ID: "r3", Title: "Neither", FilePath: "/tmp/r3", Format: "m4b"},
	}
	for i := range books {
		if _, err := store.CreateBook(&books[i]); err != nil {
			t.Fatalf("create %s: %v", books[i].ID, err)
		}
	}
	if err := store.AddBookTag("r1", "fantasy"); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if err := store.AddBookTag("r3", "untagged-author"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	rel, err := LoadBookRelations(store, books)
	if err != nil {
		t.Fatalf("LoadBookRelations: %v", err)
	}
	for i := range books {
		want := BookToDoc(store, &books[i])
		got := BookToDocWithRelations(&books[i], rel)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: batch doc %+v\n  want per-book doc %+v", books[i].ID, got, want)
		}
	}
	if rel.Authors[author.ID] == nil || rel.Series[series.ID] == nil || len(rel.Tags["r1"]) != 1 {
		t.Fatalf("relations not resolved: %+v", rel)
	}
}

type failingRelationsStore struct{ err error }

func (f failingRelationsStore) GetAuthorsByIDs([]int) (map[int]*database.Author, error) {
	return nil, f.err
}
func (f failingRelationsStore) GetSeriesByIDs([]int) (map[int]*database.Series, error) {
	return map[int]*database.Series{9: {ID: 9, Name: "Still Resolved"}}, nil
}
func (f failingRelationsStore) GetBookTagsByBookIDs([]string) (map[string][]string, error) {
	return nil, f.err
}

// TestLoadBookRelations_PartialFailureIsReturnedNotSwallowed: a failed
// batch read leaves that relation empty, keeps the others, and surfaces
// the error instead of hiding it the way the point-read path did.
func TestLoadBookRelations_PartialFailureIsReturnedNotSwallowed(t *testing.T) {
	boom := errors.New("boom")
	aid, sid := 4, 9
	books := []database.Book{{ID: "p1", AuthorID: &aid, SeriesID: &sid}}
	rel, err := LoadBookRelations(failingRelationsStore{err: boom}, books)
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped boom, got %v", err)
	}
	if rel == nil {
		t.Fatal("relations must be non-nil on partial failure")
	}
	doc := BookToDocWithRelations(&books[0], rel)
	if doc.Author != "" || doc.Series != "Still Resolved" || len(doc.Tags) != 0 {
		t.Fatalf("partial doc = %+v", doc)
	}
}
