// file: internal/database/census_test.go
// version: 1.2.0
// guid: b6c66985-8717-4b85-b9df-85f7eb9291f3
// last-edited: 2026-10-04

package database

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
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

// requireCensusConserved checks that every sstable entry and every table byte
// was attributed to exactly one family (within rounding). Valid after a flush,
// when exact families' memtable points are zero.
func requireCensusConserved(t *testing.T, c *DBCensus) {
	t.Helper()
	var entries int64
	var disk uint64
	for _, f := range c.Families {
		entries += f.Entries
		disk += f.DiskBytes
	}
	diff := entries - c.TotalEntries
	if diff < 0 {
		diff = -diff
	}
	require.LessOrEqual(t, diff, int64(len(c.Families)),
		"sum of family entries (%d) must equal total entries (%d) within rounding", entries, c.TotalEntries)
	ddiff := int64(disk) - int64(c.TotalTableBytes)
	if ddiff < 0 {
		ddiff = -ddiff
	}
	require.LessOrEqual(t, ddiff, int64(len(c.Families)),
		"sum of family disk bytes (%d) must equal total table bytes (%d) within rounding", disk, c.TotalTableBytes)
}

// forceEstimated makes every family take the apportioning path for one test.
func forceEstimated(t *testing.T) {
	t.Helper()
	old := censusExactThreshold
	censusExactThreshold = 0
	t.Cleanup(func() { censusExactThreshold = old })
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
	require.Equal(t, CensusMethodExact, bv.Method)
	require.False(t, bv.Estimated)
	require.Equal(t, 1, bv.Tables)
	require.Positive(t, bv.RawKeyBytes)
	require.Equal(t, int64(500), bv.RawValueBytes, "500 one-byte values")
	require.Positive(t, bv.DiskBytes)

	ol := censusFamily(t, c, "opv2:log:")
	require.Equal(t, int64(300), ol.Keys)
	require.Equal(t, CensusMethodExact, ol.Method)

	// The parent excludes its registered child.
	require.Zero(t, censusFamily(t, c, "opv2:").Keys)
	require.Equal(t, CensusMethodEmpty, censusFamily(t, c, "opv2:").Method)
	// Pre-registered A5 families are reported even at zero.
	require.Zero(t, censusFamily(t, c, "opv2:open:").Keys)

	require.Equal(t, CensusFamilyFiguresBasis, c.FamilyFiguresBasis)
	require.Contains(t, c.Notes, censusNoteKeyDefinition)
	require.Contains(t, c.Notes, censusNoteExact)
	require.Contains(t, c.Notes, censusNoteHiddenSystem)
	require.Positive(t, c.TotalTables)
	require.Positive(t, c.DiskSpaceUsage)
	requireCensusConserved(t, c)
}

// The same two families apportioned from properties (exact pass disabled):
// separate tables still give exact figures.
func TestDBCensus_EstimatedFamiliesInSeparateTablesAreExactAnyway(t *testing.T) {
	forceEstimated(t)
	p := newCensusTestStore(t, true)
	writeCensusKeys(t, p, 500, func(i int) string { return fmt.Sprintf("book_ver:B%03d:%020d", i%7, i) })
	require.NoError(t, p.db.Flush())
	writeCensusKeys(t, p, 300, func(i int) string { return fmt.Sprintf("opv2:log:OP:%020d:%010d", i, i) })
	require.NoError(t, p.db.Flush())

	c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	bv := censusFamily(t, c, "book_ver:")
	require.Equal(t, int64(500), bv.Keys)
	require.Equal(t, CensusMethodEstimated, bv.Method)
	require.Zero(t, bv.ErrorBoundKeys, "a table holding one family has no apportioning error")
	require.Equal(t, int64(300), censusFamily(t, c, "opv2:log:").Keys)
	requireCensusConserved(t, c)
}

func TestDBCensus_StraddlingTableIsEstimatedAndConserved(t *testing.T) {
	forceEstimated(t)
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
	bf, fp := censusFamily(t, c, "book_file:"), censusFamily(t, c, "fpidx:")
	require.True(t, bf.Estimated)
	require.True(t, fp.Estimated)
	require.Positive(t, bf.ErrorBoundKeys)
	require.Positive(t, fp.ErrorBoundKeys)
	requireCensusConserved(t, c)
}

// With the exact pass on (the default), the same straddling table is counted
// exactly.
func TestDBCensus_StraddlingSmallFamiliesAreCountedExactly(t *testing.T) {
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
	for _, prefix := range []string{"book_file:", "fpidx:"} {
		f := censusFamily(t, c, prefix)
		require.Equal(t, CensusMethodExact, f.Method, prefix)
		require.Equal(t, int64(200), f.Keys, prefix)
		require.Zero(t, f.Deletions, prefix)
		require.Equal(t, int64(200), f.Entries, prefix)
	}
	require.Positive(t, c.ExactPassKeys)
	requireCensusConserved(t, c)
}

