// file: internal/books/store/migrations_atomicity_test.go
// version: 1.1.3
// guid: f60be44c-31da-4f41-84d4-11c26dcbe1c2
// last-edited: 2026-11-09

package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/mediacore/storage"
	"github.com/stretchr/testify/require"
)

// TestMigrationReplayDoesNotRerunRecordedUp is the regression test for DB-02,
// on core storage. HEAD's runner once wrote a migration's record and its
// version bump separately, and a crash between them left the record durable
// with the version behind, so the next boot re-ran the same Up. Books'
// VersionSource reads that state as applied. The store here is one HEAD left
// at the upgrade floor (LegacyCap) with step LegacyCap+1's record already
// durable, and a library: key that step would rename if it ran: the first
// core boot must count the step as applied, leave the key alone, and run the
// steps after it.
func TestMigrationReplayDoesNotRerunRecordedUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.pebble")
	store, err := NewPebbleStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	const stale = "library:path:/lib/The Killing Joke"
	rawSet(t, path, nil, stale, "gotham")
	rewindToR0(t, path, LegacyCap, LegacyCap+1)

	store, err = NewPebbleStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	db := AsPebbleStore(store).DB()
	_, closer, err := db.Get([]byte(stale))
	require.NoError(t, err, "step %d was recorded as applied; its Up must not run again", LegacyCap+1)
	require.NoError(t, closer.Close())
	v, err := getCurrentVersion(store)
	require.NoError(t, err)
	require.Equal(t, LatestMigrationVersion(), v, "the version must still advance past the recorded migration")
	for n := LegacyCap + 2; n <= LatestMigrationVersion(); n++ {
		rec, err := store.GetUserPreference(migrationRecordKey(n))
		require.NoError(t, err)
		require.NotNil(t, rec, "migration %d must be recorded", n)
	}
}

// TestMigrationBookkeepingIsRecordedAndVersioned covers the ordinary path: a
// step core storage runs leaves HEAD's two rows, the migration_<n> record and
// db_version, in HEAD's format (R0 reads them), with the fields core adds.
func TestMigrationBookkeepingIsRecordedAndVersioned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recorded.pebble")
	store, err := NewPebbleStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	rewindToR0(t, path, LegacyCap, LegacyCap)

	store, err = NewPebbleStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	v, err := getCurrentVersion(store)
	require.NoError(t, err)
	require.Equal(t, LatestMigrationVersion(), v)

	for _, n := range []int{LegacyCap + 1, LegacyCap + 2} {
		pref, err := store.GetUserPreference(migrationRecordKey(n))
		require.NoError(t, err)
		require.NotNil(t, pref, "migration %d", n)
		require.NotNil(t, pref.Value)
		var head MigrationRecord // HEAD's shape decodes the row
		require.NoError(t, json.Unmarshal([]byte(*pref.Value), &head))
		require.Equal(t, n, head.Version)
		require.NotEmpty(t, head.Description)
		require.False(t, head.AppliedAt.IsZero())
		var row migrationRecordRow
		require.NoError(t, json.Unmarshal([]byte(*pref.Value), &row))
		require.NotEmpty(t, row.Build, "migration %d records the binary that ran it", n)
		require.Equal(t, storage.Additive.String(), row.Compat, "migration %d", n)
	}
}

// TestImportPathKeyStepRenamesLegacyLibraryKeys: step LegacyCap+1 moves every
// library: key to its import_path: name and counter:library to
// counter:import_path, in the step's one batch, on the first core boot of a
// store at the upgrade floor; an existing import_path counter wins, and a
// second boot changes nothing.
func TestImportPathKeyStepRenamesLegacyLibraryKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "import-path.pebble")
	store, err := NewPebbleStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	rawSet(t, path, nil, "library:1", `{"id":1,"path":"/legacy","name":"Legacy","enabled":true,"book_count":0}`)
	rawSet(t, path, nil, "library:path:/legacy", "1")
	rawSet(t, path, nil, "counter:library", "2")
	rewindToR0(t, path, LegacyCap, LegacyCap)

	boot := func() map[string]string {
		store, err := NewPebbleStore(path)
		require.NoError(t, err)
		require.NoError(t, store.Close())
		db := rawOpen(t, path, nil)
		defer func() { require.NoError(t, db.Close()) }()
		out := map[string]string{}
		for _, key := range []string{"library:1", "library:path:/legacy", "counter:library", "import_path:1", "import_path:path:/legacy", "counter:import_path"} {
			val, closer, err := db.Get([]byte(key))
			if errors.Is(err, pebble.ErrNotFound) {
				continue
			}
			require.NoError(t, err)
			out[key] = string(val)
			require.NoError(t, closer.Close())
		}
		return out
	}
	got := boot()
	require.Equal(t, map[string]string{
		"import_path:1":            `{"id":1,"path":"/legacy","name":"Legacy","enabled":true,"book_count":0}`,
		"import_path:path:/legacy": "1",
		"counter:import_path":      "2",
	}, got, "the legacy keys and the counter move; nothing is left under library:")
	require.Equal(t, got, boot(), "a second boot changes nothing")
}
