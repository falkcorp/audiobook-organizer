// file: internal/metabatch/search_query_test.go
// version: 1.0.0
// guid: f94991be-ebe4-4d6d-8f4e-922b68a3dda0
// last-edited: 2026-09-28

package metabatch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeBookFiles struct {
	files []database.BookFile
	err   error
}

func (f fakeBookFiles) GetBookFiles(string) ([]database.BookFile, error) { return f.files, f.err }

func strp(s string) *string { return &s }

func TestResolveCandidateSearchQuery_Fallbacks(t *testing.T) {
	cases := []struct {
		name      string
		book      database.Book
		files     fakeBookFiles
		wantTitle string
		wantSrc   string // "" = not usable
	}{
		{name: "real title wins", book: database.Book{Title: "Eldest", TranscribedTitle: strp("Other")},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceTitle},
		{name: "empty title uses book transcription", book: database.Book{Title: "", TranscribedTitle: strp("Marvel's Planet Hulk")},
			wantTitle: "Marvel's Planet Hulk", wantSrc: SearchQuerySourceTranscribedTitle},
		{name: "chapter number uses book transcription", book: database.Book{Title: "Chapter 3", TranscribedTitle: strp("Eldest")},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceTranscribedTitle},
		{name: "bare number uses file transcription", book: database.Book{Title: "03"},
			files:     fakeBookFiles{files: []database.BookFile{{Missing: true, TranscribedTitle: strp("Gone")}, {TranscribedTitle: strp("Eldest")}}},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFileTranscribedText},
		{name: "chapter fragment falls back to folder", book: database.Book{Title: "06 Chapter 6", FilePath: "/library/Paolini/Eldest/06.mp3"},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
		{name: "placeholder title uses present file's folder", book: database.Book{Title: "Unknown Title", FilePath: "/library/Paolini/Eldest"},
			files:     fakeBookFiles{files: []database.BookFile{{FilePath: "/library/Paolini/Eldest/CD1/01.mp3"}}},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
		{name: "directory FilePath names its own folder", book: database.Book{Title: "Unknown Title", FilePath: "/library/Paolini/Eldest"},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
		{name: "transcription that is itself a placeholder is refused", book: database.Book{Title: "", TranscribedTitle: strp("Chapter 1"), FilePath: "/library/Paolini/Eldest/01.mp3"},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
		{name: "organizer placeholder folder is refused", book: database.Book{Title: "Unknown Title", FilePath: "/library/Unknown Author/Unknown Title/book.m4b"}},
		{name: "library-root folder is refused", book: database.Book{Title: "03", FilePath: "/library/03.mp3"}},
		{name: "files error still tries the book path", book: database.Book{Title: "", FilePath: "/library/Paolini/Eldest/98.mp3"},
			files:     fakeBookFiles{err: errors.New("boom")},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
		{name: "nothing usable", book: database.Book{Title: "Unknown Title"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.book.Title
			q := ResolveCandidateSearchQuery(tc.files, &tc.book)
			if tc.book.Title != before {
				t.Fatalf("resolver wrote the book's title: %q -> %q", before, tc.book.Title)
			}
			if tc.wantSrc == "" {
				if q.Usable {
					t.Fatalf("got usable %+v, want a skip", q)
				}
				return
			}
			if !q.Usable || q.Title != tc.wantTitle || q.Source != tc.wantSrc {
				t.Fatalf("got %+v, want %q from %s", q, tc.wantTitle, tc.wantSrc)
			}
		})
	}
}

func TestTranscribedSearchQueries_Order(t *testing.T) {
	book := &database.Book{ID: "b", TranscribedTitle: strp("Book Level")}
	files := fakeBookFiles{files: []database.BookFile{
		{TranscribedTitle: strp("File One")},
		{Missing: true, TranscribedTitle: strp("Missing File")},
		{TranscribedTitle: strp("Book Level")},
		{TranscribedTitle: strp("Chapter 2")},
	}}
	got := TranscribedSearchQueries(files, book)
	want := []CandidateSearchQuery{
		{Title: "Book Level", Source: SearchQuerySourceTranscribedTitle, Usable: true},
		{Title: "File One", Source: SearchQuerySourceFileTranscribedText, Usable: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