// TestDBCensus_EmptyFamiliesInsideAStraddlingTableGetZero pins the range
// probe: one small table holds book_file: and book_ver: keys, and every family
// and gap range between them in key order is empty (book_file_*:,
// book_narrators:, book_sig:, book_tag: and the unregistered gaps). Span bytes
// are block-granular, so without the probe each of those empty ranges received
// a share of the table. No startup key (counter:, system:) sorts between the
// two, so the table's 400 keys must all stay with the two families. Run on
// the apportioning path, which is where the leak was.
func TestDBCensus_EmptyFamiliesInsideAStraddlingTableGetZero(t *testing.T) {
	forceEstimated(t)
	p := newCensusTestStore(t, true)
	b := p.db.NewBatch()
	for i := 0; i < 200; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book_file:BK:%05d", i)), []byte("file-row"), nil))
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book_ver:BK:%020d", i)), []byte("snapshot"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())

	c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)

	empty := []string{
		"book_file_acoustid:", "book_file_error:", "book_file_errors_by_book:", "book_file_gone:",
		"book_file_hash:", "book_file_id:", "book_file_orig_hash:", "book_file_path:", "book_file_pid:",
		"book_narrators:", "book_sig:", "book_tag:", unregisteredFamily,
	}
	for _, prefix := range empty {
		f := censusFamily(t, c, prefix)
		require.Equal(t, CensusMethodEmpty, f.Method, prefix)
		require.Zero(t, f.Keys, "empty family %s must get no keys", prefix)
		require.Zero(t, f.Deletions, "empty family %s must get no deletions", prefix)
		require.Zero(t, f.RawKeyBytes, "empty family %s must get no key bytes", prefix)
		require.Zero(t, f.DiskBytes, "empty family %s must get no disk bytes", prefix)
		require.Zero(t, f.Tables, "empty family %s must count no tables", prefix)
		require.False(t, f.Estimated, "empty family %s must not be flagged", prefix)
	}
	bf, bv := censusFamily(t, c, "book_file:"), censusFamily(t, c, "book_ver:")
	require.Equal(t, int64(400), bf.Keys+bv.Keys, "the table's 400 keys stay with the two families that hold keys")
	require.Positive(t, bf.Keys)
	require.Positive(t, bv.Keys)
	require.True(t, bf.Estimated)
	require.True(t, bv.Estimated)
	requireCensusConserved(t, c)
}

// writeThenDeleteAuthors builds the reviewer's B1 probe: 5000 book:author:
// and 5000 book:hash: keys in one table, then every book:author: key deleted
// and flushed into a second table.
func writeThenDeleteAuthors(t *testing.T, p *PebbleStore) {
	t.Helper()
	b := p.db.NewBatch()
	for i := 0; i < 5000; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book:author:%05d", i)), []byte("a"), nil))
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book:hash:%05d", i)), []byte("h"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())
	d := p.db.NewBatch()
	for i := 0; i < 5000; i++ {
		require.NoError(t, d.Delete([]byte(fmt.Sprintf("book:author:%05d", i)), nil))
	}
	require.NoError(t, d.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())
}

// TestDBCensus_TombstoneOnlyFamilyIsNotDropped pins B1: a family whose keys
// were all deleted holds only tombstones and shadowed versions. First() finds
// no live key there, but the range is not empty and must keep its entries
// and disk bytes — the purge-then-measure shape later releases rely on.
func TestDBCensus_TombstoneOnlyFamilyIsNotDropped(t *testing.T) {
	p := newCensusTestStore(t, true)
	writeThenDeleteAuthors(t, p)

	c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	au := censusFamily(t, c, "book:author:")
	require.Equal(t, CensusMethodExact, au.Method)
	require.Zero(t, au.Keys, "no live book:author: key remains")
	require.Equal(t, int64(10000), au.Entries, "5000 shadowed puts plus 5000 tombstones")
	require.Equal(t, int64(10000), au.Deletions)
	require.Positive(t, au.DiskBytes)
	h := censusFamily(t, c, "book:hash:")
	require.Equal(t, int64(5000), h.Keys)
	require.Zero(t, h.Deletions)
	requireCensusConserved(t, c)
}

