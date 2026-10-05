// file: internal/applygate/manual_only_franchise_test.go
// version: 1.4.0
// guid: 4c1e8a73-9b2d-4f60-a5e7-3d8f1b6c2a94
// last-edited: 2026-10-05

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
func (f moFake) GetSeriesByID(int) (*database.Series, error)          { return nil, nil }

func moReaders(f moFake) ManualOnlyReaders {
	return ManualOnlyReaders{Files: f, Series: f, Authors: f, Tags: f}
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

// Owner decision 2026-10-05: "Missy" alone in a narrator or publisher credit
// no longer holds a book; in a title, path or series it still does.
func TestMissyCreditNotHeld_TitleStillHeld(t *testing.T) {
	b := &database.Book{ID: "b", Title: "To Boldly Go", FilePath: "/lib/Em Stevens/To Boldly Go", Narrator: sp("Missy Cambridge")}
	if g := BulkManualOnlyGuard(moReaders(moFake{}), b, ""); g.StoreDetail != "" || g.ReadErr != "" {
		t.Errorf("narrator Missy Cambridge held by the store guard: %+v", g)
	}
	if r, d := ManualOnlyDetail(b, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true}); r != "" {
		t.Errorf("narrator Missy Cambridge held: %s", d)
	}
	b = &database.Book{ID: "b", Title: "x", FilePath: "/lib/x", Publisher: sp("Missy Elliott Publishing")}
	if r, d := ManualOnlyDetail(b, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true}); r != "" {
		t.Errorf("publisher Missy held: %s", d)
	}
	// The range's album shape in a narrator still holds.
	for _, n := range []string{"Missy - Series 2", "Missy Series 2", "Missy: The Lumiat", "Michelle Gomez - Missy"} {
		b = &database.Book{ID: "b", Title: "x", FilePath: "/lib/x", Narrator: sp(n)}
		if r, _ := ManualOnlyDetail(b, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true}); r != ReasonOwnerManualOnly {
			t.Errorf("narrator %q not held", n)
		}
		if g := BulkManualOnlyGuard(moReaders(moFake{}), b, ""); g.StoreDetail == "" {
			t.Errorf("narrator %q not held by the store guard", n)
		}
	}
	b = &database.Book{ID: "b", Title: "x", FilePath: "/lib/x", Narrator: sp("Missy Elliott")}
	if r, _ := ManualOnlyDetail(b, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true}); r != "" {
		t.Error("narrator Missy Elliott held")
	}
	// A narrator naming more than Missy still holds.
	b = &database.Book{ID: "b", Title: "x", FilePath: "/lib/x", Narrator: sp("Missy - Big Finish Productions")}
	if r, _ := ManualOnlyDetail(b, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true}); r != ReasonOwnerManualOnly {
		t.Error("narrator naming Big Finish not held")
	}
	// Title, path and series keep the original pattern.
	for _, b := range []*database.Book{
		{ID: "b", Title: "Missy and the Doctor", FilePath: "/lib/x"},
		{ID: "b", Title: "x", FilePath: "/lib/Missy/Series 2/01.mp3"},
	} {
		if r, _ := ManualOnlyDetail(b, nil, TranscribedSearch{}, ManualOnlyGuard{Bulk: true}); r != ReasonOwnerManualOnly {
			t.Errorf("%q / %q not held", b.Title, b.FilePath)
		}
	}
	if !IsOwnerManualOnly("", "Missy Series 2") {
		t.Error("series Missy Series 2 not held")
	}
}

// bookRowManualOnly reads every field of the row, with the Missy credit rule.
func TestBookRowManualOnly(t *testing.T) {
	cases := []struct {
		core database.BookCore
		want bool
	}{
		{database.BookCore{FilePath: "/lib/Doctor Who/x"}, true},
		{database.BookCore{FilePath: "/lib/x", Title: "Genesis of the Cybermen"}, true},
		{database.BookCore{FilePath: "/lib/x", Narrator: sp("Stargate SG-1 - Series 2")}, true},
		{database.BookCore{FilePath: "/lib/x", Publisher: sp("Big Finish Productions")}, true},
		{database.BookCore{FilePath: "/lib/x", TranscribedTitle: sp("Doctor Who: The Chimes of Midnight")}, true},
		{database.BookCore{FilePath: "/lib/x", Narrator: sp("Missy Cambridge")}, false},
		{database.BookCore{FilePath: "/lib/Adrian Tchaikovsky/War Master's Gate", Title: "War Master's Gate"}, false},
	}
	for _, c := range cases {
		if got := bookRowManualOnly(&c.core, "") != ""; got != c.want {
			t.Errorf("%+v: got %v, want %v", c.core, got, c.want)
		}
	}
	if bookRowManualOnly(&database.BookCore{FilePath: "/lib/x"}, "Torchwood") == "" {
		t.Error("series not read")
	}
}

