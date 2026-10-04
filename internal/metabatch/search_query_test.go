// file: internal/metabatch/search_query_test.go
// version: 1.14.1
// guid: f94991be-ebe4-4d6d-8f4e-922b68a3dda0
// last-edited: 2026-10-01

package metabatch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	// callsByDir counts listings per folder, shared across copies.
	callsByDir map[string]int
	// importRoots are this store's import paths; importErr fails the read;
	// importCalls counts reads, shared across copies.
	importRoots []string
	importErr   error
	importCalls *atomic.Int32
}

func (f fakeBookFiles) GetAllImportPaths() ([]database.ImportPath, error) {
	if f.importCalls != nil {
		f.importCalls.Add(1)
	}
	if f.importErr != nil {
		return nil, f.importErr
	}
	out := make([]database.ImportPath, 0, len(f.importRoots))
	for i, r := range f.importRoots {
		out = append(out, database.ImportPath{ID: i + 1, Path: r})
	}
	return out, nil
}

func (f fakeBookFiles) GetBookFiles(string) ([]database.BookFile, error) { return f.files, f.err }

func (f fakeBookFiles) LiveBookPathsUnderDir(dir string) (map[string]string, error) {
	if f.dirCalls != nil {
		*f.dirCalls++
	}
	if f.callsByDir != nil {
		f.callsByDir[dir]++
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

func i64p(n int64) *int64 { return &n }

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
		// Owner, 2026-10-01: an empty or placeholder title on a chapter-number
		// FILE beside its siblings is skipped too, not searched as "Eldest".
		{"empty title on a chapter-number file", "/library/Paolini/Eldest", "98.mp3", "", []string{"97.mp3"}},
		{"placeholder title on a chapter-number file", "/library/Paolini/Eldest", "98.mp3", "Unknown Title", []string{"97.mp3"}},
	}
	// Each at a short chapter length and at the 20-25 min a file-split part
	// commonly runs (E1: the 10-min consolidation threshold must not exempt
	// these). The duration exemption is productMinSec (2 h) and applies only
	// to the counted and trailing-token shapes.
	for _, dur := range []int{300, 20 * 60, 25 * 60} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/%ds", tc.name, dur), func(t *testing.T) {
				path := tc.dir + "/" + tc.self
				book := database.Book{ID: "self", Title: tc.title, FilePath: path, TranscribedTitle: strp("Whole Work Title"), Duration: ip(dur)}
				file := database.BookFile{FilePath: path, Duration: dur, FileSize: int64(dur) * 8000, TranscribedTitle: strp("Whole Work Title")}
				files := fakeBookFiles{files: []database.BookFile{file}, dir: siblingRows(tc.dir, tc.self, tc.sibs...)}
				for _, memo := range []*FolderMemo{nil, NewFolderMemo()} {
					q := ResolveCandidateSearchQueryMemo(files, &book, memo)
					if q.Usable {
						t.Fatalf("got usable %+v, want a skip", q)
					}
					if q.SkipKind != SkipKindSiblingPart {
						t.Fatalf("SkipKind = %q, want %q", q.SkipKind, SkipKindSiblingPart)
					}
				}
			})
		}
	}
}

