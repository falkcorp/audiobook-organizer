// file: internal/database/migrations.go
// version: 1.41.0
// guid: 9a8b7c6d-5e4f-3d2c-1b0a-9f8e7d6c5b4a
// last-edited: 2026-07-07

package database

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// MigrationFunc represents a migration operation
type MigrationFunc func(store Store) error

// Migration represents a single database migration
type Migration struct {
	Version     int
	Description string
	Up          MigrationFunc
	Down        MigrationFunc // Optional rollback
}

// MigrationRecord tracks applied migrations
type MigrationRecord struct {
	Version     int       `json:"version"`
	Description string    `json:"description"`
	AppliedAt   time.Time `json:"applied_at"`
}

// DatabaseVersion stores the current schema version
type DatabaseVersion struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// migrations is the ordered list of all migrations
var migrations = []Migration{
	{
		Version:     1,
		Description: "Initial schema with authors, series, books, playlists",
		Up:          migration001Up,
		Down:        nil,
	},
	{
		Version:     2,
		Description: "Add import paths and operations tables",
		Up:          migration002Up,
		Down:        nil,
	},
	{
		Version:     3,
		Description: "Add user preferences",
		Up:          migration003Up,
		Down:        nil,
	},
	{
		Version:     4,
		Description: "Add extended Pebble keyspace (users, sessions, segments, playback)",
		Up:          migration004Up,
		Down:        nil,
	},
	{
		Version:     5,
		Description: "Add media info and version management fields to books",
		Up:          migration005Up,
		Down:        nil,
	},
	{
		Version:     6,
		Description: "Add original and organized file hash tracking",
		Up:          migration006Up,
		Down:        nil,
	},
	{
		Version:     7,
		Description: "Rename import paths to import paths",
		Up:          migration007Up,
		Down:        nil,
	},
	{
		Version:     8,
		Description: "Add do_not_import table for hash blocklist",
		Up:          migration008Up,
		Down:        nil,
	},
	{
		Version:     9,
		Description: "Add state machine and lifecycle fields to books",
		Up:          migration009Up,
		Down:        nil,
	},
	{
		Version:     10,
		Description: "Add metadata_states table for metadata provenance",
		Up:          migration010Up,
		Down:        nil,
	},
	{
		Version:     11,
		Description: "Add iTunes import metadata fields to books",
		Up:          migration011Up,
		Down:        nil,
	},
	{
		Version:     12,
		Description: "Add created_at and updated_at timestamps to books table",
		Up:          migration012Up,
		Down:        nil,
	},
	{
		Version:     13,
		Description: "Add wanted state support and multi-path tracking",
		Up:          migration013Up,
		Down:        nil,
	},
	{
		Version:     14,
		Description: "Flag books with corrupted organize paths for review",
		Up:          migration014Up,
		Down:        nil,
	},
	{
		Version:     15,
		Description: "Add book_authors junction table, cover_url, and narrators_json",
		Up:          migration015Up,
		Down:        nil,
	},
	{
		Version:     16,
		Description: "Add users, sessions, book_segments, playback tables",
		Up:          migration016Up,
		Down:        nil,
	},
	{
		Version:     17,
		Description: "Add composite indexes and FTS5 full-text search",
		Up:          migration017Up,
		Down:        nil,
	},
	{
		Version:     18,
		Description: "Add itunes_library_state table for change detection",
		Up:          migration018Up,
		Down:        nil,
	},
	{
		Version:     19,
		Description: "Add metadata_changes_history table for undo support",
		Up:          migration019Up,
		Down:        nil,
	},
	{
		Version:     20,
		Description: "Add narrators and book_narrators tables",
		Up:          migration020Up,
		Down:        nil,
	},
	{
		Version:     21,
		Description: "Add operation_summary_logs table for persistent operation history",
		Up:          migration021Up,
		Down:        nil,
	},
	{
		Version:     22,
		Description: "Backfill book_authors by splitting '&'-joined author names; backfill book_narrators from legacy narrator field",
		Up:          migration022Up,
		Down:        nil,
	},
	{
		Version:     23,
		Description: "Add metadata_updated_at and last_written_at timestamp columns to books",
		Up:          migration023Up,
		Down:        nil,
	},
	{
		Version:     24,
		Description: "Add metadata_review_status column to books",
		Up:          migration024Up,
		Down:        nil,
	},
	{
		Version:     25,
		Description: "Add asin column to books",
		Up:          migration025Up,
		Down:        nil,
	},
	{
		Version:     26,
		Description: "Create book_tombstones table",
		Up:          migration026Up,
		Down:        nil,
	},
	{
		Version:     27,
		Description: "Add result_data column to operations",
		Up:          migration027Up,
		Down:        nil,
	},
	{
		Version:     28,
		Description: "Add external provider ID columns (open_library_id, hardcover_id, google_books_id)",
		Up:          migration028Up,
		Down:        nil,
	},
	{
		Version:     29,
		Description: "Add operation_changes table for undo/rollback tracking",
		Up:          migration029Up,
		Down:        nil,
	},
	{
		Version:     30,
		Description: "Add file_hash column to book_segments for auto-relinking",
		Up:          migration030Up,
		Down:        nil,
	},
	{
		Version:     31,
		Description: "Add system_activity_log table and logs_pruned flag",
		Up:          migration031Up,
		Down:        nil,
	},
	{
		Version:     32,
		Description: "Add scan cache columns for incremental scanning",
		Up:          migration032Up,
		Down:        nil,
	},
	{
		Version:     33,
		Description: "Add deferred_itunes_updates table for transcode path changes",
		Up:          migration033Up,
		Down:        nil,
	},
	{
		Version:     34,
		Description: "Add external_id_map table for PID/ASIN mapping",
		Up:          migration034Up,
		Down:        nil,
	},
	{
		Version:     35,
		Description: "Add book_path_history table for file rename/move tracking",
		Up:          migration035Up,
		Down:        nil,
	},
	{
		Version:     36,
		Description: "Add genre column to books table",
		Up:          migration036Up,
		Down:        nil,
	},
	{
		Version:     37,
		Description: "Add book_tags table for user-defined tags",
		Up:          migration037Up,
		Down:        nil,
	},
	{
		Version:     38,
		Description: "Add itunes_path column to books table",
		Up:          migration038Up,
		Down:        nil,
	},
	{
		Version:     39,
		Description: "Create book_files table and migrate book_segments data",
		Up:          migration039Up,
		Down:        nil,
	},
	{
		Version:     40,
		Description: "Add last_organize_operation_id and last_organized_at columns to books",
		Up:          migration040Up,
		Down:        nil,
	},
	{
		Version:     41,
		Description: "Add itunes_sync_status column to books for dirty tracking",
		Up:          migration041Up,
		Down:        nil,
	},
	{
		Version:     42,
		Description: "Drop dead tables and add missing indexes",
		Up:          migration042Up,
		Down:        nil,
	},
	{
		Version:     43,
		Description: "Drop deprecated book_segments table",
		Up:          migration043Up,
		Down:        nil,
	},
	{
		Version:     44,
		Description: "Add PID provenance and removed_at to external_id_map",
		Up:          migration044Up,
		Down:        nil,
	},
	{
		Version:     45,
		Description: "Add operation_results table for structured operation output",
		Up:          migration045Up,
		Down:        nil,
	},
	{
		Version:     46,
		Description: "Add book_alternative_titles table for search + dedup variant matching",
		Up:          migration046Up,
		Down:        nil,
	},
	{
		Version:     47,
		Description: "Add source column to book_tags so system-applied tags can be distinguished from user tags",
		Up:          migration047Up,
		Down:        nil,
	},
	{
		Version:     48,
		Description: "Add author_tags and series_tags tables for author/series-level tagging",
		Up:          migration048Up,
		Down:        nil,
	},
	{
		Version:     49,
		Description: "Add acoustid_fingerprint and acoustid_duration columns to book_files for content-based matching",
		Up:          migration049Up,
		Down:        nil,
	},
	{
		Version:     50,
		Description: "Replace single acoustid_fingerprint with 7 segment columns",
		Up:          migration050Up,
		Down:        nil,
	},
	{
		Version:     51,
		Description: "Add quarantine_reason and quarantined_at columns to books",
		Up:          migration051Up,
		Down:        nil,
	},
	{
		Version:     52,
		Description: "Add ai_jobs and ai_job_payloads tables for unified batch tracking",
		Up:          migration052Up,
		Down:        nil,
	},
	{
		Version:     53,
		Description: "Add post_metadata_hash column to book_files for post-write SHA tracking",
		Up:          migration053Up,
		Down:        nil,
	},
	{
		Version:     54,
		Description: "Add metadata_rejections table for auditing rejected metadata candidates",
		Up:          migration054Up,
		Down:        nil,
	},
	{
		Version:     55,
		Description: "Add metadata_source_hash column to books for metadata-based deduplication (MATCH-1)",
		Up:          migration055Up,
		Down:        nil,
	},
	{
		Version:     56,
		Description: "Add merged_into_book_id column to books for chapter consolidation (MATCH-2)",
		Up:          migration056Up,
		Down:        nil,
	},
	{
		Version:     57,
		Description: "Add unique index on (file_hash, source_import_path) to prevent duplicate audiobook records",
		Up:          migration057Up,
		Down:        nil,
	},
	{
		Version:     58,
		Description: "Add book signature columns for unified per-book audio fingerprint",
		Up:          migration058Up,
		Down:        nil,
	},
	{
		Version:     59,
		Description: "Add Unified Operations System v2 core schema",
		Up:          migration059Up,
		Down:        migration059Down,
	},
	{
		Version:     60,
		Description: "Add partial book signature mask/coverage and file fingerprint diagnosis columns",
		Up:          migration060Up,
		Down:        nil,
	},
}

