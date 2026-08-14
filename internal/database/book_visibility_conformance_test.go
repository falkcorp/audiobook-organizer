// file: internal/database/book_visibility_conformance_test.go
// version: 1.2.0
// guid: e398a03c-a0e4-4e64-8da3-3beac8e0ff6b
// last-edited: 2026-08-14

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// softDelConformanceFixture builds a library with a known live/soft-deleted
// split. Helper name is task-unique on purpose: several suites in this package
// define fixture builders and a generic name collides on rebase.
type softDelConformanceFixture struct {
	liveIDs    []string
	deletedIDs []string
}

func buildSoftDelConformanceFixture(t *testing.T, store Store) softDelConformanceFixture {
	t.Helper()

	var fx softDelConformanceFixture
	yes := true

	for _, title := range []string{"Live Alpha", "Live Bravo", "Live Charlie"} {
		created, err := store.CreateBook(&Book{Title: title, FilePath: "/lib/" + title})
		require.NoError(t, err)
		fx.liveIDs = append(fx.liveIDs, created.ID)
	}

	// Soft-delete via the same write path production uses: set the flag on an
	// existing row and update it. Creating a row that is born deleted would
	// not exercise the memdb re-index that a real deletion goes through.
	for _, title := range []string{"Trashed Delta", "Trashed Echo"} {
		created, err := store.CreateBook(&Book{Title: title, FilePath: "/lib/" + title})
		require.NoError(t, err)
		created.MarkedForDeletion = &yes
		_, err = store.UpdateBook(created.ID, created)
		require.NoError(t, err)
		fx.deletedIDs = append(fx.deletedIDs, created.ID)
	}

	return fx
}

// TestGetAllBooksCore_MemDBAndPebbleAgree is the conformance gate for the
// soft-delete contract.
//
// GetAllBooksCore has two backing implementations — a memdb index walk
// (UseMemDB=true, the production default) and a Pebble keyspace scan — and
// they silently disagreed about soft-deleted rows for the entire life of the
// memdb query layer. The Pebble path filtered them unconditionally; the memdb
// path filtered them only when a caller passed a "marked_for_deletion" filter,
// and no caller ever did. Measured on prod 2026-08-13: 63,869 live books were
// scanned as 67,824 by every full-library op in the system.
//
// Asserting each path against a hardcoded expectation would NOT have caught
// this — whoever wrote the memdb path would have written the memdb
// expectation. What catches it is holding the two implementations to the SAME
// answer on the SAME fixture.
func TestGetAllBooksCore_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSoftDelConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()

	// Non-vacuity guard: if the fixture has no soft-deleted rows the whole
	// suite below passes without testing anything.
	require.NotEmpty(t, fx.deletedIDs, "fixture must contain soft-deleted books or this test is vacuous")

	idsOf := func(books []BookCore) map[string]struct{} {
		out := make(map[string]struct{}, len(books))
		for _, b := range books {
			out[b.ID] = struct{}{}
		}
		return out
	}

	results := map[bool]map[string]struct{}{}
	counts := map[bool]int{}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}

		t.Run(label, func(t *testing.T) {
			books, err := store.GetAllBooksCore(0, 0)
			require.NoError(t, err)
			got := idsOf(books)

			for _, id := range fx.liveIDs {
				require.Contains(t, got, id, "live book missing from GetAllBooksCore")
			}
			for _, id := range fx.deletedIDs {
				require.NotContains(t, got, id,
					"soft-deleted book leaked into GetAllBooksCore — this is the prod bug that made "+
						"organize, dedup, every backfill and the ABS library count operate on trashed rows")
			}
			require.Len(t, books, len(fx.liveIDs))

			n, err := store.CountAllBooks()
			require.NoError(t, err)
			require.Equal(t, len(fx.liveIDs), n,
				"CountAllBooks documents itself as counting non-deleted books and is the progress "+
					"denominator for every full-library op")

			results[useMemDB] = got
			counts[useMemDB] = n
		})
	}

	// The actual conformance assertion: the two implementations of one
	// interface method must be indistinguishable to a caller.
	require.Equal(t, results[true], results[false],
		"memdb and Pebble implementations of GetAllBooksCore returned different book sets")
	require.Equal(t, counts[true], counts[false],
		"memdb and Pebble implementations of CountAllBooks returned different counts")

	// The escape hatch must still work: a caller that explicitly asks for
	// deleted rows gets them. Without this, the fix above would have silently
	// broken ListSoftDeletedBooks-style queries.
	t.Run("ExplicitFilterStillReturnsDeleted", func(t *testing.T) {
		p.UseMemDB = true
		books, err := p.mem().GetAllBooksCore(0, 0, map[string]interface{}{"marked_for_deletion": true})
		require.NoError(t, err)
		got := idsOf(books)
		for _, id := range fx.deletedIDs {
			require.Contains(t, got, id,
				"explicit marked_for_deletion=true filter must still return trashed rows")
		}
		require.Len(t, books, len(fx.deletedIDs))
	})
}

