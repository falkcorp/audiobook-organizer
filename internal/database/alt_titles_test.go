// file: internal/database/alt_titles_test.go
// version: 1.1.1
// guid: e1f2a3b4-c5d6-7890-abcd-ef0123456789

package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBookAlternativeTitles_Pebble verifies the round-trip behavior of
// the book_alternative_titles table: add, get, remove, set, idempotency.
// Uses the shared newTestActivityStore-style setup but for the main
// SQLite store.
func TestBookAlternativeTitles_Pebble(t *testing.T) {
	s := setupTestPebbleStore(t)
	bookID := "01HKEXAMPLE00000000000000"

	// Empty to start
	alts, err := s.GetBookAlternativeTitles(bookID)
	require.NoError(t, err)
	assert.Empty(t, alts)

	// Add one
	require.NoError(t, s.AddBookAlternativeTitle(bookID, "Foundation and Empire", "user", "en"))
	alts, err = s.GetBookAlternativeTitles(bookID)
	require.NoError(t, err)
	require.Len(t, alts, 1)
	assert.Equal(t, "Foundation and Empire", alts[0].Title)
	assert.Equal(t, "user", alts[0].Source)
	assert.Equal(t, "en", alts[0].Language)

	// Idempotent: re-adding the same title is a no-op.
	require.NoError(t, s.AddBookAlternativeTitle(bookID, "Foundation and Empire", "auto_ampersand", ""))
	alts, _ = s.GetBookAlternativeTitles(bookID)
	assert.Len(t, alts, 1, "re-add should not duplicate")
	assert.Equal(t, "user", alts[0].Source, "original source preserved")

	// Add a second variant
	require.NoError(t, s.AddBookAlternativeTitle(bookID, "Foundation & Empire", "auto_ampersand", ""))
	alts, _ = s.GetBookAlternativeTitles(bookID)
	assert.Len(t, alts, 2)

	// Remove one
	require.NoError(t, s.RemoveBookAlternativeTitle(bookID, "Foundation & Empire"))
	alts, _ = s.GetBookAlternativeTitles(bookID)
	require.Len(t, alts, 1)
	assert.Equal(t, "Foundation and Empire", alts[0].Title)

	// Set replaces everything
	require.NoError(t, s.SetBookAlternativeTitles(bookID, []BookAlternativeTitle{
		{Title: "Japanese Title", Source: "user", Language: "ja"},
		{Title: "English Translation", Source: "user", Language: "en"},
	}))
	alts, _ = s.GetBookAlternativeTitles(bookID)
	assert.Len(t, alts, 2)

	// Empty title rejected
	err = s.AddBookAlternativeTitle(bookID, "", "user", "")
	assert.Error(t, err)

	// Different book — isolation
	other := "01HKOTHER00000000000000000"
	alts, _ = s.GetBookAlternativeTitles(other)
	assert.Empty(t, alts)
}

// setupTestPebbleStore is a minimal PebbleStore factory for tests.
func setupTestPebbleStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}
