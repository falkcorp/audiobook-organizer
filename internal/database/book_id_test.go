// file: internal/database/book_id_test.go
// version: 1.0.0
// guid: 8b2e6d91-4c3a-4f7e-a1d5-6e9c0b3f2a47
// last-edited: 2026-09-13

package database

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBookID(t *testing.T) {
	cases := []struct {
		id      string
		wantErr bool
	}{
		{"", false}, // empty means "mint a ULID"
		{"01J9ZK3Q7X8Y9Z0A1B2C3D4E5F", false},
		{"b1", false},
		{"_leading-underscore", false},
		{"a:b", true},
		{":", true},
		{"trailing:", true},
		{"book:my:id", true},
	}
	for _, tc := range cases {
		err := ValidateBookID(tc.id)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateBookID(%q) err = %v, wantErr %v", tc.id, err, tc.wantErr)
		}
		if err != nil && !errors.Is(err, ErrInvalidBookID) {
			t.Errorf("ValidateBookID(%q) err = %v, want errors.Is ErrInvalidBookID", tc.id, err)
		}
	}
}

// TestCreateBook_RejectsColonInCallerSuppliedID proves the guard sits on the
// real write path: the create fails with ErrInvalidBookID and neither the
// colon-bearing key nor the key its first segment would alias is written.
func TestCreateBook_RejectsColonInCallerSuppliedID(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	book := &Book{ID: "my:id", Title: "Colon", FilePath: "/tmp/colon", Format: "m4b"}
	created, err := store.CreateBook(book)
	if !errors.Is(err, ErrInvalidBookID) {
		t.Fatalf("CreateBook err = %v, want ErrInvalidBookID", err)
	}
	if created != nil {
		t.Errorf("CreateBook returned %+v on error, want nil", created)
	}
	if !strings.Contains(err.Error(), `"my:id"`) {
		t.Errorf("error %q does not name the offending id", err)
	}
	if book.CreatedAt != nil {
		t.Errorf("rejected book was mutated (CreatedAt set)")
	}
	for _, id := range []string{"my:id", "my"} {
		got, gerr := store.GetBookByID(id)
		if gerr != nil {
			t.Fatalf("GetBookByID(%q): %v", id, gerr)
		}
		if got != nil {
			t.Errorf("GetBookByID(%q) = %+v, want no row after rejected create", id, got)
		}
	}
}

// TestCreateBook_ColonGuardLeavesValidIDsAlone is the positive control: a
// minted ID and a colon-free caller-supplied ID still create normally.
func TestCreateBook_ColonGuardLeavesValidIDsAlone(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	minted, err := store.CreateBook(&Book{Title: "Minted", FilePath: "/tmp/minted", Format: "m4b"})
	if err != nil {
		t.Fatalf("CreateBook (minted): %v", err)
	}
	if minted.ID == "" || strings.Contains(minted.ID, ":") {
		t.Errorf("minted ID = %q, want a non-empty colon-free ULID", minted.ID)
	}

	supplied, err := store.CreateBook(&Book{ID: "caller-id", Title: "Supplied", FilePath: "/tmp/supplied", Format: "m4b"})
	if err != nil {
		t.Fatalf("CreateBook (caller id): %v", err)
	}
	got, err := store.GetBookByID(supplied.ID)
	if err != nil || got == nil || got.ID != "caller-id" {
		t.Fatalf("GetBookByID(caller-id) = %+v, %v; want the created row", got, err)
	}
}
