// file: internal/audiobooks/restore_library_state_test.go
// version: 1.1.0
// guid: ddf71ad3-a1e7-432e-a422-41aa7f87a782
// last-edited: 2026-10-01

package audiobooks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func restoredRow(t *testing.T, f *vptest.Fixture, id string) *database.Book {
	t.Helper()
	b, err := f.S.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}

// TestRestoreAudiobook_ReturnsTheStateBeforeTrash is visibility audit
// 2026-10-01 §3 #4's write-path cause: RestoreAudiobook always wrote
// "imported", so an organized book restored from the trash dropped out of ABS,
// which lists only "organized" rows. The owner's rule: restore the state the
// book had before it was trashed.
func TestRestoreAudiobook_ReturnsTheStateBeforeTrash(t *testing.T) {
	f, svc := handoffFixture(t)
	id := f.Book(t, vptest.Spec{ID: "org", Primary: "nil"}) // organized, ungrouped, nil flag

	_, err := svc.DeleteAudiobook(context.Background(), id, &DeleteAudiobookOptions{SoftDelete: true})
	require.NoError(t, err)
	require.Equal(t, "deleted", *restoredRow(t, f, id).LibraryState)

	_, err = svc.RestoreAudiobook(context.Background(), id)
	require.NoError(t, err)

	b := restoredRow(t, f, id)
	require.False(t, b.IsSoftDeleted())
	require.NotNil(t, b.LibraryState)
	require.Equal(t, "organized", *b.LibraryState)
	require.True(t, database.ABSLibraryFilter().Matches(b), "a restored organized book must be listed by ABS again")
}

// A merge loser restored from the trash is a book of its own again: keeping
// MergedIntoBookID would leave it hidden by every merge-aware path.
func TestRestoreAudiobook_ClearsMergedInto(t *testing.T) {
	f, svc := handoffFixture(t)
	survivor := f.Book(t, vptest.Spec{ID: "survivor", Primary: "nil"})
	loser := f.Book(t, vptest.Spec{ID: "loser", Primary: "nil"})
	_, err := f.S.ModifyBook(loser, func(b *database.Book) error {
		b.MergedIntoBookID = &survivor
		return nil
	})
	require.NoError(t, err)
	f.SoftDelete(t, loser)

	_, err = svc.RestoreAudiobook(context.Background(), loser)
	require.NoError(t, err)

	b := restoredRow(t, f, loser)
	require.Nil(t, b.MergedIntoBookID, "a restored merge loser must not stay pointed at its survivor")
	require.Equal(t, "organized", *b.LibraryState, "a trash path that did not relabel the row keeps its state")
}

// legacyTrash trashes a row the way DeleteAudiobook did before the pre-trash
// state was recorded: label "deleted", nothing remembered.
func legacyTrash(t *testing.T, f *vptest.Fixture, id string) {
	t.Helper()
	_, err := f.S.ModifyBook(id, func(b *database.Book) error {
		v, now, deleted := true, time.Now(), "deleted"
		b.MarkedForDeletion, b.MarkedForDeletionAt, b.LibraryState = &v, &now, &deleted
		return nil
	})
	require.NoError(t, err)
}

// A book trashed before the state was recorded falls back by where its present
// files are: inside the library root is organized, outside it is imported.
func TestRestoreAudiobook_LegacyTrashFallsBackByFileLocation(t *testing.T) {
	f, svc := handoffFixture(t)
	inside := f.Book(t, vptest.Spec{ID: "inside", Primary: "nil"})
	outside := f.Book(t, vptest.Spec{ID: "outside", Primary: "nil", Outside: true})
	legacyTrash(t, f, inside)
	legacyTrash(t, f, outside)

	_, err := svc.RestoreAudiobook(context.Background(), inside)
	require.NoError(t, err)
	_, err = svc.RestoreAudiobook(context.Background(), outside)
	require.NoError(t, err)

	require.Equal(t, "organized", *restoredRow(t, f, inside).LibraryState)
	require.Equal(t, "imported", *restoredRow(t, f, outside).LibraryState)
}

// The recorded state, not the file location, decides: a book inside the
// library root whose state was "imported" (the stale-state population) comes
// back "imported", not promoted into ABS by the fallback.
func TestRestoreAudiobook_RecordedStateBeatsFileLocation(t *testing.T) {
	f, svc := handoffFixture(t)
	id := f.Book(t, vptest.Spec{ID: "stale", Primary: "nil", State: "imported"})

	_, err := svc.DeleteAudiobook(context.Background(), id, &DeleteAudiobookOptions{SoftDelete: true})
	require.NoError(t, err)
	_, err = svc.RestoreAudiobook(context.Background(), id)
	require.NoError(t, err)

	b := restoredRow(t, f, id)
	require.Equal(t, "imported", *b.LibraryState)
	require.Nil(t, b.PreTrashLibraryState, "the record is cleared once used")
}

// Reverting an operation's soft delete applies the same state rule, but leaves
// MergedIntoBookID to the journal: the operation that set it records that
// change itself, and its revert restores the pre-operation value.
func TestRevertBookSoftDelete_RestoresStateKeepsMergePointer(t *testing.T) {
	f, _ := handoffFixture(t)
	id := f.Book(t, vptest.Spec{ID: "rev", Primary: "nil"})
	_, err := f.S.ModifyBook(id, func(b *database.Book) error {
		v, now, deleted, organized, survivor := true, time.Now(), "deleted", "organized", "survivor"
		b.MarkedForDeletion, b.MarkedForDeletionAt = &v, &now
		b.LibraryState, b.PreTrashLibraryState = &deleted, &organized
		b.MergedIntoBookID = &survivor
		return nil
	})
	require.NoError(t, err)

	rs := NewRevertService(f.S)
	require.NoError(t, rs.revertBookSoftDelete(&database.OperationChange{BookID: id}, undo.SoftDeleteStamps{}))

	b := restoredRow(t, f, id)
	require.False(t, b.IsSoftDeleted())
	require.Equal(t, "organized", *b.LibraryState)
	require.NotNil(t, b.MergedIntoBookID)
}