// TestGetAllBooksCore_PaginationPartitionsTheLiveSet is the pagination half of
// the conformance gate.
//
// The test above calls GetAllBooksCore(0, 0) against a 5-book fixture, which
// means BOTH the limit clamp and the offset clamp are dead code in it. Every
// real caller instead pages — GetAllBooksCore(pageSize, offset) in a loop that
// terminates when a page comes back short — so the untested interaction is the
// one production actually depends on: filtering must happen BEFORE pagination.
//
// If a path ever paginates first and filters second, a page containing trashed
// rows comes back short after filtering, the caller's `len(page) < pageSize`
// termination fires early, and the tail of the library is silently skipped.
// Both paths filter-then-paginate today; this test is what keeps that true,
// because a length check alone cannot see the difference between a page of
// [live, live] and a page of [live, trashed].
func TestGetAllBooksCore_PaginationPartitionsTheLiveSet(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")

	// Fixture is deliberately LARGER than the page size, with trashed rows
	// interleaved among live ones rather than clustered at either end — a
	// trailing block of trashed rows would only exercise the final page.
	const (
		total    = 21
		pageSize = 4
	)
	yes := true
	var wantLive []string
	var deleted []string

	for i := 0; i < total; i++ {
		title := fmt.Sprintf("Paged %02d", i)
		created, err := store.CreateBook(&Book{Title: title, FilePath: "/lib/paged/" + title})
		require.NoError(t, err)
		if i%3 == 0 {
			created.MarkedForDeletion = &yes
			_, err = store.UpdateBook(created.ID, created)
			require.NoError(t, err)
			deleted = append(deleted, created.ID)
			continue
		}
		wantLive = append(wantLive, created.ID)
	}
	p.WaitForWarmup()

	// Non-vacuity guards. The fixture must actually straddle a page boundary
	// and must actually contain trash, or this test proves nothing.
	require.Greater(t, len(wantLive), pageSize,
		"fixture must exceed one page or the pagination clamps are never exercised")
	require.NotEmpty(t, deleted, "fixture must contain soft-deleted books or this test is vacuous")

	traverse := map[bool][]string{}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}

		t.Run(label, func(t *testing.T) {
			seen := map[string]struct{}{}
			var order []string
			pages := 0

			// This loop is written the way production callers write it,
			// including the short-page termination that is the actual hazard.
			for offset := 0; ; offset += pageSize {
				page, err := store.GetAllBooksCore(pageSize, offset)
				require.NoError(t, err)
				pages++
				require.LessOrEqual(t, len(page), pageSize, "page exceeded the requested limit")

				for _, b := range page {
					require.NotContains(t, seen, b.ID,
						"book returned on two different pages — pages must be disjoint")
					seen[b.ID] = struct{}{}
					order = append(order, b.ID)
				}
				if len(page) < pageSize {
					break
				}
				require.Less(t, pages, total, "pagination did not terminate")
			}

			// Partition: every live book appears exactly once (disjointness is
			// asserted above), and nothing else appears at all.
			require.Len(t, order, len(wantLive),
				"paged traversal did not recover the live set exactly — a short page from "+
					"paginate-then-filter would truncate the tail here")
			for _, id := range wantLive {
				require.Contains(t, seen, id, "live book missing from the paged traversal")
			}
			for _, id := range deleted {
				require.NotContains(t, seen, id, "soft-deleted book leaked into a page")
			}

			traverse[useMemDB] = order
		})
	}

	// Cross-implementation conformance on the paged path, as sets: the two
	// backends must be indistinguishable to a paging caller.
	require.ElementsMatch(t, traverse[true], traverse[false],
		"memdb and Pebble paged traversals of GetAllBooksCore recovered different book sets")
}

// itunesPIDConformanceFixture builds books with iTunes persistent IDs, split
// live/soft-deleted, plus one book with NO PID that must never appear.
type itunesPIDConformanceFixture struct {
	livePIDBookIDs    []string
	deletedPIDBookIDs []string
	noPIDBookID       string
}

func buildITunesPIDConformanceFixture(t *testing.T, store Store) itunesPIDConformanceFixture {
	t.Helper()

	var fx itunesPIDConformanceFixture
	yes := true

	mk := func(title, pid string) *Book {
		b := &Book{Title: title, FilePath: "/lib/" + title}
		if pid != "" {
			p := pid
			b.ITunesPersistentID = &p
		}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		return created
	}

	for i, title := range []string{"PID Live One", "PID Live Two", "PID Live Three"} {
		created := mk(title, fmt.Sprintf("AAAA00000000000%d", i))
		fx.livePIDBookIDs = append(fx.livePIDBookIDs, created.ID)
	}

	// Soft-delete through the real update path so the memdb re-index runs,
	// exactly as buildSoftDelConformanceFixture does — a row born deleted
	// would skip the transition this method has to survive.
	for i, title := range []string{"PID Trashed One", "PID Trashed Two"} {
		created := mk(title, fmt.Sprintf("BBBB00000000000%d", i))
		created.MarkedForDeletion = &yes
		_, err := store.UpdateBook(created.ID, created)
		require.NoError(t, err)
		fx.deletedPIDBookIDs = append(fx.deletedPIDBookIDs, created.ID)
	}

	fx.noPIDBookID = mk("No PID At All", "").ID
	return fx
}