// With no trustworthy duration, a counted or trailing-token title needs a
// big set (unknownDurationMinSiblings) -- chapter sets run 10-341 rows on
// prod, whole-book sets 2-5. A milliseconds value read as seconds would make
// a 2-minute chapter a 33-hour "product"; it is treated as unknown.
func TestResolveCandidateSearchQuery_UnknownDurationNeedsABigSet(t *testing.T) {
	const dir = "/library/Authors/S M Stirling/The Sunrise Lands"
	five := []string{"002 The Sunrise Lands 1.mp3", "003 The Sunrise Lands 1.mp3", "004 The Sunrise Lands 1.mp3", "005 The Sunrise Lands 1.mp3", "006 The Sunrise Lands 1.mp3"}
	cases := []struct {
		name   string
		sibs   []string
		dur    int
		size   int64
		usable bool
	}{
		{"unknown duration, big set", five, 0, 0, false},
		{"unknown duration, small set", five[:2], 0, 0, true},
		{"milliseconds duration is unknown, big set", five, 120000, 2_000_000, false},
		{"absurd duration with no size is unknown, big set", five, 9_000_000, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := dir + "/001 The Sunrise Lands 1.mp3"
			book := database.Book{ID: "self", Title: "The Sunrise Lands 1", FilePath: path}
			files := fakeBookFiles{files: []database.BookFile{{FilePath: path, Duration: tc.dur, FileSize: tc.size}},
				dir: siblingRows(dir, "001 The Sunrise Lands 1.mp3", tc.sibs...)}
			if q := ResolveCandidateSearchQuery(files, &book); q.Usable != tc.usable {
				t.Fatalf("got %+v, want usable=%v", q, tc.usable)
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
	// A long duration is trusted only beside a size that reads it as seconds
	// (rowDurationSec): 11 h at 64 kb/s.
	const longSize int64 = long * 8000
	cases := []struct {
		name       string
		book       database.Book
		files      []database.BookFile
		dir        map[string]string
		dirErr     bool
		importRoot string
		want       string
		wantSrc    string
		noList     bool
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
		{name: "Mistborn 1 of a long flat set (whole-book duration)", book: database.Book{ID: "self", Title: "Mistborn 1", FilePath: author + "/Mistborn 1.m4b", Duration: ip(long), FileSize: i64p(longSize)},
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
			files: []database.BookFile{{FilePath: "/library/Authors/Pierce Brown/Dark Age (2 of 3).m4b", Duration: long, FileSize: longSize}},
			dir:   siblingRows("/library/Authors/Pierce Brown", "Dark Age (2 of 3).m4b", "Dark Age (1 of 3).m4b", "Dark Age (3 of 3).m4b"),
			want:  "Dark Age (2 of 3)", wantSrc: SearchQuerySourceTitle, noList: true},
		{name: "Shadow's Edge (1 of 2) [Dramatized Adaptation]", book: database.Book{ID: "self", Title: "Shadow's Edge (1 of 2) [Dramatized Adaptation]", FilePath: "/library/Authors/Brent Weeks/Shadow's Edge (1 of 2).m4b"},
			dir:  siblingRows("/library/Authors/Brent Weeks", "Shadow's Edge (1 of 2).m4b", "Shadow's Edge (2 of 2).m4b"),
			want: "Shadow's Edge (1 of 2) [Dramatized Adaptation]", wantSrc: SearchQuerySourceTitle},
		{name: "Dune (1 of 2) alone", book: database.Book{ID: "self", Title: "Dune (1 of 2)", FilePath: author + "/Dune (1 of 2).m4b"},
			dir: siblingRows(author, "Dune (1 of 2).m4b"), want: "Dune (1 of 2)", wantSrc: SearchQuerySourceTitle},
		{name: "Wheel of Time #3 of 14 beside unrelated books", book: database.Book{ID: "self", Title: "Wheel of Time #3 of 14", FilePath: author + "/Wheel of Time 3.m4b"},
			dir: siblingRows(author, "Wheel of Time 3.m4b", others...), want: "Wheel of Time #3 of 14", wantSrc: SearchQuerySourceTitle},
		{name: "Mistborn Series 1 of 3 with a whole-book duration", book: database.Book{ID: "self", Title: "Mistborn Series 1 of 3", FilePath: author + "/Mistborn Series 1 of 3.m4b", Duration: ip(long), FileSize: i64p(longSize)},
			dir: siblingRows(author, "Mistborn Series 1 of 3.m4b", "Mistborn Series 2 of 3.m4b", "Mistborn Series 3 of 3.m4b"), want: "Mistborn Series 1 of 3", wantSrc: SearchQuerySourceTitle, noList: true},
		// W2: a row filed directly under a configured root never lists it.
		{name: "row directly under an import root", book: database.Book{ID: "self", Title: "Cobra 100 of 151", FilePath: "/imports/Zahn Rips/Cobra 100 of 151.mp3"},
			dir: siblingRows("/imports/Zahn Rips", "Cobra 100 of 151.mp3", "a.mp3", "b.mp3"), importRoot: "/imports/Zahn Rips",
			want: "Cobra 100 of 151", wantSrc: SearchQuerySourceTitle, noList: true},
		// W-b: same-stem counted siblings only.
		{name: "Golden Son beside other products' parts", book: database.Book{ID: "self", Title: "Golden Son (Part 1 of 2)", FilePath: "/library/Authors/Pierce Brown/Golden Son (Part 1 of 2).m4b", Duration: ip(1500)},
			dir: siblingRows("/library/Authors/Pierce Brown", "Golden Son (Part 1 of 2).m4b",
				"Red Rising (Part 1 of 2).m4b", "Red Rising (Part 2 of 2).m4b", "Morning Star (Part 1 of 2).m4b"),
			want: "Golden Son (Part 1 of 2)", wantSrc: SearchQuerySourceTitle},
		// W-b: a 3-book set with no durations is too small to be chapters.
		{name: "Mistborn 1 of 3 with no durations", book: database.Book{ID: "self", Title: "Mistborn 1", FilePath: author + "/Mistborn 1.m4b"},
			dir: siblingRows(author, "Mistborn 1.m4b", "Mistborn 2.m4b", "Mistborn 3.m4b"), want: "Mistborn 1", wantSrc: SearchQuerySourceTitle},
		{name: "Dune (1 of 3) with no durations", book: database.Book{ID: "self", Title: "Dune (1 of 3)", FilePath: author + "/Dune (1 of 3).m4b"},
			dir: siblingRows(author, "Dune (1 of 3).m4b", "Dune (2 of 3).m4b", "Dune (3 of 3).m4b"), want: "Dune (1 of 3)", wantSrc: SearchQuerySourceTitle},
		// Owner rule: a lone chapter-number file keeps its stand-ins.
		{name: "lone empty-title chapter file uses its folder", book: database.Book{ID: "self", Title: "", FilePath: "/library/Paolini/Eldest/98.mp3"},
			dir: siblingRows("/library/Paolini/Eldest", "98.mp3"), want: "Eldest", wantSrc: SearchQuerySourceFolderTitle},
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
			if tc.importRoot != "" {
				f.importRoots = []string{tc.importRoot}
			}
			q := ResolveCandidateSearchQueryMemo(f, &tc.book, NewFolderMemo())
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

// The fetch paths pass a FolderMemo; the apply and gate paths
// (batch_apply_one.go) call ResolveCandidateSearchQuery with none. Both must
// reach the same verdict for the same row -- including a row directly under
// an import root, which neither may list.
func TestResolveCandidateSearchQuery_FetchAndApplyAgree(t *testing.T) {
	rows := []struct {
		book database.Book
		dir  map[string]string
	}{
		{database.Book{ID: "self", Title: "Cobra 100 of 151", FilePath: "/imports/Zahn Rips/Cobra 100 of 151.mp3"},
			siblingRows("/imports/Zahn Rips", "Cobra 100 of 151.mp3", "Cobra 099 of 151.mp3", "Cobra 101 of 151.mp3")},
		{database.Book{ID: "self", Title: "Cobra 100 of 151", FilePath: "/library/Authors/Timothy Zahn/Cobra/Cobra 100 of 151.mp3", Duration: ip(1200)},
			siblingRows("/library/Authors/Timothy Zahn/Cobra", "Cobra 100 of 151.mp3", "Cobra 099 of 151.mp3", "Cobra 101 of 151.mp3")},
		{database.Book{ID: "self", Title: "98", FilePath: "/library/Paolini/Eldest/98.mp3"},
			siblingRows("/library/Paolini/Eldest", "98.mp3", "97.mp3")},
		{database.Book{ID: "self", Title: "Plan B", FilePath: "/library/Authors/Various/Plan B.m4b"},
			siblingRows("/library/Authors/Various", "Plan B.m4b", "Dune.m4b")},
		{database.Book{ID: "self", Title: "Great Sky River 18 6", FilePath: wrappedPath("/library/Authors/Gregory Benford", "Great Sky River 18 6.mp3"), Duration: ip(1200)},
			wrappedRows("/library/Authors/Gregory Benford", "Great Sky River 18 6.mp3", "Great Sky River 18 5.mp3", "Great Sky River 18 4.mp3")},
	}
	for _, r := range rows {
		calls := 0
		f := fakeBookFiles{dir: r.dir, dirCalls: &calls, importRoots: []string{"/imports/Zahn Rips"}}
		apply := ResolveCandidateSearchQuery(f, &r.book)
		fetch := ResolveCandidateSearchQueryMemo(f, &r.book, NewFolderMemo())
		if apply != fetch {
			t.Errorf("%s: apply %+v, fetch %+v", r.book.FilePath, apply, fetch)
		}
		if strings.Contains(r.book.FilePath, "/Great Sky River 18 6/") && apply.SkipKind != SkipKindCousinPart {
			t.Errorf("%s: got %+v, want a %q skip on both paths", r.book.FilePath, apply, SkipKindCousinPart)
		}
		if strings.HasPrefix(r.book.FilePath, "/imports/Zahn Rips/") && calls != 0 {
			t.Errorf("%s: an import root was listed %d times", r.book.FilePath, calls)
		}
	}
}

// numbered returns the file names fmt-built from pattern for 1..n, skipping
// skip (the row's own number).
func numbered(pattern string, n, skip int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		if i != skip {
			out = append(out, fmt.Sprintf(pattern, i))
		}
	}
	return out
}

// Regression cases from the third review of #3638 (probes probe3-5):
// a counted row is keyed by its FILE name, not its tag title; a duration
// that may be milliseconds is unknown, never a whole product; a rip-folder
// row is judged by duration first; "Unknown"/"Untitled" are placeholders.
func TestResolveCandidateSearchQuery_PartRowEvidence(t *testing.T) {
	const (
		zahn  = "/library/Authors/Timothy Zahn/Cobra"
		aber  = "/library/Authors/Joe Abercrombie/Before They Are Hanged"
		sun   = "/library/Authors/S M Stirling/The Sunrise Lands"
		rip   = "/library/Authors/J K Rowling/Harry Potter 1-7 [64k]"
		eldst = "/library/Authors/Christopher Paolini/Eldest"
		hp    = "Harry Potter %d.m4b"
		tenH  = 10 * 3600
		ms    = 300000 // 5 min stored in milliseconds
	)
	sunSibs := numbered("%03d The Sunrise Lands 1.mp3", 6, 1)
	cases := []struct {
		name     string
		dir      string
		self     string
		title    string
		sibs     []string
		fileDur  int
		fileSize int64
		bookDur  *int
		bookSize *int64
		skip     bool
	}{
		// E-A: the row's own file name keys its set.
		{"tag title, author-prefixed files", aber, "Joe Abercrombie - Before They Are Hanged 002 of 341.mp3", "Before They Are Hanged 002 of 341",
			numbered("Joe Abercrombie - Before They Are Hanged %03d of 341.mp3", 7, 2), 600, 0, nil, nil, true},
		{"author-prefixed files, bare title", zahn, "Timothy Zahn - Cobra 100 of 151.mp3", "Cobra 100 of 151",
			[]string{"Timothy Zahn - Cobra 099 of 151.mp3", "Timothy Zahn - Cobra 101 of 151.mp3"}, 1200, 0, nil, nil, true},
		{"some siblings author-prefixed", zahn, "Cobra 100 of 151.mp3", "Cobra 100 of 151",
			[]string{"Timothy Zahn - Cobra 099 of 151.mp3", "Cobra 101 of 151.mp3"}, 1200, 0, nil, nil, true},
		{"tag title with (Unabridged)", zahn, "Cobra 100 of 151.mp3", "Cobra (Unabridged) 100 of 151",
			[]string{"Cobra 099 of 151.mp3", "Cobra 101 of 151.mp3"}, 1200, 0, nil, nil, true},
		{"underscore file names", zahn, "Cobra_100_of_151.mp3", "Cobra 100 of 151",
			[]string{"Cobra_099_of_151.mp3", "Cobra_101_of_151.mp3"}, 1200, 0, nil, nil, true},
		{"another set's parts are not siblings", "/library/Authors/Pierce Brown", "Golden Son (Part 1 of 2).m4b", "Golden Son (Part 1 of 2)",
			[]string{"Red Rising (Part 1 of 2).m4b", "Red Rising (Part 2 of 2).m4b"}, 1200, 0, nil, nil, false},
		{"a stem inside a word is not a suffix match", zahn, "Cobra 100 of 151.mp3", "Cobra 100 of 151",
			[]string{"Anacobra 099 of 151.mp3", "Anacobra 101 of 151.mp3"}, 1200, 0, nil, nil, false},

		// E-B: a value that may be milliseconds is unknown (6-row set: a part).
		{"2.5 h with no size is unknown", sun, "001 The Sunrise Lands 1.mp3", "The Sunrise Lands 1", sunSibs, 9000, 0, nil, nil, true},
		{"2.5 h with a size is a product", sun, "001 The Sunrise Lands 1.mp3", "The Sunrise Lands 1", sunSibs, 9000, 9000 * 8000, nil, nil, false},
		{"file rejected as ms, book copy not used", sun, "001 The Sunrise Lands 1.mp3", "The Sunrise Lands 1", sunSibs, ms, 4_800_000, ip(ms), nil, true},
		{"book value judged by the book size", sun, "001 The Sunrise Lands 1.mp3", "The Sunrise Lands 1", sunSibs, 0, 0, ip(ms), i64p(4_800_000), true},
		{"book value with no size is unknown", sun, "001 The Sunrise Lands 1.mp3", "The Sunrise Lands 1", sunSibs, 0, 0, ip(9000), nil, true},
		{"file without size judged by the book size", sun, "001 The Sunrise Lands 1.mp3", "The Sunrise Lands 1", sunSibs, 9000, 0, nil, i64p(9000 * 8000), false},

		// W-2: rip-folder rows are judged by duration first (an untitled
		// row reaches the folder check before any transcription stand-in).
		{"rip box set, 10 h files", rip, "Harry Potter 1.m4b", "", numbered(hp, 7, 1), tenH, tenH * 8000, nil, nil, false},
		{"rip box set, unknown duration, small set", rip, "Harry Potter 1.m4b", "", numbered(hp, 3, 1), 0, 0, nil, nil, false},
		{"rip folder, unknown duration, big set", rip, "Harry Potter 1.m4b", "", numbered(hp, 7, 1), 0, 0, nil, nil, true},
		{"rip folder, short file", rip, "Harry Potter 1.m4b", "", numbered(hp, 2, 1), 1200, 0, nil, nil, true},
		{"rip title is the folder, 10 h file", rip, "Harry Potter 1.m4b", "Harry Potter 1-7 [64k]", numbered(hp, 7, 1), tenH, tenH * 8000, nil, nil, false},
		{"rip title is the folder, short file", rip, "Harry Potter 1.m4b", "Harry Potter 1-7 [64k]", numbered(hp, 2, 1), 1200, 0, nil, nil, true},

		// N-2: bare placeholders on a chapter-number file.
		{"Unknown on Chapter 98", eldst, "Chapter 98.mp3", "Unknown", []string{"Chapter 97.mp3"}, 300, 0, nil, nil, true},
		{"Untitled on 98", eldst, "98.mp3", "Untitled", []string{"97.mp3"}, 300, 0, nil, nil, true},
		{"Unknown alone in its folder", eldst, "98.mp3", "Unknown", nil, 300, 0, nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.dir + "/" + tc.self
			book := database.Book{ID: "self", Title: tc.title, FilePath: path, Duration: tc.bookDur, FileSize: tc.bookSize}
			files := fakeBookFiles{files: []database.BookFile{{FilePath: path, Duration: tc.fileDur, FileSize: tc.fileSize}},
				dir: siblingRows(tc.dir, tc.self, tc.sibs...)}
			q := ResolveCandidateSearchQuery(files, &book)
			if got := q.SkipKind == SkipKindSiblingPart; got != tc.skip {
				t.Fatalf("got %+v, want skipped as a sibling part = %v", q, tc.skip)
			}
		})
	}
}

// Import roots come from the store the resolver is handed, never from
// process state: two stores in one process each see only their own roots.
// Until 2026-10-04 the roots were a package global the server set in
// NewServer; a test's closed store stayed registered and a later resolve
// panicked "pebble: closed" inside it (TestApplyCachedCandidate_GateRefuses).
func TestImportRoots_ComeFromTheStoreHandedIn(t *testing.T) {
	rows := siblingRows("/imports/a", "Cobra 100 of 151.mp3", "Cobra 099 of 151.mp3", "Cobra 101 of 151.mp3")
	book := database.Book{ID: "self", Title: "Cobra 100 of 151", FilePath: "/imports/a/Cobra 100 of 151.mp3"}
	for _, memo := range []*FolderMemo{nil, NewFolderMemo()} {
		rootCalls, otherCalls := 0, 0
		root := fakeBookFiles{dir: rows, dirCalls: &rootCalls, importRoots: []string{"/imports/a"}}
		other := fakeBookFiles{dir: rows, dirCalls: &otherCalls, importRoots: []string{"/imports/b"}}
		if memo != nil {
			// Separate passes: a memo belongs to one pass over one store.
			ResolveCandidateSearchQueryMemo(root, &book, memo)
			ResolveCandidateSearchQueryMemo(other, &book, NewFolderMemo())
		} else {
			ResolveCandidateSearchQuery(root, &book)
			ResolveCandidateSearchQuery(other, &book)
		}
		if rootCalls != 0 {
			t.Errorf("memo=%v: the store whose import root holds the row listed it %d times", memo != nil, rootCalls)
		}
		if otherCalls == 0 {
			t.Errorf("memo=%v: a store without that import root saw the other store's roots", memo != nil)
		}
	}
}

// Concurrent resolves on one memo read the import paths once and never see
// an empty list while the first read is in flight: the candidate op starts
// 16-32 workers at once, and a worker answering "not a root" during the
// first load would list a whole import root.
func TestFolderMemo_ImportRootsLoadOnceUnderConcurrency(t *testing.T) {
	const workers = 32
	var reads atomic.Int32
	var listed atomic.Int32
	rows := siblingRows("/imports/Zahn Rips", "Cobra 100 of 151.mp3", "Cobra 099 of 151.mp3", "Cobra 101 of 151.mp3")
	memo := NewFolderMemo()
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			calls := 0
			f := fakeBookFiles{dir: rows, dirCalls: &calls, importRoots: []string{"/imports/Zahn Rips"}, importCalls: &reads}
			book := database.Book{ID: "self", Title: "Cobra 100 of 151", FilePath: "/imports/Zahn Rips/Cobra 100 of 151.mp3"}
			ResolveCandidateSearchQueryMemo(f, &book, memo)
			listed.Add(int32(calls))
		}()
	}
	wg.Wait()
	if n := reads.Load(); n != 1 {
		t.Errorf("import paths read %d times across %d concurrent resolves, want 1", n, workers)
	}
	if n := listed.Load(); n != 0 {
		t.Errorf("an import root was listed %d times: some worker read an empty root list", n)
	}
}

// A failed import-path read keeps the previous list (none, on a first read)
// and is retried after one TTL window, not by every row; a later success is
// used.
func TestFolderMemo_ImportRootsReadFailureIsRetriedNextWindow(t *testing.T) {
	var reads atomic.Int32
	rows := siblingRows("/imports/Zahn Rips", "Cobra 100 of 151.mp3", "Cobra 099 of 151.mp3")
	book := database.Book{ID: "self", Title: "Cobra 100 of 151", FilePath: "/imports/Zahn Rips/Cobra 100 of 151.mp3"}
	memo := NewFolderMemo()
	calls := 0
	bad := fakeBookFiles{dir: rows, dirCalls: &calls, importErr: errors.New("store down"), importCalls: &reads}
	good := fakeBookFiles{dir: rows, dirCalls: &calls, importRoots: []string{"/imports/Zahn Rips"}, importCalls: &reads}

	ResolveCandidateSearchQueryMemo(bad, &book, memo)
	ResolveCandidateSearchQueryMemo(good, &book, memo)
	if n := reads.Load(); n != 1 {
		t.Fatalf("import paths read %d times within one window, want 1 (a failure must not be retried per row)", n)
	}

	memo.rootsMu.Lock()
	memo.rootsAt = time.Now().Add(-importRootsTTL) // the window has passed
	memo.rootsMu.Unlock()
	calls = 0
	ResolveCandidateSearchQueryMemo(good, &book, memo)
	if n := reads.Load(); n != 2 {
		t.Fatalf("import paths read %d times, want 2 (the next window must retry)", n)
	}
	if calls != 0 {
		t.Fatalf("after a successful re-read the import root was still listed %d times", calls)
	}
}

// wrappedPath is parent/<name less extension>/<name>: a file alone in a
// folder named for it.
func wrappedPath(parent, name string) string {
	return parent + "/" + strings.TrimSuffix(name, filepath.Ext(name)) + "/" + name
}

// wrappedRows returns a listing in the folder-per-file layout: the book under
// test as "self" plus one row per cousin name, each in its own folder named
// like its file, all under parent.
func wrappedRows(parent, self string, cousins ...string) map[string]string {
	rows := map[string]string{"self": wrappedPath(parent, self)}
	for i, n := range cousins {
		rows[fmt.Sprintf("cousin%d", i)] = wrappedPath(parent, n)
	}
	return rows
}

// A short fragment the scanner filed as its own row in a folder named
// exactly like the file ("Gregory Benford/Great Sky River 18 6/Great Sky
// River 18 6.mp3") has no sibling in its folder; its set is the like-named
// folders beside it. Prod searched thousands of these against Audible until
// 2026-10-01. Each is SKIPPED as a cousin part: a trusted duration under
// cousinMaxPartSec and a set signal a shelf of whole books lacks.
func TestResolveCandidateSearchQuery_FolderWrappedPartRowsAreSkipped(t *testing.T) {
	const (
		benford = "/library/Authors/Gregory Benford"
		eldest  = "/library/Authors/Christopher Paolini/Eldest"
	)
	gsr := numbered("Great Sky River 18 %d.mp3", 7, 6)
	cases := []struct {
		name    string
		parent  string
		self    string
		title   string
		cousins []string
		dur     int
	}{
		{"sub-numbered stem, 68 s", benford, "Great Sky River 18 6.mp3", "Great Sky River 18 6", gsr, 68},
		{"sub-numbered stem, two cousins", benford, "Great Sky River 18 6.mp3", "Great Sky River 18 6",
			[]string{"Great Sky River 18 5.mp3", "Great Sky River 18 4.mp3"}, 1200},
		{"counted part of 180, 262 s", benford, "In The Ocean Of Night 123 of 180.mp3", "In The Ocean Of Night 123 of 180",
			[]string{"In The Ocean Of Night 122 of 180.mp3", "In The Ocean Of Night 124 of 180.mp3", "Great Sky River 179 of 200.mp3"}, 262},
		{"counted part of 200", benford, "Great Sky River 179 of 200.mp3", "Great Sky River 179 of 200",
			[]string{"Great Sky River 178 of 200.mp3", "Great Sky River 180 of 200.mp3"}, 1200},
		{"single-token stem with 12 cousins", benford, "Stem 3.mp3", "Stem 3", numbered("Stem %d.mp3", 13, 3), 300},
		{"chapter number beside chapter-number cousins", eldest, "98.mp3", "98", []string{"97.mp3", "96.mp3"}, 300},
		{"empty title on a chapter-number file beside chapter-number cousins", eldest, "98.mp3", "", []string{"97.mp3", "96.mp3"}, 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := wrappedPath(tc.parent, tc.self)
			book := database.Book{ID: "self", Title: tc.title, FilePath: path, TranscribedTitle: strp("Whole Work Title")}
			file := database.BookFile{FilePath: path, Duration: tc.dur, FileSize: int64(tc.dur) * 8000}
			files := fakeBookFiles{files: []database.BookFile{file}, dir: wrappedRows(tc.parent, tc.self, tc.cousins...)}
			for _, memo := range []*FolderMemo{nil, NewFolderMemo()} {
				q := ResolveCandidateSearchQueryMemo(files, &book, memo)
				if q.Usable || q.SkipKind != SkipKindCousinPart {
					t.Fatalf("memo=%v: got %+v, want a %q skip", memo != nil, q, SkipKindCousinPart)
				}
			}
		})
	}
}

