// file: internal/database/memdb_integrity_test.go
// version: 1.0.0
// guid: 2b8f61d4-9c07-4e3a-a154-8d29f0b7e3c6
// last-edited: 2026-08-23

package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// The unfiltered series reference counter must not answer from a memdb that is
// known to be missing rows.
//
// PR #2782 hardened getAllSeriesBookRefCountsPebble to abort on an undecodable
// book row, because a short count reads as "referenced by nothing" and
// dedup.series-prune deletes on the strength of it. But
// PebbleStore.GetAllSeriesBookRefCounts dispatches to the memdb whenever
// UseMemDB is set, and pebble_store.go hardcodes that to true, so in production
// the hardened scan never ran. The memdb branch had no equivalent guard: warmup
// had already dropped the undecodable rows and published the memdb as complete.
//
// These tests pin all three halves of the fix — that a clean warmup is NOT
// flagged (the positive control), that a decode failure IS, and that the store
// falls through to Pebble rather than going quiet.

// seedSeriesBook writes one book that is the ONLY reference to its series, and
// returns the book ID and series ID. Every test below turns on that book: if
// the counter loses it, the series counts zero and becomes deletable.
func seedSeriesBook(t *testing.T, store *PebbleStore, n int) (string, int) {
	t.Helper()

	series, err := store.CreateSeries(fmt.Sprintf("Lone Series %d", n), nil)
	require.NoError(t, err)

	hash := fmt.Sprintf("integrityhash%03d", n)
	book, err := store.CreateBook(&Book{
		Title:    fmt.Sprintf("Integrity Book %03d", n),
		FilePath: fmt.Sprintf("/tmp/integrity_%03d.m4b", n),
		FileHash: &hash,
		SeriesID: &series.ID,
	})
	require.NoError(t, err)
	return book.ID, series.ID
}

// TestSeriesRefCounts_CleanWarmupIsNotFlagged is the POSITIVE CONTROL, and it
// is the more important of the pair.
//
// A guard that refuses everything passes every fail-open test while quietly
// turning dedup.series-prune into a no-op — which looks like safety and is
// actually the guard being inert. The two loss sites sit next to control flow
// that legitimately returns (false, nil) on nearly every key (the
// `strings.Count(key, ":") != 1` secondary-index skip, and the list-valued
// phases' unconditional return), so a fix that hooked "callback returned false"
// instead of the two real failures would mark every table incomplete on every
// warmup and still pass the corruption test below.
func TestSeriesRefCounts_CleanWarmupIsNotFlagged(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	_, seriesID := seedSeriesBook(t, store, 1)

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))

	require.Empty(t, mem.LostRows(),
		"a warmup over well-formed rows must record NO losses; if it does, the "+
			"guard is hooked to normal control flow and every ref count will "+
			"refuse forever")

	counts, err := mem.GetAllSeriesBookRefCounts()
	require.NoError(t, err, "an intact memdb must answer, not refuse")
	require.Equal(t, 1, counts[seriesID],
		"the seeded book is the only reference to its series and must be counted")
}

// TestSeriesRefCounts_UndecodableRowIsNotSilentlyDropped is the fail-open
// repro. Before the fix this returned a map with the series ABSENT and a nil
// error — "referenced by nothing" — while the book was still on disk holding
// the series_id.
//
// The corruption is a DECODE failure specifically. That is the path warmup
// never counted: json.Unmarshal failures returned (false, nil) without
// touching skips, so skipped_total read 0 through a warmup that had dropped
// the row, and a fix keyed on the pre-existing skips map would not have caught
// this at all.
func TestSeriesRefCounts_UndecodableRowIsNotSilentlyDropped(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	bookID, seriesID := seedSeriesBook(t, store, 2)

	// Corrupt the book's stored value in place. The key still exists and still
	// sorts inside the scan range, so the row is visited and then lost.
	require.NoError(t, store.db.Set([]byte("book:"+bookID), []byte("{not json"), pebble.Sync))

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store),
		"one bad row must not abort the whole warmup -- that resilience is "+
			"deliberate and is exactly why the loss has to be tracked instead")

	require.Positive(t, mem.LostRows()[memTableBooks],
		"an undecodable book value must be recorded as a lost row")

	_, err = mem.GetAllSeriesBookRefCounts()
	require.Error(t, err, "the memdb must refuse rather than return a short count")
	require.ErrorIs(t, err, ErrMemdbIncomplete)

	// End to end through the store, which is what every caller actually holds.
	// Pebble is corrupt too here, so the hardened scan aborts and the whole
	// call fails closed. What must NOT happen is the pre-fix behaviour: a map
	// missing seriesID returned with a nil error.
	store.memPtr.Store(mem)
	counts, err := store.GetAllSeriesBookRefCounts()
	require.Error(t, err,
		"a store that cannot read the row holding the only reference to a "+
			"series must refuse to answer, not report the series as unreferenced")
	require.Zero(t, counts[seriesID],
		"sanity: no count survives the refusal")
}