// TestListBooksByITunesPID_ExcludesTrashOnBothPaths is the conformance gate for
// the iTunes mapping listing.
//
// This method's two implementations always AGREED — both returned soft-deleted
// books — so it was consistent behaviour rather than the memdb/Pebble drift
// PR #2392 fixed, and it was left alone there deliberately. What made it wrong
// is the caller: ITunesHandler's writeback preview decides which metadata is
// offered for writing back into the iTunes library, and a book the user put in
// the trash should not be in that set.
//
// The flag flip below only became meaningful in the same commit as this test.
// PebbleStore.ListBooksByITunesPID previously dispatched on memdb PUBLICATION
// alone, ignoring UseMemDB, so this loop would have run the memdb path twice
// and asserted memdb == memdb — green regardless of what the Pebble branch did.
// A conformance test over two implementations is worth exactly as much as the
// selector that picks between them.
func TestListBooksByITunesPID_ExcludesTrashOnBothPaths(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildITunesPIDConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	// Non-vacuity guards: without trashed rows the exclusion assertion is
	// unfalsifiable, and without live rows an implementation that returned
	// nothing at all would pass.
	require.NotEmpty(t, fx.deletedPIDBookIDs, "fixture must contain soft-deleted iTunes-mapped books")
	require.NotEmpty(t, fx.livePIDBookIDs, "fixture must contain live iTunes-mapped books")

	got := map[bool][]string{}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}

		t.Run(label, func(t *testing.T) {
			books, err := store.ListBooksByITunesPID(0, 0)
			require.NoError(t, err)

			ids := make([]string, 0, len(books))
			for _, b := range books {
				ids = append(ids, b.ID)
				require.NotNil(t, b.ITunesPersistentID, "row without a PID leaked into the listing")
				require.NotEmpty(t, *b.ITunesPersistentID, "row with an empty PID leaked into the listing")
			}

			require.ElementsMatch(t, fx.livePIDBookIDs, ids,
				"listing did not return exactly the live iTunes-mapped books")
			for _, id := range fx.deletedPIDBookIDs {
				require.NotContains(t, ids, id,
					"soft-deleted book reached the iTunes mapping listing — the writeback "+
						"preview would offer to write metadata for a book in the trash")
			}
			require.NotContains(t, ids, fx.noPIDBookID, "book without a PID leaked into the listing")

			got[useMemDB] = ids
		})
	}
	p.UseMemDB = true

	require.ElementsMatch(t, got[true], got[false],
		"memdb and Pebble implementations of ListBooksByITunesPID disagree")
}

// TestListSoftDeletedBooks_MemDBAndPebbleAgree covers the OTHER method whose
// memdb dispatch ignored UseMemDB until 2026-08-14.
//
// Making a fallback reachable and not testing it is its own defect, and this
// one is load-bearing: findOrphanBookFiles unions this result into its "known
// book" set and fails closed if it errors, because a soft-deleted book still
// owns its book_files and deleting them is what makes it unrestorable. Prod
// carries 3,953 such books.
func TestListSoftDeletedBooks_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSoftDelConformanceFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "memdb must be published for the flag flip to select two paths")
	require.NotEmpty(t, fx.deletedIDs, "fixture must contain soft-deleted books")
	require.NotEmpty(t, fx.liveIDs, "fixture must contain live books to be excluded")

	got := map[bool][]string{}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}

		t.Run(label, func(t *testing.T) {
			books, err := store.ListSoftDeletedBooks(0, 0, nil)
			require.NoError(t, err)

			ids := make([]string, 0, len(books))
			for _, b := range books {
				ids = append(ids, b.ID)
			}

			// This method is the inverse of every other visibility check in
			// the package: here the trash is the ANSWER, not the exclusion.
			require.ElementsMatch(t, fx.deletedIDs, ids,
				"soft-deleted listing did not return exactly the trashed set")
			for _, id := range fx.liveIDs {
				require.NotContains(t, ids, id, "live book appeared in the soft-deleted listing")
			}

			got[useMemDB] = ids
		})
	}
	p.UseMemDB = true

	require.ElementsMatch(t, got[true], got[false],
		"memdb and Pebble implementations of ListSoftDeletedBooks disagree")
}
