// file: internal/reconcile/merge_dupe_stored_row_test.go
// version: 1.0.0
// guid: 2f7d4c8a-6e1b-4a93-b5d0-9c3e8f2a1d67
// last-edited: 2026-09-15

package reconcile

import (
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A1#15 (#3435 review): "fill where empty" must be decided on the STORED row.
// The hydrated copy of the winner has no narrator; by the time the merge
// writes, another writer has filled it. The dupe's narrator must not win.
func TestMergeDupeIntoStoredRow_FillsOnlyWhatIsEmptyOnTheStoredRow(t *testing.T) {
	var mu sync.Mutex
	concurrent := "Concurrent Narrator"
	stored := &database.Book{ID: "P", Title: "Winner", Narrator: &concurrent}
	m := &database.MockStore{}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		mu.Lock()
		defer mu.Unlock()
		if id != stored.ID {
			return nil, nil
		}
		cp := *stored
		return &cp, nil
	}
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *b
		stored = &cp
		return b, nil
	}
	m.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) { return nil, nil }

	cached := &database.Book{ID: "P", Title: "Winner"} // hydrated before the concurrent write
	dupeNarrator, dupePublisher := "Dupe Narrator", "Dupe Press"
	dupe := &database.Book{ID: "D", Title: "Winner", Narrator: &dupeNarrator, Publisher: &dupePublisher}

	written, merged, err := mergeDupeIntoStoredRow(m, cached, dupe, "test")
	require.NoError(t, err)
	require.NotNil(t, written)
	require.NotNil(t, written.Narrator)
	assert.Equal(t, "Concurrent Narrator", *written.Narrator, "the narrator filled concurrently must survive the merge")
	require.NotNil(t, written.Publisher)
	assert.Equal(t, "Dupe Press", *written.Publisher, "a column empty on the stored row is filled from the dupe")
	assert.NotContains(t, merged, database.FieldKeyNarrator)
	assert.Contains(t, merged, "publisher")
}