// TestSeriesRefCounts_FallsThroughToPebbleWhenMemdbIsIncomplete pins the other
// branch: the memdb lost a row but Pebble is intact, so the store must answer
// CORRECTLY from the authoritative scan rather than propagating the refusal.
//
// That distinction is the whole reason the fall-through exists. Refusing would
// be safe but would stall the nightly prune until the next restart; Pebble can
// still answer, so it should.
//
// The loss is injected directly rather than by corrupting a row, because this
// case requires memdb to be incomplete while Pebble is READABLE — which is what
// an insert rejected by a memdb index rule produces, and what a corrupted value
// by construction cannot.
func TestSeriesRefCounts_FallsThroughToPebbleWhenMemdbIsIncomplete(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	_, seriesID := seedSeriesBook(t, store, 3)

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))
	require.Empty(t, mem.LostRows(), "precondition: the warmup itself is clean")

	// Simulate a row memdb could not admit but Pebble holds fine.
	mem.recordLostRows(memTableBooks, 1)
	store.memPtr.Store(mem)

	_, memErr := mem.GetAllSeriesBookRefCounts()
	require.ErrorIs(t, memErr, ErrMemdbIncomplete,
		"precondition: the memdb half refuses")

	counts, err := store.GetAllSeriesBookRefCounts()
	require.NoError(t, err,
		"Pebble is intact and authoritative, so the store must fall through "+
			"and answer rather than propagate the memdb's refusal")
	require.Equal(t, 1, counts[seriesID],
		"the fall-through must produce the real count, not an empty map")
}

// TestRequireTablesComplete_OnlyFiresForTheNamedTables guards the blast radius.
// A loss in an unrelated table must not disable the series counter: over-
// refusing is the failure mode that makes a guard inert.
func TestRequireTablesComplete_OnlyFiresForTheNamedTables(t *testing.T) {
	mem, err := NewMemStore()
	require.NoError(t, err)

	mem.recordLostRows(memTableBlockedHashes, 3)

	require.NoError(t, mem.requireTablesComplete("series reference count", memTableBooks),
		"a loss in blocked_hashes says nothing about the books table")

	err = mem.requireTablesComplete("series reference count", memTableBooks, memTableBlockedHashes)
	require.ErrorIs(t, err, ErrMemdbIncomplete)
	require.Contains(t, err.Error(), memTableBlockedHashes+"=3",
		"the error must name the table and count so the log says what was untrusted")

	// And a reset -- what a fresh warmup does -- clears it.
	mem.resetLostRows()
	require.NoError(t, mem.requireTablesComplete("series reference count", memTableBlockedHashes))
	require.Empty(t, mem.LostRows())
}

// TestWarmupCountsDecodeFailuresInSkippedTotal pins the observability half.
// skipped_total is the field an operator greps to decide whether a restart lost
// anything; it counted insert rejections only, so a library full of undecodable
// rows reported zero skips.
func TestWarmupCountsDecodeFailuresInSkippedTotal(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	bookID, _ := seedSeriesBook(t, store, 4)
	require.NoError(t, store.db.Set([]byte("book:"+bookID), []byte("{not json"), pebble.Sync))

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))

	lost := mem.LostRows()
	require.Equal(t, 1, lost[memTableBooks])
	require.Len(t, lost, 1, "only the books table lost a row")

	// The counter is per-table and additive, and ErrMemdbIncomplete is the
	// sentinel every caller matches on.
	require.True(t, errors.Is(
		mem.requireTablesComplete("x", memTableBooks), ErrMemdbIncomplete))
}
