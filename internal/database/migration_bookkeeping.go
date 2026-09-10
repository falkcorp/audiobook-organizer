// file: internal/database/migration_bookkeeping.go
// version: 1.0.0
// guid: c6c9d25e-d026-45d9-b093-54b39966e584
// last-edited: 2026-09-10

package database

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// Migration bookkeeping (DB-02).
//
// RunMigrations records a completed migration in two places: a per-version
// `migration_<n>` preference and the `db_version` preference. Written as two
// independent Set calls, a crash between them leaves the record durable while
// the version still reports the pre-migration number, so the next boot re-runs
// the same Up. This file folds both writes into a single atomic Pebble batch,
// and migrations.go treats an existing record as proof the Up already completed.

// dbVersionPreferenceKey is the preference key holding the current schema version.
const dbVersionPreferenceKey = "db_version"

// preferenceWrite is one key/value pair destined for the preference keyspace.
type preferenceWrite struct {
	Key   string
	Value string
}

// migrationBookkeepingWriter is an optional capability. A store that implements
// it can commit the migration record and the version bump as one durable write;
// a store that does not falls back to two ordered writes, which the replay check
// in RunMigrations then covers.
type migrationBookkeepingWriter interface {
	setPreferencesAtomic(writes []preferenceWrite) error
}

// PebbleStore is the store InitializeStore hands RunMigrations in production,
// so this assertion is what guarantees the atomic path is the live one rather
// than a branch only tests reach.
var _ migrationBookkeepingWriter = (*PebbleStore)(nil)

// setPreferencesAtomic writes several preference keys in one Pebble batch, so
// either every key lands or none does.
//
// It mirrors SetUserPreference's read-modify-write semantics: an existing
// preference keeps its ID and gets a new value and timestamp, a new one is
// allocated an ID via nextID. Note that nextID commits its counter with its own
// p.db.Set outside this batch — a crash mid-batch can therefore burn a
// preference ID, which is harmless (IDs are opaque and monotonic), but it means
// "atomic" here covers the preference rows themselves, not the ID counter.
func (p *PebbleStore) setPreferencesAtomic(writes []preferenceWrite) error {
	batch := p.db.NewBatch()
	defer func() { _ = batch.Close() }()

	for _, w := range writes {
		existing, err := p.GetUserPreference(w.Key)
		if err != nil {
			return fmt.Errorf("read preference %q: %w", w.Key, err)
		}

		value := w.Value
		var pref *UserPreference
		if existing != nil {
			pref = existing
			pref.Value = &value
			pref.UpdatedAt = time.Now()
		} else {
			id, err := p.nextID("preference")
			if err != nil {
				return fmt.Errorf("allocate preference id for %q: %w", w.Key, err)
			}
			pref = &UserPreference{
				ID:        id,
				Key:       w.Key,
				Value:     &value,
				UpdatedAt: time.Now(),
			}
		}

		data, err := json.Marshal(pref)
		if err != nil {
			return fmt.Errorf("marshal preference %q: %w", w.Key, err)
		}
		if err := batch.Set([]byte(fmt.Sprintf("preference:%s", w.Key)), data, nil); err != nil {
			return fmt.Errorf("stage preference %q: %w", w.Key, err)
		}
	}

	return batch.Commit(pebble.Sync)
}

// migrationRecordKey is the preference key holding the applied-migration record
// for a given schema version.
func migrationRecordKey(version int) string {
	return fmt.Sprintf("migration_%d", version)
}

// migrationRecordPayload serialises the applied-migration record for m.
func migrationRecordPayload(m Migration) (string, error) {
	data, err := json.Marshal(MigrationRecord{
		Version:     m.Version,
		Description: m.Description,
		AppliedAt:   time.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal migration record: %w", err)
	}
	return string(data), nil
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

// migrationAlreadyRecorded reports whether the applied-migration record for this
// version is already durable. It is the signal that a previous boot ran this
// migration's Up to completion, even if the version bump that should have
// followed never made it to disk.
func migrationAlreadyRecorded(store migrationStore, version int) (bool, error) {
	pref, err := store.GetUserPreference(migrationRecordKey(version))
	if err != nil {
		return false, err
	}
	return pref != nil && pref.Value != nil, nil
}

// commitMigrationBookkeeping durably marks migration m as applied: it writes the
// applied-migration record and advances the schema version.
//
// On a store that can batch (every production store — InitializeStore hands
// RunMigrations a *PebbleStore) both keys land in one atomic write, so the crash
// window this function exists to close does not exist at all.
//
// The fallback path writes the record FIRST and the version SECOND, and that
// order is load-bearing: RunMigrations treats a record without a version bump as
// "already applied, do not re-run", so a crash between the two writes degrades
// to a skipped Up rather than a replayed one. Reversing these two calls
// reintroduces DB-02.
func commitMigrationBookkeeping(store migrationStore, m Migration) error {
	record, err := migrationRecordPayload(m)
	if err != nil {
		return err
	}
	version, err := databaseVersionPayload(m.Version)
	if err != nil {
		return err
	}

	writes := []preferenceWrite{
		{Key: migrationRecordKey(m.Version), Value: record},
		{Key: dbVersionPreferenceKey, Value: version},
	}

	// Prefer the atomic path. AsPebbleStore unwraps a decorated store, which a
	// plain type assertion on store would miss.
	var batcher migrationBookkeepingWriter
	if ps := AsPebbleStore(store); ps != nil {
		batcher = ps
	} else if b, ok := store.(migrationBookkeepingWriter); ok {
		batcher = b
	}
	if batcher != nil {
		if err := batcher.setPreferencesAtomic(writes); err != nil {
			return fmt.Errorf("failed to commit migration %d bookkeeping: %w", m.Version, err)
		}
		return nil
	}

	for _, w := range writes {
		if err := store.SetUserPreference(w.Key, w.Value); err != nil {
			return fmt.Errorf("failed to write %q for migration %d: %w", w.Key, m.Version, err)
		}
	}
	return nil
}
