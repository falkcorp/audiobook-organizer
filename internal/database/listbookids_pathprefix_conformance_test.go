// file: internal/database/listbookids_pathprefix_conformance_test.go
// version: 1.0.0
// guid: 4e8b1c07-6d92-4a35-b8f1-2c9a7e40d5b3
// last-edited: 2026-08-14

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	conformanceAlphaPrefix = "/import/alpha"
	conformanceBetaPrefix  = "/import/beta"
)

// idTrashFixture builds books under two import-path prefixes, split live and
// soft-deleted, so both the ID listing and the prefix count are falsifiable:
// forgetting the trash filter makes alpha count 5 instead of 3 and puts two
// extra IDs in the listing. Beta has no trash, so a mutation that drops *every*
// book also fails.
type idTrashFixture struct {
	liveAlphaIDs   []string
	trashAlphaIDs  []string
	liveBetaID     string
	wantAlphaCount int
	wantBetaCount  int
}

func buildIDTrashFixture(t *testing.T, store Store) idTrashFixture {
	t.Helper()

	yes := true
	var fx idTrashFixture

	mk := func(prefix, title string) *Book {
		created, err := store.CreateBook(&Book{
			Title:    title,
			FilePath: fmt.Sprintf("%s/%s.m4b", prefix, title),
		})
		require.NoError(t, err)
		return created
	}

	for _, title := range []string{"AlphaLiveOne", "AlphaLiveTwo", "AlphaLiveThree"} {
		fx.liveAlphaIDs = append(fx.liveAlphaIDs, mk(conformanceAlphaPrefix, title).ID)
	}

	// Soft-delete via UpdateBook so the memdb re-index runs; a row born deleted
	// would skip the live->trashed transition these methods have to survive.
	for _, title := range []string{"AlphaTrashOne", "AlphaTrashTwo"} {
		created := mk(conformanceAlphaPrefix, title)
		created.MarkedForDeletion = &yes
		_, err := store.UpdateBook(created.ID, created)
		require.NoError(t, err)
		fx.trashAlphaIDs = append(fx.trashAlphaIDs, created.ID)
	}

	fx.liveBetaID = mk(conformanceBetaPrefix, "BetaLiveOne").ID

	fx.wantAlphaCount = 3
	fx.wantBetaCount = 1
	return fx
}

// TestListBookIDsAndPathPrefix_ExcludeTrashOnBothPaths is the conformance gate
// for two more methods whose implementations drifted on soft-delete.
//
// ListBookIDs: MemStore filtered soft-deleted books, the Pebble scan did not.
// The Pebble scan never unmarshalled anything — it read keys only — which is
// exactly why the filter was missing. ~20 full-library maintenance ops
// enumerate through this method.
//
// CountBooksByPathPrefix: same drift, plus its dispatch gated on memdb
// PUBLICATION alone rather than UseMemDB, so the Pebble branch was unreachable
// whenever memdb was up and could not be tested at all. That gate is fixed in
// the same commit as this test — without the fix the loop below would run the
// memdb path twice and assert memdb == memdb.
func TestListBookIDsAndPathPrefix_ExcludeTrashOnBothPaths(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildIDTrashFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	require.NotEmpty(t, fx.trashAlphaIDs, "fixture must contain soft-deleted books")
	require.NotEmpty(t, fx.liveAlphaIDs, "fixture must contain live books")

	originalUseMemDB := p.UseMemDB
	defer func() { p.UseMemDB = originalUseMemDB }()

	idsByPath := map[bool]map[string]bool{}
	alphaCounts := map[bool]int{}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}

		t.Run(label, func(t *testing.T) {
			ids, err := p.ListBookIDs()
			require.NoError(t, err)
			seen := map[string]bool{}
			for _, id := range ids {
				seen[id] = true
			}

			for _, id := range fx.liveAlphaIDs {
				require.True(t, seen[id], "live book %s missing from ListBookIDs", id)
			}
			require.True(t, seen[fx.liveBetaID], "live beta book missing from ListBookIDs")
			for _, id := range fx.trashAlphaIDs {
				require.False(t, seen[id], "soft-deleted book %s must not appear in ListBookIDs", id)
			}

			alpha, err := p.CountBooksByPathPrefix(conformanceAlphaPrefix)
			require.NoError(t, err)
			beta, err := p.CountBooksByPathPrefix(conformanceBetaPrefix)
			require.NoError(t, err)

			require.Equal(t, fx.wantAlphaCount, alpha,
				"alpha count must exclude its %d trashed books", len(fx.trashAlphaIDs))
			require.Equal(t, fx.wantBetaCount, beta,
				"beta has no trash; its count must be unaffected")

			idsByPath[useMemDB] = seen
			alphaCounts[useMemDB] = alpha
		})
	}

	// Conformance proper: both implementations must agree on every fixture book.
	for _, id := range append(append([]string{}, fx.liveAlphaIDs...), fx.trashAlphaIDs...) {
		require.Equal(t, idsByPath[true][id], idsByPath[false][id],
			"memdb and Pebble disagree on whether ListBookIDs includes %s", id)
	}
	require.Equal(t, alphaCounts[true], alphaCounts[false],
		"memdb and Pebble disagree on CountBooksByPathPrefix")
}

// TestComputeLibraryStats_MemDBAndPebbleAgree covers the third gate fixed here.
//
// computeLibraryStats also dispatched on memdb publication alone, so its Pebble
// scan was unreachable in any store with memdb up. Both implementations already
// filtered soft-deleted books correctly — this test exists because making a
// fallback reachable and then not testing it is its own defect, and because the
// Pebble scan is what serves the dashboard during the cold-start window.
func TestComputeLibraryStats_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildIDTrashFixture(t, store)

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	originalUseMemDB := p.UseMemDB
	defer func() { p.UseMemDB = originalUseMemDB }()

	wantTotal := len(fx.liveAlphaIDs) + 1 // + beta; trashed books excluded

	totals := map[bool]int{}
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		// computeLibraryStats directly, not GetDashboardStats: the latter serves
		// from a cache and would return the first path's answer to the second.
		stats, err := p.computeLibraryStats()
		require.NoError(t, err)
		require.NotNil(t, stats)
		totals[useMemDB] = stats.TotalBooks
	}

	require.Equal(t, wantTotal, totals[true], "memdb TotalBooks must exclude trashed books")
	require.Equal(t, wantTotal, totals[false], "Pebble TotalBooks must exclude trashed books")
	require.Equal(t, totals[true], totals[false], "memdb and Pebble disagree on TotalBooks")
}