// RunMigrations applies all pending migrations
func RunMigrations(store Store) error {
	currentVersion, err := getCurrentVersion(store)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	slog.Info("current database version", "version", currentVersion)

	// Find migrations to apply
	pendingMigrations := []Migration{}
	for _, m := range migrations {
		if m.Version > currentVersion {
			pendingMigrations = append(pendingMigrations, m)
		}
	}

	if len(pendingMigrations) == 0 {
		slog.Info("database is up to date", "version", currentVersion)
		return nil
	}

	slog.Info("applying migrations", "count", len(pendingMigrations))

	// Apply each migration
	for _, m := range pendingMigrations {
		slog.Info("applying migration", "version", m.Version, "description", m.Description)

		if err := m.Up(store); err != nil {
			return fmt.Errorf("migration %d failed: %w", m.Version, err)
		}

		// Record migration
		if err := recordMigration(store, m); err != nil {
			return fmt.Errorf("failed to record migration %d: %w", m.Version, err)
		}

		// Update version
		if err := setVersion(store, m.Version); err != nil {
			return fmt.Errorf("failed to update version to %d: %w", m.Version, err)
		}

		slog.Info("migration completed", "version", m.Version)
	}

	slog.Info("all migrations completed", "version", pendingMigrations[len(pendingMigrations)-1].Version)

	return nil
}