// One book per folder, named for the book, is also how whole books are
// shelved, so cousins are evidence only under cousinPart's rules: a trusted
// duration under 30 min (an unknown one, or 30 min-2 h, never counts them),
// a set signal for a single-token or small counted title, same-set cousins
// counted once per folder, a chapter-named FILE for a chapter-number title,
// never for rip details, and never under a root, generic, genre or
// many-works parent or one over the row cap. The first six cases are the
// whole-book series the adversarial review of #3640 found refused at
// 874c56c8a.
func TestResolveCandidateSearchQuery_FolderWrappedWholeBooksAreSearched(t *testing.T) {
	const (
		various = "/library/Authors/Various"
		benford = "/library/Authors/Gregory Benford"
		gaiman  = "/library/Authors/Neil Gaiman"
		tenH    = 10 * 3600
	)
	gsr := numbered("Great Sky River 18 %d.mp3", 7, 6)
	type sz int
	const (
		sized sz = iota // FileSize = dur * 8000 (a trusted duration)
		noSize
	)
	bigParent := wrappedRows(benford, "Great Sky River 18 6.mp3", gsr...)
	for i := range cousinParentMaxRows {
		bigParent[fmt.Sprintf("filler%d", i)] = fmt.Sprintf("%s/Other/%04d.mp3", benford, i)
	}
	cases := []struct {
		name        string
		parent      string
		self        string
		title       string
		dir         map[string]string // nil: wrappedRows(parent, self, cousins...)
		cousins     []string
		dur         int
		size        sz
		importRoot  string
		dirErr      bool
		want        string // "" : only assert it was not refused as a part
		parentLists int    // how often the parent may be listed; with a memo, a
		// case with parentLists 1 MUST list it (the cousin code was reached)
	}{
		// The review's probes.
		{name: "Mistborn 1-7, no duration", parent: various, self: "Mistborn 1.m4b", title: "Mistborn 1",
			cousins: numbered("Mistborn %d.m4b", 7, 1), want: "Mistborn 1"},
		{name: "Mistborn 1-7, 10 h with no size", parent: various, self: "Mistborn 1.m4b", title: "Mistborn 1",
			cousins: numbered("Mistborn %d.m4b", 7, 1), dur: tenH, size: noSize, want: "Mistborn 1"},
		{name: "Magic Tree House 1-3, 1 h", parent: various, self: "Magic Tree House 1.m4b", title: "Magic Tree House 1",
			cousins: numbered("Magic Tree House %d.m4b", 3, 1), dur: 3600, want: "Magic Tree House 1"},
		{name: "Wheel of Time #03 of 14 beside 13, no duration", parent: various, self: "Wheel of Time #03 of 14.m4b", title: "Wheel of Time #03 of 14",
			cousins: numbered("Wheel of Time #%02d of 14.m4b", 14, 3), want: "Wheel of Time #03 of 14"},
		{name: "Wheel of Time 01-14, no duration", parent: various, self: "Wheel of Time 01.m4b", title: "Wheel of Time 01",
			cousins: numbered("Wheel of Time %02d.m4b", 14, 1), want: "Wheel of Time 01"},
		{name: "Discworld 01-41, 10 h with no size", parent: various, self: "Discworld 01.m4b", title: "Discworld 01",
			cousins: numbered("Discworld %02d.m4b", 41, 1), dur: tenH, size: noSize, want: "Discworld 01"},
		{name: "Part 1 beside Part 2, 1 h", parent: various, self: "Part 1.m4b", title: "Part 1",
			cousins: []string{"Part 2.m4b"}, dur: 3600},

		// The 30 min gate: a real fragment set, but a long file.
		{name: "sub-numbered stem at 45 min", parent: benford, self: "Great Sky River 18 6.mp3", title: "Great Sky River 18 6",
			cousins: gsr, dur: 45 * 60, want: "Great Sky River 18 6"},
		{name: "sub-numbered stem at exactly 30 min", parent: benford, self: "Great Sky River 18 6.mp3", title: "Great Sky River 18 6",
			cousins: gsr, dur: cousinMaxPartSec, want: "Great Sky River 18 6"},
		{name: "counted title at 10 h", parent: benford, self: "Great Sky River 179 of 200.mp3", title: "Great Sky River 179 of 200",
			cousins: numbered("Great Sky River %03d of 200.mp3", 6, 0), dur: tenH, want: "Great Sky River 179 of 200"},

		// The set signal.
		{name: "single-token stem with 3 cousins at 5 min", parent: benford, self: "Stem 3.mp3", title: "Stem 3",
			cousins: []string{"Stem 1.mp3", "Stem 2.mp3", "Stem 4.mp3"}, dur: 300, want: "Stem 3", parentLists: 1},
		{name: "Dragon Saga 2 beside Dragon Saga 1 and 3 at 5 min", parent: various, self: "Dragon Saga 2.m4b", title: "Dragon Saga 2",
			cousins: []string{"Dragon Saga 1.m4b", "Dragon Saga 3.m4b"}, dur: 300, want: "Dragon Saga 2", parentLists: 1},
		{name: "counted title of 7 beside its 6 others at 5 min", parent: various, self: "Saga 3 of 7.m4b", title: "Saga 3 of 7",
			cousins: numbered("Saga %d of 7.m4b", 7, 3), dur: 300, want: "Saga 3 of 7", parentLists: 1},
		{name: "counted title beside another set's parts", parent: benford, self: "Great Sky River 179 of 200.mp3", title: "Great Sky River 179 of 200",
			cousins: []string{"In The Ocean Of Night 122 of 180.mp3", "In The Ocean Of Night 124 of 180.mp3"}, dur: 1200,
			want: "Great Sky River 179 of 200", parentLists: 1},
		{name: "twins inside one cousin folder count once", parent: benford, self: "Great Sky River 179 of 200.mp3", title: "Great Sky River 179 of 200",
			dir: map[string]string{
				"self":  wrappedPath(benford, "Great Sky River 179 of 200.mp3"),
				"twin1": wrappedPath(benford, "Great Sky River 178 of 200.mp3"),
				"twin2": wrappedPath(benford, "Great Sky River 178 of 200.mp3"),
				"twin3": wrappedPath(benford, "Great Sky River 178 of 200.m4b"),
			}, dur: 1200, want: "Great Sky River 179 of 200", parentLists: 1},

		// Chapter-number titles need a chapter-named FILE.
		{name: "junk chapter tag on a wrapped whole book beside chapter-named cousins", parent: various, self: "Dune.m4b", title: "01",
			cousins: []string{"02.m4b", "03.m4b"}, dur: 300, want: "Dune"},

		// Rip details never borrow cousins.
		{name: "rip details never borrow cousins", parent: gaiman, self: "American Gods [64k].m4b", title: "American Gods [64k]",
			cousins: []string{"Coraline [64k].m4b", "Stardust [64k].m4b", "Neverwhere [64k].m4b", "Anansi Boys [64k].m4b", "Norse Mythology [64k].m4b"},
			dur:     300, want: "American Gods"},

		// Parents never listed.
		{name: "a parent that is an import root", parent: "/imports/Zahn Rips", self: "Great Sky River 18 6.mp3", title: "Great Sky River 18 6",
			cousins: gsr, dur: 68, importRoot: "/imports/Zahn Rips", want: "Great Sky River 18 6"},
		{name: "a genre-shelf parent", parent: "/library/Science Fiction", self: "Great Sky River 18 6.mp3", title: "Great Sky River 18 6",
			cousins: gsr, dur: 68, want: "Great Sky River 18 6"},
		{name: "an Authors parent", parent: "/library/Authors", self: "Great Sky River 18 6.mp3", title: "Great Sky River 18 6",
			cousins: gsr, dur: 68, want: "Great Sky River 18 6"},
		{name: "an iTunes Media parent", parent: "/library/iTunes Media", self: "Great Sky River 18 6.mp3", title: "Great Sky River 18 6",
			cousins: gsr, dur: 68, want: "Great Sky River 18 6"},
		{name: "own folder unreadable", parent: benford, self: "Great Sky River 18 6.mp3", title: "Great Sky River 18 6",
			cousins: gsr, dur: 68, dirErr: true, want: "Great Sky River 18 6"},
		{name: "a parent over the row cap", parent: benford, self: "Great Sky River 18 6.mp3", title: "Great Sky River 18 6",
			dir: bigParent, dur: 68, want: "Great Sky River 18 6", parentLists: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := wrappedPath(tc.parent, tc.self)
			dir := tc.dir
			if dir == nil {
				dir = wrappedRows(tc.parent, tc.self, tc.cousins...)
			}
			size := int64(tc.dur) * 8000
			if tc.size == noSize {
				size = 0
			}
			calls := map[string]int{}
			f := fakeBookFiles{dir: dir, callsByDir: calls,
				files: []database.BookFile{{FilePath: path, Duration: tc.dur, FileSize: size}}}
			if tc.dirErr {
				f.dirErr = errors.New("boom")
			}
			if tc.importRoot != "" {
				f.importRoots = []string{tc.importRoot}
			}
			book := database.Book{ID: "self", Title: tc.title, FilePath: path}
			for _, memo := range []*FolderMemo{nil, NewFolderMemo()} {
				clear(calls)
				q := ResolveCandidateSearchQueryMemo(f, &book, memo)
				if q.SkipKind != "" {
					t.Fatalf("memo=%v: got %+v, want no part skip", memo != nil, q)
				}
				if tc.want != "" && (!q.Usable || q.Title != tc.want) {
					t.Fatalf("memo=%v: got %+v, want %q searched", memo != nil, q, tc.want)
				}
				if n := calls[tc.parent]; n > tc.parentLists || (memo != nil && n != tc.parentLists) {
					t.Fatalf("memo=%v: parent listed %d times, want %d", memo != nil, n, tc.parentLists)
				}
			}
		})
	}
}

