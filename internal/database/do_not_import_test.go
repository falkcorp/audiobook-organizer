// file: internal/database/do_not_import_test.go
// version: 2.0.1
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-10-03

// NOTE(fable5 T022): TestDoNotImport_SQLite removed (SQLite store deleted).

package database

import (
	"testing"
	"time"
)

func TestDoNotImport_Pebble(t *testing.T) {
	store, err := NewPebbleStoreInMemory("do_not_import")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Run migrations
	if err := RunMigrations(store); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	testDoNotImportOperations(t, store)
}

func testDoNotImportOperations(t *testing.T, store Store) {
	testHash1 := "abc123def456"
	testHash2 := "xyz789uvw012"
	reason1 := "duplicate file"
	reason2 := "corrupted audio"

	// Test 1: Initially no hashes should be blocked
	blocked, err := store.IsHashBlocked(testHash1)
	if err != nil {
		t.Fatalf("IsHashBlocked failed: %v", err)
	}
	if blocked {
		t.Error("expected hash to not be blocked initially")
	}

	// Test 2: Add a blocked hash
	if err := store.AddBlockedHash(testHash1, reason1); err != nil {
		t.Fatalf("AddBlockedHash failed: %v", err)
	}

	// Test 3: Verify hash is now blocked
	blocked, err = store.IsHashBlocked(testHash1)
	if err != nil {
		t.Fatalf("IsHashBlocked failed: %v", err)
	}
	if !blocked {
		t.Error("expected hash to be blocked after adding")
	}

	// Test 4: Get specific blocked hash
	entry, err := store.GetBlockedHashByHash(testHash1)
	if err != nil {
		t.Fatalf("GetBlockedHashByHash failed: %v", err)
	}
	if entry == nil {
		t.Fatal("expected to find blocked hash entry")
	}
	if entry.Hash != testHash1 {
		t.Errorf("expected hash %s, got %s", testHash1, entry.Hash)
	}
	if entry.Reason != reason1 {
		t.Errorf("expected reason %s, got %s", reason1, entry.Reason)
	}
	if entry.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}

	// Test 5: Add second blocked hash
	if err := store.AddBlockedHash(testHash2, reason2); err != nil {
		t.Fatalf("AddBlockedHash failed: %v", err)
	}

	// Test 6: Get all blocked hashes
	allBlocked, err := store.GetAllBlockedHashes()
	if err != nil {
		t.Fatalf("GetAllBlockedHashes failed: %v", err)
	}
	if len(allBlocked) != 2 {
		t.Errorf("expected 2 blocked hashes, got %d", len(allBlocked))
	}

	// Verify both hashes are in the list
	foundHash1, foundHash2 := false, false
	for _, entry := range allBlocked {
		if entry.Hash == testHash1 {
			foundHash1 = true
			if entry.Reason != reason1 {
				t.Errorf("hash1: expected reason %s, got %s", reason1, entry.Reason)
			}
		}
		if entry.Hash == testHash2 {
			foundHash2 = true
			if entry.Reason != reason2 {
				t.Errorf("hash2: expected reason %s, got %s", reason2, entry.Reason)
			}
		}
	}
	if !foundHash1 || !foundHash2 {
		t.Error("expected to find both blocked hashes in list")
	}

	// Test 7: Remove a blocked hash
	if err := store.RemoveBlockedHash(testHash1); err != nil {
		t.Fatalf("RemoveBlockedHash failed: %v", err)
	}

	// Test 8: Verify hash is no longer blocked
	blocked, err = store.IsHashBlocked(testHash1)
	if err != nil {
		t.Fatalf("IsHashBlocked failed: %v", err)
	}
	if blocked {
		t.Error("expected hash to not be blocked after removal")
	}

	// Test 9: Verify only one hash remains
	allBlocked, err = store.GetAllBlockedHashes()
	if err != nil {
		t.Fatalf("GetAllBlockedHashes failed: %v", err)
	}
	if len(allBlocked) != 1 {
		t.Errorf("expected 1 blocked hash after removal, got %d", len(allBlocked))
	}
	if len(allBlocked) > 0 && allBlocked[0].Hash != testHash2 {
		t.Errorf("expected remaining hash to be %s, got %s", testHash2, allBlocked[0].Hash)
	}

	// Test 10: Test updating a blocked hash (via INSERT OR REPLACE / upsert)
	newReason := "updated reason"
	time.Sleep(10 * time.Millisecond) // Ensure different timestamp
	if err := store.AddBlockedHash(testHash2, newReason); err != nil {
		t.Fatalf("AddBlockedHash (update) failed: %v", err)
	}

	entry, err = store.GetBlockedHashByHash(testHash2)
	if err != nil {
		t.Fatalf("GetBlockedHashByHash failed: %v", err)
	}
	if entry == nil {
		t.Fatal("expected to find updated blocked hash entry")
	}
	if entry.Reason != newReason {
		t.Errorf("expected updated reason %s, got %s", newReason, entry.Reason)
	}

	// Test 11: Get non-existent hash
	entry, err = store.GetBlockedHashByHash("nonexistent")
	if err != nil {
		t.Fatalf("GetBlockedHashByHash for nonexistent hash failed: %v", err)
	}
	if entry != nil {
		t.Error("expected nil for nonexistent hash")
	}

	// Test 12: Remove all remaining hashes
	if err := store.RemoveBlockedHash(testHash2); err != nil {
		t.Fatalf("RemoveBlockedHash failed: %v", err)
	}

	allBlocked, err = store.GetAllBlockedHashes()
	if err != nil {
		t.Fatalf("GetAllBlockedHashes failed: %v", err)
	}
	if len(allBlocked) != 0 {
		t.Errorf("expected 0 blocked hashes after removing all, got %d", len(allBlocked))
	}
}