// getCurrentVersion retrieves the current schema version
func getCurrentVersion(store Store) (int, error) {
	// Try to get version from preferences
	pref, err := store.GetUserPreference("db_version")
	if err != nil || pref == nil || pref.Value == nil {
		// No version found, assume version 0 (fresh database)
		return 0, nil
	}

	var version DatabaseVersion
	if err := json.Unmarshal([]byte(*pref.Value), &version); err != nil {
		return 0, fmt.Errorf("failed to parse version: %w", err)
	}

	return version.Version, nil
}

// setVersion updates the current schema version
func setVersion(store Store, version int) error {
	dbVersion := DatabaseVersion{
		Version:   version,
		UpdatedAt: time.Now(),
	}

	data, err := json.Marshal(dbVersion)
	if err != nil {
		return fmt.Errorf("failed to marshal version: %w", err)
	}

	return store.SetUserPreference("db_version", string(data))
}

// recordMigration stores a migration record
func recordMigration(store Store, m Migration) error {
	record := MigrationRecord{
		Version:     m.Version,
		Description: m.Description,
		AppliedAt:   time.Now(),
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal migration record: %w", err)
	}

	key := fmt.Sprintf("migration_%d", m.Version)
	return store.SetUserPreference(key, string(data))
}

// Migration implementations

// migration001Up initializes the basic schema
func migration001Up(store Store) error {
	// Basic schema is created automatically by store initialization
	// This migration just validates the structure exists
	slog.Info("- Validating basic schema (authors, series, books, playlists)")
	return nil
}

// migration002Up adds import paths and operations support
func migration002Up(store Store) error {
	// These structures are already supported by the current store interface
	slog.Info("- Adding import paths and operations support")
	return nil
}