// A cousin parent over cousinParentMaxRows is reported through its own
// rate-limited warning, once per read.
func TestResolveCandidateSearchQuery_CousinParentCapIsReported(t *testing.T) {
	const parent = "/library/Authors/Gregory Benford"
	rows := wrappedRows(parent, "Great Sky River 18 6.mp3", numbered("Great Sky River 18 %d.mp3", 7, 6)...)
	for i := range cousinParentMaxRows {
		rows[fmt.Sprintf("filler%d", i)] = fmt.Sprintf("%s/Other/%04d.mp3", parent, i)
	}
	path := wrappedPath(parent, "Great Sky River 18 6.mp3")
	f := fakeBookFiles{dir: rows, files: []database.BookFile{{FilePath: path, Duration: 68, FileSize: 68 * 8000}}}
	book := database.Book{ID: "self", Title: "Great Sky River 18 6", FilePath: path}
	before, listBefore := cousinCapWarn.failures.Load(), listWarn.failures.Load()
	memo := NewFolderMemo()
	for range 3 {
		if q := ResolveCandidateSearchQueryMemo(f, &book, memo); q.SkipKind != "" {
			t.Fatalf("got %+v, want no part skip", q)
		}
	}
	if got := cousinCapWarn.failures.Load() - before; got != 1 {
		t.Fatalf("cap reported %d times over 3 rows of one pass, want 1", got)
	}
	if got := listWarn.failures.Load() - listBefore; got != 0 {
		t.Fatalf("cap reported as a listing failure %d times, want 0", got)
	}
}

