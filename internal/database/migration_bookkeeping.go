// file: internal/database/migration_bookkeeping.go
// version: 2.0.0
// guid: c6c9d25e-d026-45d9-b093-54b39966e584
// last-edited: 2026-11-08

package database

import (
	"encoding/json"
	"fmt"
	"time"
)

// Migration bookkeeping (DB-02), in HEAD's row format.
//
// A completed migration is recorded in two preference rows: a per-version
// `migration_<n>` record and the `db_version` schema version. From release
// R1 books' VersionSource (booksBookkeeping, booksspec.go) writes both in one
// batch and reads them back for core storage, counting a record whose
// version bump was lost as applied, as HEAD's runner did. The row format is
// unchanged, so R0 reads an R1 store's bookkeeping.

// dbVersionPreferenceKey is the preference key holding the current schema version.
const dbVersionPreferenceKey = "db_version"

// migrationRecordKey is the preference key holding the applied-migration record
// for a given schema version.
func migrationRecordKey(version int) string {
	return fmt.Sprintf("migration_%d", version)
}

// databaseVersionPayload serialises the schema-version record.
func databaseVersionPayload(version int) (string, error) {
	data, err := json.Marshal(DatabaseVersion{
		Version:   version,
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal version: %w", err)
	}
	return string(data), nil
}