// migration003Up adds user preferences support
func migration003Up(store Store) error {
	// User preferences already supported by current interface
	slog.Info("- Adding user preferences support")
	return nil
}

// migration004Up adds extended Pebble keyspace
func migration004Up(store Store) error {
	// Extended keyspace (users, sessions, segments, playback) already supported
	slog.Info("- Adding extended Pebble keyspace (users, sessions, segments, playback)")
	return nil
}

// migration005Up adds media info and version management fields to books table
func migration005Up(store Store) error {
	slog.Info("- Adding media info and version management fields to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration006Up adds original and organized file hash tracking columns
func migration006Up(store Store) error {
	slog.Info("- Adding original/organized file hash columns to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration007Up renames library folder entities to import paths across backends.
func migration007Up(store Store) error {
	slog.Info("- Renaming import paths to import paths")

	if s, ok := store.(*PebbleStore); ok {
		if err := s.migrateImportPathKeys(); err != nil {
			return fmt.Errorf("failed to migrate Pebble import path keys: %w", err)
		}
	}
	// SQLite branch removed (fable5 T022).
	return nil
}

func migration008Up(store Store) error {
	slog.Info("- Adding do_not_import table for hash blocklist")
	// SQLite branch removed (fable5 T022); Pebble keyspace available by default.
	slog.Info("- Pebble keyspace for do_not_import enabled")
	return nil
}

// migration009Up adds state machine and lifecycle tracking fields to books table
func migration009Up(store Store) error {
	slog.Info("- Adding state machine and lifecycle fields to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration010Up adds metadata_states table for persisted metadata provenance
func migration010Up(store Store) error {
	slog.Info("- Adding metadata_states table for metadata provenance")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration011Up adds iTunes import metadata fields to books table.
func migration011Up(store Store) error {
	slog.Info("- Adding iTunes import metadata fields to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration012Up adds created_at and updated_at timestamp columns to books table
func migration012Up(store Store) error {
	slog.Info("- Adding created_at and updated_at timestamp columns to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration013Up adds wanted state support and multi-path tracking
func migration013Up(store Store) error {
	slog.Info("- Adding wanted state support and multi-path tracking")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration014Up flags books with corrupted organize paths (unresolved
// placeholders like {series} or {author}) by setting library_state to
// 'needs_review'. This is a one-time cleanup for paths written before the
// leftover-placeholder guard was added to expandPattern.
func migration014Up(store Store) error {
	slog.Info("Running migration 14 Flag books with corrupted organize paths")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration014UpPebble handles the corrupted-path check for PebbleDB stores.
func migration014UpPebble(store Store) error {
	books, err := store.GetAllBooksCore(1000000, 0)
	if err != nil {
		return fmt.Errorf("migration 14: failed to list books: %w", err)
	}

	flagged := 0
	for _, core := range books {
		if !strings.Contains(core.FilePath, "{") {
			continue
		}
		// FilePath contains a literal brace — flag for review. Hydrate before
		// writeback: core is Core (slim); writing it straight through
		// UpdateBook would wipe the denormalized Author/Series on Pebble.
		book, hydrateErr := store.GetBookByID(core.ID)
		if hydrateErr != nil || book == nil {
			slog.Info("- Warning could not hydrate book", "value", core.ID, "path", core.FilePath, "error", hydrateErr)
			continue
		}
		state := "needs_review"
		book.LibraryState = &state
		if _, updateErr := store.UpdateBook(book.ID, book); updateErr != nil {
			slog.Info("- Warning could not flag book", "value", book.ID, "path", book.FilePath, "error", updateErr)
			continue
		}
		flagged++
	}
	slog.Info("- Flagged", "count", flagged)
	return nil
}

// migration015Up adds book_authors junction table, cover_url, and narrators_json
func migration015Up(store Store) error {
	slog.Info("- Adding book_authors junction table, cover_url, and narrators_json")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration016Up creates users, sessions, book_segments, and playback tracking tables
func migration016Up(store Store) error {
	slog.Info("- Adding users, sessions, book_segments, and playback tables")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration017Up adds composite indexes for common queries and FTS5 full-text search
func migration017Up(store Store) error {
	slog.Info("- Adding composite indexes and FTS5 full-text search")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

func migration018Up(store Store) error {
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

func migration019Up(store Store) error {
	slog.Info("- Adding metadata_changes_history table for undo support")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// GetMigrationHistory returns all applied migrations
func GetMigrationHistory(store Store) ([]MigrationRecord, error) {
	// Get all preferences that start with "migration_"
	allPrefs, err := store.GetAllUserPreferences()
	if err != nil {
		return nil, fmt.Errorf("failed to get preferences: %w", err)
	}

	records := []MigrationRecord{}
	for _, pref := range allPrefs {
		if len(pref.Key) > 10 && pref.Key[:10] == "migration_" {
			if pref.Value == nil {
				continue
			}
			var record MigrationRecord
			if err := json.Unmarshal([]byte(*pref.Value), &record); err != nil {
				slog.Warn("failed to parse migration record", "key", pref.Key, "error", err)
				continue
			}
			records = append(records, record)
		}
	}

	return records, nil
}

// migration020Up adds narrators and book_narrators tables
func migration020Up(store Store) error {
	slog.Info("- Adding narrators and book_narrators tables")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration021Up adds the operation_summary_logs table for persistent operation history
func migration021Up(store Store) error {
	slog.Info("- Adding operation_summary_logs table for persistent operation history")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration022Up backfills the book_authors and book_narrators junction tables
// for books that were imported before the multi-author "&" splitting feature.
//
// Authors: For each book_authors row whose referenced author name contains " & ",
// this migration splits the name, creates individual author records as needed,
// replaces the old junction row with one row per split name (role: "author" for
// position 0, "co-author" for subsequent positions).
//
// Narrators: For each book that has a non-empty books.narrator field but no rows
// in book_narrators, this migration splits on " & ", creates narrator records as
// needed, and inserts the junction rows.
//
// The migration is idempotent: it uses INSERT OR IGNORE and only touches rows
// where the author name actually contains " & ".
func migration022Up(store Store) error {
	slog.Info("- Running migration 22 backfill book_authors (&-split) and book_narrators")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration023Up adds metadata_updated_at and last_written_at timestamp columns to books table.
// metadata_updated_at is set only when user-visible metadata changes; last_written_at is set
// when metadata is written back to audio files on disk.
func migration023Up(store Store) error {
	slog.Info("- Adding metadata_updated_at and last_written_at to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration024Up adds metadata_review_status column to books table.
func migration024Up(store Store) error {
	slog.Info("- Adding metadata_review_status to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration025Up adds asin column to books table.
func migration025Up(store Store) error {
	slog.Info("- Column already exists, skipping")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration026Up creates book_tombstones table for safe deletion pattern.
func migration026Up(store Store) error {
	slog.Info("- book_tombstones table created successfully")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration027Up adds result_data column to operations table.
func migration027Up(store Store) error {
	slog.Info("- result_data column already exists")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration028Up adds external provider ID columns to books table.
func migration028Up(store Store) error {
	slog.Info("- Adding external provider ID columns (open_library_id, hardcover_id, google_books_id)")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

func migration029Up(store Store) error {
	slog.Info("- Creating operation_changes table for undo/rollback tracking")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

func migration030Up(store Store) error {
	slog.Info("- Adding file_hash column to book_segments for auto-relinking")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

func migration031Up(store Store) error {
	slog.Info("- Adding system_activity_log table and logs_pruned flag")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration032Up adds scan cache columns for incremental scanning
func migration032Up(store Store) error {
	slog.Info("- Adding scan cache columns for incremental scanning")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration033Up creates the deferred_itunes_updates table for storing
// transcode path changes that should be applied on the next iTunes sync.
func migration033Up(store Store) error {
	slog.Info("- Creating deferred_itunes_updates table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration034Up creates the external_id_map table for mapping external
// identifiers (iTunes PIDs, Audible ASINs, etc.) to book IDs.
func migration034Up(store Store) error {
	slog.Info("- Creating external_id_map table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration035Up creates the book_path_history table for tracking file
// rename/move operations over time.
func migration035Up(store Store) error {
	slog.Info("- Creating book_path_history table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration036Up adds the genre column to the books table.
func migration036Up(store Store) error {
	slog.Info("- Adding genre column to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration038Up adds itunes_path column to books table.
func migration038Up(store Store) error {
	slog.Info("- Adding itunes_path column to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration037Up creates the book_tags table for user-defined tags.
func migration037Up(store Store) error {
	slog.Info("- Creating book_tags table for user-defined tags")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration039Up creates the book_files table and migrates data from book_segments.
// book_files uses ULID string book_id (matching books.id directly), whereas
// book_segments used a legacy CRC32 numeric hash of the ULID as book_id.
func migration039Up(store Store) error {
	slog.Info("- Creating book_files table and migrating book_segments data")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration040Up adds last_organize_operation_id and last_organized_at columns to books.
func migration040Up(store Store) error {
	slog.Info("- Adding last_organize_operation_id and last_organized_at to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration041Up adds itunes_sync_status column to books for tracking whether
// each book's metadata has been written back to the iTunes library.
// Values: "synced" (up-to-date), "dirty" (changed), "unlinked" (no iTunes), nil (unknown).
func migration041Up(store Store) error {
	slog.Info("- Adding itunes_sync_status column to books table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration042Up drops dead tables and adds missing indexes for common query patterns.
func migration042Up(store Store) error {
	slog.Info("- Dropping dead tables and adding missing indexes")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration043Up drops the deprecated book_segments table.
// Data was migrated to book_files in migration 39. No production code reads this table.
func migration043Up(store Store) error {
	slog.Info("- Dropping deprecated book_segments table and adding remaining indexes")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration044Up adds PID lifecycle tracking to external_id_map.
// provenance: "itunes" (imported), "generated" (we created), "recycled" (reused)
// removed_at: timestamp when we sent a remove to the ITL; null while live
func migration044Up(store Store) error {
	slog.Info("- Added provenance and removed_at to external_id_map")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration045Up creates the operation_results table for structured per-book operation output.
func migration045Up(store Store) error {
	slog.Info("- Created operation_results table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration046Up creates the book_alternative_titles table. This powers
// two things:
//
//  1. Dedup Layer 1 exact-title matching can iterate every alt title on
//     both sides of a comparison, so manga romaji vs English, subtitle
//     reordering, or rebrands all collapse to the same normalized form.
//  2. Library search can index alt titles alongside the primary title,
//     so searching for either variant finds the book.
//
// Schema:
//
//	id         — autoincrement primary key
//	book_id    — books.id (cascade on delete)
//	title      — the alternate title string
//	source     — where the alt came from: 'user', 'metadata_fetch',
//	             'auto_ampersand', 'itunes_import', 'manga_romaji', etc.
//	language   — optional ISO-639 code for the language variant
//	created_at — audit trail
//
// UNIQUE(book_id, title) prevents dup rows for the same variant.
func migration046Up(store Store) error {
	slog.Info("- Created book_alternative_titles table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration047Up adds a `source` column to book_tags so system-
// applied tags (dedup:merge-survivor:llm-auto, metadata:source:*,
// ...) can be distinguished from user-applied tags. PebbleStore
// stores the same field in the serialized value; this migration
// keeps SQLite as a first-class store option in sync.
//
// Example sources and the tags they emit:
//
//	source='user'   — tag was added via the UI or API by a human
//	source='system' — tag was added automatically by the server:
//	    dedup:merge-survivor[:auto-hash|auto-isbn|llm-auto]
//	    dedup:duration-match
//	    dedup:duration-abridged
//	    metadata:source:{audible,hardcover,google_books,openlibrary,audnexus}
//	    metadata:language:{en,es,fr,...} (from applied metadata)
//	    import:scan (future), organize:applied (future), ...
//
// The column defaults to 'user' so every existing row stays valid
// without a data migration, and existing AddBookTag callers don't
// need to be touched — they just keep writing user-sourced tags.
//
// An index on source lets "tag:metadata:source:google_books AND
// source=system" filter cheaply for the metadata-upgrade workflow.
func migration047Up(store Store) error {
	slog.Info("- Added source column to book_tags")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration048Up adds parallel `author_tags` and `series_tags`
// tables so tagging works at every entity level, not just books.
// Motivating use cases:
//
//   - Mark an author as language-locked: tag with `language:en`,
//     then the metadata fetcher and dedup engine skip candidates
//     in other languages without inspecting every book.
//   - Mark a series as `completed` / `on-hold` / `dropped` for
//     read-list management.
//   - Future "policy" tags: `policy:english-only` on an author or
//     series binds a named settings bundle to every book that
//     inherits through the author/series hierarchy.
//   - System-applied provenance tags on authors/series after a
//     merge or metadata apply — same namespace as book_tags.
//
// Schema mirrors book_tags after migration 47: primary key on
// (entity_id, tag), `source` column defaulting to 'user', plus
// indexes on tag and source for reverse lookup and filtering.
func migration048Up(store Store) error {
	slog.Info("- Created author_tags and series_tags tables")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration049Up adds acoustid_fingerprint and acoustid_duration columns to
// book_files. These store AcoustID fingerprints (from fpcalc) for
// content-based matching that survives metadata rewrites and file moves.
func migration049Up(store Store) error {
	slog.Info("- Added acoustid_fingerprint, acoustid_duration to book_files")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration051Up adds quarantine_reason and quarantined_at columns to books.
func migration051Up(store Store) error {
	slog.Info("- Added quarantine_reason, quarantined_at to books")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration052Up creates the ai_jobs tracking table and ai_job_payloads
// blob table used by the internal/ai/aijobs package to route bulk LLM work
// through the OpenAI Batch API.
func migration052Up(store Store) error {
	slog.Info("- Created ai_jobs, ai_job_payloads")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration050Up replaces the single acoustid_fingerprint/acoustid_duration
// columns with 7 per-segment columns: acoustid_seg0 through acoustid_seg6.
// The old acoustid_fingerprint column is retained (SQLite cannot drop columns
// in older versions) but is no longer read or written by the application.
// PebbleDB is schema-free; the new segment fields live on the struct.
func migration050Up(store Store) error {
	slog.Info("- Added acoustid_seg0–acoustid_seg6 to book_files")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration053Up adds post_metadata_hash to book_files for recording the SHA-256
// of the file after a metadata tag write. This allows the pre-write identity
// (original_file_hash) to always be recoverable even after tags are modified.
func migration053Up(store Store) error {
	slog.Info("- Added post_metadata_hash to book_files")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration054Up creates the metadata_rejections table for auditing every
// candidate that was rejected for a book (user action, below-threshold score,
// duration mismatch, wrong language, or skipped in the UI).
func migration054Up(store Store) error {
	slog.Info("- Created metadata_rejections table")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration055Up adds metadata_source_hash to books for metadata-based dedup (MATCH-1).
// The value is sha256("{source}:{canonical_id}"), e.g. sha256("audible:B0XXXXXXXX").
// Identical hashes mean two book records were applied from the exact same external record
// and are almost certainly duplicates.
func migration055Up(store Store) error {
	slog.Info("- Added metadata_source_hash to books, created index idx_books_metadata_source_hash")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration056Up adds merged_into_book_id to books for chapter consolidation (MATCH-2).
// When MergeChapterBooks() absorbs a chapter file into a consolidated book, the
// source book row has is_primary_version set to 0 and merged_into_book_id set to
// the primary book's ID so the merge is auditable.
func migration056Up(store Store) error {
	slog.Info("- Added merged_into_book_id to books, created index idx_books_merged_into")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration057Up adds a unique index on (file_hash, source_import_path) to prevent
// duplicate audiobook records for the same physical file within each library context.
// The index is partial (WHERE file_hash IS NOT NULL) to avoid affecting existing rows
// with NULL or empty hashes during migration.
func migration057Up(store Store) error {
	slog.Info("- Added unique index on (file_hash, source_import_path)")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration058Up adds book signature columns for unified per-book audio fingerprint
// (spec: 2026-05-03-unified-book-fingerprint.md). These columns synthesize a
// deterministic book-level fingerprint from the per-file 7-segment chromaprints,
// enabling dedup matching across different file splits (e.g., 1 .m4b vs 30 .mp3s).
func migration058Up(store Store) error {
	slog.Info("- Added book_sig_v1, book_sig_segments, book_sig_built_at to books")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration059Up adds the UOS v2 core schema described in
// docs/superpowers/specs/2026-05-04-unified-operations-system.md §2.1.
func migration059Up(store Store) error {
	slog.Info("- Added UOS v2 core schema")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration059Down removes the UOS v2 core schema added by migration059Up.
func migration059Down(store Store) error {
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}

// migration060Up adds partial book-signature coverage columns to books and
// structured fingerprint-failure diagnosis columns to book_files.
func migration060Up(store Store) error {
	slog.Info("+ Added partial sig mask/coverage to books, diagnosis columns to book_files")
	// SQLite-only migration; no-op for PebbleStore.
	return nil
}
