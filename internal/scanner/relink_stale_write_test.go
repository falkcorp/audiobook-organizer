// file: internal/scanner/relink_stale_write_test.go
// version: 1.0.0
// guid: 9c3e7a51-4b8f-4d26-a0e9-6f2d1b5c8a37
// last-edited: 2026-09-14

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// relinkHookStore runs hook once, right after the scanner reads the book its
// embedded organizer ID names -- before the relink writes it.
type relinkHookStore struct {
	*database.PebbleStore
	hookID string
	once   sync.Once
	hook   func()
}

func (s *relinkHookStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if id == s.hookID {
		s.once.Do(s.hook)
	}
	return b, err
}

// The organizer-ID relink changes only FilePath. A field another writer
// commits between the relink's read and its write must survive; the old code
// wrote the whole stale row back.
func TestSaveBookToDatabase_RelinkKeepsFieldsWrittenAfterTheRead(t *testing.T) {
	inner, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)

	existing, err := inner.CreateBook(&database.Book{
		Title: "Moved Book", FilePath: "/old/path/moved.m4b", Format: "m4b", Description: new("old"),
	})
	require.NoError(t, err)

	hs := &relinkHookStore{PebbleStore: inner, hookID: existing.ID}
	hs.hook = func() {
		_, err := inner.ModifyBook(existing.ID, func(b *database.Book) error { b.Description = new("new"); return nil })
		require.NoError(t, err)
	}
	orig := database.GetGlobalStore()
	database.SetGlobalStore(inner)
	SetStore(hs)
	t.Cleanup(func() { database.SetGlobalStore(orig); SetStore(nil) })

	fpath := filepath.Join(t.TempDir(), "moved.m4b")
	require.NoError(t, os.WriteFile(fpath, []byte("data"), 0o644))

	require.NoError(t, saveBookToDatabase(context.Background(), &Book{
		Title: "Moved Book", FilePath: fpath, Format: ".m4b", BookOrganizerID: existing.ID,
	}))

	got, err := inner.GetBookByID(existing.ID)
	require.NoError(t, err)
	require.Equal(t, fpath, got.FilePath, "relink did not move the path")
	require.NotNil(t, got.Description)
	require.Equal(t, "new", *got.Description, "concurrent description edit reverted")
}
