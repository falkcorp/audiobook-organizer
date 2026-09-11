// file: internal/database/migrations_atomicity_test.go
// version: 1.1.0
// guid: f60be44c-31da-4f41-84d4-11c26dcbe1c2
// last-edited: 2026-09-11

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// swapMigrationRegistry replaces the package-level migration registry for the
// duration of one test and restores it afterwards. Safe because no test in this
// package that touches the registry calls t.Parallel: Go resumes parallel tests
// only after every sequential top-level test has finished.
func swapMigrationRegistry(t *testing.T, replacement []Migration) {
	t.Helper()
	original := migrations
	migrations = replacement
	t.Cleanup(func() { migrations = original })
}

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

// TestMigrationReplayDoesNotRerunRecordedUp is the regression test for DB-02.
//
// RunMigrations used to perform three unbatched writes per migration: the
// migration's own Up effect, the migration record, and the schema-version bump.
// A crash after the record write but before the version write left the database
// reporting the pre-migration version, so the next boot re-ran the SAME Up. This
// test reconstructs exactly that persisted state — record present, version not
// yet advanced — and asserts the runner recognises the migration as applied and
// only advances the version.
func TestMigrationReplayDoesNotRerunRecordedUp(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	upCalls := 0
	testMigration := Migration{
		Version:     9001,
		Description: "DB-02 replay-safety probe",
		Up: func(migrationStore) error {
			upCalls++
			return nil
		},
	}
	swapMigrationRegistry(t, []Migration{testMigration})

	// Persisted state left behind by a crash between the two bookkeeping
	// writes: version still 9000, but the record for 9001 is already durable.
	require.NoError(t, setVersion(store, 9000))
	require.NoError(t, recordMigration(store, testMigration))

	require.NoError(t, RunMigrations(store))

	require.Equal(t, 0, upCalls,
		"migration 9001 was already recorded as applied; its Up must not run again")

	got, err := getCurrentVersion(store)
	require.NoError(t, err)
	require.Equal(t, 9001, got, "the version must still be advanced past the recorded migration")
}

// TestMigrationBookkeepingIsRecordedAndVersioned covers the ordinary path: an
// unrecorded migration runs, and both bookkeeping keys land.
func TestMigrationBookkeepingIsRecordedAndVersioned(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	upCalls := 0
	testMigration := Migration{
		Version:     9002,
		Description: "DB-02 normal-path probe",
		Up: func(migrationStore) error {
			upCalls++
			return nil
		},
	}
	swapMigrationRegistry(t, []Migration{testMigration})

	require.NoError(t, setVersion(store, 9001))
	require.NoError(t, RunMigrations(store))

	require.Equal(t, 1, upCalls, "an unrecorded migration must run exactly once")

	got, err := getCurrentVersion(store)
	require.NoError(t, err)
	require.Equal(t, 9002, got)

	rec, err := store.GetUserPreference("migration_9002")
	require.NoError(t, err)
	require.NotNil(t, rec, "the migration record must be written alongside the version")
}

// TestMigrationBookkeepingUsesTheAtomicPath proves the store RunMigrations
// actually receives in production takes the batched branch of
// commitMigrationBookkeeping, not the two-write fallback. Without this the fix
// could be inert in prod and still look green.
func TestMigrationBookkeepingUsesTheAtomicPath(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ps := AsPebbleStore(store)
	require.NotNil(t, ps, "InitializeStore hands RunMigrations a *PebbleStore")

	var batcher migrationBookkeepingWriter = ps
	require.NoError(t, batcher.setPreferencesAtomic([]preferenceWrite{
		{Key: "db02_atomic_a", Value: "a"},
		{Key: "db02_atomic_b", Value: "b"},
	}))

	for key, want := range map[string]string{"db02_atomic_a": "a", "db02_atomic_b": "b"} {
		pref, err := store.GetUserPreference(key)
		require.NoError(t, err)
		require.NotNil(t, pref, "key %s", key)
		require.NotNil(t, pref.Value, "key %s", key)
		require.Equal(t, want, *pref.Value, "key %s", key)
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
