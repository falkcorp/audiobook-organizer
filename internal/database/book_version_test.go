// file: internal/database/book_version_test.go
// version: 1.0.1
// guid: 7a3d9e1c-5b4f-4a60-b8c5-2e7f0c1b9a48

package database

import (
	"path/filepath"
	"testing"
)

func TestBookVersion_CreateAndGet(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	v, err := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusActive, Format: "m4b",
		Source: "deluge", TorrentHash: "abc123",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if v.ID == "" {
		t.Fatal("ID should be auto-assigned")
	}
	if v.Version != 1 {
		t.Errorf("Version = %d, want 1", v.Version)
	}

	got, err := store.GetBookVersion(v.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v / %v", got, err)
	}
	if got.Format != "m4b" {
		t.Errorf("Format = %q", got.Format)
	}
}

func TestBookVersion_SingleActiveInvariant(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusActive, Format: "m4b", Source: "imported",
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Second active for same book should fail.
	if _, err := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusActive, Format: "mp3", Source: "imported",
	}); err == nil {
		t.Error("expected single-active-per-book invariant to reject second active")
	}

	// Alt is fine.
	if _, err := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusAlt, Format: "mp3", Source: "imported",
	}); err != nil {
		t.Errorf("alt status should be allowed: %v", err)
	}

	// Different book's active is fine.
	if _, err := store.CreateBookVersion(&BookVersion{
		BookID: "b2", Status: BookVersionStatusActive, Format: "m4b", Source: "imported",
	}); err != nil {
		t.Errorf("second book's active should be allowed: %v", err)
	}
}

func TestBookVersion_GetActiveForBook(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b1", Status: BookVersionStatusAlt, Format: "mp3", Source: "imported"})
	active, _ := store.CreateBookVersion(&BookVersion{BookID: "b1", Status: BookVersionStatusActive, Format: "m4b", Source: "imported"})
	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b1", Status: BookVersionStatusAlt, Format: "flac", Source: "imported"})

	got, err := store.GetActiveVersionForBook("b1")
	if err != nil || got == nil {
		t.Fatalf("GetActiveVersionForBook: %v, %v", got, err)
	}
	if got.ID != active.ID {
		t.Errorf("active ID = %q, want %q", got.ID, active.ID)
	}
}

func TestBookVersion_GetByTorrentHash(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	v, _ := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusInactivePurged,
		Format: "m4b", Source: "deluge", TorrentHash: "hash-abc",
	})

	got, err := store.GetBookVersionByTorrentHash("hash-abc")
	if err != nil || got == nil {
		t.Fatalf("GetBookVersionByTorrentHash: %v, %v", got, err)
	}
	if got.ID != v.ID {
		t.Errorf("lookup mismatch: got %q, want %q", got.ID, v.ID)
	}

	miss, err := store.GetBookVersionByTorrentHash("not-in-library")
	if err != nil {
		t.Errorf("miss should not error: %v", err)
	}
	if miss != nil {
		t.Errorf("miss should be nil")
	}
}

func TestBookVersion_UpdateStatusTransitions(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	v, _ := store.CreateBookVersion(&BookVersion{
		BookID: "b1", Status: BookVersionStatusActive, Format: "m4b", Source: "deluge",
	})

	// Active → alt (demoting a primary). The active-pointer index
	// should clear so GetActiveVersionForBook returns nil.
	v.Status = BookVersionStatusAlt
	if err := store.UpdateBookVersion(v); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := store.GetActiveVersionForBook("b1")
	if got != nil {
		t.Errorf("expected no active version after demoting, got %v", got)
	}

	// After update, Version counter bumped.
	after, _ := store.GetBookVersion(v.ID)
	if after.Version != 2 {
		t.Errorf("Version = %d after update, want 2", after.Version)
	}
}

func TestBookVersion_ListTrashedAndPurged(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b1", Status: BookVersionStatusTrash, Format: "m4b", Source: "imported"})
	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b2", Status: BookVersionStatusTrash, Format: "mp3", Source: "imported"})
	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b3", Status: BookVersionStatusInactivePurged, Format: "flac", Source: "imported"})
	_, _ = store.CreateBookVersion(&BookVersion{BookID: "b4", Status: BookVersionStatusBlockedForRedownload, Format: "m4b", Source: "deluge", TorrentHash: "xyz"})

	trashed, _ := store.ListTrashedBookVersions()
	if len(trashed) != 2 {
		t.Errorf("trashed = %d, want 2", len(trashed))
	}

	purged, _ := store.ListPurgedBookVersions()
	// Includes both inactive_purged and blocked_for_redownload per spec.
	if len(purged) != 2 {
		t.Errorf("purged = %d, want 2 (inactive_purged + blocked_for_redownload)", len(purged))
	}
}
