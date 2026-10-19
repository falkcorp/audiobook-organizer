// file: internal/database/migrations_extra_test.go
// version: 2.0.1
// guid: 67d3f1c5-8c24-4a3c-9a79-35fb6d68fdd9
// last-edited: 2026-10-19

// NOTE(fable5 T022): Removed TestMigration007UpWithLegacyTable,
// TestMigration022_BackfillMultipleAuthorsNarrators,
// TestMigration022_SingleAuthorUntouched, and TestMigration022_Idempotent —
// all used direct sqliteStore.db.Exec SQL DDL. migration007Up and
// migration022Up are now no-ops on PebbleStore.

package database

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGetCurrentVersionWithPreference(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	version := DatabaseVersion{Version: 3, UpdatedAt: time.Now()}
	data, err := json.Marshal(version)
	if err != nil {
		t.Fatalf("marshal version failed: %v", err)
	}
	if err := store.SetUserPreference("db_version", string(data)); err != nil {
		t.Fatalf("SetUserPreference failed: %v", err)
	}
	got, err := getCurrentVersion(store)
	if err != nil {
		t.Fatalf("getCurrentVersion failed: %v", err)
	}
	if got != 3 {
		t.Fatalf("expected version 3, got %d", got)
	}
}

func TestGetCurrentVersionInvalidPreference(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	if err := store.SetUserPreference("db_version", "not-json"); err != nil {
		t.Fatalf("SetUserPreference failed: %v", err)
	}
	if _, err := getCurrentVersion(store); err == nil {
		t.Fatal("expected getCurrentVersion to fail on invalid JSON")
	}
}
