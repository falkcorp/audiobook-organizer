// file: internal/metabatch/search_query_test.go
// version: 1.8.0
// guid: f94991be-ebe4-4d6d-8f4e-922b68a3dda0
// last-edited: 2026-09-30

package metabatch

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeBookFiles struct {
	files []database.BookFile
	err   error
	// dir is every live book row's id -> FilePath, answered for any folder
	// (LiveBookPathsUnderDir filters by prefix like the store does).
	dir    map[string]string
	dirErr error
	// dirCalls counts folder listings, shared across copies.
	dirCalls *int
}

func (f fakeBookFiles) GetBookFiles(string) ([]database.BookFile, error) { return f.files, f.err }

func (f fakeBookFiles) LiveBookPathsUnderDir(dir string) (map[string]string, error) {
	if f.dirCalls != nil {
		*f.dirCalls++
	}
	if f.dirErr != nil {
		return nil, f.dirErr
	}
	out := map[string]string{}
	for id, p := range f.dir {
		if strings.HasPrefix(p, dir+"/") {
			out[id] = p
		}
	}
	return out, nil
}

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

// siblingRows returns a folder listing: the book under test as "self" plus
// one row per name, all directly in dir.
func siblingRows(dir, self string, names ...string) map[string]string {
	rows := map[string]string{"self": dir + "/" + self}
	for i, n := range names {
		rows[fmt.Sprintf("sib%d", i)] = dir + "/" + n
	}
	return rows
}

func ip(n int) *int { return &n }

