// file: internal/database/census_test.go
// version: 1.4.0
// guid: b6c66985-8717-4b85-b9df-85f7eb9291f3
// last-edited: 2026-10-04

package database

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/require"
)

// newCensusRawStore opens an in-memory Pebble with automatic compactions OFF
// (and L0 thresholds raised so writes never stall), so a background
// compaction cannot drop tombstones or merge tables under a test. It has no
// memdb, so retired/signal counts are absent.
func newCensusRawStore(t *testing.T) *PebbleStore {
	t.Helper()
	db, err := pebble.Open("", &pebble.Options{
		FS:                          vfs.NewMem(),
		FormatMajorVersion:          pebble.FormatNewest,
		DisableAutomaticCompactions: true,
		L0CompactionThreshold:       1000,
		L0CompactionFileThreshold:   1000,
		L0StopWritesThreshold:       10000,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &PebbleStore{db: db}
}

// newCensusTestStore opens a full in-memory PebbleStore with memdb, for the
// tests that need memdb (retired counts, history orphans, caching).
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
// of an estimated census went to exactly one family (within rounding).
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

func freshCensus(t *testing.T, p *PebbleStore) *DBCensus {
	t.Helper()
	c, err := p.DBCensus(context.Background(), CensusOptions{Fresh: true})
	require.NoError(t, err)
	require.Equal(t, CensusKindEstimated, c.Kind)
	return c
}

func runExact(t *testing.T, p *PebbleStore) *DBCensus {
	t.Helper()
	c, err := p.RunExactCensus(context.Background(), ExactCensusOptions{})
	require.NoError(t, err)
	require.Equal(t, CensusKindExact, c.Kind)
	return c
}

func TestDBCensus_EstimatedFamiliesInSeparateTables(t *testing.T) {
	p := newCensusRawStore(t)
	writeCensusKeys(t, p, 500, func(i int) string { return fmt.Sprintf("book_ver:B%03d:%020d", i%7, i) })
	require.NoError(t, p.db.Flush())
	writeCensusKeys(t, p, 300, func(i int) string { return fmt.Sprintf("opv2:log:OP:%020d:%010d", i, i) })
	require.NoError(t, p.db.Flush())

	c := freshCensus(t, p)
	bv := censusFamily(t, c, "book_ver:")
	require.Equal(t, int64(500), bv.Keys)
	require.Equal(t, CensusMethodEstimated, bv.Method)
	require.True(t, bv.Estimated)
	require.Zero(t, bv.ErrorBoundKeys, "a table holding one family has no apportioning error")
	require.Equal(t, 1, bv.Tables)
	require.Positive(t, bv.DiskBytes)
	require.Equal(t, int64(300), censusFamily(t, c, "opv2:log:").Keys)

	require.Equal(t, CensusMethodEmpty, censusFamily(t, c, "opv2:").Method, "the parent excludes its child")
	require.Zero(t, censusFamily(t, c, "opv2:open:").Keys, "pre-registered A5 family reported at zero")

	require.Equal(t, CensusFamilyFiguresBasis, c.FamilyFiguresBasis)
	require.Contains(t, c.Notes, censusNoteKeyDefinition)
	requireNoteContains(t, c.Notes, "memtable")
	require.Contains(t, c.Notes, censusNoteHiddenSystem)
	require.Nil(t, c.LastExact, "no exact census has run")
	require.Nil(t, c.History, "history comes only from the exact census")
	requireCensusConserved(t, c)
}

func requireNoteContains(t *testing.T, notes []string, sub string) {
	t.Helper()
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return
		}
	}
	t.Fatalf("no note containing %q in %v", sub, notes)
}

func writeBigCatKeys(t *testing.T, p *PebbleStore, from, n int) {
	t.Helper()
	val := make([]byte, 1024)
	b := p.db.NewBatch()
	for i := from; i < from+n; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("cat:%06d", i)), val, nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
}

