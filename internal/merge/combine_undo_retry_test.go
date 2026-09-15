// file: internal/merge/combine_undo_retry_test.go
// version: 1.1.0
// guid: 9d2f4b61-8a3e-4c07-b5d1-6e9a0c7f2b48
// last-edited: 2026-09-14

package merge

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failNthStore fails the nth call of one store method, then passes every call.
type failNthStore struct {
	database.Store
	method string
	nth    int32
	calls  atomic.Int32
}

func (s *failNthStore) Unwrap() database.Store { return s.Store }

func (s *failNthStore) hit(method string) error {
	if method == s.method && s.calls.Add(1) == s.nth {
		return fmt.Errorf("injected %s failure", method)
	}
	return nil
}

func (s *failNthStore) ReassignExternalID(source, externalID, newBookID string) error {
	if err := s.hit("ReassignExternalID"); err != nil {
		return err
	}
	return s.Store.ReassignExternalID(source, externalID, newBookID)
}

func (s *failNthStore) DeleteBookFile(id string) error {
	if err := s.hit("DeleteBookFile"); err != nil {
		return err
	}
	return s.Store.DeleteBookFile(id)
}

func (s *failNthStore) MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error {
	if err := s.hit("MoveBookFilesToBook"); err != nil {
		return err
	}
	return s.Store.MoveBookFilesToBook(fileIDs, sourceBookID, targetBookID)
}

// TestCombineUndo_RetryAfterMidwayFailure: an undo that fails part way used to
// leave the journal "undo_failed", which the preconditions refused, and the
// half-restored library also failed every "still combined" check. The retry
// must now succeed and land exactly where a clean undo does -- the pre-combine
// state TestCombineUndo_RoundTripRestoresExactly pins for a clean undo.
//
// Undo order: step 1 restores absA/absB, step 2 moves absA's two rows back
// (one MoveBookFilesToBook), deletes absB's created row (DeleteBookFile #1)
// and then the survivor's own created row (DeleteBookFile #2), step 3 moves
// the PID back (ReassignExternalID). Each case stops attempt 1 at a different
// point, so the retry crosses a different mix of done and not-done items.
//
// Every case stops before step 3's progress restore, so on the retry survivor
// progress is still post-combine and the retry reports no warnings. An
// injection INSIDE the progress loop would need that assertion revisited.
func TestCombineUndo_RetryAfterMidwayFailure(t *testing.T) {
	cases := []struct {
		name   string
		method string
		nth    int32
		// midway checks the state attempt 1 left, proving the case really
		// stopped where it claims.
		midway func(t *testing.T, store database.Store, f undoFixture)
	}{
		{
			name: "step2 first file move", method: "MoveBookFilesToBook", nth: 1,
			midway: func(t *testing.T, store database.Store, f undoFixture) {
				files, err := store.GetBookFiles(f.absA)
				require.NoError(t, err)
				assert.Empty(t, files, "absA's rows are still on the survivor")
			},
		},
		{
			name: "step2 survivor created row", method: "DeleteBookFile", nth: 2,
			midway: func(t *testing.T, store database.Store, f undoFixture) {
				files, err := store.GetBookFiles(f.absA)
				require.NoError(t, err)
				assert.Len(t, files, 2, "absA's rows already moved back")
				bFiles, err := store.GetBookFiles(f.absB)
				require.NoError(t, err)
				assert.Empty(t, bFiles)
				sFiles, err := store.GetBookFiles(f.survivor)
				require.NoError(t, err)
				assert.Len(t, sFiles, 1, "only the survivor's own created row is left: absB's created row is already gone")
			},
		},
		{
			name: "step3 external id", method: "ReassignExternalID", nth: 1,
			midway: func(t *testing.T, store database.Store, f undoFixture) {
				owner, err := store.GetBookByExternalID("itunes", f.pid)
				require.NoError(t, err)
				assert.Equal(t, f.survivor, owner, "the PID has not moved back yet")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := setupTestStore(t)
			f := seedUndoFixture(t, store)
			all := []string{f.survivor, f.absA, f.absB}

			preBooks := map[string]bookView{}
			for _, id := range all {
				preBooks[id] = viewBook(t, store, id)
			}
			preFiles := viewFiles(t, store, all...)
			preLocks := lockFields(t, store, f.survivor)
			preAProg, preAStatus, preASegs := progressView(t, store, f.user.ID, f.absA)
			preSProg, preSStatus, preSSegs := progressView(t, store, f.user.ID, f.survivor)

			res, err := NewService(store).CombineBooks(all, f.survivor, &CombineOverride{Title: "Combined", Narrator: "New Narrator", Author: "Jane Undo"})
			require.NoError(t, err)
			require.NotEmpty(t, res.JournalID)

			ms := NewService(&failNthStore{Store: store, method: tc.method, nth: tc.nth})

			_, err = ms.UndoCombine(res.JournalID)
			require.Error(t, err, "the injected failure must surface")
			var refused *CombineUndoRefusedError
			require.NotErrorAs(t, err, &refused, "attempt 1 passed its preconditions and failed while applying")
			j, err := ms.GetCombineJournal(res.JournalID)
			require.NoError(t, err)
			require.Equal(t, CombineJournalUndoFailed, j.Status)
			assert.False(t, viewBook(t, store, f.absA).SoftDel, "step 1 already restored the absorbed rows")
			tc.midway(t, store, f)

			undo, err := ms.UndoCombine(res.JournalID)
			require.NoError(t, err, "a failed undo must be retriable")
			assert.Empty(t, undo.Warnings, "a retry reports what a clean undo reports")

			for _, id := range all {
				assert.Equal(t, preBooks[id], viewBook(t, store, id), "book %s restored exactly", id)
			}
			assert.Equal(t, preFiles, viewFiles(t, store, all...))
			assert.Equal(t, preLocks, lockFields(t, store, f.survivor))
			owner, err := store.GetBookByExternalID("itunes", f.pid)
			require.NoError(t, err)
			assert.Equal(t, f.absA, owner)
			item, err := database.AsSyncIdentityStore(store).ResolveSyncItem(f.aSync)
			require.NoError(t, err)
			assert.Equal(t, f.aSync, item.SyncID, "redirect cleared")
			aProg, aStatus, aSegs := progressView(t, store, f.user.ID, f.absA)
			assert.Equal(t, []any{preAProg, preAStatus, preASegs}, []any{aProg, aStatus, aSegs})
			sProg, sStatus, sSegs := progressView(t, store, f.user.ID, f.survivor)
			assert.Equal(t, []any{preSProg, preSStatus, preSSegs}, []any{sProg, sStatus, sSegs})

			j, err = ms.GetCombineJournal(res.JournalID)
			require.NoError(t, err)
			assert.Equal(t, CombineJournalUndone, j.Status)
			assert.Empty(t, j.LastError)
			_, err = ms.UndoCombine(res.JournalID)
			require.ErrorAs(t, err, &refused, "an undone combine still cannot be undone twice")

			dbtest.AssertStoreInvariants(t, store)
		})
	}
}
