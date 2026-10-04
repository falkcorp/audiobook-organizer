// file: internal/importer/combined_author_test.go
// version: 1.1.0
// guid: 3f25295a-33d6-4702-bedb-e8807a41713a
// last-edited: 2026-10-04

package importer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// An artist naming two existing authors links each of them, the first as the
// primary, and credits both once the book exists; no combined author row and
// no author created.
func TestImportFile_MultiAuthorArtistIsSplit(t *testing.T) {
	withSupportedExt(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "J.N. Chaney, Jonathan P. Brazee - Mission Creep.m4b")
	body := append([]byte("\x00\x00\x00\x1cftypM4A \x00\x00\x02\x00M4A isomiso2"), bytes.Repeat([]byte("audio-payload"), 64)...)
	require.NoError(t, os.WriteFile(path, body, 0o600))

	var rows []*database.BookFile
	store := importStore(dir, &rows)
	authors := map[string]*database.Author{
		"J. N. Chaney":       {ID: 1, Name: "J. N. Chaney"},
		"Jonathan P. Brazee": {ID: 2, Name: "Jonathan P. Brazee"},
	}
	nextID := 10
	var created []string
	store.GetAuthorByNameFunc = func(name string) (*database.Author, error) { return authors[name], nil }
	store.CreateAuthorFunc = func(name string) (*database.Author, error) {
		created = append(created, name)
		nextID++
		a := &database.Author{ID: nextID, Name: name}
		authors[name] = a
		return a, nil
	}
	var credited []database.BookAuthor
	store.SetBookAuthorsFunc = func(_ string, as []database.BookAuthor) error {
		credited = as
		return nil
	}
	var book *database.Book
	store.CreateBookFunc = func(b *database.Book) (*database.Book, error) {
		out := *b
		out.ID = "01JIMPORTEDBOOK000000000"
		book = &out
		return &out, nil
	}
	is := NewImportService(store)
	_, err := is.ImportFile(&ImportFileRequest{FilePath: path})
	require.NoError(t, err)
	require.NotNil(t, book)
	require.Empty(t, created, "both authors exist: nothing is created")
	require.NotNil(t, book.AuthorID, "the extractor read the artist from the file name")
	require.Equal(t, authors["J. N. Chaney"].ID, *book.AuthorID)
	require.Len(t, credited, 2)
	require.Equal(t, authors["Jonathan P. Brazee"].ID, credited[1].AuthorID)
	require.Equal(t, 1, credited[1].Position)
}

// SF5: the importer never creates an author. One part is no author yet, so
// the credit does not split, and the whole artist string is not created
// either: the book stays authorless, as it did before 2026-10-04 on a plain
// lookup miss.
func TestImportFile_UnknownPartLeavesTheBookAuthorless(t *testing.T) {
	withSupportedExt(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "J.N. Chaney, Jonathan P. Brazee - Mission Creep.m4b")
	body := append([]byte("\x00\x00\x00\x1cftypM4A \x00\x00\x02\x00M4A isomiso2"), bytes.Repeat([]byte("audio-payload"), 64)...)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	var rows []*database.BookFile
	store := importStore(dir, &rows)
	authors := map[string]*database.Author{"J. N. Chaney": {ID: 1, Name: "J. N. Chaney"}}
	store.GetAuthorByNameFunc = func(name string) (*database.Author, error) { return authors[name], nil }
	store.CreateAuthorFunc = func(name string) (*database.Author, error) {
		t.Fatalf("the importer must not create an author (%q)", name)
		return nil, nil
	}
	var book *database.Book
	store.CreateBookFunc = func(b *database.Book) (*database.Book, error) {
		out := *b
		out.ID = "01JIMPORTEDBOOK000000000"
		book = &out
		return &out, nil
	}
	_, err := NewImportService(store).ImportFile(&ImportFileRequest{FilePath: path})
	require.NoError(t, err)
	require.NotNil(t, book)
	require.Nil(t, book.AuthorID)
}
