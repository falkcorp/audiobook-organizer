// file: internal/database/author_getter_conformance_test.go
// version: 1.0.0
// guid: 5b3e9f47-2a81-4c06-b9d3-7e14a8c02f65
// last-edited: 2026-08-14

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// authorGetterConformanceFixture builds a library that separates the four ways
// a book can be attached to an author, because the two getters under test
// disagreed on two of them.
//
// Helper name is task-unique on purpose: several suites in this package define
// fixture builders and a generic name collides on rebase.
type authorGetterConformanceFixture struct {
	authorID int

	legacyBookID      string // linked via Book.AuthorID only (position 0)
	coAuthorBookID    string // linked via the book_authors junction only
	nonPrimaryBookID  string // junction link on a NON-primary version
	softDeletedBookID string // junction link, but the book is in the trash
	unrelatedBookID   string // no link at all
}

func buildAuthorGetterConformanceFixture(t *testing.T, store Store) authorGetterConformanceFixture {
	t.Helper()

	var fx authorGetterConformanceFixture

	author, err := store.CreateAuthor("Conformance Coauthor")
	require.NoError(t, err)
	fx.authorID = author.ID

	// Someone else, so the junction rows we write are realistic credit lists
	// rather than a single-author degenerate case.
	primary, err := store.CreateAuthor("Conformance Primary")
	require.NoError(t, err)

	mk := func(title string, opts func(*Book)) *Book {
		b := &Book{Title: title, FilePath: "/lib/conf/" + title}
		if opts != nil {
			opts(b)
		}
		created, cErr := store.CreateBook(b)
		require.NoError(t, cErr)
		return created
	}

	yes := true
	no := false

	// 1. Legacy attachment: the author IS the book's denormalized primary.
	legacy := mk("Legacy Primary Link", func(b *Book) {
		id := fx.authorID
		b.AuthorID = &id
		b.IsPrimaryVersion = &yes
	})
	fx.legacyBookID = legacy.ID

	// 2. Co-author: the author exists ONLY as a junction row at position 1.
	//    This is how authors 2..n of a credit list are stored — Book.AuthorID
	//    holds the first author and nothing else.
	coAuthor := mk("Coauthor Junction Link", func(b *Book) {
		id := primary.ID
		b.AuthorID = &id
		b.IsPrimaryVersion = &yes
	})
	require.NoError(t, store.SetBookAuthors(coAuthor.ID, []BookAuthor{
		{BookID: coAuthor.ID, AuthorID: primary.ID, Role: "author", Position: 0},
		{BookID: coAuthor.ID, AuthorID: fx.authorID, Role: "author", Position: 1},
	}))
	fx.coAuthorBookID = coAuthor.ID

	// 3. The row that exposes the bug: a co-author credit on a NON-primary
	//    version. memdb drops it, Pebble keeps it.
	nonPrimary := mk("Coauthor On NonPrimary Version", func(b *Book) {
		id := primary.ID
		b.AuthorID = &id
		b.IsPrimaryVersion = &no
	})
	require.NoError(t, store.SetBookAuthors(nonPrimary.ID, []BookAuthor{
		{BookID: nonPrimary.ID, AuthorID: primary.ID, Role: "author", Position: 0},
		{BookID: nonPrimary.ID, AuthorID: fx.authorID, Role: "author", Position: 1},
	}))
	fx.nonPrimaryBookID = nonPrimary.ID

	// 4. Trash: linked, but soft-deleted through the real update path so the
	//    memdb re-index runs. Neither getter may return it.
	trashed := mk("Coauthor But Trashed", func(b *Book) {
		id := primary.ID
		b.AuthorID = &id
		b.IsPrimaryVersion = &yes
	})
	require.NoError(t, store.SetBookAuthors(trashed.ID, []BookAuthor{
		{BookID: trashed.ID, AuthorID: fx.authorID, Role: "author", Position: 1},
	}))
	trashed.MarkedForDeletion = &yes
	_, err = store.UpdateBook(trashed.ID, trashed)
	require.NoError(t, err)
	fx.softDeletedBookID = trashed.ID

	// 5. Control: an implementation that returned everything would pass every
	//    "contains" assertion below without this.
	fx.unrelatedBookID = mk("Unrelated Book", func(b *Book) {
		id := primary.ID
		b.AuthorID = &id
		b.IsPrimaryVersion = &yes
	}).ID

	return fx
}

// authorGetterIDs runs one getter under both backing implementations and
// returns the ID set each produced, keyed by UseMemDB.
//
// The flag flip is the whole point: both GetBooksByAuthorIDCore and
// GetBooksByAuthorIDWithRoleCore gate on `p.UseMemDB && p.mem() != nil`, so
// flipping UseMemDB genuinely selects two different bodies of code. (This was
// NOT true of ListBooksByITunesPID before 2026-08-14 — it dispatched on memdb
// publication alone, which would have run the memdb path twice and asserted
// memdb == memdb. A conformance test is worth exactly as much as the selector
// that picks between the implementations, so this is checked, not assumed.)
func authorGetterIDs(
	t *testing.T,
	p *PebbleStore,
	get func() ([]BookCore, error),
) map[bool]map[string]struct{} {
	t.Helper()

	out := map[bool]map[string]struct{}{}
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		books, err := get()
		require.NoError(t, err)
		ids := make(map[string]struct{}, len(books))
		for _, b := range books {
			ids[b.ID] = struct{}{}
		}
		out[useMemDB] = ids
	}
	p.UseMemDB = true
	return out
}

