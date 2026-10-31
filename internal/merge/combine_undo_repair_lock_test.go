// file: internal/merge/combine_undo_repair_lock_test.go
// version: 1.0.0
// guid: 69231be7-4f65-4f30-89ff-e01c12a59008
// last-edited: 2026-10-30

package merge

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// repairLockUndoFixture plants what the combine undo's lock restore reads:
// the survivor's title lock as the override wrote it (a person's lock on
// "Year One"), and a journal whose before-state is a repair lock from opID.
func repairLockUndoFixture(t *testing.T, store database.Store, bookID, opID string) *CombineOverrideUndo {
	t.Helper()
	encoded, err := metastate.Encode("Year One")
	require.NoError(t, err)
	written := database.MetadataFieldState{BookID: bookID, Field: database.FieldKeyTitle, OverrideValue: encoded, OverrideLocked: true}
	require.NoError(t, store.UpsertMetadataFieldState(&written))
	before := database.MetadataFieldState{BookID: bookID, Field: database.FieldKeyTitle, OverrideLocked: true, LockSource: database.RepairLockSource(opID)}
	after := written
	return &CombineOverrideUndo{
		FieldStates:      map[string]*database.MetadataFieldState{database.FieldKeyTitle: &before},
		FieldStatesAfter: map[string]*database.MetadataFieldState{database.FieldKeyTitle: &after},
	}
}

func titleState(t *testing.T, store database.Store, bookID string) database.MetadataFieldState {
	t.Helper()
	rows, err := store.GetMetadataFieldStates(bookID)
	require.NoError(t, err)
	for _, r := range rows {
		if r.Field == database.FieldKeyTitle {
			return r
		}
	}
	t.Fatalf("no title state for %s", bookID)
	return database.MetadataFieldState{}
}

// plantFieldLockRow journals the repair's lock row for the book's title; when
// reverted it is stamped the way the operation revert stamps it.
func plantFieldLockRow(t *testing.T, store database.Store, opID, bookID string, reverted bool) {
	t.Helper()
	id := fmt.Sprintf("chg-%s-%s", opID, bookID)
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		ID: id, OperationID: opID, BookID: bookID, ChangeType: "field_lock",
		FieldName: database.FieldKeyTitle, OldValue: "unlocked", NewValue: "locked", CreatedAt: time.Now(),
	}))
	if reverted {
		require.NoError(t, store.MarkOperationChangesReverted(opID, []string{id}))
	}
}

// A before-state lock whose repair operation has been reverted is not put
// back: the revert lifted that lock, and a restored copy would belong to no
// one. The undo says so in its warnings.
func TestRestoreOverrideLocks_DropsARevertedRepairLock(t *testing.T) {
	store := setupTestStore(t)
	const book, op = "book-batman-1", "op-x"
	o := repairLockUndoFixture(t, store, book, op)
	plantFieldLockRow(t, store, op, book, true)

	warns, err := NewService(store).restoreOverrideLocks(book, o)
	require.NoError(t, err)
	require.Len(t, warns, 1)
	require.Contains(t, warns[0], op)
	require.Contains(t, warns[0], "reverted")
	got := titleState(t, store, book)
	require.False(t, got.OverrideLocked, "the reverted repair's lock is not re-installed")
	require.Empty(t, got.LockSource)
	require.Nil(t, got.OverrideValue)
}

// The same journal, with the repair operation still standing: the lock goes
// back exactly as it was, repair source and all, with no warning.
func TestRestoreOverrideLocks_KeepsAStandingRepairLock(t *testing.T) {
	store := setupTestStore(t)
	const book, op = "book-batman-2", "op-y"
	o := repairLockUndoFixture(t, store, book, op)
	plantFieldLockRow(t, store, op, book, false)

	warns, err := NewService(store).restoreOverrideLocks(book, o)
	require.NoError(t, err)
	require.Empty(t, warns)
	got := titleState(t, store, book)
	require.True(t, got.OverrideLocked)
	require.Equal(t, database.RepairLockSource(op), got.LockSource)
}

// A restore racing ClaimRepairLocks on one book ends as exactly one complete
// write: the claimed lock (a person's, no source) when the claim ran second,
// the restored repair lock when it ran first. Run with -race.
func TestRestoreOverrideLocks_StripeAgainstClaimRepairLocks(t *testing.T) {
	store := setupTestStore(t)
	svc := NewService(store)
	for i := 0; i < 50; i++ {
		book := fmt.Sprintf("book-robin-%d", i)
		op := fmt.Sprintf("op-z%d", i)
		o := repairLockUndoFixture(t, store, book, op)
		plantFieldLockRow(t, store, op, book, false)

		var wg sync.WaitGroup
		var restoreErr, claimErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, restoreErr = svc.restoreOverrideLocks(book, o) }()
		go func() {
			defer wg.Done()
			claimErr = database.ClaimRepairLocks(store, book, []string{database.FieldKeyTitle})
		}()
		wg.Wait()
		require.NoError(t, restoreErr)
		require.NoError(t, claimErr)

		got := titleState(t, store, book)
		require.True(t, got.OverrideLocked)
		require.Nil(t, got.OverrideValue, "never the override's value mixed into either outcome")
		require.Contains(t, []string{"", database.RepairLockSource(op)}, got.LockSource)
	}
}