// A row that is one file of a set the scanner filed as separate book rows
// must be SKIPPED, not searched -- by its own title, its folder or a
// transcription: each names the whole work, and a whole-book candidate on a
// chapter row stamps the book onto the chapter (65 rows on 2026-09-30).
// Every measured title shape is here, in a work folder named for the book
// with every stand-in on offer, so a fallback that undoes the refusal fails.
func TestResolveCandidateSearchQuery_SiblingPartRowsAreSkipped(t *testing.T) {
	const lib = "/library/Authors"
	cases := []struct {
		name  string
		dir   string
		self  string
		title string
		sibs  []string
	}{
		{"N of M", lib + "/Joe Abercrombie/Before They Are Hanged", "Before They Are Hanged 002 of 341.mp3",
			"Before They Are Hanged 002 of 341", []string{"Before They Are Hanged 001 of 341.mp3", "Before They Are Hanged 003 of 341.mp3"}},
		{"N of M again", lib + "/Timothy Zahn/Cobra", "Cobra 100 of 151.mp3", "Cobra 100 of 151", []string{"Cobra 099 of 151.mp3", "Cobra 101 of 151.mp3"}},
		{"cut-off N of", lib + "/Brandon Sanderson/Elantris", "Elantris 084 of.mp3", "Elantris 084 of", []string{"Elantris 083 of.mp3", "Elantris 085 of.mp3"}},
		{"copy suffix (one sibling is enough)", lib + "/Brandon Sanderson/Elantris", "Elantris_copy179.mp3", "Elantris_copy179", []string{"Elantris_copy178.mp3"}},
		{"Part N of M inside a subtitle", lib + "/S M Stirling/The Tears of the Sun", "Part 02 of 63.mp3",
			"The Tears of the Sun A Novel of the Change Part 02 of 63", []string{"Part 01 of 63.mp3", "Part 03 of 63.mp3"}},
		{"trailing number with stem siblings", lib + "/S M Stirling/The Sunrise Lands", "The Sunrise Lands 1.mp3",
			"The Sunrise Lands 1", []string{"The Sunrise Lands 2.mp3", "The Sunrise Lands 3.mp3"}},
		{"trailing number, every sibling carries the SAME token behind a track prefix", lib + "/S M Stirling/The Sunrise Lands", "001 The Sunrise Lands 1.mp3",
			"The Sunrise Lands 1", []string{"002 The Sunrise Lands 1.mp3", "003 The Sunrise Lands 1.mp3", "004 The Sunrise Lands 1.mp3"}},
		{"trailing capital letter", lib + "/Anne Bishop/Sealed to the Flame", "Sealed to the Flame E.mp3",
			"Sealed to the Flame E", []string{"Sealed to the Flame A.mp3", "Sealed to the Flame D.mp3"}},
		{"trailing capital letter again", lib + "/Robert Jordan/A Promise to Lews Therin", "A Promise to Lews Therin C.mp3",
			"A Promise to Lews Therin C", []string{"A Promise to Lews Therin A.mp3", "A Promise to Lews Therin B.mp3"}},
		{"rip-detail folder name on a chapter row", lib + "/Neil Gaiman/2002 - Neil Gaiman - American Gods [64k 20;57;42 577MB]", "05.mp3",
			"2002 - Neil Gaiman - American Gods [64k 20;57;42 577MB]", []string{"04.mp3", "06.mp3"}},
		{"rip-detail folder as the stand-in for a number title", lib + "/Neil Gaiman/2002 - Neil Gaiman - American Gods [64k 20;57;42 577MB]", "05.mp3",
			"05", []string{"04.mp3", "06.mp3"}},
		// Owner, 2026-09-30: a plain-number chapter row beside its chapter
		// siblings is skipped too; it used to borrow "Eldest".
		{"chapter fragment beside chapter siblings", "/library/Paolini/Eldest", "06.mp3", "06 Chapter 6", []string{"05.mp3", "07.mp3"}},
		{"bare number beside one chapter sibling", "/library/Paolini/Eldest", "98.mp3", "98", []string{"97.mp3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.dir + "/" + tc.self
			book := database.Book{ID: "self", Title: tc.title, FilePath: path, TranscribedTitle: strp("Whole Work Title")}
			file := database.BookFile{FilePath: path, Duration: 300, TranscribedTitle: strp("Whole Work Title")}
			files := fakeBookFiles{files: []database.BookFile{file}, dir: siblingRows(tc.dir, tc.self, tc.sibs...)}
			q := ResolveCandidateSearchQuery(files, &book)
			if q.Usable {
				t.Fatalf("got usable %+v, want a skip", q)
			}
			if q.SkipKind != SkipKindSiblingPart {
				t.Fatalf("SkipKind = %q, want %q", q.SkipKind, SkipKindSiblingPart)
			}
		})
	}
}