// S1/S4: a census flushes a memtable worth flushing, at most once per store
// per censusFlushMinInterval (fresh included), and says so in a note.
func TestDBCensus_FlushesTheMemtableAtMostOncePerInterval(t *testing.T) {
	p := newCensusRawStore(t)
	writeBigCatKeys(t, p, 0, 1200) // > censusFlushMinMemtable
	c := freshCensus(t, p)
	require.Equal(t, int64(1200), censusFamily(t, c, "cat:").Keys)
	requireNoteContains(t, c.Notes, "was flushed")
	requireCensusConserved(t, c)

	writeBigCatKeys(t, p, 1200, 1200)
	c = freshCensus(t, p)
	requireNoteContains(t, c.Notes, "last census flush")
	require.Equal(t, int64(1200), censusFamily(t, c, "cat:").Keys, "inside the interval the new writes stay in the memtable")
}

func TestDBCensus_SmallMemtableIsNotFlushed(t *testing.T) {
	p := newCensusRawStore(t)
	writeCensusKeys(t, p, 10, func(i int) string { return fmt.Sprintf("cat:%05d", i) })
	c := freshCensus(t, p)
	requireNoteContains(t, c.Notes, "memtable not flushed")
	require.Zero(t, censusFamily(t, c, "cat:").Keys)
}

