// file: internal/database/migrations_extra_test.go
// version: 2.0.0
// guid: 67d3f1c5-8c24-4a3c-9a79-35fb6d68fdd9
// last-edited: 2026-06-10

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

// TestGetMigrationHistory tests the GetMigrationHistory function.
func TestGetMigrationHistory(t *testing.T) {
	t.Run("returns empty list when no migrations", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		records, err := GetMigrationHistory(store)
		if err != nil {
			t.Fatalf("GetMigrationHistory failed: %v", err)
		}
		if len(records) != 0 {
			t.Errorf("expected 0 migration records, got %d", len(records))
		}
	})

	t.Run("returns migration records", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Add some migration records as preferences
		migrations := []MigrationRecord{
			{
				Version:     1,
				Description: "initial_schema",
				AppliedAt:   time.Now().Add(-2 * time.Hour),
			},
			{
				Version:     2,
				Description: "add_tags_table",
				AppliedAt:   time.Now().Add(-1 * time.Hour),
			},
			{
				Version:     3,
				Description: "add_indexes",
				AppliedAt:   time.Now(),
			},
		}

		for _, migration := range migrations {
			data, err := json.Marshal(migration)
			if err != nil {
				t.Fatalf("failed to marshal migration: %v", err)
			}
			key := "migration_" + migration.Description
			if err := store.SetUserPreference(key, string(data)); err != nil {
				t.Fatalf("failed to set migration preference: %v", err)
			}
		}

		// Get migration history
		records, err := GetMigrationHistory(store)
		if err != nil {
			t.Fatalf("GetMigrationHistory failed: %v", err)
		}

		if len(records) != len(migrations) {
			t.Errorf("expected %d migration records, got %d", len(migrations), len(records))
		}

		// Verify records contain expected data
		foundVersions := make(map[int]bool)
		for _, record := range records {
			foundVersions[record.Version] = true
			if record.Description == "" {
				t.Error("migration record has empty description")
			}
			if record.AppliedAt.IsZero() {
				t.Error("migration record has zero AppliedAt time")
			}
		}

		// Check that all expected versions were found
		for _, migration := range migrations {
			if !foundVersions[migration.Version] {
				t.Errorf("migration version %d not found in history", migration.Version)
			}
		}
	})

	t.Run("skips invalid preference values", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Add a migration with invalid JSON
		if err := store.SetUserPreference("migration_invalid", "not-json"); err != nil {
			t.Fatalf("failed to set invalid preference: %v", err)
		}

		// Add a migration with nil value
		if err := store.SetUserPreference("migration_nil", ""); err != nil {
			t.Fatalf("failed to set nil preference: %v", err)
		}

		// Should not error, just skip invalid entries
		records, err := GetMigrationHistory(store)
		if err != nil {
			t.Fatalf("GetMigrationHistory failed: %v", err)
		}

		// Should be 0 since we only added invalid entries
		if len(records) != 0 {
			t.Errorf("expected 0 records with only invalid migrations, got %d", len(records))
		}
	})

	t.Run("ignores non-migration preferences", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()

		// Add some non-migration preferences
		if err := store.SetUserPreference("some_other_pref", "value"); err != nil {
			t.Fatalf("failed to set non-migration preference: %v", err)
		}
		if err := store.SetUserPreference("another_pref", "value2"); err != nil {
			t.Fatalf("failed to set another preference: %v", err)
		}

		// Add one valid migration
		migration := MigrationRecord{
			Version:     99,
			Description: "test_migration",
			AppliedAt:   time.Now(),
		}
		data, err := json.Marshal(migration)
		if err != nil {
			t.Fatalf("failed to marshal migration: %v", err)
		}
		if err := store.SetUserPreference("migration_test", string(data)); err != nil {
			t.Fatalf("failed to set migration preference: %v", err)
		}

		// Should only return migration preferences
		records, err := GetMigrationHistory(store)
		if err != nil {
			t.Fatalf("GetMigrationHistory failed: %v", err)
		}

		if len(records) != 1 {
			t.Errorf("expected 1 migration record, got %d", len(records))
		}

		if len(records) > 0 && records[0].Version != 99 {
			t.Errorf("expected migration version 99, got %d", records[0].Version)
		}
	})
}