// TestGetBooksByAuthorIDWithRoleCore_MemDBAndPebbleAgree is the conformance gate
// for the getter every merge and delete path depends on.
//
// GetBooksByAuthorIDWithRoleCore is what a caller uses to find the books it must
// relink BEFORE deleting an author — author_conjunction_repair.go:290,
// entities_ops.go:89, ai_author_reassign.go:33, maintenance/author.go:169. Its
// two implementations disagreed: the Pebble junction scan returned co-author
// credits on non-primary versions, the memdb walk silently dropped them
// (memdb_reads.go, the IsPrimaryVersion filter).
//
// That is not a reporting discrepancy. A merge that cannot see a link deletes
// the author anyway and ORPHANS the junction row. Measured on prod 2026-08-14:
// the same repair op reported 86 books relinked 4 s after a restart (Pebble
// path) and 84 warm (memdb path), and the two missing links belonged to author
// 46627 — a co-author on non-primary versions.
//
// The contract asserted here is COMPLETENESS: for this getter a missed link is
// data loss, while an extra one is at worst redundant work.
func TestGetBooksByAuthorIDWithRoleCore_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildAuthorGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	got := authorGetterIDs(t, p, func() ([]BookCore, error) {
		return store.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	})

	for _, useMemDB := range []bool{true, false} {
		ids := got[useMemDB]

		require.Contains(t, ids, fx.legacyBookID,
			"book linked via the legacy Book.AuthorID field is missing")
		require.Contains(t, ids, fx.coAuthorBookID,
			"co-author linked via the book_authors junction is missing — authors 2..n of a "+
				"credit list exist ONLY as junction rows")
		require.Contains(t, ids, fx.nonPrimaryBookID,
			"co-author credit on a NON-primary version is missing — a merge that cannot see "+
				"this link deletes the author and orphans the junction row")

		require.NotContains(t, ids, fx.softDeletedBookID,
			"soft-deleted book leaked into the author's book list")
		require.NotContains(t, ids, fx.unrelatedBookID,
			"book with no link to this author was returned")
		require.Len(t, ids, 3)
	}

	require.Equal(t, got[true], got[false],
		"memdb and Pebble implementations of GetBooksByAuthorIDWithRoleCore returned "+
			"different book sets — this is what made the same repair op report 86 books cold "+
			"and 84 warm against identical data")
}

// TestGetBooksByAuthorIDCore_MemDBAndPebbleAgree is the conformance gate for the
// listing getter.
//
// This one diverged in the OPPOSITE direction from the WithRole variant above,
// which is why the pair stayed plausible for so long: the Pebble path scanned
// only the legacy Book.AuthorID field and never opened the junction at all, so
// it under-reported co-authors, while memdb under-reported non-primary versions.
// Two errors pointing opposite ways keep aggregate counts believable and only a
// specific row — a co-author on a non-primary version — exposes either.
//
// The contract asserted here is the PRIMARY-VERSION VIEW: junction + legacy,
// soft-deleted excluded, non-primary excluded. That is what memdb already
// returned, and memdb is what prod serves outside the ~132 s warmup window, so
// this pins today's steady-state listing behaviour rather than changing it.
func TestGetBooksByAuthorIDCore_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildAuthorGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	got := authorGetterIDs(t, p, func() ([]BookCore, error) {
		return store.GetBooksByAuthorIDCore(fx.authorID)
	})

	for _, useMemDB := range []bool{true, false} {
		ids := got[useMemDB]

		require.Contains(t, ids, fx.legacyBookID,
			"book linked via the legacy Book.AuthorID field is missing")
		require.Contains(t, ids, fx.coAuthorBookID,
			"co-author linked via the book_authors junction is missing — the Pebble path "+
				"never opened the junction table, so a co-author's books never appeared in "+
				"listings served during warmup")

		require.NotContains(t, ids, fx.nonPrimaryBookID,
			"non-primary version leaked into the listing view")
		require.NotContains(t, ids, fx.softDeletedBookID,
			"soft-deleted book leaked into the author's book list")
		require.NotContains(t, ids, fx.unrelatedBookID,
			"book with no link to this author was returned")
		require.Len(t, ids, 2)
	}

	require.Equal(t, got[true], got[false],
		"memdb and Pebble implementations of GetBooksByAuthorIDCore returned different book sets")
}

// TestAuthorGetters_WithRoleIsASupersetOfCore states the relationship between
// the two getters directly, so that a future change which quietly re-narrows
// WithRole fails here even if both of its implementations are changed together
// and stay self-consistent.
//
// The conformance tests above hold each getter's two implementations to each
// other; nothing in them would notice if BOTH implementations of WithRole
// started dropping non-primary versions. This does.
func TestAuthorGetters_WithRoleIsASupersetOfCore(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildAuthorGetterConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "memdb must be published")

	withRole, err := store.GetBooksByAuthorIDWithRoleCore(fx.authorID)
	require.NoError(t, err)
	core, err := store.GetBooksByAuthorIDCore(fx.authorID)
	require.NoError(t, err)

	inWithRole := make(map[string]struct{}, len(withRole))
	for _, b := range withRole {
		inWithRole[b.ID] = struct{}{}
	}
	for _, b := range core {
		require.Contains(t, inWithRole, b.ID,
			"a book visible to the listing getter was invisible to the getter that merges "+
				"and deletes consult — that is the shape that orphans junction rows")
	}

	// Non-vacuity: the two must actually differ, or "superset" is trivially
	// true and this test would keep passing if both collapsed to the same set.
	require.Greater(t, len(withRole), len(core),
		"fixture must contain a book visible only to WithRole (the non-primary co-author "+
			"credit) or this assertion is vacuous")
}
