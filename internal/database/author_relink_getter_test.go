// file: internal/database/author_relink_getter_test.go
// version: 1.0.0
// guid: 3f6a0d92-7c14-4b8e-9e51-b2d7c84a16f3
// last-edited: 2026-09-12

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetBooksByAuthorIDForRelinkCore_MemDBAndPebbleAgree is the conformance
// gate for the relink getter. It must return the trashed book -- the one
// GetBooksByAuthorIDWithRoleCore deliberately omits -- from BOTH bodies.
//
// A relink list without the trash is how a merge destroyed trashed books'
// credits: DeleteAuthor's junction sweep removes the author from every
// book_authors row, trashed books included, and the relink never moved them.
func TestGetBooksByAuthorIDForRelinkCore_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildAuthorGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	got := authorGetterIDs(t, p, func() ([]BookCore, error) {
		return store.GetBooksByAuthorIDForRelinkCore(fx.authorID)
	})

	for _, useMemDB := range []bool{true, false} {
		ids := got[useMemDB]
		require.Contains(t, ids, fx.legacyBookID, "useMemDB=%v: legacy AuthorID link missing", useMemDB)
		require.Contains(t, ids, fx.coAuthorBookID, "useMemDB=%v: junction-only co-author link missing", useMemDB)
		require.Contains(t, ids, fx.nonPrimaryBookID, "useMemDB=%v: non-primary version link missing", useMemDB)
		require.Contains(t, ids, fx.softDeletedBookID,
			"useMemDB=%v: trashed book missing from the relink list -- DeleteAuthor's sweep would erase its credit", useMemDB)
		require.NotContains(t, ids, fx.unrelatedBookID, "useMemDB=%v: unrelated book returned", useMemDB)
		require.Len(t, ids, 4, "useMemDB=%v", useMemDB)
	}
	require.Equal(t, got[true], got[false],
		"memdb and Pebble implementations of GetBooksByAuthorIDForRelinkCore returned different sets")

	// The listing getter must NOT have been widened by the shared body.
	listed := authorGetterIDs(t, p, func() ([]BookCore, error) {
		return store.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	})
	for _, useMemDB := range []bool{true, false} {
		require.NotContains(t, listed[useMemDB], fx.softDeletedBookID,
			"useMemDB=%v: GetBooksByAuthorIDWithRoleCore started returning the trash", useMemDB)
	}
}

// TestGetAllAuthorBookRefBuckets_ClassifiesLiveTrashedDangling pins the three
// buckets under both bodies, and that they sum to the flat count every existing
// guard reads.
func TestGetAllAuthorBookRefBuckets_ClassifiesLiveTrashedDangling(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	const author = 7101

	mkAuthorRefBook(t, store, "relink-live", author, true, false)
	mkAuthorRefBook(t, store, "relink-live-nonprimary", author, false, false)
	mkAuthorRefBook(t, store, "relink-trashed", author, true, true)
	// A junction row whose book row does not exist: nothing can relink it and
	// DeleteAuthor's sweep removes it.
	require.NoError(t, store.SetBookAuthors("relink-ghost-book", []BookAuthor{
		{BookID: "relink-ghost-book", AuthorID: author, Role: "author", Position: 0},
	}))
	// Junction + legacy on the same book is ONE reference, not two.
	both := mkAuthorRefBook(t, store, "relink-both", author, true, false)
	require.NoError(t, store.SetBookAuthors(both.ID, []BookAuthor{
		{BookID: both.ID, AuthorID: author, Role: "author", Position: 0},
	}))

	want := AuthorRefBuckets{Live: 3, Trashed: 1, Dangling: 1}
	require.True(t, store.IsMemReady())
	for _, useMemDB := range []bool{true, false} {
		store.UseMemDB = useMemDB
		buckets, err := store.GetAllAuthorBookRefBuckets()
		require.NoError(t, err)
		require.Equal(t, want, buckets[author], "useMemDB=%v", useMemDB)

		flat, err := store.GetAllAuthorBookRefCounts()
		require.NoError(t, err)
		require.Equal(t, want.Total(), flat[author], "useMemDB=%v: flat count must equal the bucket sum", useMemDB)
	}
	store.UseMemDB = true

	viaHelper, err := AuthorRefBucketCounts(store)
	require.NoError(t, err)
	require.Equal(t, want, viaHelper[author])
	require.Equal(t, 4, want.Resolvable())
}

// TestAuthorRefBucketCounts_FailsClosed: a store without the capability is an
// error, never an empty map that would let every merge through.
func TestAuthorRefBucketCounts_FailsClosed(t *testing.T) {
	_, err := AuthorRefBucketCounts(struct{}{})
	require.Error(t, err)
}

// TestVerifyAuthorUnlinked_RefusesWhileATrashedBookStillCredits is the shared
// pre-delete gate: a trashed book still crediting the author must block the
// delete, and an author nothing credits must pass.
func TestVerifyAuthorUnlinked_RefusesWhileATrashedBookStillCredits(t *testing.T) {
	store := seedAuthorRefStore(t, t.TempDir())
	const linked, unlinked = 7201, 7202

	mkAuthorRefBook(t, store, "verify-trashed", linked, true, true)

	err := VerifyAuthorUnlinked(store, linked)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAuthorStillLinked), "want ErrAuthorStillLinked, got %v", err)

	require.NoError(t, VerifyAuthorUnlinked(store, unlinked))
}
