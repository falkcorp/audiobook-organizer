// file: internal/metadata/taglib_via_test.go
// version: 1.0.0
// guid: 97d5ff02-e20c-43ac-9288-7ba40141cbc4
// last-edited: 2026-11-12

package metadata

import (
	"context"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/stretchr/testify/require"
)

// secondStore gives path a book_file row in a store that is NOT the installed
// hash store, so a test can tell which store a write recorded through.
func secondStore(t *testing.T, path string) (*database.PebbleStore, *database.BookFile) {
	t.Helper()
	other, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	book, err := other.CreateBook(&database.Book{Title: "Gotham Nights", FilePath: path})
	require.NoError(t, err)
	before, err := filehash.BookFileHash(path)
	require.NoError(t, err)
	require.NoError(t, other.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: path, FileHash: before}))
	row, err := other.GetBookFileByPath(path)
	require.NoError(t, err)
	require.NotNil(t, row)
	return other, row
}

// WriteSingleTagContextVia records the new hashes through the store it is
// given and leaves the installed package store alone (F5-01: a maintenance job
// reaches its store only through what its shell hands it).
func TestWriteSingleTagContextVia_RecordsThroughTheGivenStore(t *testing.T) {
	setVerifyAfterWrite(t, true)
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path, installed, installedRow := hashedFixture(t, name)
			given, givenRow := secondStore(t, path)

			require.NoError(t, WriteSingleTagContextVia(context.Background(), given, path, "COMPOSER", "Harvey Dent"))

			after, err := filehash.BookFileHash(path)
			require.NoError(t, err)
			require.NotEqual(t, givenRow.FileHash, after, "fixture error: the write did not change the file's bytes")
			got, err := given.GetBookFileByPath(path)
			require.NoError(t, err)
			require.Equal(t, after, got.FileHash, "the given store's row did not learn the new hash")
			stale, err := installed.GetBookFileByPath(path)
			require.NoError(t, err)
			require.Equal(t, installedRow.FileHash, stale.FileHash, "the installed store was written although a store was given")
		})
	}
}

// A nil recorder records nothing, not even through the installed store.
func TestWriteSingleTagContextVia_NilRecorderRecordsNothing(t *testing.T) {
	setVerifyAfterWrite(t, true)
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path, installed, installedRow := hashedFixture(t, name)
			require.NoError(t, WriteSingleTagContextVia(context.Background(), nil, path, "COMPOSER", "Harvey Dent"))
			after, err := filehash.BookFileHash(path)
			require.NoError(t, err)
			require.NotEqual(t, installedRow.FileHash, after, "fixture error: the write did not change the file's bytes")
			got, err := installed.GetBookFileByPath(path)
			require.NoError(t, err)
			require.Equal(t, installedRow.FileHash, got.FileHash, "a nil recorder fell back to the installed store")
		})
	}
}

// WriteSingleTagContext is WriteSingleTagContextVia over the installed store.
func TestWriteSingleTagContext_StillRecordsThroughTheInstalledStore(t *testing.T) {
	setVerifyAfterWrite(t, true)
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			path, installed, row := hashedFixture(t, name)
			require.NoError(t, WriteSingleTagContext(context.Background(), path, "COMPOSER", "Harvey Dent"))
			after, err := filehash.BookFileHash(path)
			require.NoError(t, err)
			require.NotEqual(t, row.FileHash, after)
			got, err := installed.GetBookFileByPath(path)
			require.NoError(t, err)
			require.Equal(t, after, got.FileHash)
		})
	}
}

// A failed read-back reaches the provenance recorder of the store given, not
// the installed one.
func TestWriteSingleTagContextVia_FailedVerificationRecordsOnTheGivenStore(t *testing.T) {
	setVerifyAfterWrite(t, true)
	path, installed, installedRow := hashedFixture(t, "test_sample.m4b")
	given, givenRow := secondStore(t, path)

	err := WriteSingleTagContextVia(context.Background(), given, path, "TRACKNUMBER", "side A")
	require.ErrorIs(t, err, tagger.ErrVerifyAfterWrite)

	observed := func(s *database.PebbleStore, id string) int {
		history, herr := s.GetFileHistory(id)
		require.NoError(t, herr)
		n := 0
		for _, e := range history {
			if e.Kind == database.FileEventObserved {
				n++
			}
		}
		return n
	}
	require.Equal(t, 1, observed(given, givenRow.ID), "the given store's row has no provenance event")
	require.Zero(t, observed(installed, installedRow.ID), "the installed store got the event")
}
