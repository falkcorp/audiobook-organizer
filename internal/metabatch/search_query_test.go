// file: internal/metabatch/search_query_test.go
// version: 1.6.0
// guid: f94991be-ebe4-4d6d-8f4e-922b68a3dda0
// last-edited: 2026-09-29

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

// fakeBookFiles has no author links; a test that needs them sets Book.Author
// (the snapshot) or uses fakeBookFilesAuthors.
func (f fakeBookFiles) GetBookAuthors(string) ([]database.BookAuthor, error) { return nil, nil }
func (f fakeBookFiles) GetAuthorByID(int) (*database.Author, error)          { return nil, nil }

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
		// A heading in words or a front-matter name is a real title unless
		// the book's files say it names a part.
		{name: "heading title on a single-file book is searched as-is", book: database.Book{Title: "Act One", TranscribedTitle: strp("Other")},
			files:     fakeBookFiles{files: []database.BookFile{{FilePath: "/library/Nina Kiriki/Act One/act one.m4b"}}},
			wantTitle: "Act One", wantSrc: SearchQuerySourceTitle},
		{name: "front-matter title on a single-file book is searched as-is", book: database.Book{Title: "Epilogue"},
			wantTitle: "Epilogue", wantSrc: SearchQuerySourceTitle},
		{name: "heading title under a folder naming another work uses the transcription", book: database.Book{Title: "Book Two", TranscribedTitle: strp("Eldest")},
			files: fakeBookFiles{files: []database.BookFile{
				{TrackNumber: 1, FilePath: "/library/Paolini/Inheritance/01.mp3"}, {TrackNumber: 2, FilePath: "/library/Paolini/Inheritance/02.mp3"}}},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceTranscribedTitle},
		// The number of files is not evidence: a real book in chapters, in a
		// folder of its own name, is searched by its title.
		{name: "multi-file Act One in its own folder is searched as-is", book: database.Book{Title: "Act One", TranscribedTitle: strp("Other")},
			files: fakeBookFiles{files: []database.BookFile{
				{TrackNumber: 1, FilePath: "/library/Nina Kiriki/Act One/01.mp3"}, {TrackNumber: 2, FilePath: "/library/Nina Kiriki/Act One/02.mp3"}}},
			wantTitle: "Act One", wantSrc: SearchQuerySourceTitle},
		{name: "multi-file Forward in its own folder is searched as-is", book: database.Book{Title: "Forward", FilePath: "/library/Blake Crouch/Forward"},
			files: fakeBookFiles{files: []database.BookFile{
				{TrackNumber: 1, FilePath: "/library/Blake Crouch/Forward/01.mp3"}, {TrackNumber: 2, FilePath: "/library/Blake Crouch/Forward/02.mp3"}}},
			wantTitle: "Forward", wantSrc: SearchQuerySourceTitle},
		{name: "multi-file heading with no folder evidence is searched as-is", book: database.Book{Title: "Act One"},
			files:     fakeBookFiles{files: []database.BookFile{{TrackNumber: 1}, {TrackNumber: 2}}},
			wantTitle: "Act One", wantSrc: SearchQuerySourceTitle},
		// A file's own tag equal to the title corroborates it; the folder here
		// is a placeholder and says nothing, so the tag alone decides.
		{name: "multi-file Chapter One tagged on its files is refused", book: database.Book{Title: "Chapter One"},
			files: fakeBookFiles{files: []database.BookFile{
				{TrackNumber: 1, Title: "Chapter One", FilePath: "/library/Unknown Author/Unknown Title/01.mp3"},
				{TrackNumber: 2, Title: "Book Two", FilePath: "/library/Unknown Author/Unknown Title/02.mp3"}}}},
		{name: "multi-file Book Two tagged on its files is refused", book: database.Book{Title: "Book Two"},
			files: fakeBookFiles{files: []database.BookFile{
				{TrackNumber: 1, Title: "Chapter One", FilePath: "/library/Unknown Author/Unknown Title/01.mp3"},
				{TrackNumber: 2, Title: "Book Two", FilePath: "/library/Unknown Author/Unknown Title/02.mp3"}}}},
		{name: "front-matter title that is a numbered file's own tag uses the folder", book: database.Book{Title: "Prologue"},
			files:     fakeBookFiles{files: []database.BookFile{{TrackNumber: 1, Title: "Prologue", FilePath: "/library/Paolini/Eldest/01.mp3"}}},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
		{name: "front-matter transcription on a single-file book is a usable stand-in", book: database.Book{Title: "", TranscribedTitle: strp("Interlude")},
			wantTitle: "Interlude", wantSrc: SearchQuerySourceTranscribedTitle},
		{name: "front-matter transcription on a multi-file book is refused", book: database.Book{Title: "", TranscribedTitle: strp("Introduction"), FilePath: "/library/Paolini/Eldest"},
			files:     fakeBookFiles{files: []database.BookFile{{FilePath: "/library/Paolini/Eldest/01.mp3"}, {FilePath: "/library/Paolini/Eldest/02.mp3"}}},
			wantTitle: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
		{name: "heading folder on a single-file book is a usable stand-in", book: database.Book{Title: "", FilePath: "/library/Ron Carlson/Dedication/book.m4b"},
			wantTitle: "Dedication", wantSrc: SearchQuerySourceFolderTitle},
		{name: "files error: a heading title is searched as-is", book: database.Book{Title: "Book X"},
			files:     fakeBookFiles{err: errors.New("boom")},
			wantTitle: "Book X", wantSrc: SearchQuerySourceTitle},
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

// fakeBookFilesAuthors is fakeBookFiles with live author links.
type fakeBookFilesAuthors struct {
	fakeBookFiles
	links   []database.BookAuthor
	authors map[int]string
	err     error
}

func (f fakeBookFilesAuthors) GetBookAuthors(string) ([]database.BookAuthor, error) {
	return f.links, f.err
}
func (f fakeBookFilesAuthors) GetAuthorByID(id int) (*database.Author, error) {
	if n, ok := f.authors[id]; ok {
		return &database.Author{ID: id, Name: n}, nil
	}
	return nil, nil
}

// A heading title filed directly under its author's folder is a book called
// that, not a part of a work: the author folder is no corroboration. On a
// root WorkFolderTitle does not treat as generic, the folder it returns for
// ".../Anne Roiphe/Epilogue.m4b" is the author's, and the book used to be
// searched by "Anne Roiphe".
func TestResolveCandidateSearchQuery_AuthorFolderIsNoHeadingEvidence(t *testing.T) {
	const path = "/mnt/stuff/Anne Roiphe/Epilogue.m4b"
	files := fakeBookFiles{files: []database.BookFile{{FilePath: path}}}
	aid := 7
	cases := []struct {
		name    string
		book    database.Book
		reader  SearchQueryReader
		want    string
		wantSrc string
	}{
		{name: "live primary author names the folder",
			book:   database.Book{ID: "b1", Title: "Epilogue", FilePath: path, AuthorID: &aid},
			reader: fakeBookFilesAuthors{fakeBookFiles: files, authors: map[int]string{7: "Anne Roiphe"}},
			want:   "Epilogue", wantSrc: SearchQuerySourceTitle},
		{name: "joined co-author names the folder",
			book: database.Book{ID: "b1", Title: "Epilogue", FilePath: path},
			reader: fakeBookFilesAuthors{fakeBookFiles: files,
				links: []database.BookAuthor{{BookID: "b1", AuthorID: 9}}, authors: map[int]string{9: "anne  roiphe"}},
			want: "Epilogue", wantSrc: SearchQuerySourceTitle},
		{name: "snapshot author names the folder",
			book:   database.Book{ID: "b1", Title: "Epilogue", FilePath: path, Author: &database.Author{Name: "Anne Roiphe"}},
			reader: files, want: "Epilogue", wantSrc: SearchQuerySourceTitle},
		{name: "author read fault never refuses the title",
			book:   database.Book{ID: "b1", Title: "Epilogue", FilePath: path, AuthorID: &aid},
			reader: fakeBookFilesAuthors{fakeBookFiles: files, err: errors.New("boom")},
			want:   "Epilogue", wantSrc: SearchQuerySourceTitle},
		{name: "inverted author name matches the folder",
			book:   database.Book{ID: "b1", Title: "Epilogue", FilePath: path, AuthorID: &aid},
			reader: fakeBookFilesAuthors{fakeBookFiles: files, authors: map[int]string{7: "Roiphe, Anne"}},
			want:   "Epilogue", wantSrc: SearchQuerySourceTitle},
		{name: "decomposed accent in the folder matches the precomposed author",
			book: database.Book{ID: "b1", Title: "Epilogue", FilePath: "/mnt/stuff/Jose\u0301 Saramago/Epilogue.m4b", AuthorID: &aid},
			reader: fakeBookFilesAuthors{fakeBookFiles: fakeBookFiles{files: []database.BookFile{{FilePath: "/mnt/stuff/Jose\u0301 Saramago/Epilogue.m4b"}}},
				authors: map[int]string{7: "Jos\u00e9 Saramago"}},
			want: "Epilogue", wantSrc: SearchQuerySourceTitle},
		// File-tag evidence needs no authors: an author read fault must not
		// discard it. "Book Two" tagged "Book Two" on its file is a part, so
		// the transcription stands in.
		{name: "file-tag evidence survives an author read fault",
			book: database.Book{ID: "b1", Title: "Book Two", TranscribedTitle: strp("Eldest"), FilePath: path, AuthorID: &aid},
			reader: fakeBookFilesAuthors{fakeBookFiles: fakeBookFiles{files: []database.BookFile{{Title: "Book Two", FilePath: path}}},
				err: errors.New("boom")},
			want: "Eldest", wantSrc: SearchQuerySourceTranscribedTitle},
		// A folder that is NOT the author still corroborates: Eldest's
		// prologue is searched by the work folder.
		{name: "work folder by another name still corroborates",
			book: database.Book{ID: "b1", Title: "Prologue", FilePath: "/mnt/stuff/Christopher Paolini/Eldest/Prologue.mp3", AuthorID: &aid},
			reader: fakeBookFilesAuthors{fakeBookFiles: fakeBookFiles{files: []database.BookFile{{FilePath: "/mnt/stuff/Christopher Paolini/Eldest/Prologue.mp3"}}},
				authors: map[int]string{7: "Christopher Paolini"}},
			want: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := ResolveCandidateSearchQuery(tc.reader, &tc.book)
			if !q.Usable || q.Title != tc.want || q.Source != tc.wantSrc {
				t.Fatalf("got %+v, want %q from %s", q, tc.want, tc.wantSrc)
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
