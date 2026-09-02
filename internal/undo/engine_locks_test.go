// file: internal/undo/engine_locks_test.go
// version: 1.0.0
// guid: 4d9a7c3e-6b21-4f85-a0e9-3c8d5f2b7a16
// last-edited: 2026-09-02

package undo

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// undoLockFixture seeds a book whose title was set to "Fetched Title" and
// publisher to "Fetched Press" by op1 (from "Original Title" / "Original
// Press"), then the user edited the title to "My Title" and LOCKED it.
func undoLockFixture(t *testing.T, lockTitle bool) (*database.PebbleStore, string) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	pub := "Fetched Press"
	book, err := store.CreateBook(&database.Book{Title: "My Title", Publisher: &pub, FilePath: "/x/book.m4b", Format: "m4b"})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	if lockTitle {
		mine := "My Title"
		if err := store.UpsertMetadataFieldState(&database.MetadataFieldState{
			BookID: book.ID, Field: database.FieldKeyTitle, OverrideValue: &mine, OverrideLocked: true,
		}); err != nil {
			t.Fatalf("lock title: %v", err)
		}
	}
	if err := store.CreateOperationChange(&database.OperationChange{
		ID: "c1", OperationID: "op1", BookID: book.ID,
		ChangeType: "metadata_update",
		OldValue:   `{"title":"Original Title","publisher":"Original Press"}`,
		NewValue:   `{"title":"Fetched Title","publisher":"Fetched Press"}`,
		CreatedAt:  time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("create change: %v", err)
	}
	return store, book.ID
}

func TestRunUndoOperation_LockedTitleIsNotRestored(t *testing.T) {
	store, bookID := undoLockFixture(t, true)

	result, err := RunUndoOperation(store, "op1", nil, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Reverted != 0 || result.Failed != 1 {
		t.Fatalf("reverted=%d failed=%d, want 0/1: the change was only partly undone", result.Reverted, result.Failed)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "user-locked") || !strings.Contains(result.Errors[0], "title") {
		t.Fatalf("errors = %v, want one naming the locked title", result.Errors)
	}

	got, _ := store.GetBookByID(bookID)
	if got.Title != "My Title" {
		t.Errorf("title = %q, want the user's locked value kept", got.Title)
	}
	if got.Publisher == nil || *got.Publisher != "Original Press" {
		t.Errorf("publisher = %v, want the unlocked field restored", got.Publisher)
	}
	changes, _ := store.GetOperationChanges("op1")
	if changes[0].RevertedAt != nil {
		t.Error("a change that was not fully undone must not be marked reverted")
	}
}

func TestRunUndoOperation_UnlockedTitleIsRestored(t *testing.T) {
	store, bookID := undoLockFixture(t, false)

	result, err := RunUndoOperation(store, "op1", nil, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Reverted != 1 {
		t.Fatalf("reverted = %d, want 1", result.Reverted)
	}
	got, _ := store.GetBookByID(bookID)
	if got.Title != "Original Title" {
		t.Errorf("title = %q: the fixture cannot observe the lock if an unlocked title does not restore", got.Title)
	}
}

// A single-field change whose one field is locked writes nothing at all.
func TestRunUndoOperation_SingleLockedFieldWritesNothing(t *testing.T) {
	writes := 0
	title := "My Title"
	store := &database.MockStore{
		GetOperationChangesFunc: func(string) ([]*database.OperationChange, error) {
			return []*database.OperationChange{{ID: "c1", OperationID: "op1", BookID: "b1",
				ChangeType: "metadata_update", FieldName: "title", OldValue: "Original Title"}}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) { return &database.Book{ID: id, Title: title}, nil },
		GetMetadataFieldStatesFunc: func(id string) ([]database.MetadataFieldState, error) {
			return []database.MetadataFieldState{{BookID: id, Field: database.FieldKeyTitle, OverrideLocked: true}}, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) { writes++; return b, nil },
	}
	result, err := RunUndoOperation(store, "op1", nil, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if writes != 0 {
		t.Errorf("UpdateBook called %d times for a fully-locked change, want 0", writes)
	}
	if result.Failed != 1 {
		t.Errorf("failed = %d, want 1", result.Failed)
	}
}

func TestRunUndoOperation_LockReadErrorWritesNothing(t *testing.T) {
	writes := 0
	store := &database.MockStore{
		GetOperationChangesFunc: func(string) ([]*database.OperationChange, error) {
			return []*database.OperationChange{{ID: "c1", OperationID: "op1", BookID: "b1",
				ChangeType: "metadata_update", FieldName: "title", OldValue: "Original Title"}}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) { return &database.Book{ID: id, Title: "Now"}, nil },
		GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) {
			return nil, errors.New("pebble: closed")
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) { writes++; return b, nil },
	}
	result, err := RunUndoOperation(store, "op1", nil, nil)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if writes != 0 || result.Failed != 1 {
		t.Errorf("writes=%d failed=%d, want 0/1: fail closed", writes, result.Failed)
	}
	if len(result.Errors) == 0 || !strings.Contains(result.Errors[0], database.ErrFieldLocksUnavailable.Error()) {
		t.Errorf("errors = %v, want the lock-unavailable sentinel", result.Errors)
	}
}
