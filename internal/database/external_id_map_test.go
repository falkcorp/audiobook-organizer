// file: internal/database/external_id_map_test.go
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

package database

import (
	"testing"
)

func TestExternalIDMapping_CreateAndGet(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create a mapping
	mapping := &ExternalIDMapping{
		Source:     "itunes",
		ExternalID: "ABC123",
		BookID:     "BOOK001",
		FilePath:   "/path/to/file.m4b",
	}
	if err := store.CreateExternalIDMapping(mapping); err != nil {
		t.Fatalf("CreateExternalIDMapping failed: %v", err)
	}

	// Retrieve by external ID
	bookID, err := store.GetBookByExternalID("itunes", "ABC123")
	if err != nil {
		t.Fatalf("GetBookByExternalID failed: %v", err)
	}
	if bookID != "BOOK001" {
		t.Fatalf("expected book_id BOOK001, got %s", bookID)
	}

	// Not found
	bookID, err = store.GetBookByExternalID("itunes", "NONEXISTENT")
	if err != nil {
		t.Fatalf("GetBookByExternalID failed: %v", err)
	}
	if bookID != "" {
		t.Fatalf("expected empty string for missing ID, got %s", bookID)
	}
}

func TestExternalIDMapping_TrackNumber(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	tn := 3
	mapping := &ExternalIDMapping{
		Source:      "itunes",
		ExternalID:  "TRACK3",
		BookID:      "BOOK002",
		TrackNumber: &tn,
	}
	if err := store.CreateExternalIDMapping(mapping); err != nil {
		t.Fatalf("CreateExternalIDMapping failed: %v", err)
	}

	mappings, err := store.GetExternalIDsForBook("BOOK002")
	if err != nil {
		t.Fatalf("GetExternalIDsForBook failed: %v", err)
	}
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mappings))
	}
	if mappings[0].TrackNumber == nil || *mappings[0].TrackNumber != 3 {
		t.Fatalf("expected track_number 3, got %v", mappings[0].TrackNumber)
	}
}

func TestExternalIDMapping_TombstoneBlocksLookup(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	mapping := &ExternalIDMapping{
		Source:     "itunes",
		ExternalID: "PID_TOMBSTONE",
		BookID:     "BOOK003",
	}
	if err := store.CreateExternalIDMapping(mapping); err != nil {
		t.Fatalf("CreateExternalIDMapping failed: %v", err)
	}

	// Verify it's not tombstoned
	tombstoned, err := store.IsExternalIDTombstoned("itunes", "PID_TOMBSTONE")
	if err != nil {
		t.Fatalf("IsExternalIDTombstoned failed: %v", err)
	}
	if tombstoned {
		t.Fatal("expected not tombstoned initially")
	}

	// Tombstone it
	if err := store.TombstoneExternalID("itunes", "PID_TOMBSTONE"); err != nil {
		t.Fatalf("TombstoneExternalID failed: %v", err)
	}

	// Verify tombstone blocks lookup
	bookID, err := store.GetBookByExternalID("itunes", "PID_TOMBSTONE")
	if err != nil {
		t.Fatalf("GetBookByExternalID failed: %v", err)
	}
	if bookID != "" {
		t.Fatalf("expected empty string for tombstoned ID, got %s", bookID)
	}

	// Verify IsExternalIDTombstoned returns true
	tombstoned, err = store.IsExternalIDTombstoned("itunes", "PID_TOMBSTONE")
	if err != nil {
		t.Fatalf("IsExternalIDTombstoned failed: %v", err)
	}
	if !tombstoned {
		t.Fatal("expected tombstoned after TombstoneExternalID")
	}
}

func TestExternalIDMapping_ReassignExternalIDs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create multiple mappings for one book
	for _, pid := range []string{"PID_A", "PID_B", "PID_C"} {
		if err := store.CreateExternalIDMapping(&ExternalIDMapping{
			Source:     "itunes",
			ExternalID: pid,
			BookID:     "OLD_BOOK",
		}); err != nil {
			t.Fatalf("CreateExternalIDMapping failed: %v", err)
		}
	}

	// Reassign to new book
	if err := store.ReassignExternalIDs("OLD_BOOK", "NEW_BOOK"); err != nil {
		t.Fatalf("ReassignExternalIDs failed: %v", err)
	}

	// Verify all point to new book
	for _, pid := range []string{"PID_A", "PID_B", "PID_C"} {
		bookID, err := store.GetBookByExternalID("itunes", pid)
		if err != nil {
			t.Fatalf("GetBookByExternalID failed: %v", err)
		}
		if bookID != "NEW_BOOK" {
			t.Fatalf("expected NEW_BOOK for %s, got %s", pid, bookID)
		}
	}

	// Old book should have no mappings
	oldMappings, err := store.GetExternalIDsForBook("OLD_BOOK")
	if err != nil {
		t.Fatalf("GetExternalIDsForBook failed: %v", err)
	}
	if len(oldMappings) != 0 {
		t.Fatalf("expected 0 mappings for OLD_BOOK, got %d", len(oldMappings))
	}

	// New book should have 3
	newMappings, err := store.GetExternalIDsForBook("NEW_BOOK")
	if err != nil {
		t.Fatalf("GetExternalIDsForBook failed: %v", err)
	}
	if len(newMappings) != 3 {
		t.Fatalf("expected 3 mappings for NEW_BOOK, got %d", len(newMappings))
	}
}