// BookManualOnly is the row check plus every store read: a book whose row is
// clean is held by a file, a credit or a tag alone, and a read failure is an
// error, never "not held".
func TestBookManualOnly_RowAndStore(t *testing.T) {
	clean := func() *database.Book {
		return &database.Book{ID: "b", Title: "Spare Parts", FilePath: "/lib/Unknown Author/Spare Parts"}
	}
	if held, d, err := BookManualOnly(moReaders(moFake{}), clean()); held || err != nil {
		t.Fatalf("clean book: held=%v detail=%q err=%v", held, d, err)
	}
	b := clean()
	b.FilePath = "/lib/Doctor Who/Spare Parts"
	if held, d, err := BookManualOnly(moReaders(moFake{}), b); !held || err != nil || !strings.Contains(d, "path") {
		t.Errorf("row path: held=%v detail=%q err=%v", held, d, err)
	}
	f := moFake{files: []database.BookFile{{FilePath: "/lib/Big Finish/Spare Parts/01.mp3"}}}
	if held, d, err := BookManualOnly(moReaders(f), clean()); !held || err != nil || !strings.Contains(d, "file") {
		t.Errorf("file path: held=%v detail=%q err=%v", held, d, err)
	}
	id := 9
	b = clean()
	b.AuthorID = &id
	f = moFake{authors: map[int]*database.Author{9: {ID: 9, Name: "Big Finish Productions"}}}
	if held, d, err := BookManualOnly(moReaders(f), b); !held || err != nil || !strings.Contains(d, "author") {
		t.Errorf("author credit: held=%v detail=%q err=%v", held, d, err)
	}
	f = moFake{tags: []database.BookTag{{Tag: "franchise:doctor-who"}}}
	if held, d, err := BookManualOnly(moReaders(f), clean()); !held || err != nil || !strings.Contains(d, "tag") {
		t.Errorf("tag: held=%v detail=%q err=%v", held, d, err)
	}
	f = moFake{tagErr: errors.New("boom")}
	if held, _, err := BookManualOnly(moReaders(f), clean()); held || err == nil {
		t.Errorf("tag read failure: held=%v err=%v, want an error and not held", held, err)
	}
	if _, _, err := BookManualOnly(ManualOnlyReaders{}, clean()); err == nil {
		t.Error("no file reader: want an error")
	}
}

// A nil reader is an error, never a skipped leg -- whichever of the four it
// is, and even when the row alone would hold the book (a caller wired
// without a reader must fail on every book, not only on the clean ones).
func TestBookManualOnly_NilReaderIsAnError(t *testing.T) {
	full := moReaders(moFake{})
	cases := map[string]func(*ManualOnlyReaders){
		"book_file": func(r *ManualOnlyReaders) { r.Files = nil },
		"series":    func(r *ManualOnlyReaders) { r.Series = nil },
		"author":    func(r *ManualOnlyReaders) { r.Authors = nil },
		"tag":       func(r *ManualOnlyReaders) { r.Tags = nil },
	}
	for leg, drop := range cases {
		r := full
		drop(&r)
		for _, path := range []string{"/lib/Unknown Author/Spare Parts", "/lib/Doctor Who/Spare Parts"} {
			b := &database.Book{ID: "b", Title: "Spare Parts", FilePath: path}
			held, _, err := BookManualOnly(r, b)
			if err == nil || held || !strings.Contains(err.Error(), leg) {
				t.Errorf("nil %s reader, path %q: held=%v err=%v, want an error naming %q", leg, path, held, err, leg)
			}
		}
	}
	if held, _, err := BookManualOnly(full, &database.Book{ID: "b", Title: "Spare Parts", FilePath: "/lib/x"}); held || err != nil {
		t.Errorf("all four readers: held=%v err=%v, want not held and no error", held, err)
	}
}
