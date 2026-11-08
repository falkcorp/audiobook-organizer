// file: internal/database/r0store_test.go
// version: 1.0.0
// guid: 0b6e4d2a-7c91-4f35-8a1e-5d9c3f7b2e64
// last-edited: 2026-11-08

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// Stores as HEAD (R0) leaves them, for the tests of release R1's first boot
// on core storage: the legacy migrations a production store has not run yet,
// the first-boot checkpoint, and the store R0 must still open.

// getCurrentVersion reads books' db_version row (HEAD's schema version); 0
// when it is absent, an error when it does not decode.
func getCurrentVersion(store migrationStore) (int, error) {
	pref, err := store.GetUserPreference(dbVersionPreferenceKey)
	if err != nil || pref == nil || pref.Value == nil {
		return 0, err
	}
	var version DatabaseVersion
	if err := json.Unmarshal([]byte(*pref.Value), &version); err != nil {
		return 0, fmt.Errorf("parse version: %w", err)
	}
	return version.Version, nil
}

// rewindToR0 turns the closed store at path back into the store HEAD left at
// schema version `version`: every core key (c/ and m/) and core sidecar
// (.storage-levels, .migration-checkpoint) removed, books' applied-migration
// records above recordsThrough deleted and db_version set to version, keeping
// its row ID. recordsThrough above version is a record whose version bump was
// lost (DB-02). Books' own rows and the .storage-format sidecar, which HEAD
// writes too, stay as they are. The next core open is that store's first
// boot on core storage.
func rewindToR0(t *testing.T, path string, version, recordsThrough int) {
	t.Helper()
	require.GreaterOrEqual(t, recordsThrough, version)
	db := rawOpen(t, path, nil)
	defer func() { require.NoError(t, db.Close()) }()

	b := db.NewBatch()
	require.NoError(t, b.DeleteRange([]byte("c/"), []byte("c0"), nil))
	require.NoError(t, b.DeleteRange([]byte("m/"), []byte("m0"), nil))

	it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte(migrationRecordPrefix), UpperBound: []byte("preference:migration`")})
	require.NoError(t, err)
	for ok := it.First(); ok; ok = it.Next() {
		n, err := strconv.Atoi(strings.TrimPrefix(string(it.Key()), migrationRecordPrefix))
		require.NoError(t, err, "record key %q", it.Key())
		if n > recordsThrough {
			require.NoError(t, b.Delete(append([]byte(nil), it.Key()...), nil))
		}
	}
	require.NoError(t, it.Close())

	key := []byte("preference:" + dbVersionPreferenceKey)
	raw, closer, err := db.Get(key)
	require.NoError(t, err, "a store a core build opened has a db_version row")
	var pref UserPreference
	require.NoError(t, json.Unmarshal(raw, &pref))
	require.NoError(t, closer.Close())
	payload, err := databaseVersionPayload(version)
	require.NoError(t, err)
	pref.Value = &payload
	row, err := json.Marshal(pref)
	require.NoError(t, err)
	require.NoError(t, b.Set(key, row, nil))
	require.NoError(t, b.Commit(pebble.Sync))

	for _, suffix := range []string{".storage-levels", StorageMigrationCheckpointSuffix} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

// coreKeysAt lists the c/ keys of the closed store at path.
func coreKeysAt(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, k := range rawKeysAt(t, path, nil) {
		if strings.HasPrefix(k, "c/") {
			out = append(out, k)
		}
	}
	return out
}
