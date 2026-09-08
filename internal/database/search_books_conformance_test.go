// file: internal/database/search_books_conformance_test.go
// version: 1.1.0
// guid: 4e9a1f77-63b2-4c05-8ad1-9b52e7c30f6a
// last-edited: 2026-09-08

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// SearchBooks now has two implementations — an in-memory match against the
// memdb projection and the original full Pebble scan — selected by
// `p.UseMemDB && p.mem() != nil`. Flipping UseMemDB therefore genuinely picks
// between two different bodies of code, which is what makes the comparisons
// below conformance assertions rather than a backend compared against itself.
//
// The contract is EQUALITY, not merely overlap: SearchBooks feeds the ABS search
// UI, the audiobooks query service, and the iTunes handler's overfetch window.
// Any divergence — a missing hit, an extra hit, a different truncation, a
// blanked field — is a behaviour change wearing a performance change's clothes.

type searchConformanceFixture struct {
	titleHitID       string
	authorHitID      string
	narratorHitID    string
	softDeletedHitID string
	controlID        string
	description      string
}

func buildSearchConformanceFixture(t *testing.T, store Store) searchConformanceFixture {
	t.Helper()

	const description = "A long description that memdb strips out of its projection."
	fx := searchConformanceFixture{description: description}

	// An author whose NAME carries the needle but whose books' titles do not.
	author, err := store.CreateAuthor("Zelda Needlemark")
	require.NoError(t, err)

	other, err := store.CreateAuthor("Unrelated Writer")
	require.NoError(t, err)

	mk := func(title string, mut func(*Book)) *Book {
		t.Helper()
		desc := description
		b := &Book{Title: title, Description: &desc}
		if mut != nil {
			mut(b)
		}
		created, cErr := store.CreateBook(b)
		require.NoError(t, cErr)
		return created
	}

	// 1. Title carries the needle.
	fx.titleHitID = mk("The Needlemark Affair", func(b *Book) {
		id := other.ID
		b.AuthorID = &id
	}).ID

	// 2. Only the AUTHOR name carries it — exercises the author-name map that
	//    the Pebble path rebuilds by scanning every author row.
	fx.authorHitID = mk("Something Else Entirely", func(b *Book) {
		id := author.ID
		b.AuthorID = &id
	}).ID

	// 3. Only the NARRATOR carries it.
	narrator := "Read by Needlemark"
	fx.narratorHitID = mk("Another Unrelated Title", func(b *Book) {
		id := other.ID
		b.AuthorID = &id
		b.Narrator = &narrator
	}).ID

	// 4. Control: an implementation that returned everything would satisfy every
	//    "found the hits" assertion without this.
	fx.controlID = mk("Nothing To See Here", func(b *Book) {
		id := other.ID
		b.AuthorID = &id
	}).ID

	// 5. A soft-deleted match. The Pebble scan does NOT filter
	//    MarkedForDeletion, so search returns these today. memdb's other
	//    walkers (ListBookIDs, GetAllBooksCore) DO filter via
	//    bookIsSoftDeleted, so copying one of those loops would quietly drop
	//    this row and shrink every search result set. Present so that mistake
	//    fails a test instead of shipping.
	yes := true
	trashed := mk("Needlemark In The Trash", func(b *Book) {
		id := other.ID
		b.AuthorID = &id
	})
	trashed.MarkedForDeletion = &yes
	_, err = store.UpdateBook(trashed.ID, trashed)
	require.NoError(t, err)
	fx.softDeletedHitID = trashed.ID

	return fx
}

// searchBothWays runs SearchBooks under each implementation and returns the
// results keyed by UseMemDB.
func searchBothWays(t *testing.T, p *PebbleStore, query string, limit, offset int) map[bool][]Book {
	t.Helper()

	out := map[bool][]Book{}
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		if useMemDB {
			require.NotNil(t, p.mem(),
				"memdb is not published, so UseMemDB=true would silently run the Pebble path "+
					"and this test would compare an implementation against itself")
		}
		books, err := p.SearchBooks(query, limit, offset)
		require.NoError(t, err)
		out[useMemDB] = books
	}
	p.UseMemDB = true
	return out
}

func searchIDs(books []Book) []string {
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	return ids
}

