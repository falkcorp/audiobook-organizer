// file: internal/database/migration061_test.go
// version: 1.0.0
// guid: 13221161-da29-4cdf-b781-6e6506da9552
// last-edited: 2026-09-11

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// seedPlaceholderAndCleanBooks creates one author, one series, one book whose
// organize path still holds an unresolved {series} placeholder, and one clean
// book. Both books are linked to the author and series so the test can prove
// the denormalized fields survive the repair.
func seedPlaceholderAndCleanBooks(t *testing.T, store Store) (corruptedID, cleanID string) {
	t.Helper()

	author, err := store.CreateAuthor("DB-03 Placeholder Author")
	require.NoError(t, err)
	series, err := store.CreateSeries("DB-03 Placeholder Series", &author.ID)
	require.NoError(t, err)

	corrupted, err := store.CreateBook(&Book{
		Title:    "DB-03 Corrupted",
		FilePath: "/lib/db03/{series}/DB-03 Corrupted",
		AuthorID: &author.ID,
		SeriesID: &series.ID,
	})
	require.NoError(t, err)

	clean, err := store.CreateBook(&Book{
		Title:    "DB-03 Clean",
		FilePath: "/lib/db03/DB-03 Placeholder Series/DB-03 Clean",
		AuthorID: &author.ID,
		SeriesID: &series.ID,
	})
	require.NoError(t, err)

	return corrupted.ID, clean.ID
}

// TestMigration061FlagsOnlyPlaceholderPaths runs the repair body directly: the
// book with a literal '{' in its path must land in needs_review, the clean book
// must be untouched, and hydrating through GetBookByID before UpdateBook must
// keep the author/series linkage the slim BookCore projection would have
// dropped.
func TestMigration061FlagsOnlyPlaceholderPaths(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	corruptedID, cleanID := seedPlaceholderAndCleanBooks(t, store)

	before, err := store.GetBookByID(corruptedID)
	require.NoError(t, err)
	require.NotNil(t, before)
	require.Nil(t, before.LibraryState, "seed must start unflagged")

	require.NoError(t, migration061Up(store))

	after, err := store.GetBookByID(corruptedID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.NotNil(t, after.LibraryState, "book with an unresolved placeholder must be flagged")
	require.Equal(t, corruptedOrganizePathState, *after.LibraryState)

	// The denormalized/linked fields must survive the writeback.
	require.Equal(t, before.AuthorID, after.AuthorID, "AuthorID must survive the repair")
	require.Equal(t, before.SeriesID, after.SeriesID, "SeriesID must survive the repair")
	require.Equal(t, before.Author, after.Author, "denormalized Author must survive the repair")
	require.Equal(t, before.Series, after.Series, "denormalized Series must survive the repair")
	require.Equal(t, before.FilePath, after.FilePath, "the path itself is left for review, not rewritten")

	cleanBook, err := store.GetBookByID(cleanID)
	require.NoError(t, err)
	require.NotNil(t, cleanBook)
	require.Nil(t, cleanBook.LibraryState, "a clean path must not be flagged")
}

// TestMigration061RunsThroughRunnerFromProductionVersion reconstructs the
// production bookkeeping state — schema version 60, every migration up to and
// including 14 recorded as applied — and proves RunMigrations reaches the
// repair. This is the case the original wiring would have failed: a body
// placed inside migration014Up is never invoked once the stored version is
// past 14, which is why the repair carries version 61.
func TestMigration061RunsThroughRunnerFromProductionVersion(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	corruptedID, cleanID := seedPlaceholderAndCleanBooks(t, store)

	// Production state before this deploy: 14 (and everything through 60)
	// already recorded, version counter at 60.
	for _, m := range migrations {
		if m.Version <= 60 {
			require.NoError(t, recordMigration(store, m))
		}
	}
	require.NoError(t, setVersion(store, 60))

	require.NoError(t, RunMigrations(store))

	got, err := getCurrentVersion(store)
	require.NoError(t, err)
	require.Equal(t, 61, got, "the runner must advance to the repair migration")

	rec, err := store.GetUserPreference(migrationRecordKey(61))
	require.NoError(t, err)
	require.NotNil(t, rec, "migration 61 must be recorded as applied")

	corrupted, err := store.GetBookByID(corruptedID)
	require.NoError(t, err)
	require.NotNil(t, corrupted.LibraryState, "the runner must have executed the repair")
	require.Equal(t, corruptedOrganizePathState, *corrupted.LibraryState)

	cleanBook, err := store.GetBookByID(cleanID)
	require.NoError(t, err)
	require.Nil(t, cleanBook.LibraryState)
}

// TestMigration014StaysNoOp pins the design decision: migration 14 is recorded
// as applied on every existing database, so its body must remain inert rather
// than carry the repair.
func TestMigration014StaysNoOp(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	corruptedID, _ := seedPlaceholderAndCleanBooks(t, store)

	require.NoError(t, migration014Up(store))

	book, err := store.GetBookByID(corruptedID)
	require.NoError(t, err)
	require.Nil(t, book.LibraryState, "migration 14 is the historical no-op; the repair lives in 61")
}

// TestMigration061IsIdempotentOnFlaggedBooks proves a second run performs no
// write: the flagged book keeps its UpdatedAt from the first pass.
func TestMigration061IsIdempotentOnFlaggedBooks(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	corruptedID, _ := seedPlaceholderAndCleanBooks(t, store)

	require.NoError(t, migration061Up(store))
	first, err := store.GetBookByID(corruptedID)
	require.NoError(t, err)
	require.NotNil(t, first.UpdatedAt)

	require.NoError(t, migration061Up(store))
	second, err := store.GetBookByID(corruptedID)
	require.NoError(t, err)
	require.Equal(t, *first.UpdatedAt, *second.UpdatedAt, "an already-flagged book must not be rewritten")
}
