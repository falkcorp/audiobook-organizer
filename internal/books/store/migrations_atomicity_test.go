// file: internal/books/store/migrations_atomicity_test.go
// version: 1.1.3
// guid: f60be44c-31da-4f41-84d4-11c26dcbe1c2
// last-edited: 2026-11-09

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/mediacore/storage"
	"github.com/stretchr/testify/require"
)

// pebbleDigest returns a byte-exact fingerprint of the entire Pebble keyspace
// plus the number of live keys. Two digests are equal if and only if nothing in
// the database changed, down to the serialized timestamps inside each value.
func pebbleDigest(t *testing.T, store Store) (string, int) {
	t.Helper()
	ps := AsPebbleStore(store)
	require.NotNil(t, ps, "test requires a PebbleStore")

	iter, err := ps.db.NewIter(&pebble.IterOptions{})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	h := sha256.New()
	keys := 0
	for iter.First(); iter.Valid(); iter.Next() {
		h.Write(iter.Key())
		h.Write([]byte{0})
		h.Write(iter.Value())
		h.Write([]byte{0})
		keys++
	}
	require.NoError(t, iter.Error())
	return hex.EncodeToString(h.Sum(nil)), keys
}

// TestMigrationReplayDoesNotRerunRecordedUp is the regression test for DB-02,
// on core storage. HEAD's runner once wrote a migration's record and its
// version bump separately, and a crash between them left the record durable
// with the version behind, so the next boot re-ran the same Up. Books'
// VersionSource reads that state as applied. The store here is one HEAD left
// at version 60 with migration 61's record already durable, and a book whose
// placeholder path migration 61 would flag if it ran: the first core boot
// must count 61 as applied, leave the book alone, and run 62 onwards.
func TestMigrationReplayDoesNotRerunRecordedUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.pebble")
	store, err := NewPebbleStore(path)
	require.NoError(t, err)
	joker, err := store.CreateBook(&Book{Title: "The Killing Joke", FilePath: "/lib/{series}/The Killing Joke"})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	rewindToR0(t, path, 60, 61)

	store, err = NewPebbleStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	got, err := store.GetBookByID(joker.ID)
	require.NoError(t, err)
	require.Nil(t, got.LibraryState, "migration 61 was recorded as applied; its Up must not run again")
	v, err := getCurrentVersion(store)
	require.NoError(t, err)
	require.Equal(t, LatestMigrationVersion(), v, "the version must still advance past the recorded migration")
	for n := 62; n <= LatestMigrationVersion(); n++ {
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
	rewindToR0(t, path, LegacyCap-1, LegacyCap-1)

	store, err = NewPebbleStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	v, err := getCurrentVersion(store)
	require.NoError(t, err)
	require.Equal(t, LatestMigrationVersion(), v)

	for _, n := range []int{LegacyCap, LegacyCap + 1} {
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
		if n > LegacyCap {
			require.Equal(t, storage.Additive.String(), row.Compat, "migration %d", n)
		}
	}
}

// TestMigrationUpFunctionsAreIdempotent enforces the idempotency contract
// documented on MigrationFunc. Every registered migration's Up runs twice
// against the same seeded store; the second pass must not error and must not
// change a single byte of the keyspace.
//
// The store is seeded with real rows first: several registered Up functions
// (migration061Up, migration007Up) iterate books, so an empty store would
// never enter their bodies and the test would prove nothing about them.
//
// The comparison is byte-exact. A future migration that rewrites a row with a
// fresh UpdatedAt on every run will fail here even though the logical content is
// unchanged — that is deliberate. Such a migration must be made write-only-when-
// changed, because an unconditional rewrite is exactly what makes a replayed
// migration destructive.
func TestMigrationUpFunctionsAreIdempotent(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	author, err := store.CreateAuthor("DB-02 Idempotency Author")
	require.NoError(t, err)

	for _, seed := range []struct{ title, path string }{
		{"DB-02 Idempotency One", "/lib/db02/one"},
		{"DB-02 Idempotency Two", "/lib/db02/two"},
		{"DB-02 Idempotency Brace", "/lib/db02/{three}"},
	} {
		_, err := store.CreateBook(&Book{Title: seed.title, FilePath: seed.path, AuthorID: &author.ID})
		require.NoError(t, err, "seeding %s", seed.title)
	}

	for _, m := range migrations {
		if m.Up == nil {
			continue
		}
		require.NoError(t, m.Up(store), "migration %d first pass", m.Version)
	}
	firstDigest, firstKeys := pebbleDigest(t, store)

	for _, m := range migrations {
		if m.Up == nil {
			continue
		}
		require.NoError(t, m.Up(store), "migration %d second pass", m.Version)
	}
	secondDigest, secondKeys := pebbleDigest(t, store)

	require.Equal(t, firstKeys, secondKeys,
		"a second pass over every registered Up changed the live key count")
	require.Equal(t, firstDigest, secondDigest,
		"a second pass over every registered Up changed the keyspace; see the MigrationFunc idempotency contract in migrations.go")
}
