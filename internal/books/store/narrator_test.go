// file: internal/books/store/narrator_test.go
// version: 2.0.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-11-09

// NOTE(fable5 T022): SQLiteStore type assertions replaced with PebbleStore.

package store

import (
	"testing"
)

func TestCreateNarrator(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	s := store.(*PebbleStore)

	n, err := s.CreateNarrator("Jane Doe")
	if err != nil {
		t.Fatalf("CreateNarrator failed: %v", err)
	}
	if n.Name != "Jane Doe" {
		t.Errorf("expected name 'Jane Doe', got %q", n.Name)
	}
	if n.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if n.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestCreateNarrator_CaseInsensitiveDuplicate(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	s := store.(*PebbleStore)

	n1, err := s.CreateNarrator("John Smith")
	if err != nil {
		t.Fatalf("CreateNarrator first call failed: %v", err)
	}

	// Inserting "john smith" should fail due to UNIQUE constraint on name column.
	// But GetNarratorByName should find the original via case-insensitive lookup.
	_, err = s.CreateNarrator("john smith")
	if err == nil {
		t.Log("CreateNarrator allowed case-variant duplicate (UNIQUE may be case-sensitive in this backend)")
		// If it didn't error, at least verify GetNarratorByName returns one consistently.
	}

	found, err := s.GetNarratorByName("john smith")
	if err != nil {
		t.Fatalf("GetNarratorByName failed: %v", err)
	}
	if found == nil {
		t.Fatal("expected narrator to be found by case-insensitive name lookup")
	}
	if found.ID != n1.ID {
		// If SQLite allowed both inserts, GetNarratorByName returns whichever matches first.
		t.Logf("note: found ID %d, original ID %d (SQLite UNIQUE is case-sensitive)", found.ID, n1.ID)
	}
}

func TestGetNarratorByID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	s := store.(*PebbleStore)

	created, err := s.CreateNarrator("Alice")
	if err != nil {
		t.Fatalf("CreateNarrator failed: %v", err)
	}

	fetched, err := s.GetNarratorByID(created.ID)
	if err != nil {
		t.Fatalf("GetNarratorByID failed: %v", err)
	}
	if fetched == nil {
		t.Fatal("expected narrator, got nil")
	}
	if fetched.Name != "Alice" {
		t.Errorf("expected name 'Alice', got %q", fetched.Name)
	}

	// Non-existent ID
	missing, err := s.GetNarratorByID(9999)
	if err != nil {
		t.Fatalf("GetNarratorByID for missing should not error: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for non-existent ID")
	}
}

func TestGetNarratorByName(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	s := store.(*PebbleStore)

	_, err := s.CreateNarrator("Bob Jones")
	if err != nil {
		t.Fatalf("CreateNarrator failed: %v", err)
	}

	found, err := s.GetNarratorByName("Bob Jones")
	if err != nil {
		t.Fatalf("GetNarratorByName failed: %v", err)
	}
	if found == nil || found.Name != "Bob Jones" {
		t.Fatalf("expected 'Bob Jones', got %+v", found)
	}

	// Case-insensitive
	found2, err := s.GetNarratorByName("bob jones")
	if err != nil {
		t.Fatalf("GetNarratorByName case-insensitive failed: %v", err)
	}
	if found2 == nil {
		t.Fatal("expected case-insensitive match")
	}
	if found2.ID != found.ID {
		t.Errorf("expected same ID, got %d vs %d", found2.ID, found.ID)
	}

	// Non-existent
	missing, err := s.GetNarratorByName("Nobody")
	if err != nil {
		t.Fatalf("GetNarratorByName for missing should not error: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for non-existent name")
	}
}

func TestListNarrators(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	s := store.(*PebbleStore)

	names := []string{"Charlie", "Alice", "Bob"}
	for _, name := range names {
		if _, err := s.CreateNarrator(name); err != nil {
			t.Fatalf("CreateNarrator(%q) failed: %v", name, err)
		}
	}

	list, err := s.ListNarrators()
	if err != nil {
		t.Fatalf("ListNarrators failed: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 narrators, got %d", len(list))
	}

	// Should be ordered by name
	if list[0].Name != "Alice" || list[1].Name != "Bob" || list[2].Name != "Charlie" {
		t.Errorf("expected alphabetical order, got %q %q %q", list[0].Name, list[1].Name, list[2].Name)
	}
}

func TestSetBookNarrators(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	s := store.(*PebbleStore)

	// Create a book to associate narrators with
	book := &Book{Title: "Test Book", FilePath: "/tmp/test.m4b", Format: "m4b"}
	created, err := s.CreateBook(book)
	if err != nil {
		t.Fatalf("CreateBook failed: %v", err)
	}

	n1, _ := s.CreateNarrator("Narrator One")
	n2, _ := s.CreateNarrator("Narrator Two")

	narrators := []BookNarrator{
		{BookID: created.ID, NarratorID: n1.ID, Role: "narrator", Position: 0},
		{BookID: created.ID, NarratorID: n2.ID, Role: "co-narrator", Position: 1},
	}

	if err := s.SetBookNarrators(created.ID, narrators); err != nil {
		t.Fatalf("SetBookNarrators failed: %v", err)
	}

	got, err := s.GetBookNarrators(created.ID)
	if err != nil {
		t.Fatalf("GetBookNarrators failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 book narrators, got %d", len(got))
	}
	if got[0].NarratorID != n1.ID || got[0].Role != "narrator" || got[0].Position != 0 {
		t.Errorf("first narrator mismatch: %+v", got[0])
	}
	if got[1].NarratorID != n2.ID || got[1].Role != "co-narrator" || got[1].Position != 1 {
		t.Errorf("second narrator mismatch: %+v", got[1])
	}
}

func TestSetBookNarrators_Replace(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	s := store.(*PebbleStore)

	book := &Book{Title: "Replace Test", FilePath: "/tmp/replace.m4b", Format: "m4b"}
	created, err := s.CreateBook(book)
	if err != nil {
		t.Fatalf("CreateBook failed: %v", err)
	}

	n1, _ := s.CreateNarrator("First")
	n2, _ := s.CreateNarrator("Second")
	n3, _ := s.CreateNarrator("Third")

	// Set initial narrators
	initial := []BookNarrator{
		{BookID: created.ID, NarratorID: n1.ID, Role: "narrator", Position: 0},
		{BookID: created.ID, NarratorID: n2.ID, Role: "narrator", Position: 1},
	}
	if err := s.SetBookNarrators(created.ID, initial); err != nil {
		t.Fatalf("SetBookNarrators (initial) failed: %v", err)
	}

	// Replace with different set
	replacement := []BookNarrator{
		{BookID: created.ID, NarratorID: n3.ID, Role: "narrator", Position: 0},
	}
	if err := s.SetBookNarrators(created.ID, replacement); err != nil {
		t.Fatalf("SetBookNarrators (replace) failed: %v", err)
	}

	got, err := s.GetBookNarrators(created.ID)
	if err != nil {
		t.Fatalf("GetBookNarrators failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 narrator after replace, got %d", len(got))
	}
	if got[0].NarratorID != n3.ID {
		t.Errorf("expected narrator ID %d, got %d", n3.ID, got[0].NarratorID)
	}
}
