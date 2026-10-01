// file: internal/undo/book_create_test.go
// version: 1.0.1
// guid: 4e1b7c93-2d58-4a06-9f3e-8c5a1d7b2e60
// last-edited: 2026-10-01

package undo

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// noProgressStore exposes only RepairBookCreateStore's own methods, so it
// cannot read listening progress (no merge.UserProgressMerger).
type noProgressStore struct{ RepairBookCreateStore }

// TestCheckRepairBookCreate_UserStateRefusals: a created book someone
// listened to, or that gained a live external id, is not hidden by the
// revert; a store that cannot read progress fails closed.
func TestCheckRepairBookCreate_UserStateRefusals(t *testing.T) {
	setup := func(t *testing.T) (*database.PebbleStore, *database.OperationChange) {
		t.Helper()
		store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		store.WaitForWarmup()
		b, err := store.CreateBook(&database.Book{Title: "Sword", FilePath: "/lib/books/itunes/A/Sword"})
		if err != nil {
			t.Fatal(err)
		}
		c := &database.OperationChange{ID: "chg-1", OperationID: "op-1", BookID: b.ID,
			ChangeType: ChangeTypeRepairBookCreate, FieldName: "book",
			NewValue: mustEncode(t, RepairBookCreateValue{ID: b.ID, Title: "Sword", FilePath: "/lib/books/itunes/A/Sword"})}
		return store, c
	}

	t.Run("clean", func(t *testing.T) {
		store, c := setup(t)
		if err := CheckRepairBookCreate(store, c); err != nil {
			t.Fatalf("an untouched created book must be restorable, got %v", err)
		}
	})
	t.Run("listening progress", func(t *testing.T) {
		store, c := setup(t)
		u, err := store.CreateUser("reader", "r@example.com", "argon2id", "x", []string{"user"}, "active")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: c.BookID, Status: "in_progress", ProgressPct: 30}); err != nil {
			t.Fatal(err)
		}
		if got := RefusalReason(CheckRepairBookCreate(store, c)); got != ReasonChangedSince {
			t.Fatalf("progress: reason %q, want %q", got, ReasonChangedSince)
		}
	})
	t.Run("live external id", func(t *testing.T) {
		store, c := setup(t)
		if err := store.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "00000000000000CD", BookID: c.BookID}); err != nil {
			t.Fatal(err)
		}
		if got := RefusalReason(CheckRepairBookCreate(store, c)); got != ReasonChangedSince {
			t.Fatalf("external id: reason %q, want %q", got, ReasonChangedSince)
		}
	})
	t.Run("store without progress", func(t *testing.T) {
		store, c := setup(t)
		if got := RefusalReason(CheckRepairBookCreate(noProgressStore{store}, c)); got != ReasonFieldUnreadable {
			t.Fatalf("no progress reader: reason %q, want %q (fail closed)", got, ReasonFieldUnreadable)
		}
	})
}

func mustEncode(t *testing.T, v RepairBookCreateValue) string {
	t.Helper()
	s, err := v.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
