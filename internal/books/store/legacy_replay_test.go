// file: internal/books/store/legacy_replay_test.go
// version: 1.0.1
// guid: 3c9e5a71-4d2b-4f86-b1e0-8a7d6c2f5b49
// last-edited: 2026-11-09

package store

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Books' HEAD-style steps 1..LegacyCap on core storage (05-storage §5S.3.5,
// R2-4, DATA2-2, DATA4-19): the legacy steps a production store below
// LegacyCap has not run yet run first, on books' own claims, and replaying
// them changes no data row.

// legacySeededStore is a store at path with library rows the legacy steps
// read (a book with a placeholder path migration 61 flags, an author, a
// series), closed and rewound to HEAD at version `at`.
func legacySeededStore(t *testing.T, path string, at int) {
	t.Helper()
	store, err := NewPebbleStore(path)
	require.NoError(t, err)
	author, err := store.CreateAuthor("Jeph Loeb")
	require.NoError(t, err)
	for _, b := range []struct{ title, path string }{
		{"Hush", "/lib/hush/Hush.m4b"},
		{"The Long Halloween", "/lib/{series}/The Long Halloween.m4b"},
		{"Dark Victory", "/lib/dark-victory/Dark Victory.m4b"},
	} {
		_, err := store.CreateBook(&Book{Title: b.title, FilePath: b.path, AuthorID: &author.ID})
		require.NoError(t, err)
	}
	require.NoError(t, store.Close())
	rewindToR0(t, path, at, at)
}

// bootKVs opens the store at path with this build's opener (the first core
// boot after a rewind), closes it, and returns its raw rows.
func bootKVs(t *testing.T, path string) map[string]string {
	t.Helper()
	store, err := NewPebbleStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	db := rawOpen(t, path, nil)
	defer func() { require.NoError(t, db.Close()) }()
	it, err := db.NewIter(nil)
	require.NoError(t, err)
	out := map[string]string{}
	for ok := it.First(); ok; ok = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	require.NoError(t, it.Error())
	require.NoError(t, it.Close())
	return out
}

// dataRows drops the bookkeeping a replay rewrites by design: core's rows
// (c/, m/: a new install marker and ledgers), books' migration records and
// db_version (new applied_at and updated_at), and the preference counter the
// re-staged records take their IDs from.
func dataRows(kvs map[string]string) map[string]string {
	out := maps.Clone(kvs)
	for k := range out {
		if strings.HasPrefix(k, "c/") || strings.HasPrefix(k, "m/") ||
			strings.HasPrefix(k, migrationRecordPrefix) || k == "preference:"+dbVersionPreferenceKey || k == "counter:preference" {
			delete(out, k)
		}
	}
	return out
}

// TestLegacyMigrationsReplayIdempotent (05-storage §5S.10, DATA4-19): books'
// legacy steps 59..LegacyCap run by the first core boot of a store HEAD left
// at 58, then again on the same store rewound to 58, leave every data row
// byte-identical: a replay after a lost version bump (DB-02) or a restore
// changes nothing it already changed.
func TestLegacyMigrationsReplayIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.pebble")
	legacySeededStore(t, path, 58)
	first := dataRows(bootKVs(t, path))
	rewindToR0(t, path, 58, 58)
	second := dataRows(bootKVs(t, path))
	require.Equal(t, first, second, "replaying books' legacy steps 59..LegacyCap changed data rows")
}

// TestLegacySteps59to66RunFirst (05-storage §5S.10, R2-4, DATA2-2): the first
// core boot of a store HEAD left at books v58 succeeds and runs steps
// 59..LegacyCap first, on books' own claims (strict claims refuse anything
// else), each recorded in HEAD's format with the binary that ran it, and all
// of them before books' first core-era step. Core's own steps wait for the
// last legacy step by the step graph's edge (core/storage
// TestLegacyStepsRunBeforeCoreSteps).
func TestLegacySteps59to66RunFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v58.pebble")
	legacySeededStore(t, path, 58)
	store, err := NewPebbleStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	v, err := getCurrentVersion(store)
	require.NoError(t, err)
	require.Equal(t, LatestMigrationVersion(), v)

	applied := map[int]time.Time{}
	for n := 59; n <= LatestMigrationVersion(); n++ {
		pref, err := store.GetUserPreference(migrationRecordKey(n))
		require.NoError(t, err)
		require.NotNil(t, pref, "migration %d has no record", n)
		require.NotNil(t, pref.Value)
		var row migrationRecordRow
		require.NoError(t, json.Unmarshal([]byte(*pref.Value), &row))
		require.Equal(t, n, row.Version)
		require.NotEmpty(t, row.Build, "migration %d does not record the binary that ran it", n)
		if n <= LegacyCap {
			require.Empty(t, row.Compat, "legacy step %d recorded a compat HEAD never wrote", n)
		}
		applied[n] = row.AppliedAt
	}
	for n := 59; n <= LegacyCap; n++ {
		for m := LegacyCap + 1; m <= LatestMigrationVersion(); m++ {
			require.False(t, applied[n].After(applied[m]), "legacy step %d (%s) ran after step %d (%s)", n, applied[n], m, applied[m])
		}
	}
}
