// file: internal/database/book_version_delete_purging_test.go
// version: 1.0.0
// guid: 9e4a7c20-3d15-4b68-a2f9-6c0d8e1b5a73
// last-edited: 2026-10-27

package database

import (
	"errors"
	"testing"
)

// A hard delete of a version whose purge is running would delete the record the
// purge reads to know which files it still owes a removal and which it has
// already taken, stranding the rest on disk with nothing naming them. The store
// refuses it, under the version's stripe, whichever handler asks.
func TestDeleteBookVersion_RefusesAVersionWhosePurgeIsRunning(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	v, err := store.CreateBookVersion(&BookVersion{BookID: "dark-knight-returns", Status: BookVersionStatusTrash, Format: "m4b", Source: "imported"})
	if err != nil {
		t.Fatal(err)
	}
	claimed := *v
	claimed.Status = BookVersionStatusPurging
	claimed.PurgeUnlinked = []string{"/lib/Gotham/Dark Knight Returns/part1.m4b"}
	if err := store.UpdateBookVersion(&claimed); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := store.DeleteBookVersion(v.ID); !errors.Is(err, ErrVersionPurging) {
		t.Fatalf("DeleteBookVersion of a purging version = %v, want ErrVersionPurging", err)
	}
	got, err := store.GetBookVersion(v.ID)
	if err != nil || got == nil {
		t.Fatalf("the refused delete removed the record: %v / %v", got, err)
	}
	if got.Status != BookVersionStatusPurging || len(got.PurgeUnlinked) != 1 {
		t.Errorf("the record changed: status=%s unlinked=%v", got.Status, got.PurgeUnlinked)
	}
}

// The states a hard delete is for are not refused: a purged version, and a
// purge that gave up (the owner was shown its files and chose to drop the record).
func TestDeleteBookVersion_StillDeletesAPurgedOrPurgeFailedVersion(t *testing.T) {
	store, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	purged, err := store.CreateBookVersion(&BookVersion{BookID: "year-one", Status: BookVersionStatusInactivePurged, Format: "m4b", Source: "imported"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBookVersion(purged.ID); err != nil {
		t.Fatalf("delete of a purged version: %v", err)
	}

	trash, err := store.CreateBookVersion(&BookVersion{BookID: "year-two", Status: BookVersionStatusTrash, Format: "m4b", Source: "imported"})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{BookVersionStatusPurging, BookVersionStatusPurgeFailed} {
		c := *trash
		c.Status = to
		if err := store.UpdateBookVersion(&c); err != nil {
			t.Fatalf("-> %s: %v", to, err)
		}
	}
	if err := store.DeleteBookVersion(trash.ID); err != nil {
		t.Fatalf("delete of a purge_failed version: %v", err)
	}
	if got, _ := store.GetBookVersion(trash.ID); got != nil {
		t.Error("the purge_failed version is still there")
	}
}
