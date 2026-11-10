// file: internal/books/store/migrations_runner.go
// version: 2.1.1
// guid: 6c1acba7-c650-40dc-abf4-87f9cc8d5b35
// last-edited: 2026-11-09

package store

import "time"

// Books' migrations run on core storage from release R1 (05-storage §5S.3.5):
// versions 1..LegacyCap are books' LegacyMigrations (booksLegacySteps,
// booksspec.go), each Up run by core over books' LegacyRange and then
// recorded in a batch of its own, as HEAD did (not atomic, §5S.3.5
// "Atomicity"), and later versions are books' Migrations (booksMigrations),
// each one batch with its bookkeeping. HEAD's runner (RunMigrations) is gone: core reads the
// applied version through books' VersionSource, which keeps HEAD's
// db_version and migration_<n> rows, so R0 still reads them.

// MigrationFunc represents a migration operation.
//
// # Idempotency contract (MANDATORY — enforced by TestMigrationUpFunctionsAreIdempotent)
//
// An Up function MUST be idempotent: running it a second time against a store it
// has already been applied to must succeed and must leave the database
// byte-identical. Core will not knowingly replay an Up — books' VersionSource
// counts a durable applied-migration record as applied (DB-02) — but a store
// restored to an older point, or a record lost with its step, can replay one,
// so a replay must be harmless.
//
// In practice this means: read the current state and skip rows that already have
// the new shape, rather than rewriting unconditionally. An unconditional rewrite
// fails the enforcement test even when the values are unchanged, because a fresh
// UpdatedAt is itself a mutation — and an unconditional rewrite is precisely the
// shape that makes a replayed migration destructive.
type MigrationFunc func(store migrationStore) error

// Migration represents a single database migration
type Migration struct {
	Version     int
	Description string
	Up          MigrationFunc
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
