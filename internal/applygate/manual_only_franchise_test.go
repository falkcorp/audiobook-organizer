// file: internal/applygate/manual_only_franchise_test.go
// version: 1.0.1
// guid: 4c1e8a73-9b2d-4f60-a5e7-3d8f1b6c2a94
// last-edited: 2026-10-04

package applygate

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type moFake struct {
	files   []database.BookFile
	tags    []database.BookTag
	tagErr  error
	authors map[int]*database.Author
	links   []database.BookAuthor
}

func (f moFake) GetBookFiles(string) ([]database.BookFile, error) { return f.files, nil }
func (f moFake) GetBookTagsDetailed(string) ([]database.BookTag, error) {
	return f.tags, f.tagErr
}
func (f moFake) GetBookAuthors(string) ([]database.BookAuthor, error) { return f.links, nil }
func (f moFake) GetAuthorByID(id int) (*database.Author, error)       { return f.authors[id], nil }

func moReaders(f moFake) ManualOnlyReaders {
	return ManualOnlyReaders{Files: f, Authors: f, Tags: f}
}

func sp(s string) *string { return &s }

// The bulk-apply guard now reads narrator, publisher, author credits and the
// book's franchise tags (census gaps 1 and 2).
func TestBulkManualOnlyGuard_CreditsAndTags(t *testing.T) {
	neutral := func() *database.Book {
		return &database.Book{ID: "b", Title: "Gift of the Gods", FilePath: "/lib/Unknown Author/Gift of the Gods"}
	}
	b := neutral()
	b.Narrator = sp("Stargate SG-1 - Series 2")
	if g := BulkManualOnlyGuard(moReaders(moFake{}), b, ""); !strings.Contains(g.StoreDetail, "narrator") {
		t.Errorf("narrator album not held: %+v", g)
	}
	b = neutral()
	b.Publisher = sp("Big Finish Productions")
	if g := BulkManualOnlyGuard(moReaders(moFake{}), b, ""); !strings.Contains(g.StoreDetail, "publisher") {
		t.Errorf("publisher not held: %+v", g)
	}
	b = neutral()
	id := 7
	b.AuthorID = &id
	f := moFake{authors: map[int]*database.Author{7: {ID: 7, Name: "Big Finish Productions"}}}
	if g := BulkManualOnlyGuard(moReaders(f), b, ""); !strings.Contains(g.StoreDetail, "author") {
		t.Errorf("author credit not held: %+v", g)
	}
	f = moFake{tags: []database.BookTag{{Tag: "franchise:doctor-who", Source: "franchise-matcher"}}}
	if g := BulkManualOnlyGuard(moReaders(f), neutral(), ""); !strings.Contains(g.StoreDetail, "franchise:doctor-who") {
		t.Errorf("franchise tag not held: %+v", g)
	}
	f = moFake{tags: []database.BookTag{{Tag: "franchise:star-wars", Source: "user"}}}
	if g := BulkManualOnlyGuard(moReaders(f), neutral(), ""); g.StoreDetail != "" {
		t.Errorf("another franchise's tag held: %+v", g)
	}
	f = moFake{tagErr: errors.New("down")}
	if g := BulkManualOnlyGuard(moReaders(f), neutral(), ""); g.ReadErr == "" {
		t.Errorf("tag read failure must fail closed: %+v", g)
	}
	if g := BulkManualOnlyGuard(moReaders(moFake{}), neutral(), ""); g.StoreDetail != "" || g.ReadErr != "" {
		t.Errorf("neutral book held: %+v", g)
	}
	// A census range term on a book_file path (Big Finish download folder).
	f = moFake{files: []database.BookFile{{FilePath: "/newbooks/wmaster11-the-war-master.mp3a/01.mp3"}}}
	if g := BulkManualOnlyGuard(moReaders(f), neutral(), ""); !strings.Contains(g.StoreDetail, "file") {
		t.Errorf("BF download folder not held: %+v", g)
	}
}

// ManualOnlyDetail reads the narrator and publisher without a store.
func TestManualOnlyDetail_NarratorPublisher(t *testing.T) {
	b := &database.Book{ID: "b", Title: "Killing Time", FilePath: "/lib/x", Narrator: sp("The War Master - Series 12")}
	if r, _ := ManualOnlyDetail(b, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true}); r != ReasonOwnerManualOnly {
		t.Errorf("narrator album: reason %q", r)
	}
	b = &database.Book{ID: "b", Title: "War Master's Gate", FilePath: "/lib/Adrian Tchaikovsky/x"}
	if r, d := ManualOnlyDetail(b, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true}); r != "" {
		t.Errorf("Tchaikovsky held: %s", d)
	}
}
