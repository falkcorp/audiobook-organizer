// file: internal/merge/combine_busy_move_test.go
// version: 1.0.0
// guid: 0f6c8f0e-6a52-4a39-8b7a-5d2f1c9e4b17
// last-edited: 2026-10-26

package merge

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// busyBulkStore reports what the store reports when a bulk move's rows kept
// changing under it for every staging round: ErrBookFileConflict, nothing written.
type busyBulkStore struct{ database.Store }

func (s *busyBulkStore) Unwrap() database.Store { return s.Store }

func (s *busyBulkStore) MoveBookFilesToBookBulk(moves []database.BookFileMove, target string) error {
	return fmt.Errorf("MoveBookFilesToBookBulk: gave up after 10 staging rounds: %w", database.ErrBookFileConflict)
}

// A combine whose bulk move was refused as busy wrote nothing of the absorbed
// books. It must say so as a refusal (not a combine "stopped part-way" asking for
// a revert), leave no Pending journal, keep the absorbed books live with their
// files, and succeed when asked again.
func TestCombineBooks_BusyBulkMoveIsARefusalNotAHalfDoneCombine(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	all := []string{f.survivor, f.absA, f.absB}
	filesBefore := map[string]int{}
	for _, id := range []string{f.absA, f.absB} {
		files, ferr := store.GetBookFiles(id)
		require.NoError(t, ferr)
		filesBefore[id] = len(files)
	}
	require.NotZero(t, filesBefore[f.absA]+filesBefore[f.absB], "the fixture must give the bulk move something to move")

	_, err := NewService(&busyBulkStore{Store: store}).CombineBooks(all, f.survivor, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, database.ErrBookFileConflict), "the cause stays visible so the caller can answer retry-later: %v", err)
	var incomplete *CombineIncompleteError
	assert.False(t, errors.As(err, &incomplete), "nothing was absorbed, so there is nothing to revert: %v", err)
	assert.True(t, IsRetryLater(err), "the HTTP and review layers answer a busy combine as retry-later (503): %v", err)

	ms := NewService(store)
	journals, jerr := ms.ListCombineJournals(0)
	require.NoError(t, jerr)
	for _, j := range journals {
		assert.NotEqual(t, CombineJournalPending, j.Status, "a refused combine must not leave a Pending journal (%s)", j.ID)
		assert.Empty(t, j.Absorbed, "the journal must not list absorbed books that were never touched")
	}
	for _, id := range []string{f.absA, f.absB} {
		b, gerr := store.GetBookByID(id)
		require.NoError(t, gerr)
		require.NotNil(t, b)
		assert.True(t, b.MarkedForDeletion == nil || !*b.MarkedForDeletion, "absorbed book %s was soft-deleted", id)
		files, ferr := store.GetBookFiles(id)
		require.NoError(t, ferr)
		assert.Len(t, files, filesBefore[id], "absorbed book %s's files changed", id)
	}

	_, err = ms.CombineBooks(all, f.survivor, nil)
	require.NoError(t, err, "asked again once the rows settled, the combine succeeds")
}