// One memo lists a wrapped row's parent once across every cousin of a pass,
// and keeps that listing apart from the parent's own direct-children entry.
func TestFolderMemo_ListsWrappedParentOnce(t *testing.T) {
	const parent = "/library/Authors/Gregory Benford"
	names := numbered("Great Sky River %03d of 200.mp3", 5, 0)
	rows := wrappedRows(parent, names[0], names[1:]...)
	// A loose row directly in the parent: its direct listing is a separate
	// memo entry from the wrapped one.
	rows["loose"] = parent + "/Timescape.m4b"
	calls := map[string]int{}
	memo := NewFolderMemo()
	for i, name := range names {
		path := wrappedPath(parent, name)
		id := "self"
		if i > 0 {
			id = fmt.Sprintf("cousin%d", i-1)
		}
		book := database.Book{ID: id, Title: strings.TrimSuffix(name, ".mp3"), FilePath: path}
		f := fakeBookFiles{dir: rows, callsByDir: calls, files: []database.BookFile{{FilePath: path, Duration: 1200, FileSize: 1200 * 8000}}}
		if q := ResolveCandidateSearchQueryMemo(f, &book, memo); q.Usable {
			t.Fatalf("%s: got usable %+v, want a skip", name, q)
		}
	}
	if calls[parent] != 1 {
		t.Fatalf("parent listed %d times across %d cousins, want 1", calls[parent], len(names))
	}
	for _, name := range names {
		if d := filepath.Dir(wrappedPath(parent, name)); calls[d] != 1 {
			t.Fatalf("%s listed %d times, want 1", d, calls[d])
		}
	}
	direct, err := memo.list(fakeBookFiles{dir: rows, callsByDir: calls}, parent)
	if err != nil || len(direct) != 1 || direct["loose"] == "" {
		t.Fatalf("direct listing of the parent = %v, %v; want only the loose row", direct, err)
	}
}

