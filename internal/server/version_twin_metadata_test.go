// file: internal/server/version_twin_metadata_test.go
// version: 1.0.0
// guid: 8b7da4e2-27d6-48df-af29-a925a2f8338e
// last-edited: 2026-10-06

package server

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// BooksWithMetadataSourceHashInMemory reaches PebbleStore's memdb-only lookup
// through the production decorator (indexedStore), answers from memdb once it
// is warm, and fails closed with ErrMemDBNotReady when memdb is off -- never
// the full scan.
func TestServer_BooksWithMetadataSourceHashInMemory_ThroughIndexedStore(t *testing.T) {
	inner, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()
	require.True(t, inner.IsMemReady())

	hash := "h-synthetic-twin"
	created, err := inner.CreateBook(&database.Book{Title: "Synthetic Saga", FilePath: "/lib/Synthetic Saga",
		MetadataSourceHash: &hash})
	require.NoError(t, err)

	s := &Server{store: &indexedStore{Store: inner}}
	books, err := s.BooksWithMetadataSourceHashInMemory(hash)
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, created.ID, books[0].ID)

	inner.UseMemDB = false
	books, err = s.BooksWithMetadataSourceHashInMemory(hash)
	require.True(t, errors.Is(err, database.ErrMemDBNotReady), "memdb off: %v", err)
	require.Nil(t, books)
}

// A store without the in-memory capability fails closed too.
func TestServer_BooksWithMetadataSourceHashInMemory_NoCapability(t *testing.T) {
	s := &Server{store: &database.MockStore{}}
	_, err := s.BooksWithMetadataSourceHashInMemory("h")
	require.True(t, errors.Is(err, database.ErrMemDBNotReady), "%v", err)
}