// Same probe on the apportioning path: the tombstone-only family still gets
// its entries and disk bytes, and the live family is not handed them.
func TestDBCensus_TombstoneOnlyFamilyIsNotDroppedWhenEstimated(t *testing.T) {
	forceEstimated(t)
	p := newCensusTestStore(t, true)
	writeThenDeleteAuthors(t, p)

	c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	au := censusFamily(t, c, "book:author:")
	require.Equal(t, CensusMethodEstimated, au.Method)
	require.Positive(t, au.Entries)
	require.Positive(t, au.Deletions)
	require.Positive(t, au.DiskBytes)
	h := censusFamily(t, c, "book:hash:")
	require.Less(t, h.Deletions, int64(5000), "book:hash: must not absorb all the book:author: tombstones")
	requireCensusConserved(t, c)
}

func TestDBCensus_ProbeSeesTombstonesButNotEmptiness(t *testing.T) {
	p := newCensusTestStore(t, true)
	writeThenDeleteAuthors(t, p)
	ranges := keyFamilyRanges(keyFamilies)
	probes, err := p.censusProbeRanges(ranges)
	require.NoError(t, err)
	byFam := map[string]censusRangeProbe{}
	for ri, r := range ranges {
		if pr, ok := byFam[r.Family]; !ok || !pr.entries {
			byFam[r.Family] = probes[ri]
		}
	}
	require.False(t, byFam["book:author:"].live)
	require.Equal(t, uint64(10000), byFam["book:author:"].points)
	require.True(t, byFam["book:author:"].entries)
	require.True(t, byFam["book:hash:"].live)
	require.False(t, byFam["book:path:"].entries)
	require.Zero(t, byFam["book:path:"].points)
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

// TestDBCensus_OneCallerCancellingDoesNotFailTheOthers pins S3: the shared
// computation runs detached from the first caller's context.
func TestDBCensus_OneCallerCancellingDoesNotFailTheOthers(t *testing.T) {
	p := newCensusTestStore(t, true)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	censusBeforeCompute = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	t.Cleanup(func() { censusBeforeCompute = nil })

	ctxA, cancelA := context.WithCancel(context.Background())
	errA := make(chan error, 1)
	go func() {
		_, err := p.DBCensus(ctxA, CensusOptions{Fresh: true})
		errA <- err
	}()
	<-entered // A's call is computing and holds the flight

	type res struct {
		c   *DBCensus
		err error
	}
	resB := make(chan res, 1)
	go func() {
		c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
		resB <- res{c, err}
	}()

	cancelA()
	require.ErrorIs(t, <-errA, context.Canceled, "the cancelled caller gets its own cancellation")
	close(release)
	b := <-resB
	require.NoError(t, b.err, "the other caller must still get the census")
	require.NotNil(t, b.c)
	require.NotEmpty(t, b.c.Families)
}

// TestDBCensus_ColdMemdbIsNotCached pins N1: a census taken before memdb is
// warm lacks retired/signal counts and must not be served from cache.
func TestDBCensus_ColdMemdbIsNotCached(t *testing.T) {
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	p := &PebbleStore{db: db} // no memdb: mem() is nil

	first, err := p.DBCensus(context.Background(), CensusOptions{})
	require.NoError(t, err)
	require.Nil(t, first.Retired)
	require.Contains(t, first.Notes, censusNoteMemdbCold)
	second, err := p.DBCensus(context.Background(), CensusOptions{})
	require.NoError(t, err)
	require.False(t, second.Cached, "a cold-memdb census must not be cached")
}

// TestCensusCache_IsBounded pins N3: the package cache never holds more than
// censusCacheMaxStores stores.
func TestCensusCache_IsBounded(t *testing.T) {
	now := time.Now()
	for i := 0; i < censusCacheMaxStores+5; i++ {
		censusCachePut(new(pebble.DB), false, &DBCensus{}, now.Add(time.Duration(i)*time.Second))
	}
	censusCache.mu.Lock()
	n := len(censusCache.m)
	censusCache.mu.Unlock()
	require.LessOrEqual(t, n, censusCacheMaxStores)
}

// TestDBCensus_DeepPassStopsAtItsBudget pins the deep time budget: past the
// deadline the pass stops and reports partial history with a note.
func TestDBCensus_DeepPassStopsAtItsBudget(t *testing.T) {
	p := newCensusTestStore(t, true)
	writeCensusKeys(t, p, 3*censusCtxCheckEvery, func(i int) string { return fmt.Sprintf("book_ver:B%05d:%020d", i%50, i) })
	h, notes, err := p.censusHistory(context.Background(), time.Now().Add(-time.Second))
	require.NoError(t, err)
	require.True(t, h.Partial)
	require.Less(t, h.Entries, int64(3*censusCtxCheckEvery))
	require.NotEmpty(t, notes)
	require.Contains(t, notes[0], "history is partial")

	full, _, err := p.censusHistory(context.Background(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, full.Partial)
	require.Equal(t, int64(3*censusCtxCheckEvery), full.Entries)
}
