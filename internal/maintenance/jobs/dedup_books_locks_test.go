// file: internal/maintenance/jobs/dedup_books_locks_test.go
// version: 1.0.0
// guid: 6c1d4e9b-3a72-4f58-b0e6-8d2a5c7f1e34
// last-edited: 2026-09-02

package jobs

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ddLockFixture is a keeper with a blank, user-LOCKED narrator and a dup that
// still carries the stale narrator plus an unlocked publisher the keeper lacks.
// Every write is captured by book ID.
func ddLockFixture(t *testing.T, locked []string, lockErr error) (*database.MockStore, map[string]*database.Book) {
	t.Helper()
	keeper := &database.Book{ID: "keep", Title: "Keeper"}
	writes := map[string]*database.Book{}
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id == "keep" {
				cp := *keeper
				return &cp, nil
			}
			return &database.Book{ID: id}, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			cp := *b
			writes[id] = &cp
			return b, nil
		},
		GetMetadataFieldStatesFunc: func(id string) ([]database.MetadataFieldState, error) {
			if lockErr != nil {
				return nil, lockErr
			}
			if id != "keep" {
				return nil, nil
			}
			rows := make([]database.MetadataFieldState, 0, len(locked))
			for _, k := range locked {
				rows = append(rows, database.MetadataFieldState{BookID: id, Field: k, OverrideLocked: true})
			}
			return rows, nil
		},
	}
	return store, writes
}

func ddDup() *database.Book {
	return &database.Book{ID: "dup", Title: "Dup", Narrator: new("Stale Narrator"), Publisher: new("Real Publisher")}
}

func TestDDMergeDuplicateBook_KeeperLockedBlankStaysBlank(t *testing.T) {
	store, writes := ddLockFixture(t, []string{database.FieldKeyNarrator}, nil)

	if err := ddMergeDuplicateBook(store, &database.Book{ID: "keep"}, ddDup(), false, nil); err != nil {
		t.Fatalf("ddMergeDuplicateBook: %v", err)
	}
	keeper := writes["keep"]
	if keeper == nil {
		t.Fatal("keeper was never written; the unlocked publisher should have been filled")
	}
	if keeper.Narrator != nil {
		t.Errorf("keeper.Narrator = %q, want nil: the user locked it blank", *keeper.Narrator)
	}
	if keeper.Publisher == nil || *keeper.Publisher != "Real Publisher" {
		t.Errorf("keeper.Publisher = %v, want Real Publisher: publisher is unlocked", keeper.Publisher)
	}
	if dup := writes["dup"]; dup == nil || dup.MarkedForDeletion == nil || !*dup.MarkedForDeletion {
		t.Error("the dup must still be soft-deleted after a lock-respecting merge")
	}
}

// Locks unreadable: fail closed. The keeper is not written AND the dup is not
// deleted -- its fields may be the only copy of what the keeper lacks.
func TestDDMergeDuplicateBook_LockReadErrorLeavesBothRows(t *testing.T) {
	store, writes := ddLockFixture(t, nil, errors.New("pebble: closed"))

	err := ddMergeDuplicateBook(store, &database.Book{ID: "keep"}, ddDup(), false, nil)
	if !errors.Is(err, database.ErrFieldLocksUnavailable) {
		t.Fatalf("err = %v, want ErrFieldLocksUnavailable", err)
	}
	if len(writes) != 0 {
		t.Fatalf("%d rows written, want 0: %v", len(writes), writes)
	}
}