func TestDBCensus_StraddlingTableIsEstimatedAndConserved(t *testing.T) {
	p := newCensusRawStore(t)
	b := p.db.NewBatch()
	for i := 0; i < 200; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book_file:BK:%05d", i)), []byte("file-row"), nil))
		require.NoError(t, b.Set([]byte(fmt.Sprintf("fpidx:%05d", i)), []byte("lsh"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())

	c := freshCensus(t, p)
	bf, fp := censusFamily(t, c, "book_file:"), censusFamily(t, c, "fpidx:")
	require.True(t, bf.Estimated)
	require.True(t, fp.Estimated)
	require.Positive(t, bf.ErrorBoundKeys)
	require.Positive(t, fp.ErrorBoundKeys)
	requireCensusConserved(t, c)
}

// TestDBCensus_EmptyFamiliesInsideAStraddlingTableGetZero pins the range
// probe: one small table holds book_file: and book_ver: keys, and every family
// and gap range between them in key order is empty. Span bytes are
// block-granular, so without the probe each empty range received a share.
func TestDBCensus_EmptyFamiliesInsideAStraddlingTableGetZero(t *testing.T) {
	p := newCensusRawStore(t)
	b := p.db.NewBatch()
	for i := 0; i < 200; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book_file:BK:%05d", i)), []byte("file-row"), nil))
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book_ver:BK:%020d", i)), []byte("snapshot"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())

	c := freshCensus(t, p)
	empty := []string{
		"book_file_acoustid:", "book_file_error:", "book_file_errors_by_book:", "book_file_gone:",
		"book_file_hash:", "book_file_id:", "book_file_orig_hash:", "book_file_path:", "book_file_pid:",
		"book_narrators:", "book_sig:", "book_tag:", unregisteredFamily,
	}
	for _, prefix := range empty {
		f := censusFamily(t, c, prefix)
		require.Equal(t, CensusMethodEmpty, f.Method, prefix)
		require.Zero(t, f.Keys, prefix)
		require.Zero(t, f.Entries, prefix)
		require.Zero(t, f.DiskBytes, prefix)
		require.Zero(t, f.Tables, prefix)
	}
	bf, bv := censusFamily(t, c, "book_file:"), censusFamily(t, c, "book_ver:")
	require.Equal(t, int64(400), bf.Keys+bv.Keys)
	require.Positive(t, bf.Keys)
	require.Positive(t, bv.Keys)
	requireCensusConserved(t, c)
}

// writeThenDeleteAuthors: 5000 book:author: and 5000 book:hash: keys in one
// table, then every book:author: key deleted (point tombstones), flushed.
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

// A family whose keys were all deleted holds only tombstones and shadowed
// versions; it must keep its entries and disk bytes.
func TestDBCensus_TombstoneOnlyFamilyIsNotDropped(t *testing.T) {
	p := newCensusRawStore(t)
	writeThenDeleteAuthors(t, p)

	c := freshCensus(t, p)
	au := censusFamily(t, c, "book:author:")
	require.Equal(t, CensusMethodEstimated, au.Method)
	require.Positive(t, au.Entries)
	require.Positive(t, au.Deletions)
	require.Positive(t, au.DiskBytes)
	require.Less(t, censusFamily(t, c, "book:hash:").Deletions, int64(5000),
		"book:hash: must not absorb all the book:author: tombstones")
	requireCensusConserved(t, c)

	ex := runExact(t, p)
	au = censusFamily(t, ex, "book:author:")
	require.Equal(t, CensusMethodExact, au.Method)
	require.Zero(t, au.Keys)
	require.Equal(t, int64(10000), au.Entries, "5000 shadowed puts plus 5000 tombstones")
	require.Equal(t, int64(10000), au.Deletions)
	h := censusFamily(t, ex, "book:hash:")
	require.Equal(t, int64(5000), h.Keys)
	require.Zero(t, h.Deletions)
}

func TestDBCensus_ProbeSeesTombstonesButNotEmptiness(t *testing.T) {
	p := newCensusRawStore(t)
	writeThenDeleteAuthors(t, p)
	ranges := keyFamilyRanges(keyFamilies)
	probes, err := p.censusProbeRanges(context.Background(), ranges, nil)
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

// writeCatAndDedup: 1000 cat: and 1000 dedup:r: keys in one table.
func writeCatAndDedup(t *testing.T, p *PebbleStore) {
	t.Helper()
	b := p.db.NewBatch()
	for i := 0; i < 1000; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("cat:%05d", i)), []byte("catalog-entry"), nil))
		require.NoError(t, b.Set([]byte(fmt.Sprintf("dedup:r:%05d", i)), []byte("rec"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, p.db.Flush())
}

// B2: an uncompacted DeleteRange over a whole family must not make the
// family look empty and hand its entries to the neighbour.
func TestDBCensus_DeleteRangeWholeFamily(t *testing.T) {
	p := newCensusRawStore(t)
	writeCatAndDedup(t, p)
	require.NoError(t, p.db.DeleteRange([]byte("cat:"), []byte("cat;"), pebble.Sync))
	require.NoError(t, p.db.Flush())

	c := freshCensus(t, p)
	cat, dd := censusFamily(t, c, "cat:"), censusFamily(t, c, "dedup:r:")
	require.Equal(t, CensusMethodEstimated, cat.Method, "a range-deleted family is not empty until compaction")
	require.Positive(t, cat.Entries)
	require.Positive(t, cat.DiskBytes)
	require.Positive(t, cat.ErrorBoundKeys, "its keys may all be covered by the range deletion")
	require.LessOrEqual(t, dd.Keys, int64(1000)+dd.ErrorBoundKeys)
	requireCensusConserved(t, c)

	requireNoteNames(t, c.Notes, "cat:")

	ex := runExact(t, p)
	cat = censusFamily(t, ex, "cat:")
	require.Zero(t, cat.Keys, "no live cat: key remains")
	requireNoteNames(t, ex.Notes, "cat:")
	require.Equal(t, int64(1000), censusFamily(t, ex, "dedup:r:").Keys)
}

func requireNoteNames(t *testing.T, notes []string, fam string) {
	t.Helper()
	for _, n := range notes {
		if strings.Contains(n, "range deletions overlap") && strings.Contains(n, fam) {
			return
		}
	}
	t.Fatalf("no range-deletion note naming %s in %v", fam, notes)
}

func TestDBCensus_DeleteRangePartialFamily(t *testing.T) {
	p := newCensusRawStore(t)
	writeCatAndDedup(t, p)
	require.NoError(t, p.db.DeleteRange([]byte("cat:00000"), []byte("cat:00500"), pebble.Sync))
	require.NoError(t, p.db.Flush())

	c := freshCensus(t, p)
	cat := censusFamily(t, c, "cat:")
	require.Equal(t, CensusMethodEstimated, cat.Method)
	require.Positive(t, cat.ErrorBoundKeys)
	requireCensusConserved(t, c)

	ex := runExact(t, p)
	cat = censusFamily(t, ex, "cat:")
	require.Equal(t, int64(500), cat.Keys)
	require.Equal(t, int64(1000), censusFamily(t, ex, "dedup:r:").Keys)
}

func TestRunExactCensus_CountsEveryFamilyAndStoresTheResult(t *testing.T) {
	p := newCensusRawStore(t)
	b := p.db.NewBatch()
	for i := 0; i < 200; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book_file:BK:%05d", i)), []byte("file-row"), nil))
		require.NoError(t, b.Set([]byte(fmt.Sprintf("fpidx:%05d", i)), []byte("lsh"), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	writeCensusKeys(t, p, 3, func(i int) string { return fmt.Sprintf("book_ver:BOOKA:%020d", i) })
	writeCensusKeys(t, p, 12, func(i int) string { return fmt.Sprintf("book_ver:BOOKB:%020d", i) })

	ex := runExact(t, p)
	for _, prefix := range []string{"book_file:", "fpidx:"} {
		f := censusFamily(t, ex, prefix)
		require.Equal(t, CensusMethodExact, f.Method)
		require.Equal(t, int64(200), f.Keys)
		require.Zero(t, f.Deletions)
		require.Positive(t, f.RawKeyBytes)
		require.Positive(t, f.RawValueBytes)
	}
	require.Equal(t, CensusMethodEmpty, censusFamily(t, ex, "cat:").Method)
	require.Positive(t, ex.BytesRead)
	var keys, dels, entries int64
	for _, f := range ex.Families {
		keys, dels, entries = keys+f.Keys, dels+f.Deletions, entries+f.Entries
	}
	require.Equal(t, keys, ex.TotalKeys, "exact totals are the sums of the exact figures")
	require.Equal(t, dels, ex.TotalDeletions)
	require.Equal(t, entries, ex.TotalEntries)
	require.NotNil(t, ex.History)
	require.Equal(t, int64(15), ex.History.Entries)
	require.Equal(t, int64(12), ex.History.Max)
	require.Equal(t, int64(1), ex.History.Buckets["1-9"])
	require.Equal(t, int64(1), ex.History.Buckets["10-49"])
	require.Equal(t, []BookHistoryCount{{"BOOKB", 12}, {"BOOKA", 3}}, ex.History.Top)
	require.False(t, ex.History.OrphansKnown, "no memdb on a raw store")

	last, err := p.LastExactCensus()
	require.NoError(t, err)
	require.NotNil(t, last)
	require.Equal(t, int64(200), censusFamily(t, last, "fpidx:").Keys)
	prog, err := p.exactCensusProgress()
	require.NoError(t, err)
	require.Nil(t, prog, "a finished run clears its progress")

	c := freshCensus(t, p)
	require.NotNil(t, c.LastExact)
	require.Equal(t, CensusKindExact, c.LastExact.Kind)
	require.Equal(t, int64(15), c.LastExact.History.Entries)
}

// B1: an interrupted run stops mid-run, publishes nothing, and the next run
// resumes from the saved progress.
func TestRunExactCensus_ResumesAfterInterruption(t *testing.T) {
	p := newCensusRawStore(t)
	writeCensusKeys(t, p, 100, func(i int) string { return fmt.Sprintf("act:info:%05d", i) })
	writeCensusKeys(t, p, 100, func(i int) string { return fmt.Sprintf("work:%05d", i) })

	ctx, cancel := context.WithCancel(context.Background())
	_, err := p.RunExactCensus(ctx, ExactCensusOptions{Progress: func(pr ExactCensusProgress, _ string) {
		if pr.FamiliesDone >= 3 {
			cancel()
		}
	}})
	require.ErrorIs(t, err, context.Canceled)
	prog, err := p.exactCensusProgress()
	require.NoError(t, err)
	require.NotNil(t, prog)
	require.GreaterOrEqual(t, prog.FamiliesDone, 3)
	require.Less(t, prog.FamiliesDone, prog.FamiliesAll, "the cancel stopped the pass mid-run")
	last, err := p.LastExactCensus()
	require.NoError(t, err)
	require.Nil(t, last, "a cancelled run publishes nothing")
	require.NotNil(t, freshCensus(t, p).ExactInProgress)

	ex := runExact(t, p)
	require.Equal(t, int64(100), censusFamily(t, ex, "act:info:").Keys)
	require.Equal(t, int64(100), censusFamily(t, ex, "work:").Keys)
	requireNoteContains(t, ex.Notes, "resumed")
}

// B1, the reviewer's case: a context cancelled before the call reads nothing
// and marks nothing done.
func TestRunExactCensus_CancelledBeforeTheCallDoesNothing(t *testing.T) {
	p := newCensusRawStore(t)
	writeCensusKeys(t, p, 100, func(i int) string { return fmt.Sprintf("work:%05d", i) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.RunExactCensus(ctx, ExactCensusOptions{})
	require.ErrorIs(t, err, context.Canceled)
	prog, err := p.exactCensusProgress()
	require.NoError(t, err)
	require.Nil(t, prog)
	last, err := p.LastExactCensus()
	require.NoError(t, err)
	require.Nil(t, last)
}

// A context cancelled mid-family (after the first iterator refresh) leaves
// the in-family position saved; the next run resumes at k+0x00 and counts
// every key exactly once, history included. Also exercises the refresh path
// on every check (N5).
func TestRunExactCensus_ResumesInsideAFamily(t *testing.T) {
	oldRefresh, oldCheck := exactCensusIterRefresh, exactCensusCheckBytes
	exactCensusIterRefresh, exactCensusCheckBytes = 0, 1
	t.Cleanup(func() {
		exactCensusIterRefresh, exactCensusCheckBytes = oldRefresh, oldCheck
		exactCensusAfterRefresh = nil
	})
	p := newCensusRawStore(t)
	writeCensusKeys(t, p, 2000, func(i int) string { return fmt.Sprintf("book_ver:BOOK%02d:%020d", i%10, i) })

	ctx, cancel := context.WithCancel(context.Background())
	refreshes := 0
	exactCensusAfterRefresh = func(fam string) {
		if fam == "book_ver:" {
			refreshes++
			if refreshes == 3 {
				cancel()
			}
		}
	}
	_, err := p.RunExactCensus(ctx, ExactCensusOptions{})
	require.ErrorIs(t, err, context.Canceled)
	st, err := p.loadExactCensusState()
	require.NoError(t, err)
	require.NotNil(t, st.Partial)
	require.Equal(t, "book_ver:", st.Partial.Family)
	require.Positive(t, st.Partial.Count.Live)
	require.Less(t, st.Partial.Count.Live, int64(2000))
	require.NotEmpty(t, st.Partial.Hist)

	exactCensusAfterRefresh = nil
	ex := runExact(t, p)
	bv := censusFamily(t, ex, "book_ver:")
	require.Equal(t, int64(2000), bv.Keys, "no key lost or counted twice across the resume")
	require.Equal(t, int64(2000), ex.History.Entries)
	require.Equal(t, int64(10), ex.History.BooksWithHistory)
	require.Equal(t, int64(200), ex.History.Max)
}

// S3: tombstone and shadowed-version walks are charged to the budget.
func TestRunExactCensus_ChargesTombstoneWalks(t *testing.T) {
	p := newCensusRawStore(t)
	writeThenDeleteAuthors(t, p)
	ex := runExact(t, p)
	au, h := censusFamily(t, ex, "book:author:"), censusFamily(t, ex, "book:hash:")
	require.Zero(t, au.Keys)
	require.GreaterOrEqual(t, ex.BytesRead, au.RawKeyBytes+h.RawKeyBytes,
		"the bytes stepped over under the tombstones count, not only live keys")
	require.Positive(t, au.RawKeyBytes)
}

func TestRunExactCensus_RestartDiscardsProgress(t *testing.T) {
	p := newCensusRawStore(t)
	writeCensusKeys(t, p, 100, func(i int) string { return fmt.Sprintf("work:%05d", i) })
	ctx, cancel := context.WithCancel(context.Background())
	_, err := p.RunExactCensus(ctx, ExactCensusOptions{Progress: func(pr ExactCensusProgress, _ string) {
		if pr.FamiliesDone >= 2 {
			cancel()
		}
	}})
	require.ErrorIs(t, err, context.Canceled)
	ex, err := p.RunExactCensus(context.Background(), ExactCensusOptions{Restart: true})
	require.NoError(t, err)
	for _, n := range ex.Notes {
		require.NotContains(t, n, "resumed", "a restart does not resume")
	}
	require.Equal(t, int64(100), censusFamily(t, ex, "work:").Keys)
}

func TestRunExactCensus_RespectsTheReadBudget(t *testing.T) {
	p := newCensusRawStore(t)
	val := make([]byte, 1024)
	b := p.db.NewBatch()
	for i := 0; i < 3000; i++ {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("cat:%05d", i)), val, nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	start := time.Now()
	ex, err := p.RunExactCensus(context.Background(), ExactCensusOptions{ReadBytesPerSec: 1 << 20})
	require.NoError(t, err)
	require.Equal(t, int64(3000), censusFamily(t, ex, "cat:").Keys)
	// ~3 MB at 1 MB/s with a 1 MB burst: at least ~2 s.
	require.GreaterOrEqual(t, time.Since(start), 1500*time.Millisecond)
}

func TestRunExactCensus_HistoryOrphansFromMemdb(t *testing.T) {
	p := newCensusTestStore(t, true)
	writeCensusKeys(t, p, 3, func(i int) string { return fmt.Sprintf("book_ver:BOOKA:%020d", i) })
	writeCensusKeys(t, p, 12, func(i int) string { return fmt.Sprintf("book_ver:BOOKB:%020d", i) })
	ex := runExact(t, p)
	require.True(t, ex.History.OrphansKnown)
	require.Equal(t, int64(2), ex.History.OrphanBooks)
	require.Equal(t, int64(15), ex.History.OrphanEntries)
}

func TestDBCensus_CachedWithinTTL(t *testing.T) {
	p := newCensusTestStore(t, true)
	first := freshCensus(t, p)
	require.False(t, first.Cached)
	second, err := p.DBCensus(context.Background(), CensusOptions{})
	require.NoError(t, err)
	require.True(t, second.Cached)
	require.Equal(t, first.GeneratedAt, second.GeneratedAt)

	second.Families[0].Keys = -1
	third, err := p.DBCensus(context.Background(), CensusOptions{})
	require.NoError(t, err)
	require.NotEqual(t, int64(-1), third.Families[0].Keys, "a caller's copy must not edit the cache")
	require.False(t, freshCensus(t, p).Cached)
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

// S3: the shared computation runs detached from the first caller's context.
func TestDBCensus_OneCallerCancellingDoesNotFailTheOthers(t *testing.T) {
	p := newCensusRawStore(t)
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
	<-entered

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
	require.ErrorIs(t, <-errA, context.Canceled)
	close(release)
	b := <-resB
	require.NoError(t, b.err)
	require.NotNil(t, b.c)
	require.NotEmpty(t, b.c.Families)
}

// A census taken before memdb is warm is cached only for censusColdCacheTTL.
func TestDBCensus_ColdMemdbIsCachedBriefly(t *testing.T) {
	p := newCensusRawStore(t) // no memdb: mem() is nil
	first, err := p.DBCensus(context.Background(), CensusOptions{})
	require.NoError(t, err)
	require.Nil(t, first.Retired)
	require.Contains(t, first.Notes, censusNoteMemdbCold)
	second, err := p.DBCensus(context.Background(), CensusOptions{})
	require.NoError(t, err)
	require.True(t, second.Cached, "a burst during warmup is served from cache")
	require.Nil(t, censusCacheGet(p.db, first.GeneratedAt.Add(censusColdCacheTTL+time.Second)),
		"but only for censusColdCacheTTL")
	require.NotNil(t, censusCacheGet(p.db, first.GeneratedAt.Add(censusColdCacheTTL/2)))
}

// N3: the package cache never holds more than censusCacheMaxStores stores.
func TestCensusCache_IsBounded(t *testing.T) {
	now := time.Now()
	for i := 0; i < censusCacheMaxStores+5; i++ {
		censusCachePut(new(pebble.DB), &DBCensus{}, now.Add(time.Duration(i)*time.Second), censusCacheTTL)
	}
	censusCache.mu.Lock()
	n := len(censusCache.m)
	censusCache.mu.Unlock()
	require.LessOrEqual(t, n, censusCacheMaxStores)
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