func TestExternalIDMapping_BulkCreateWithDuplicates(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create an existing mapping
	if err := store.CreateExternalIDMapping(&ExternalIDMapping{
		Source:     "itunes",
		ExternalID: "EXISTING_PID",
		BookID:     "BOOK_ORIGINAL",
	}); err != nil {
		t.Fatalf("CreateExternalIDMapping failed: %v", err)
	}

	// Bulk create with duplicates (should ignore existing)
	mappings := []ExternalIDMapping{
		{Source: "itunes", ExternalID: "EXISTING_PID", BookID: "BOOK_DIFFERENT"},
		{Source: "itunes", ExternalID: "NEW_PID_1", BookID: "BOOK_BULK"},
		{Source: "itunes", ExternalID: "NEW_PID_2", BookID: "BOOK_BULK"},
	}
	if err := store.BulkCreateExternalIDMappings(mappings); err != nil {
		t.Fatalf("BulkCreateExternalIDMappings failed: %v", err)
	}

	// Existing mapping should still point to original book (INSERT OR IGNORE)
	bookID, err := store.GetBookByExternalID("itunes", "EXISTING_PID")
	if err != nil {
		t.Fatalf("GetBookByExternalID failed: %v", err)
	}
	if bookID != "BOOK_ORIGINAL" {
		t.Fatalf("expected BOOK_ORIGINAL (not overwritten), got %s", bookID)
	}

	// New mappings should exist
	bookID, err = store.GetBookByExternalID("itunes", "NEW_PID_1")
	if err != nil {
		t.Fatalf("GetBookByExternalID failed: %v", err)
	}
	if bookID != "BOOK_BULK" {
		t.Fatalf("expected BOOK_BULK, got %s", bookID)
	}
}

func TestExternalIDMapping_GetExternalIDsForBook(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create mappings for the same book from different sources
	mappings := []ExternalIDMapping{
		{Source: "itunes", ExternalID: "IPID1", BookID: "MULTI_BOOK"},
		{Source: "audible", ExternalID: "ASIN1", BookID: "MULTI_BOOK"},
		{Source: "itunes", ExternalID: "IPID2", BookID: "MULTI_BOOK"},
	}
	if err := store.BulkCreateExternalIDMappings(mappings); err != nil {
		t.Fatalf("BulkCreateExternalIDMappings failed: %v", err)
	}

	result, err := store.GetExternalIDsForBook("MULTI_BOOK")
	if err != nil {
		t.Fatalf("GetExternalIDsForBook failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 mappings, got %d", len(result))
	}

	// Check sources are present
	sources := map[string]int{}
	for _, m := range result {
		sources[m.Source]++
	}
	if sources["itunes"] != 2 {
		t.Fatalf("expected 2 itunes mappings, got %d", sources["itunes"])
	}
	if sources["audible"] != 1 {
		t.Fatalf("expected 1 audible mapping, got %d", sources["audible"])
	}
}

func TestExternalIDMapping_UpsertOverwrites(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Create initial mapping
	if err := store.CreateExternalIDMapping(&ExternalIDMapping{
		Source:     "itunes",
		ExternalID: "UPSERT_PID",
		BookID:     "BOOK_V1",
	}); err != nil {
		t.Fatalf("CreateExternalIDMapping failed: %v", err)
	}

	// Upsert with different book_id (INSERT OR REPLACE)
	if err := store.CreateExternalIDMapping(&ExternalIDMapping{
		Source:     "itunes",
		ExternalID: "UPSERT_PID",
		BookID:     "BOOK_V2",
	}); err != nil {
		t.Fatalf("CreateExternalIDMapping (upsert) failed: %v", err)
	}

	// Should now point to V2
	bookID, err := store.GetBookByExternalID("itunes", "UPSERT_PID")
	if err != nil {
		t.Fatalf("GetBookByExternalID failed: %v", err)
	}
	if bookID != "BOOK_V2" {
		t.Fatalf("expected BOOK_V2 after upsert, got %s", bookID)
	}
}

func TestExternalIDMapping_IsExternalIDTombstoned_NotFound(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	// Non-existent ID should not be tombstoned
	tombstoned, err := store.IsExternalIDTombstoned("itunes", "DOES_NOT_EXIST")
	if err != nil {
		t.Fatalf("IsExternalIDTombstoned failed: %v", err)
	}
	if tombstoned {
		t.Fatal("expected false for non-existent ID")
	}
}
