// file: internal/database/book_visibility_conformance_test.go
// version: 1.0.0
// guid: e398a03c-a0e4-4e64-8da3-3beac8e0ff6b
// last-edited: 2026-08-13

package database

import (
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
