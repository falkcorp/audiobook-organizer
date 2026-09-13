// file: internal/database/series_membership_test.go
// version: 1.0.0
// guid: 1a6f0d38-94c2-4b7e-8d51-c3e20f7a9b16
// last-edited: 2026-09-13

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func seriesMembershipIDs(books []BookCore) []string {
	ids := make([]string, len(books))
	for i := range books {
		ids[i] = books[i].ID
	}
	return ids
}

// TestGetBooksBySeriesIDsAllVersions_MatchesPerSeriesGetter is the parity gate
// for the bulk membership read (SERIES-MERGE-PERSERIES-SCAN-COST). The fixture
// carries a non-primary version (must be INCLUDED), a trashed book (must be
// EXCLUDED -- the refCounts guard depends on that gap), a second series and
// out-of-order creation, and every requested series must come back exactly as
// the per-series getter returns it, in the same order, on both store paths.
func TestGetBooksBySeriesIDsAllVersions_MatchesPerSeriesGetter(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	fx := buildSeriesGetterConformanceFixture(t, store)
	p, ok := store.(*PebbleStore)
	require.True(t, ok)
	p.WaitForWarmup()
	require.True(t, p.IsMemReady())

	other, err := store.GetBookByID(fx.otherSeriesBookID)
	require.NoError(t, err)
	require.NotNil(t, other.SeriesID)
	const emptySeries = 987654
	ids := []int{fx.seriesID, *other.SeriesID, emptySeries, fx.seriesID}

	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		name := "PebbleScanPath"
		if useMemDB {
			name = "MemDBPath"
		}
		t.Run(name, func(t *testing.T) {
			bulk, err := bulkSeriesMembership(store, ids)
			require.NoError(t, err)
			for _, id := range ids {
				want, err := store.GetBooksBySeriesIDAllVersions(id)
				require.NoError(t, err)
				require.Equal(t, seriesMembershipIDs(want), seriesMembershipIDs(bulk[id]),
					"series %d: bulk and per-series answers differ", id)
			}
			got := seriesMembershipIDs(bulk[fx.seriesID])
			require.Contains(t, got, fx.nonPrimaryBookID, "non-primary version must be included")
			require.NotContains(t, got, fx.softDeletedBookID, "trashed book must be excluded")
			require.NotContains(t, got, fx.otherSeriesBookID)
			require.Empty(t, bulk[emptySeries])
		})
	}
	// Tainted memdb: the bulk read must refuse on the memdb and fall through to
	// ONE Pebble scan that still matches the per-series getter exactly.
	t.Run("TaintedMemDBFallsThrough", func(t *testing.T) {
		p.UseMemDB = true
		m := p.mem()
		require.NotNil(t, m)
		m.recordLostRows(memTableBooks, 1)
		defer m.publishLostRows(map[string]int{}, 0)

		_, memErr := m.GetBooksBySeriesIDsAllVersions(ids)
		require.ErrorIs(t, memErr, ErrMemdbIncomplete, "memdb must refuse when books rows are lost")

		bulk, err := bulkSeriesMembership(store, ids)
		require.NoError(t, err)
		for _, id := range ids {
			want, err := store.GetBooksBySeriesIDAllVersions(id)
			require.NoError(t, err)
			require.Equal(t, seriesMembershipIDs(want), seriesMembershipIDs(bulk[id]), "series %d", id)
		}
		require.Contains(t, seriesMembershipIDs(bulk[fx.seriesID]), fx.nonPrimaryBookID)
		require.NotContains(t, seriesMembershipIDs(bulk[fx.seriesID]), fx.softDeletedBookID)
	})
	p.UseMemDB = true
}

func bulkSeriesMembership(store Store, ids []int) (SeriesBooksMap, error) {
	return SeriesMembershipAllVersions(store, ids)
}

// TestSeriesMembershipAllVersions_FailsClosedWithoutCapability: a store that
// cannot answer must be an error, never an empty map.
func TestSeriesMembershipAllVersions_FailsClosedWithoutCapability(t *testing.T) {
	m, err := SeriesMembershipAllVersions(struct{}{}, []int{1})
	require.Error(t, err)
	require.Nil(t, m)
}

// TestSeriesBooksMap_Move covers the bookkeeping every hoisted loop relies on.
func TestSeriesBooksMap_Move(t *testing.T) {
	m := SeriesBooksMap{1: {{ID: "a"}, {ID: "b"}}, 2: {{ID: "c"}}}
	to := 2
	m.Move("a", 1, &to)
	m.Move("b", 1, nil)
	m.Move("missing", 1, &to)
	require.Empty(t, m[1])
	require.Equal(t, []string{"c", "a"}, seriesMembershipIDs(m[2]))
	require.Equal(t, 2, *m[2][1].SeriesID)
}
