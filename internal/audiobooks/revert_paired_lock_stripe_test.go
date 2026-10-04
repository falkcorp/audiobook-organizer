// file: internal/audiobooks/revert_paired_lock_stripe_test.go
// version: 1.0.0
// guid: 3f0c9a51-6b7e-4d2a-9c84-1e5f7a2b8d63
// last-edited: 2026-10-03

package audiobooks

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The paired-lock check of a metadata_update restore runs under the book's
// field-state stripe, held across the value write: a person who takes the
// repair's lock over while the revert is under way either lands first (and
// the revert refuses) or after the value is back. Until 2026-10-03 the check
// read the field state outside the stripe and the value compare-and-set ran
// after it, so a take-over in between was left holding the junk title the
// revert restored.
func TestRevertMetadataUpdate_PairedLockCheckHoldsTheFieldStateStripe(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	const opID = "op-paired"
	book, err := store.CreateBook(&database.Book{Title: "Real Title", FilePath: "/x/p.m4b"})
	require.NoError(t, err)
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: book.ID,
		Field: database.FieldKeyTitle, OverrideLocked: true, LockSource: database.RepairLockSource(opID)}))
	update := &database.OperationChange{ID: "c1", OperationID: opID, BookID: book.ID, ChangeType: "metadata_update",
		FieldName: database.FieldKeyTitle, OldValue: "read by narrator", NewValue: "Real Title"}
	lock := &database.OperationChange{ID: "c2", OperationID: opID, BookID: book.ID, ChangeType: undo.ChangeTypeFieldLock,
		FieldName: database.FieldKeyTitle, OldValue: undo.FieldLockUnlocked, NewValue: undo.FieldLockLocked}
	require.NoError(t, store.CreateOperationChange(update))
	require.NoError(t, store.CreateOperationChange(lock))
	rs := NewRevertService(store)

	unlock := database.LockMetadataState(book.ID)
	done := make(chan error, 1)
	go func() { done <- rs.revertMetadataUpdate(update, nil) }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("the restore ran without the field-state stripe (err=%v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	// A person takes the lock over while the revert waits.
	require.NoError(t, store.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: book.ID,
		Field: database.FieldKeyTitle, OverrideLocked: true}))
	unlock()

	select {
	case err := <-done:
		require.Error(t, err, "the revert restored a value under a person's lock")
	case <-time.After(10 * time.Second):
		t.Fatal("the revert did not finish")
	}
	row, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, "Real Title", row.Title)
}
