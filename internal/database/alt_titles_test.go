// file: internal/database/alt_titles_test.go
// version: 1.2.0
// guid: e1f2a3b4-c5d6-7890-abcd-ef0123456789
// last-edited: 2026-10-18

package database

import (
	"encoding/json"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedAltTitles writes a legacy alt_titles:book:<id> row the way the deleted
// writer did, so reader and DeleteBook tests still have rows to find.
func seedAltTitles(t *testing.T, s *PebbleStore, bookID string, titles ...BookAlternativeTitle) {
	t.Helper()
	data, err := json.Marshal(titles)
	require.NoError(t, err)
	require.NoError(t, s.db.Set([]byte("alt_titles:book:"+bookID), data, pebble.Sync))
}

// TestBookAlternativeTitles_Pebble verifies GetBookAlternativeTitles reads the
// stored row and isolates books from each other. It is read-only: nothing in
// production writes alternative titles since 01-P81d.
func TestBookAlternativeTitles_Pebble(t *testing.T) {
	s := setupTestPebbleStore(t)
	bookID := "01HKEXAMPLE00000000000000"

	alts, err := s.GetBookAlternativeTitles(bookID)
	require.NoError(t, err)
	assert.Empty(t, alts)

	seedAltTitles(t, s, bookID,
		BookAlternativeTitle{BookID: bookID, Title: "Japanese Title", Source: "user", Language: "ja"},
		BookAlternativeTitle{BookID: bookID, Title: "English Translation", Source: "user", Language: "en"},
	)
	alts, err = s.GetBookAlternativeTitles(bookID)
	require.NoError(t, err)
	require.Len(t, alts, 2)
	assert.Equal(t, "Japanese Title", alts[0].Title)
	assert.Equal(t, "en", alts[1].Language)

	other := "01HKOTHER00000000000000000"
	alts, _ = s.GetBookAlternativeTitles(other)
	assert.Empty(t, alts)
}

// setupTestPebbleStore is a minimal PebbleStore factory for tests.
func setupTestPebbleStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory("db")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}
