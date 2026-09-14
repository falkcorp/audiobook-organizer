// file: internal/scanner/ai_parse_stale_write_test.go
// version: 1.0.0
// guid: 5f0a8e63-1d4b-4c97-a2e8-7b3c9d6f1e04
// last-edited: 2026-09-14

package scanner

import (
	"context"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// aiParseHookStore runs hook once when the AI save reads the field locks,
// which is after it has read the row and before it writes it.
type aiParseHookStore struct {
	*database.PebbleStore
	once sync.Once
	hook func()
}

func (s *aiParseHookStore) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	s.once.Do(s.hook)
	return s.PebbleStore.GetMetadataFieldStates(bookID)
}

// A field another writer commits after the AI save read the row must survive
// the AI save's gap-fill write. The old code wrote the whole stale row back.
func TestSaveAIFields_KeepsFieldsWrittenAfterTheRead(t *testing.T) {
	inner, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)

	row, err := inner.CreateBook(&database.Book{
		Title: "A Book", FilePath: "/library/a/book.m4b", Description: new("old"),
	})
	require.NoError(t, err)

	hs := &aiParseHookStore{PebbleStore: inner}
	hs.hook = func() {
		_, err := inner.ModifyBook(row.ID, func(b *database.Book) error { b.Description = new("new"); return nil })
		require.NoError(t, err)
	}
	orig := database.GetGlobalStore()
	database.SetGlobalStore(inner)
	SetStore(hs)
	t.Cleanup(func() { database.SetGlobalStore(orig); SetStore(nil) })

	_, err = saveAIFieldsToPrimary(context.Background(), row.ID, &Book{FilePath: row.FilePath, Narrator: "Some Narrator"})
	require.NoError(t, err)

	got, err := inner.GetBookByID(row.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Narrator)
	require.Equal(t, "Some Narrator", *got.Narrator)
	require.NotNil(t, got.Description)
	require.Equal(t, "new", *got.Description, "concurrent description edit reverted")
}

// A narrator another writer filled after the read is not overwritten by the
// AI's gap-fill: the gap is re-checked on the fresh row.
func TestSaveAIFields_DoesNotOverwriteGapFilledAfterTheRead(t *testing.T) {
	inner, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)

	row, err := inner.CreateBook(&database.Book{Title: "A Book", FilePath: "/library/b/book.m4b"})
	require.NoError(t, err)

	hs := &aiParseHookStore{PebbleStore: inner}
	hs.hook = func() {
		_, err := inner.ModifyBook(row.ID, func(b *database.Book) error { b.Narrator = new("User Narrator"); return nil })
		require.NoError(t, err)
	}
	orig := database.GetGlobalStore()
	database.SetGlobalStore(inner)
	SetStore(hs)
	t.Cleanup(func() { database.SetGlobalStore(orig); SetStore(nil) })

	_, err = saveAIFieldsToPrimary(context.Background(), row.ID, &Book{FilePath: row.FilePath, Narrator: "AI Narrator"})
	require.NoError(t, err)

	got, err := inner.GetBookByID(row.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Narrator)
	require.Equal(t, "User Narrator", *got.Narrator)
}
