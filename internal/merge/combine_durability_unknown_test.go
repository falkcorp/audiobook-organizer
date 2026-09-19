// file: internal/merge/combine_durability_unknown_test.go
// version: 1.0.0
// guid: 2c1e4b42-4a32-43ae-8b1b-221e8b793c22
// last-edited: 2026-09-19

package merge

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// durUnknownBulkStore applies MoveBookFilesToBookBulk for real, then reports
// that its fsync failed: the files HAVE moved.
type durUnknownBulkStore struct{ database.Store }

func (s *durUnknownBulkStore) Unwrap() database.Store { return s.Store }

func (s *durUnknownBulkStore) MoveBookFilesToBookBulk(moves []database.BookFileMove, target string) error {
	if err := s.Store.MoveBookFilesToBookBulk(moves, target); err != nil {
		return err
	}
	return fmt.Errorf("%w: injected fsync failure", database.ErrBookFileDurabilityUnknown)
}

// A combine whose bulk move was applied but not known durable must finish.
// Returning an error there left the absorbed books LIVE with zero files.
func TestCombineBooks_BulkMoveDurabilityUnknownStillCompletes(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	all := []string{f.survivor, f.absA, f.absB}

	res, err := NewService(&durUnknownBulkStore{Store: store}).CombineBooks(all, f.survivor, &CombineOverride{Title: "Combined", Narrator: "New Narrator", Author: "Jane Undo"})
	require.NoError(t, err, "the files moved; the combine must not fail on an fsync it cannot undo")
	require.NotNil(t, res)
	for _, id := range []string{f.absA, f.absB} {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b)
		require.True(t, b.MarkedForDeletion != nil && *b.MarkedForDeletion, "absorbed book %s left live", id)
	}
}
