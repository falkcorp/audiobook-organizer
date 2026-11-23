// file: internal/books/store/migrations_runner.go
// version: 2.1.1
// guid: 6c1acba7-c650-40dc-abf4-87f9cc8d5b35
// last-edited: 2026-11-09

package store

import "time"

// Books' migrations run on core storage from release R1 (05-storage §5S.3.5):
// versions above LegacyCap are books' Migrations (booksMigrations), each one
// batch with its bookkeeping; versions 1..LegacyCap are gone (R3 deleted the
// HEAD-style steps behind books' upgrade floor). HEAD's runner (RunMigrations)
// is gone too: core reads the applied version through books' VersionSource,
// which keeps HEAD's db_version and migration_<n> rows, so R0 still reads
// them.

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