// gatedLister is a BookDirLister whose reads block until every caller of the
// test has arrived (arrived), then count themselves, so concurrent misses on
// one folder are all in flight at once.
type gatedLister struct {
	arrived *sync.WaitGroup
	calls   *atomic.Int64
	rows    map[string]string
	err     error
}

func (g gatedLister) LiveBookPathsUnderDir(dir string) (map[string]string, error) {
	g.calls.Add(1)
	g.arrived.Wait()
	// Hold the read a little longer so the callers that signalled arrival
	// reach the memo while this read is still in flight.
	time.Sleep(20 * time.Millisecond)
	if g.err != nil {
		return nil, g.err
	}
	out := map[string]string{}
	for id, p := range g.rows {
		if strings.HasPrefix(p, dir+"/") {
			out[id] = p
		}
	}
	return out, nil
}

// Concurrent callers of one memo for the same folder and kind share ONE
// read -- for the direct listing and the wrapped listing alike -- and a
// failed read is reported once, not once per caller. A failed read is not
// kept: a later caller reads the folder again.
func TestFolderMemo_ConcurrentCallersShareOneRead(t *testing.T) {
	const (
		parent  = "/library/Authors/Gregory Benford"
		callers = 32
	)
	rows := wrappedRows(parent, "Great Sky River 001 of 200.mp3", numbered("Great Sky River %03d of 200.mp3", 6, 1)...)
	rows["loose"] = parent + "/Timescape.m4b"
	kinds := []struct {
		name string
		list func(m *FolderMemo, l database.BookDirLister) (map[string]string, error)
		want int
	}{
		{"direct", func(m *FolderMemo, l database.BookDirLister) (map[string]string, error) { return m.list(l, parent) }, 1},
		{"wrapped", func(m *FolderMemo, l database.BookDirLister) (map[string]string, error) {
			return m.listWrapped(l, parent)
		}, 6},
	}
	for _, k := range kinds {
		for _, failing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failing=%v", k.name, failing), func(t *testing.T) {
				var arrived sync.WaitGroup
				arrived.Add(callers)
				var calls atomic.Int64
				l := gatedLister{arrived: &arrived, calls: &calls, rows: rows}
				if failing {
					l.err = errors.New("boom")
				}
				memo := NewFolderMemo()
				failuresBefore := listWarn.failures.Load()
				var done sync.WaitGroup
				errs := make(chan error, callers)
				sizes := make(chan int, callers)
				for range callers {
					done.Add(1)
					go func() {
						defer done.Done()
						arrived.Done()
						got, err := k.list(memo, l)
						errs <- err
						sizes <- len(got)
					}()
				}
				finished := make(chan struct{})
				go func() { done.Wait(); close(finished) }()
				select {
				case <-finished:
				case <-time.After(10 * time.Second):
					t.Fatal("callers never returned")
				}
				close(errs)
				close(sizes)
				if n := calls.Load(); n != 1 {
					t.Fatalf("lister called %d times by %d concurrent callers, want 1", n, callers)
				}
				for err := range errs {
					if (err != nil) != failing {
						t.Fatalf("caller got err=%v, want failing=%v", err, failing)
					}
				}
				for n := range sizes {
					if !failing && n != k.want {
						t.Fatalf("caller got %d rows, want %d", n, k.want)
					}
				}
				wantFailures := int64(0)
				if failing {
					wantFailures = 1
				}
				if got := listWarn.failures.Load() - failuresBefore; got != wantFailures {
					t.Fatalf("failed read reported %d times, want %d", got, wantFailures)
				}
				// A later caller reuses a stored listing, but reads a failed
				// folder again: a transient fault is not kept for the pass.
				wantCalls := int64(1)
				if failing {
					wantCalls = 2
				}
				if _, err := k.list(memo, l); (err != nil) != failing || calls.Load() != wantCalls {
					t.Fatalf("later caller: err=%v calls=%d, want calls=%d", err, calls.Load(), wantCalls)
				}
			})
		}
	}
}
