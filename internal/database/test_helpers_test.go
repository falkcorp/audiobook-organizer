// file: internal/database/test_helpers_test.go
// version: 1.0.1
// guid: fa1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d
// last-edited: 2026-10-03

// NOTE(fable5 T022): setupTestDB previously created a SQLiteStore. It now
// creates a PebbleStore so all callers across the database test package
// continue to compile without changes to each individual test file.

package database

import "testing"

// setupTestDB creates a temporary PebbleStore for unit tests.
// It replaces the legacy SQLiteStore factory that was removed in fable5 T022.
func setupTestDB(t *testing.T) (Store, func()) {
	t.Helper()
	store, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("setupTestDB: NewPebbleStore: %v", err)
	}
	return store, func() { _ = store.Close() }
}