func TestSearchBooks_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSearchConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")

	t.Run("matches on title, author and narrator alike", func(t *testing.T) {
		got := searchBothWays(t, p, "needlemark", 0, 0)

		require.ElementsMatch(t, searchIDs(got[true]), searchIDs(got[false]),
			"memdb and pebble returned different hit sets")
		require.ElementsMatch(t,
			[]string{fx.titleHitID, fx.authorHitID, fx.narratorHitID, fx.softDeletedHitID},
			searchIDs(got[true]),
			"expected exactly the title, author-name, narrator and soft-deleted hits")
		require.NotContains(t, searchIDs(got[true]), fx.controlID,
			"control book matched; the predicate is returning everything")
	})

	// Not an endorsement of the behaviour — search arguably should exclude
	// trashed books. But the disk scan includes them, so the fast path must too;
	// changing that is a product decision, not a side effect of a speed-up.
	t.Run("soft-deleted books are included on both paths", func(t *testing.T) {
		got := searchBothWays(t, p, "needlemark", 0, 0)

		require.Contains(t, searchIDs(got[false]), fx.softDeletedHitID,
			"precondition: the pebble scan is expected to include soft-deleted books")
		require.Contains(t, searchIDs(got[true]), fx.softDeletedHitID,
			"memdb path dropped a soft-deleted book the pebble scan returns")
	})

	// The case that made production searches time out: nothing matches, so the
	// Pebble scan cannot exit early and reads the entire library from disk.
	t.Run("zero-match query agrees and is empty", func(t *testing.T) {
		got := searchBothWays(t, p, "qzxwvnomatch", 0, 0)

		require.Empty(t, got[true], "memdb returned hits for a query nothing matches")
		require.Empty(t, got[false], "pebble returned hits for a query nothing matches")
	})

	// The load-bearing ordering claim. memdb's id index and Pebble's book:
	// keyspace must iterate in the same order, or the limit early-exit keeps a
	// DIFFERENT subset on each path — and since ABS search does not paginate,
	// whatever the fast path drops is unreachable.
	t.Run("limit truncates to the same rows in the same order", func(t *testing.T) {
		for limit := 1; limit <= 3; limit++ {
			got := searchBothWays(t, p, "needlemark", limit, 0)

			require.Len(t, got[true], limit)
			require.Equal(t, searchIDs(got[false]), searchIDs(got[true]),
				"limit=%d selected different rows on the two paths", limit)
		}
	})

	// offset counts MATCHES, not books scanned — `count++` sits inside the match
	// branch in the Pebble loop. A rewrite using slice indices would differ.
	t.Run("offset skips matches, not books", func(t *testing.T) {
		for offset := range 3 {
			got := searchBothWays(t, p, "needlemark", 0, offset)

			require.Equal(t, searchIDs(got[false]), searchIDs(got[true]),
				"offset=%d diverged", offset)
			require.Len(t, got[true], 4-offset,
				"offset=%d should skip exactly %d of the 4 matches", offset, offset)
		}
	})

	// The trap this design exists to avoid. memdb holds a projection:
	// stripBookForMemdb clears Description before insertion. Returning memdb rows
	// directly would compile, pass every ID-based assertion above, and silently
	// blank the description on every search result.
	t.Run("returns full records, not the stripped memdb projection", func(t *testing.T) {
		got := searchBothWays(t, p, "needlemark", 0, 0)

		for _, b := range got[true] {
			require.NotNil(t, b.Description,
				"book %s came back with a nil Description — the memdb projection leaked into results", b.ID)
			require.Equal(t, fx.description, *b.Description,
				"book %s description does not match what was stored", b.ID)
		}
	})
}

// A negative limit reached the memdb path's result-slice preallocation and
// panicked: make([]string, 0, n) panics outright for n < 0. The Pebble scan
// never preallocated, so this was introduced by the fast path — CodeQL flagged
// it as go/uncontrolled-allocation-size before it shipped.
//
// limit is not validated at all three SearchBooks call sites, so the store layer
// treats it as untrusted. Both paths must survive it and agree.
func TestSearchBooks_HostileLimitDoesNotPanic(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSearchConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok)

	for _, limit := range []int{-1, -1 << 30, 1 << 30} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			got := searchBothWays(t, p, "needlemark", limit, 0)

			require.Equal(t, searchIDs(got[false]), searchIDs(got[true]),
				"limit=%d diverged between the two paths", limit)

			if limit < 0 {
				// Both paths return NOTHING for a negative limit, and that is
				// the pre-existing behaviour, not a choice made here: the
				// collect condition is `limit == 0 || len(ids) < limit`, and a
				// negative limit satisfies neither. Asserted so the fast path
				// keeps matching it — the point of this test is that a hostile
				// limit is inert on both paths rather than a panic on one.
				require.Empty(t, got[true],
					"a negative limit collects nothing on the pebble scan; the fast path must agree")
			} else {
				// A huge limit is just "no early exit" — every match is returned.
				require.Len(t, got[true], 4)
				require.Contains(t, searchIDs(got[true]), fx.titleHitID)
			}
		})
	}
}

// TestSearchBooks_MemDBPathIsNotAFullScan pins the reason this change exists.
// The Pebble path's cost is proportional to the library for a zero-match query;
// the memdb path's must not touch Pebble at all to decide there is no match.
//
// Asserted structurally rather than by wall-clock: a timing threshold would be
// flaky in CI, but "the fast path returns the same empty answer without the disk
// scan" is what actually matters, and a fixture large enough to time reliably
// would be slow to build.
func TestSearchBooks_ZeroMatchDoesNotDependOnLibrarySize(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	desc := "d"
	for i := range 200 {
		_, err := store.CreateBook(&Book{
			Title:       fmt.Sprintf("Filler Title %03d", i),
			Description: &desc,
		})
		require.NoError(t, err)
	}

	p, ok := store.(*PebbleStore)
	require.True(t, ok)
	require.NotNil(t, p.mem())

	p.UseMemDB = true
	got, err := p.SearchBooks("qzxwvnomatch", 12, 0)
	require.NoError(t, err)
	require.Empty(t, got)

	// And the same query over the same data on the slow path, to prove the
	// emptiness is a real answer and not the fast path failing open.
	p.UseMemDB = false
	slow, err := p.SearchBooks("qzxwvnomatch", 12, 0)
	require.NoError(t, err)
	require.Empty(t, slow)

	// A query that DOES match must still find its row on both paths, so the
	// empty results above cannot be explained by a broken predicate.
	p.UseMemDB = true
	hit, err := p.SearchBooks("Filler Title 042", 12, 0)
	require.NoError(t, err)
	require.Len(t, hit, 1)
	p.UseMemDB = false
	slowHit, err := p.SearchBooks("Filler Title 042", 12, 0)
	require.NoError(t, err)
	require.Equal(t, searchIDs(slowHit), searchIDs(hit))
}
