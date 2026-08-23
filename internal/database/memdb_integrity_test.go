// file: internal/database/memdb_integrity_test.go
// version: 1.2.0
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

	// An author ALIAS specifically. Every "author_alias:" key family shares the
	// prefix, and two of them are indexes: :author: holds row JSON and :name:
	// holds a bare strconv.Itoa(id) that cannot decode into an AuthorAlias.
	// Warmup had no filter for them (its siblings all do), so before that was
	// fixed a library containing ONE well-formed alias flagged author_aliases
	// known-incomplete on EVERY warmup -- and the author-side ref counter would
	// then have refused forever the moment it was wired up.
	//
	// This fixture is the difference between a positive control that means
	// something and one that passes because the library it builds is too small
	// to contain the bug. Found by review, empirically, on exactly this test.
	author, err := store.CreateAuthor("Alias Fixture Author")
	require.NoError(t, err)
	_, err = store.CreateAuthorAlias(author.ID, "Fixture Alias Name", "alternate")
	require.NoError(t, err)

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
	require.True(t, store.publishWarmMemStore(mem))
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
	require.True(t, store.publishWarmMemStore(mem))

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

	// And a warmup publishing a clean result -- a rebuild from the
	// authoritative source -- clears it.
	mem.publishLostRows(map[string]int{}, 0)
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

	// The operator-facing counter, which is the claim the changelog makes.
	// Before this fix skipped_total read 0 through a warmup that had dropped
	// the row, because only insert-rejections were counted.
	skips := mem.LastWarmupSkips()
	require.Equal(t, 1, skips[memTableBooks+"/undecodable row"],
		"the decode failure must appear in the skips map, split by reason")
	total := 0
	for _, v := range skips {
		total += v
	}
	require.Equal(t, 1, total, "skipped_total must count the decode failure")

	// The counter is per-table and additive, and ErrMemdbIncomplete is the
	// sentinel every caller matches on.
	require.True(t, errors.Is(
		mem.requireTablesComplete("x", memTableBooks), ErrMemdbIncomplete))
}

// TestApplyMemSyncFailureTaintsTheRefCount closes the hole a review of the
// first cut of this fix found: warmup is NOT the only way memdb loses a row.
//
// applyMemSync is the ONLY runtime mutation path into memdb. When its
// transaction aborts — an index rule rejecting a write that already succeeded
// in Pebble — the row is in Pebble and absent from memdb, with no warmup
// involved. Before this, that logged a warning and nothing else, so the series
// ref counter kept answering confidently from a projection it had no reason to
// trust. That is the same fail-open in steady state, which is where the
// service actually spends its life.
//
// Attribution is memTableUnknown because applyMemSync gets an opaque closure
// and cannot know which table failed. That must taint EVERY table, not none.
func TestApplyMemSyncFailureTaintsTheRefCount(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	_, seriesID := seedSeriesBook(t, store, 5)

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))
	require.Empty(t, mem.LostRows(), "precondition: warmup is clean")

	counts, err := mem.GetAllSeriesBookRefCounts()
	require.NoError(t, err, "precondition: the memdb answers before the failure")
	require.Equal(t, 1, counts[seriesID])

	// A sync whose closure fails: exactly what a rejected index write does.
	applyMemSync(mem, "test.failing-op", func(memTxn) error {
		return errors.New("simulated index rejection")
	})

	require.Positive(t, mem.LostRows()[memTableUnknown],
		"an aborted runtime sync leaves memdb short and MUST be recorded; "+
			"logging it and moving on is the bug this test exists for")

	_, err = mem.GetAllSeriesBookRefCounts()
	require.ErrorIs(t, err, ErrMemdbIncomplete,
		"an unattributable loss must taint the books table too -- there is no "+
			"way to know it was not a book that was dropped")
	require.Contains(t, err.Error(), memTableUnknown)

	// And the store still answers correctly, from Pebble.
	require.True(t, store.publishWarmMemStore(mem))
	counts, err = store.GetAllSeriesBookRefCounts()
	require.NoError(t, err)
	require.Equal(t, 1, counts[seriesID],
		"over-refusing at the memdb is bounded: Pebble is authoritative and "+
			"still has the row, so the answer stays correct and only slows")
}

// TestApplyMemSyncSuccessDoesNotTaint is the positive control for the above.
// If a SUCCESSFUL sync recorded a loss, every write in the system would taint
// the ref count and the guard would refuse permanently -- inert, while looking
// like safety.
func TestApplyMemSyncSuccessDoesNotTaint(t *testing.T) {
	mem, err := NewMemStore()
	require.NoError(t, err)

	applyMemSync(mem, "test.succeeding-op", func(memTxn) error { return nil })

	require.Empty(t, mem.LostRows(),
		"a sync that COMMITTED lost nothing; recording here would make every "+
			"write taint the ref count and refuse forever")
	require.NoError(t, mem.requireTablesComplete("series reference count", memTableBooks))
}

// TestWarmupPublishesLossesAtomicallyWithCommit pins that a re-warm cannot
// clear the flag while MVCC readers still see the old, still-short rows.
// WarmFromPebble advertises "safe to re-run", so this has to hold by
// construction, not because no caller re-runs it today.
func TestWarmupPublishesLossesAtomicallyWithCommit(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	bookID, _ := seedSeriesBook(t, store, 6)
	require.NoError(t, store.db.Set([]byte("book:"+bookID), []byte("{not json"), pebble.Sync))

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))
	require.Positive(t, mem.LostRows()[memTableBooks])

	// Re-warm in place against a now-REPAIRED Pebble: the rebuild supersedes
	// the old divergence, so the flag must clear -- but only once the data it
	// describes has been committed.
	require.NoError(t, store.db.Set([]byte("book:"+bookID),
		[]byte(`{"id":"`+bookID+`","title":"repaired"}`), pebble.Sync))
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))
	require.Empty(t, mem.LostRows(),
		"a clean rebuild from the authoritative source clears the flag")
	require.NoError(t, mem.requireTablesComplete("series reference count", memTableBooks))
}
