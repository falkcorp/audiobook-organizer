// file: internal/database/census_test.go
// version: 1.0.0
// guid: b6c66985-8717-4b85-b9df-85f7eb9291f3
// last-edited: 2026-10-03

package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// newCensusTestStore opens a small real store, waits for warmup, and flushes
// whatever startup wrote (counters, system markers) so the keys a test writes
// next land in tables of their own.
func newCensusTestStore(t *testing.T, closeOnCleanup bool) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	if closeOnCleanup {
		t.Cleanup(func() { _ = p.Close() })
	}
	p.WaitForWarmup()
	require.NoError(t, p.db.Flush())
	return p
}

func writeCensusKeys(t *testing.T, p *PebbleStore, n int, key func(i int) string) {
	t.Helper()
	b := p.db.NewBatch()
	for i := 0; i < n; i++ {
		require.NoError(t, b.Set([]byte(key(i)), []byte("v"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
}

func censusFamily(t *testing.T, c *DBCensus, prefix string) FamilyCensus {
	t.Helper()
	for _, f := range c.Families {
		if f.Prefix == prefix {
			return f
		}
	}
	t.Fatalf("family %s missing from census", prefix)
	return FamilyCensus{}
}

func requireCensusConserved(t *testing.T, c *DBCensus) {
	t.Helper()
	var sum int64
	for _, f := range c.Families {
		sum += f.Keys
	}
	diff := sum - c.TotalKeys
	if diff < 0 {
		diff = -diff
	}
	require.LessOrEqual(t, diff, int64(len(c.Families)),
		"sum of family keys (%d) must equal total keys (%d) within rounding", sum, c.TotalKeys)
}

func TestDBCensus_ExactFamiliesInSeparateTables(t *testing.T) {
	p := newCensusTestStore(t, true)
	writeCensusKeys(t, p, 500, func(i int) string { return fmt.Sprintf("book_ver:B%03d:%020d", i%7, i) })
	require.NoError(t, p.db.Flush())
	writeCensusKeys(t, p, 300, func(i int) string { return fmt.Sprintf("opv2:log:OP:%020d:%010d", i, i) })
	require.NoError(t, p.db.Flush())

	c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)

	bv := censusFamily(t, c, "book_ver:")
	require.Equal(t, int64(500), bv.Keys)
	require.False(t, bv.Estimated)
	require.Equal(t, 1, bv.Tables)
	require.Positive(t, bv.RawKeyBytes)
	require.Positive(t, bv.DiskBytes)

	ol := censusFamily(t, c, "opv2:log:")
	require.Equal(t, int64(300), ol.Keys)
	require.False(t, ol.Estimated)

	// The parent excludes its registered child.
	require.Zero(t, censusFamily(t, c, "opv2:").Keys)
	// Pre-registered A5 families are reported even at zero.
	require.Zero(t, censusFamily(t, c, "opv2:open:").Keys)

	require.Equal(t, CensusFamilyFiguresBasis, c.FamilyFiguresBasis)
	require.Contains(t, c.Notes, censusNoteKeyDefinition)
	require.Contains(t, c.Notes, censusNoteMemtable)
	require.Positive(t, c.TotalTables)
	require.Positive(t, c.DiskSpaceUsage)
	requireCensusConserved(t, c)
}

func TestDBCensus_StraddlingTableIsEstimatedAndConserved(t *testing.T) {
	p := newCensusTestStore(t, true)
	b := p.db.NewBatch()
	for i := 0; i < 200; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book_file:BK:%05d", i)), []byte("file-row"), nil))
		require.NoError(t, b.Set([]byte(fmt.Sprintf("fpidx:%05d", i)), []byte("lsh"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())

	c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	require.True(t, censusFamily(t, c, "book_file:").Estimated)
	require.True(t, censusFamily(t, c, "fpidx:").Estimated)
	requireCensusConserved(t, c)
}

func TestDBCensus_RetiredFromMemdb(t *testing.T) {
	p := newCensusTestStore(t, true)
	yes := true
	merged := "01LIVE0000000000000000000A"
	mk := func(id string, mutate func(*Book)) {
		b := &Book{ID: id, Title: "Census " + id, FilePath: "/lib/census/" + id + ".m4b"}
		if mutate != nil {
			mutate(b)
		}
		_, err := p.CreateBook(b)
		require.NoError(t, err)
	}
	mk(merged, nil)
	mk("01SOFT0000000000000000000A", func(b *Book) { b.MarkedForDeletion = &yes })
	mk("01MERG0000000000000000000A", func(b *Book) { b.MergedIntoBookID = &merged })
	for i, bid := range []string{"01SOFT0000000000000000000A", "01SOFT0000000000000000000A", "01MERG0000000000000000000A", merged} {
		require.NoError(t, p.CreateBookFile(&BookFile{
			ID: fmt.Sprintf("f%d", i), BookID: bid, FilePath: fmt.Sprintf("/lib/census/f%d.mp3", i),
			AcoustIDFingerprintDurationSec: float64(i % 2),
		}))
	}
	p.WaitForWarmup()

	c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	require.NotNil(t, c.Retired, "a warm memdb must report retired counts, notes: %v", c.Notes)
	require.NotNil(t, c.Signals)
	require.Equal(t, 2, c.Retired.RetiredBooks)
	require.Equal(t, 3, c.Retired.RetiredBookFiles)
	require.Equal(t, 1, c.Retired.SoftDeletedBooks)
	require.Equal(t, 1, c.Retired.MergedBooks)
	require.Equal(t, 2, c.Signals.FilesWithFingerprint)
	require.NotContains(t, c.Notes, censusNoteMemdbCold)
}

func TestDBCensus_HistoryOnlyWhenDeep(t *testing.T) {
	p := newCensusTestStore(t, true)
	writeCensusKeys(t, p, 3, func(i int) string { return fmt.Sprintf("book_ver:BOOKA:%020d", i) })
	writeCensusKeys(t, p, 12, func(i int) string { return fmt.Sprintf("book_ver:BOOKB:%020d", i) })

	shallow, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	require.Nil(t, shallow.History)

	deep, err := p.DBCensus(context.Background(), CensusOptions{Deep: true, Fresh: true})
	require.NoError(t, err)
	require.NotNil(t, deep.History)
	h := deep.History
	require.Equal(t, int64(15), h.Entries)
	require.Equal(t, int64(2), h.BooksWithHistory)
	require.Equal(t, int64(12), h.Max)
	require.Equal(t, int64(1), h.Buckets["1-9"])
	require.Equal(t, int64(1), h.Buckets["10-49"])
	require.True(t, h.OrphansKnown)
	require.Equal(t, int64(2), h.OrphanBooks)
	require.Equal(t, int64(15), h.OrphanEntries)
	require.Equal(t, []BookHistoryCount{{"BOOKB", 12}, {"BOOKA", 3}}, h.Top)
	require.Equal(t, 7.5, h.Mean)
}

func TestDBCensus_CachedWithinTTL(t *testing.T) {
	p := newCensusTestStore(t, true)
	first, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	require.False(t, first.Cached)

	second, err := p.DBCensus(context.Background(), CensusOptions{})
	require.NoError(t, err)
	require.True(t, second.Cached)
	require.Equal(t, first.GeneratedAt, second.GeneratedAt)

	// A caller editing its copy must not edit the cache.
	second.Families[0].Keys = -1
	third, err := p.DBCensus(context.Background(), CensusOptions{})
	require.NoError(t, err)
	require.NotEqual(t, int64(-1), third.Families[0].Keys)

	fresh, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	require.False(t, fresh.Cached)

	// The deep slot is separate from the shallow one.
	deep, err := p.DBCensus(context.Background(), CensusOptions{Deep: true})
	require.NoError(t, err)
	require.False(t, deep.Cached)
	require.NotNil(t, deep.History)
}

func TestDBCensus_ClosedStoreReturnsError(t *testing.T) {
	p := newCensusTestStore(t, false)
	require.NoError(t, p.Close())
	require.NotPanics(t, func() {
		c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
		require.Error(t, err)
		require.Nil(t, c)
	})
}

func TestBuildHistoryCensus_Percentiles(t *testing.T) {
	counts := map[string]int64{}
	for i := 1; i <= 100; i++ {
		counts[fmt.Sprintf("b%03d", i)] = int64(i)
	}
	h := buildHistoryCensus(counts)
	require.Equal(t, int64(50), h.P50)
	require.Equal(t, int64(90), h.P90)
	require.Equal(t, int64(99), h.P99)
	require.Equal(t, int64(100), h.Max)
	require.Len(t, h.Top, censusHistoryTopN)
	require.Equal(t, int64(9), h.Buckets["1-9"])
	require.Equal(t, int64(40), h.Buckets["10-49"])
	require.Equal(t, int64(50), h.Buckets["50-99"])
	require.Equal(t, int64(1), h.Buckets["100-499"])
}
