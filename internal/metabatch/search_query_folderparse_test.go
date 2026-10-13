// file: internal/metabatch/search_query_folderparse_test.go
// version: 1.0.0
// guid: 2b7e9d40-6a15-4c83-8f2e-d0a41c9b7e56
// last-edited: 2026-10-12

package metabatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The folder-parse variant (spec 02 section 3.3 Q; census bucket "author
// equals title"): a title that is a person's name or the book's own live
// author is still searchable, but the work folder holding the files names the
// book more often than not, so the folder is the question. Made-up names.

// liveAuthorFiles serves one live author link, "Mara Quill", for any book.
func liveAuthorFiles(files []database.BookFile, raw map[string][]byte) fakeBookFilesAuthors {
	return fakeBookFilesAuthors{
		fakeBookFiles: fakeBookFiles{files: files, raw: raw},
		links:         []database.BookAuthor{{AuthorID: 1, Position: 0}},
		authors:       map[int]string{1: "Mara Quill"},
	}
}

// Title equals the live author and the authority lists do not know the name
// (so the existing known-person rule does not fire): the folder
// "Author/Real Title/file" becomes the question, and the source says so.
func TestFolderParseVariant_TitleEqualsAuthorFolderIsTheQuestion(t *testing.T) {
	path := "/library/Mara Quill/The Paper Garden/The Paper Garden.m4b"
	files := liveAuthorFiles([]database.BookFile{{FilePath: path, Duration: 9 * 3600}}, nil)
	for _, title := range []string{"Mara Quill", "mara quill", "Quill, Mara", "Mara  Quill."} {
		book := database.Book{ID: "b1", Title: title, FilePath: path, Duration: ip(9 * 3600)}
		q := ResolveCandidateSearchQuery(files, &book)
		require.True(t, q.Usable, title)
		assert.Equal(t, "The Paper Garden", q.Title, title)
		assert.Equal(t, SearchQuerySourceFolderTitle, q.Source, title)
	}

	// A title that merely resembles nothing about the author is untouched.
	book := database.Book{ID: "b1", Title: "The Paper Garden", FilePath: path, Duration: ip(9 * 3600)}
	q := ResolveCandidateSearchQuery(files, &book)
	assert.Equal(t, "The Paper Garden", q.Title)
	assert.Equal(t, SearchQuerySourceTitle, q.Source)
}

// A known person's name that is NOT one of the book's people (a biography
// titled by its subject, or a misfiled credit of someone else) is searched
// by the folder when the folder offers a different, real title.
func TestFolderParseVariant_TitleIsADifferentPersonsName(t *testing.T) {
	path := "/library/Gene Holt/The Paper Garden/The Paper Garden.m4b"
	files := fakeBookFiles{files: []database.BookFile{{FilePath: path}}, raw: knownPeople(t, "Ada Penn")}
	book := database.Book{ID: "b1", Title: "Ada Penn", FilePath: path, Author: &database.Author{Name: "Gene Holt"}}
	q := ResolveCandidateSearchQuery(files, &book)
	require.True(t, q.Usable)
	assert.Equal(t, "The Paper Garden", q.Title)
	assert.Equal(t, SearchQuerySourceFolderTitle, q.Source)

	// The subject's own folder is no better a title: the book stays searched
	// by its stored title (the existing biography rule).
	bioPath := "/library/Gene Holt/Ada Penn/Ada Penn.m4b"
	bio := database.Book{ID: "b2", Title: "Ada Penn", FilePath: bioPath, Author: &database.Author{Name: "Gene Holt"}}
	q = ResolveCandidateSearchQuery(fakeBookFiles{files: []database.BookFile{{FilePath: bioPath}}, raw: files.raw}, &bio)
	assert.Equal(t, "Ada Penn", q.Title)
	assert.Equal(t, SearchQuerySourceTitle, q.Source)
}

// A folder that is itself a person gives no stand-in: the file sits directly
// under its author's folder, in "Last, First" form, or under a known
// person's.
func TestFolderParseVariant_FolderThatIsAPersonKeepsTheTitle(t *testing.T) {
	for _, path := range []string{
		"/library/Mara Quill/Mara Quill.m4b",        // the live author's own folder
		"/library/Quill, Mara/Quill, Mara.m4b",      // "Last, First"
		"/library/Ivor Penn/Ivor Penn - 01.m4b",     // a known person (below)
		"/library/Ivor Penn/Mara Quill/ignored.m4b", // work folder is the author again
	} {
		files := liveAuthorFiles([]database.BookFile{{FilePath: path}}, knownPeople(t, "Ivor Penn"))
		book := database.Book{ID: "b1", Title: "Mara Quill", FilePath: path}
		q := ResolveCandidateSearchQuery(files, &book)
		require.True(t, q.Usable, path)
		assert.Equal(t, "Mara Quill", q.Title, path)
		assert.Equal(t, SearchQuerySourceTitle, q.Source, path)
	}

	// The snapshot author alone is not a live author: the existing
	// "Hammer Fall Rising" rule (junk author, real title) is untouched.
	hf := "/library/Hammer Fall Rising/Quiet Shore/Quiet Shore.m4b"
	book := database.Book{ID: "b1", Title: "Hammer Fall Rising", FilePath: hf, Author: &database.Author{Name: "Hammer Fall Rising"}}
	q := ResolveCandidateSearchQuery(fakeBookFiles{files: []database.BookFile{{FilePath: hf}}}, &book)
	assert.Equal(t, "Hammer Fall Rising", q.Title)
	assert.Equal(t, SearchQuerySourceTitle, q.Source)
}

// A part row is refused outright, as before: no stand-in is tried for one
// file of a set filed as separate rows.
func TestFolderParseVariant_PartRowStillRefused(t *testing.T) {
	dir := "/library/Mara Quill/The Paper Garden"
	self := "The Paper Garden - 02.mp3"
	path := dir + "/" + self
	book := database.Book{ID: "self", Title: "The Paper Garden - 02", FilePath: path, Duration: ip(20 * 60)}
	files := liveAuthorFiles([]database.BookFile{{FilePath: path, Duration: 20 * 60, FileSize: 20 * 60 * 8000}}, nil)
	files.dir = siblingRows(dir, self, "The Paper Garden - 01.mp3", "The Paper Garden - 03.mp3")
	q := ResolveCandidateSearchQuery(files, &book)
	assert.False(t, q.Usable, "%+v", q)
	assert.Equal(t, SkipKindSiblingPart, q.SkipKind)

	// And a title equal to the author on such a row is refused the same way.
	self = "Mara Quill - 02.mp3"
	path = dir + "/" + self
	title := database.Book{ID: "self", Title: "Mara Quill - 02", FilePath: path, Duration: ip(20 * 60)}
	files = liveAuthorFiles([]database.BookFile{{FilePath: path, Duration: 20 * 60, FileSize: 20 * 60 * 8000}}, nil)
	files.dir = siblingRows(dir, self, "Mara Quill - 01.mp3", "Mara Quill - 03.mp3")
	q = ResolveCandidateSearchQuery(files, &title)
	assert.False(t, q.Usable, "%+v", q)
	assert.Equal(t, SkipKindSiblingPart, q.SkipKind)
}
