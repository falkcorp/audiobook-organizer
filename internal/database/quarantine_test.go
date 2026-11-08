// file: internal/database/quarantine_test.go
// version: 1.1.2
// guid: 17d03189-baf4-4f26-b01a-327526983a01
// last-edited: 2026-11-08

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBookQuarantineFields(t *testing.T) {
	reason := "taglib cannot parse file"
	b := Book{
		ID:               "test-id",
		Title:            "Test Book",
		FilePath:         "/library/.failed/Author/Book/book.m4b",
		QuarantineReason: &reason,
	}
	require.NotNil(t, b.QuarantineReason)
	require.Equal(t, "taglib cannot parse file", *b.QuarantineReason)
	require.Nil(t, b.QuarantinedAt)
}

func TestCreateBook_RecordsImportPathHistory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{
		Title:    "Dune",
		FilePath: "/imports/audible/Dune.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)

	history, err := store.GetBookPathHistory(book.ID)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "import", history[0].ChangeType)
	require.Equal(t, "", history[0].OldPath)
	require.Equal(t, "/imports/audible/Dune.m4b", history[0].NewPath)
}
