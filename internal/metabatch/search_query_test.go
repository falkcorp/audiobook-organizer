// file: internal/metabatch/search_query_test.go
// version: 1.2.0
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
		name       string
		book       database.Book
		files      fakeBookFiles
		wantTitle  string
		wantSrc    string // "" = not usable
		wantAuthor string
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
		{name: "book transcription carries its heard author", book: database.Book{Title: "", TranscribedTitle: strp("Planet Hulk"), TranscribedAuthor: strp("Greg Pak")},
			wantTitle: "Planet Hulk", wantSrc: SearchQuerySourceTranscribedTitle, wantAuthor: "Greg Pak"},
		{name: "file transcription comes from the lowest disc and track, with its author", book: database.Book{Title: "03"},
			files: fakeBookFiles{files: []database.BookFile{
				{DiscNumber: 2, TrackNumber: 1, TranscribedTitle: strp("Mid Book"), TranscribedAuthor: strp("Wrong")},
				{DiscNumber: 1, TrackNumber: 2, TranscribedTitle: strp("Also Mid Book")},
				{DiscNumber: 1, TrackNumber: 1, TranscribedTitle: strp("Eldest"), TranscribedAuthor: strp("Christopher Paolini")},
			}},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFileTranscribedText, wantAuthor: "Christopher Paolini"},
		{name: "no fall-through to a later file's transcription", book: database.Book{Title: "03", FilePath: "/library/Paolini/Eldest/03.mp3"},
			files: fakeBookFiles{files: []database.BookFile{
				{TrackNumber: 2, FilePath: "/library/Paolini/Eldest/02.mp3", TranscribedTitle: strp("Mid Book")},
				{TrackNumber: 1, FilePath: "/library/Paolini/Eldest/01.mp3"},
			}},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
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
			if !q.Usable || q.Title != tc.wantTitle || q.Source != tc.wantSrc || q.Author != tc.wantAuthor {
				t.Fatalf("got %+v, want %q by %q from %s", q, tc.wantTitle, tc.wantAuthor, tc.wantSrc)
			}
		})
	}
}

func TestIsTranscribedSource(t *testing.T) {
	for src, want := range map[string]bool{
		SearchQuerySourceTranscribedTitle:    true,
		SearchQuerySourceFileTranscribedText: true,
		SearchQuerySourceTitle:               false,
		SearchQuerySourceFolderTitle:         false,
		"":                                   false,
	} {
		if got := IsTranscribedSource(src); got != want {
			t.Errorf("IsTranscribedSource(%q) = %v, want %v", src, got, want)
		}
	}
}