// The shapes alone are not evidence: a whole book whose title ends in a
// number or letter, or carries "N of M", is searched; twins at one path are
// not siblings; a long file is never a chapter; a lone chapter row keeps its
// stand-ins; rip details on a book are cleaned off.
func TestResolveCandidateSearchQuery_SiblingShapesWithoutSiblingsAreSearched(t *testing.T) {
	const author = "/library/Authors/Various"
	others := []string{"Dune.m4b", "Neuromancer.m4b", "Hyperion.m4b"}
	const long = 11 * 3600
	cases := []struct {
		name    string
		book    database.Book
		files   []database.BookFile
		dir     map[string]string
		dirErr  bool
		roots   []string
		want    string
		wantSrc string
		noList  bool
	}{
		{name: "Plan B beside unrelated books", book: database.Book{ID: "self", Title: "Plan B", FilePath: author + "/Plan B.m4b"},
			dir: siblingRows(author, "Plan B.m4b", others...), want: "Plan B", wantSrc: SearchQuerySourceTitle},
		{name: "Apollo 13 beside unrelated books", book: database.Book{ID: "self", Title: "Apollo 13", FilePath: author + "/Apollo 13.m4b"},
			dir: siblingRows(author, "Apollo 13.m4b", others...), want: "Apollo 13", wantSrc: SearchQuerySourceTitle},
		{name: "Vitamin C beside unrelated books", book: database.Book{ID: "self", Title: "Vitamin C", FilePath: author + "/Vitamin C.m4b"},
			dir: siblingRows(author, "Vitamin C.m4b", others...), want: "Vitamin C", wantSrc: SearchQuerySourceTitle},
		{name: "Metro 2034 beside Metro 2033", book: database.Book{ID: "self", Title: "Metro 2034", FilePath: author + "/Metro 2034.m4b"},
			dir: siblingRows(author, "Metro 2034.m4b", "Metro 2033.m4b"), want: "Metro 2034", wantSrc: SearchQuerySourceTitle},
		{name: "Plan B, sibling listing fails", book: database.Book{ID: "self", Title: "Plan B", FilePath: author + "/Plan B.m4b"},
			dirErr: true, want: "Plan B", wantSrc: SearchQuerySourceTitle},
		{name: "Henry V beside Henry IV, Part 1", book: database.Book{ID: "self", Title: "Henry V", FilePath: "/library/Authors/Shakespeare/Henry V.m4b"},
			dir: siblingRows("/library/Authors/Shakespeare", "Henry V.m4b", "Henry IV, Part 1.m4b", "Henry IV, Part 2.m4b"), want: "Henry V", wantSrc: SearchQuerySourceTitle},
		{name: "Malcolm X beside Malcolm X Speaks", book: database.Book{ID: "self", Title: "Malcolm X", FilePath: "/library/Authors/Malcolm X/Malcolm X.m4b"},
			dir: siblingRows("/library/Authors/Malcolm X", "Malcolm X.m4b", "Malcolm X Speaks.m4b"), want: "Malcolm X", wantSrc: SearchQuerySourceTitle},
		{name: "World War I beside World War II", book: database.Book{ID: "self", Title: "World War I", FilePath: "/library/Authors/History/World War I.m4b"},
			dir: siblingRows("/library/Authors/History", "World War I.m4b", "World War II.m4b"), want: "World War I", wantSrc: SearchQuerySourceTitle},
		// B2: a flat folder of whole series books is too few on count, and
		// a longer one is saved by its durations.
		{name: "Mistborn 1 beside Mistborn 2 (too few)", book: database.Book{ID: "self", Title: "Mistborn 1", FilePath: author + "/Mistborn 1.m4b"},
			dir: siblingRows(author, "Mistborn 1.m4b", "Mistborn 2.m4b"), want: "Mistborn 1", wantSrc: SearchQuerySourceTitle},
		{name: "Mistborn 1 of a long flat set (whole-book duration)", book: database.Book{ID: "self", Title: "Mistborn 1", FilePath: author + "/Mistborn 1.m4b", Duration: ip(long)},
			dir: siblingRows(author, "Mistborn 1.m4b", "Mistborn 2.m4b", "Mistborn 3.m4b"), want: "Mistborn 1", wantSrc: SearchQuerySourceTitle, noList: true},
		// B2: twin rows at one path, and a same-stem file differing only by
		// extension, are not siblings.
		{name: "Fahrenheit 451 twins at one path", book: database.Book{ID: "self", Title: "Fahrenheit 451 [64k 577MB]", FilePath: "/library/Authors/Ray Bradbury/Fahrenheit 451 [64k 577MB]/f.m4b"},
			dir: map[string]string{"self": "/library/Authors/Ray Bradbury/Fahrenheit 451 [64k 577MB]/f.m4b",
				"twin1": "/library/Authors/Ray Bradbury/Fahrenheit 451 [64k 577MB]/f.m4b", "twin2": "/library/Authors/Ray Bradbury/Fahrenheit 451 [64k 577MB]/f.mp3"},
			want: "Fahrenheit 451", wantSrc: SearchQuerySourceTitle},
		// B1: dramatized products split in a few parts are whole books.
		{name: "Golden Son (Part 1 of 2) beside its other part", book: database.Book{ID: "self", Title: "Golden Son (Part 1 of 2)", FilePath: "/library/Authors/Pierce Brown/Golden Son (Part 1 of 2).m4b"},
			dir:  siblingRows("/library/Authors/Pierce Brown", "Golden Son (Part 1 of 2).m4b", "Golden Son (Part 2 of 2).m4b"),
			want: "Golden Son (Part 1 of 2)", wantSrc: SearchQuerySourceTitle},
		{name: "Dark Age (2 of 3) beside its parts, whole-part duration", book: database.Book{ID: "self", Title: "Dark Age (2 of 3)", FilePath: "/library/Authors/Pierce Brown/Dark Age (2 of 3).m4b"},
			files: []database.BookFile{{FilePath: "/library/Authors/Pierce Brown/Dark Age (2 of 3).m4b", Duration: long}},
			dir:   siblingRows("/library/Authors/Pierce Brown", "Dark Age (2 of 3).m4b", "Dark Age (1 of 3).m4b", "Dark Age (3 of 3).m4b"),
			want:  "Dark Age (2 of 3)", wantSrc: SearchQuerySourceTitle, noList: true},
		{name: "Shadow's Edge (1 of 2) [Dramatized Adaptation]", book: database.Book{ID: "self", Title: "Shadow's Edge (1 of 2) [Dramatized Adaptation]", FilePath: "/library/Authors/Brent Weeks/Shadow's Edge (1 of 2).m4b"},
			dir:  siblingRows("/library/Authors/Brent Weeks", "Shadow's Edge (1 of 2).m4b", "Shadow's Edge (2 of 2).m4b"),
			want: "Shadow's Edge (1 of 2) [Dramatized Adaptation]", wantSrc: SearchQuerySourceTitle},
		{name: "Dune (1 of 2) alone", book: database.Book{ID: "self", Title: "Dune (1 of 2)", FilePath: author + "/Dune (1 of 2).m4b"},
			dir: siblingRows(author, "Dune (1 of 2).m4b"), want: "Dune (1 of 2)", wantSrc: SearchQuerySourceTitle},
		{name: "Wheel of Time #3 of 14 beside unrelated books", book: database.Book{ID: "self", Title: "Wheel of Time #3 of 14", FilePath: author + "/Wheel of Time 3.m4b"},
			dir: siblingRows(author, "Wheel of Time 3.m4b", others...), want: "Wheel of Time #3 of 14", wantSrc: SearchQuerySourceTitle},
		{name: "Mistborn Series 1 of 3 with a whole-book duration", book: database.Book{ID: "self", Title: "Mistborn Series 1 of 3", FilePath: author + "/Mistborn Series 1 of 3.m4b", Duration: ip(long)},
			dir: siblingRows(author, "Mistborn Series 1 of 3.m4b", "Mistborn Series 2 of 3.m4b", "Mistborn Series 3 of 3.m4b"), want: "Mistborn Series 1 of 3", wantSrc: SearchQuerySourceTitle, noList: true},
		// W2: a row filed directly under a configured root never lists it.
		{name: "row directly under an import root", book: database.Book{ID: "self", Title: "Cobra 100 of 151", FilePath: "/imports/incoming/Cobra 100 of 151.mp3"},
			dir: siblingRows("/imports/incoming", "Cobra 100 of 151.mp3", "a.mp3", "b.mp3"), roots: []string{"/imports/incoming"},
			want: "Cobra 100 of 151", wantSrc: SearchQuerySourceTitle, noList: true},
		// A lone chapter row keeps its stand-ins (owner, 2026-09-30).
		{name: "lone chapter row uses its folder", book: database.Book{ID: "self", Title: "06 Chapter 6", FilePath: "/library/Paolini/Eldest/06.mp3"},
			dir: siblingRows("/library/Paolini/Eldest", "06.mp3"), want: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
		{name: "counted part on a row holding the whole work",
			book: database.Book{ID: "self", Title: "Cobra 001 of 151", FilePath: "/library/Authors/Timothy Zahn/Cobra"},
			files: []database.BookFile{{TrackNumber: 1, FilePath: "/library/Authors/Timothy Zahn/Cobra/Cobra 001 of 151.mp3"},
				{TrackNumber: 2, FilePath: "/library/Authors/Timothy Zahn/Cobra/Cobra 002 of 151.mp3"}},
			want: "Cobra 001 of 151", wantSrc: SearchQuerySourceTitle, noList: true},
		{name: "rip details on a lone row are cleaned",
			book: database.Book{ID: "self", Title: "2002 - Neil Gaiman - American Gods [64k 20;57;42 577MB]", FilePath: author + "/American Gods.m4b"},
			dir:  siblingRows(author, "American Gods.m4b"),
			want: "2002 - Neil Gaiman - American Gods", wantSrc: SearchQuerySourceTitle},
		{name: "rip details on a book beside its author's other books are cleaned",
			book: database.Book{ID: "self", Title: "American Gods [64k 577MB]", FilePath: "/library/Authors/Neil Gaiman/American Gods [64k 577MB].m4b"},
			dir:  siblingRows("/library/Authors/Neil Gaiman", "American Gods [64k 577MB].m4b", "Coraline.m4b"),
			want: "American Gods", wantSrc: SearchQuerySourceTitle},
		{name: "rip-detail folder of a whole multi-file book is cleaned",
			book: database.Book{ID: "self", Title: "", FilePath: "/library/Authors/Neil Gaiman/American Gods [64k 577MB]"},
			files: []database.BookFile{{TrackNumber: 1, FilePath: "/library/Authors/Neil Gaiman/American Gods [64k 577MB]/01.mp3"},
				{TrackNumber: 2, FilePath: "/library/Authors/Neil Gaiman/American Gods [64k 577MB]/02.mp3"}},
			want: "American Gods", wantSrc: SearchQuerySourceFolderTitle, noList: true},
		{name: "a real title never lists the folder", book: database.Book{ID: "self", Title: "Dune Messiah", FilePath: author + "/Dune Messiah.m4b"},
			dir: siblingRows(author, "Dune Messiah.m4b", others...), want: "Dune Messiah", wantSrc: SearchQuerySourceTitle, noList: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			f := fakeBookFiles{files: tc.files, dir: tc.dir, dirCalls: &calls}
			if tc.dirErr {
				f.dirErr = errors.New("boom")
			}
			q := ResolveCandidateSearchQueryMemo(f, &tc.book, NewFolderMemo(tc.roots...))
			if !q.Usable || q.Title != tc.want || q.Source != tc.wantSrc {
				t.Fatalf("got %+v, want %q from %s", q, tc.want, tc.wantSrc)
			}
			if calls > 1 {
				t.Fatalf("listed the folder %d times, want at most once", calls)
			}
			if tc.noList && calls != 0 {
				t.Fatalf("listed the folder %d times, want none", calls)
			}
		})
	}
}

// One memo lists each folder once across every row of a pass.
func TestFolderMemo_ListsEachFolderOnce(t *testing.T) {
	const dir = "/library/Paolini/Eldest"
	calls := 0
	rows := siblingRows(dir, "01.mp3", "02.mp3", "03.mp3")
	memo := NewFolderMemo()
	for _, name := range []string{"01.mp3", "02.mp3", "03.mp3"} {
		book := database.Book{ID: name, Title: strings.TrimSuffix(name, ".mp3"), FilePath: dir + "/" + name}
		f := fakeBookFiles{dir: rows, dirCalls: &calls}
		if q := ResolveCandidateSearchQueryMemo(f, &book, memo); q.Usable {
			t.Fatalf("%s: got usable %+v, want a skip", name, q)
		}
	}
	if calls != 1 {
		t.Fatalf("listed %d times across the pass, want 1", calls)
	}
}
