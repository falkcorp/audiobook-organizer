// file: internal/books/store/snapshot_pins_test.go
// version: 1.0.0
// guid: b2f6e0a4-83d1-4c57-9a7e-5d1c4e8b2f93
// last-edited: 2026-11-17

package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// pinFixture is a store whose embedding store shares its DB, as in production,
// with one book edited until it has n snapshots.
type pinFixture struct {
	s  *PebbleStore
	es *EmbeddingStore
}

func newPinFixture(t *testing.T) *pinFixture {
	t.Helper()
	s, err := NewPebbleStoreInMemory(t.Name())
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	es := NewEmbeddingStoreFromStore(s)
	require.NotNil(t, es)
	return &pinFixture{s: s, es: es}
}

// bookWithSnapshots creates a book and edits it n times, so it holds n snapshots,
// and returns the id and the snapshot timestamps oldest first.
func (f *pinFixture) bookWithSnapshots(t *testing.T, title string, n int) (string, []int64) {
	t.Helper()
	b, err := f.s.CreateBook(&Book{Title: title, FilePath: "/test/" + title + ".m4b"})
	require.NoError(t, err)
	for i := range n {
		b.Title = fmt.Sprintf("%s, printing %d", title, i)
		_, err := f.s.UpdateBook(b.ID, b)
		require.NoError(t, err)
		time.Sleep(time.Millisecond) // snapshot keys are nanosecond timestamps
	}
	return b.ID, f.timestamps(t, b.ID)
}

func (f *pinFixture) timestamps(t *testing.T, id string) []int64 {
	t.Helper()
	snaps, err := f.s.GetBookSnapshots(id, 1000)
	require.NoError(t, err)
	out := make([]int64, 0, len(snaps))
	for i := len(snaps) - 1; i >= 0; i-- { // GetBookSnapshots is newest first
		out = append(out, snaps[i].Timestamp.UnixNano())
	}
	return out
}

func (f *pinFixture) journal(t *testing.T, e AutoMergeJournalEntry) string {
	t.Helper()
	if e.MergedAt == 0 {
		e.MergedAt = time.Now().UnixNano()
	}
	key, err := f.es.PutAutoMergeJournalEntry(e)
	require.NoError(t, err)
	return key
}

func TestPrune_KeepsASnapshotALiveJournalRowPointsAt(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 8)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", WinnerPreMergeTS: ts[1]})

	deleted, err := f.s.PruneBookSnapshots(id, 3)
	require.NoError(t, err)
	require.Equal(t, 4, deleted, "8 snapshots, newest 3 kept, 1 pinned: 4 deletable")

	left := f.timestamps(t, id)
	require.Len(t, left, 4)
	require.Equal(t, ts[1], left[0], "the pinned snapshot survives beyond the keep window")
	require.Equal(t, ts[5:], left[1:], "and the newest three are kept")
}

func TestPrune_PinsBothSidesOfAnEntry(t *testing.T) {
	f := newPinFixture(t)
	w, wts := f.bookWithSnapshots(t, "Batman", 6)
	l, lts := f.bookWithSnapshots(t, "Robin", 6)
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, WinnerPreMergeTS: wts[0], LoserPreMergeTS: lts[0]})

	for _, id := range []string{w, l} {
		_, err := f.s.PruneBookSnapshots(id, 2)
		require.NoError(t, err)
	}
	require.Contains(t, f.timestamps(t, w), wts[0], "the winner's snapshot is pinned")
	require.Contains(t, f.timestamps(t, l), lts[0], "the loser's snapshot is pinned")
}

func TestPrune_UndoneEntryReleasesItsSnapshots(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", WinnerPreMergeTS: ts[0], UndoneAt: time.Now().UnixNano()})

	deleted, err := f.s.PruneBookSnapshots(id, 2)
	require.NoError(t, err)
	require.Equal(t, 4, deleted, "an undone entry is refused by UnmergeAuto, so it pins nothing")
}

// editBook writes one more snapshot of a book.
func (f *pinFixture) editBook(t *testing.T, id, title string) {
	t.Helper()
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	b.Title = title
	_, err = f.s.UpdateBook(id, b)
	require.NoError(t, err)
	time.Sleep(time.Millisecond)
}

// A merge writes its journal entry BEFORE the merge and patches the snapshot
// timestamps in AFTER it, by finding "the earliest snapshot newer than the
// baseline". In between the entry names no snapshot, so it pins a RANGE: every
// snapshot of its books newer than the baseline it recorded.
func TestPrune_ProvisionalEntryPinsEverySnapshotNewerThanItsBaseline(t *testing.T) {
	f := newPinFixture(t)
	id, before := f.bookWithSnapshots(t, "Batman", 3)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, MergedAt: time.Now().UnixNano(), WinnerBaselineTS: before[2]})
	for i := range 3 { // the merge's own writes
		f.editBook(t, id, fmt.Sprintf("merged edit %d", i))
	}
	all := f.timestamps(t, id)
	require.Len(t, all, 6)

	deleted, err := f.s.PruneBookSnapshots(id, 1)
	require.NoError(t, err)
	require.Equal(t, 3, deleted, "only the snapshots at or before the baseline are deletable")
	require.Equal(t, all[3:], f.timestamps(t, id), "everything newer than the baseline survives, however small keep is")
}

// The pin set is cached. A snapshot written AFTER it was built must still be
// pinned: the merge's own writes land exactly then.
func TestPins_ProvisionalRangeCoversASnapshotWrittenAfterTheSetWasBuilt(t *testing.T) {
	f := newPinFixture(t)
	id, before := f.bookWithSnapshots(t, "Batman", 3)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, MergedAt: time.Now().UnixNano(), WinnerBaselineTS: before[2]})

	built, err := f.s.snapshotPins() // built now, with no snapshot newer than the baseline yet
	require.NoError(t, err)
	require.Zero(t, built.Len(), "no exact pin: the entry names no timestamp")
	require.Equal(t, 2, built.RangedBooks(), "both books of the entry are pinned by range")

	f.editBook(t, id, "the merge's write")
	f.editBook(t, id, "another")
	all := f.timestamps(t, id)

	again, err := f.s.snapshotPins()
	require.NoError(t, err)
	require.Same(t, built, again, "no journal write since, so the set was not rebuilt")
	require.True(t, again.Has(id, all[3]) && again.Has(id, all[4]), "snapshots written after the build are pinned by the range")
	require.False(t, again.Has(id, before[2]), "the baseline itself is not newer than the baseline")

	deleted, err := f.s.PruneBookSnapshots(id, 1)
	require.NoError(t, err)
	require.Equal(t, 3, deleted)
	require.Equal(t, all[3:], f.timestamps(t, id))
}

// Two provisional entries on one book (a cluster merge shares its winner) merge
// to the wider range: the lower floor.
func TestPrune_TwoProvisionalEntriesOnOneBookPinFromTheLowerFloor(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)
	now := time.Now().UnixNano()
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, MergedAt: now, WinnerBaselineTS: ts[3]})
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "nightwing", Provisional: true, MergedAt: now + 1, WinnerBaselineTS: ts[1]})

	deleted, err := f.s.PruneBookSnapshots(id, 1)
	require.NoError(t, err)
	require.Equal(t, 2, deleted, "only ts[0] and ts[1], at or below the lower floor, are deletable")
	require.Equal(t, ts[2:], f.timestamps(t, id))
}

// An entry that recorded no baseline (written before the field existed, or the
// book had no snapshot yet) cannot say where its range starts, so it pins all.
func TestPrune_ProvisionalEntryWithoutABaselinePinsEverySnapshotOfBothBooks(t *testing.T) {
	f := newPinFixture(t)
	w, _ := f.bookWithSnapshots(t, "Batman", 5)
	l, _ := f.bookWithSnapshots(t, "Robin", 5)
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, Provisional: true, MergedAt: time.Now().UnixNano()})

	for _, id := range []string{w, l} {
		deleted, err := f.s.PruneBookSnapshots(id, 1)
		require.NoError(t, err)
		require.Zero(t, deleted)
		require.Len(t, f.timestamps(t, id), 5)
	}
}

// A provisional entry whose merge failed stays provisional. The row is the
// evidence the merge may have run, so it keeps pinning for as long as it exists,
// however old, and the dry run counts it.
func TestPrune_StaleProvisionalEntryStillPinsAndIsCounted(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)
	old := time.Now().Add(-48 * time.Hour).UnixNano()
	key := f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, MergedAt: old, WinnerBaselineTS: ts[1]})

	deleted, err := f.s.PruneBookSnapshots(id, 1)
	require.NoError(t, err)
	require.Equal(t, 2, deleted, "ts[0] and ts[1] are not newer than the baseline; ts[2..4] are pinned; ts[5] is the kept one")
	require.Equal(t, ts[2:], f.timestamps(t, id))

	a, err := f.s.AuditMergeJournalSnapshots(10)
	require.NoError(t, err)
	require.Equal(t, 1, a.StaleProvisional)
	require.Equal(t, []string{key}, a.StaleProvisionalIDs)
}

func TestPrune_UndoneProvisionalEntryPinsNothing(t *testing.T) {
	f := newPinFixture(t)
	id, _ := f.bookWithSnapshots(t, "Batman", 6)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, UndoneAt: time.Now().UnixNano()})

	deleted, err := f.s.PruneBookSnapshots(id, 2)
	require.NoError(t, err)
	require.Equal(t, 4, deleted)
}

func TestPrune_SharedWinnerSnapshotStaysPinnedUntilTheLastEntryIsUndone(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)
	first := f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", WinnerPreMergeTS: ts[0], MergedAt: time.Now().UnixNano()})
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "nightwing", WinnerPreMergeTS: ts[0], MergedAt: time.Now().UnixNano() + 1})

	// Undo one of the two entries that name the same snapshot.
	e, err := f.es.GetAutoMergeJournalEntry(first)
	require.NoError(t, err)
	e.UndoneAt = time.Now().UnixNano() // MergedAt is kept: it is the row's key
	_, err = f.es.PutAutoMergeJournalEntry(*e)
	require.NoError(t, err)

	_, err = f.s.PruneBookSnapshots(id, 2)
	require.NoError(t, err)
	require.Contains(t, f.timestamps(t, id), ts[0], "the other live entry still needs it")
}

func TestAuditMergeJournalSnapshots(t *testing.T) {
	f := newPinFixture(t)
	w, wts := f.bookWithSnapshots(t, "Batman", 4)
	l, lts := f.bookWithSnapshots(t, "Robin", 4)
	now := time.Now().UnixNano()
	ok := f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, WinnerPreMergeTS: wts[0], LoserPreMergeTS: lts[0], MergedAt: now})
	lostWinner := f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, WinnerPreMergeTS: 12345, LoserPreMergeTS: lts[1], MergedAt: now + 1})
	lostLoser := f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, WinnerPreMergeTS: wts[1], LoserPreMergeTS: 777, MergedAt: now + 2})
	// Not counted: undone, provisional, and one that recorded no snapshot.
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, WinnerPreMergeTS: 1, UndoneAt: now, MergedAt: now + 3})
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, Provisional: true, MergedAt: now + 4}) // young: in flight
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, MergedAt: now + 5})
	stale := f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, Provisional: true, MergedAt: now - int64(3*time.Hour)})
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, Provisional: true, UndoneAt: now, MergedAt: now - int64(4*time.Hour)}) // undone: not stale
	_ = ok

	a, err := f.s.AuditMergeJournalSnapshots(10)
	require.NoError(t, err)
	require.Equal(t, 3, a.LiveEntries)
	require.Equal(t, 2, a.MissingSnapshotEntries)
	require.Equal(t, []string{lostWinner, lostLoser}, a.MissingJournalIDs)
	require.Zero(t, a.Truncated)
	require.Equal(t, 1, a.StaleProvisional, "only the old, not-undone provisional entry")
	require.Equal(t, []string{stale}, a.StaleProvisionalIDs)

	capped, err := f.s.AuditMergeJournalSnapshots(1)
	require.NoError(t, err)
	require.Equal(t, 2, capped.MissingSnapshotEntries, "the count is not capped, only the id list")
	require.Equal(t, []string{lostWinner}, capped.MissingJournalIDs)
	require.Equal(t, 1, capped.Truncated)
}

func TestAuditMergeJournalSnapshots_ReadsAndWritesNothing(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 3)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", WinnerPreMergeTS: ts[0]})
	before := dumpKV(t, f.s)
	_, err := f.s.AuditMergeJournalSnapshots(10)
	require.NoError(t, err)
	require.Equal(t, before, dumpKV(t, f.s), "the audit is read-only")
}

func TestAuditMergeJournalSnapshots_UndecodableRowIsAnError(t *testing.T) {
	f := newPinFixture(t)
	require.NoError(t, f.s.SetRaw(dedupAutoMergeJournalPfx+"ffffffffffffffff", []byte("{not json")))
	_, err := f.s.AuditMergeJournalSnapshots(10)
	require.Error(t, err, "an unreadable journal must not read as 'nothing lost'")
}

func TestPrune_HonoursAnEntryWrittenAfterThePinSetWasBuilt(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)

	// Build the cached set with no journal rows, then write one.
	plan, err := f.s.PlanBookSnapshotPrune(id, 2)
	require.NoError(t, err)
	require.Zero(t, plan.PinnedByJournal)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", WinnerPreMergeTS: ts[0]})

	deleted, err := f.s.PruneBookSnapshots(id, 2)
	require.NoError(t, err)
	require.Equal(t, 3, deleted, "6 snapshots, newest 2 kept, 1 pinned by the entry written after the cache was built")
	require.Contains(t, f.timestamps(t, id), ts[0])
}

func TestPrune_UndecodableJournalRowFailsClosed(t *testing.T) {
	f := newPinFixture(t)
	id, _ := f.bookWithSnapshots(t, "Batman", 6)
	require.NoError(t, f.s.SetRaw(dedupAutoMergeJournalPfx+"ffffffffffffffff", []byte("{not json")))
	autoMergeJournalGen.Add(1) // a raw write bypasses PutAutoMergeJournalEntry

	deleted, err := f.s.PruneBookSnapshots(id, 2)
	require.Error(t, err, "a row that cannot be read might be the one that names a snapshot")
	require.Zero(t, deleted)
	require.Len(t, f.timestamps(t, id), 6, "nothing was deleted")

	_, err = f.s.PlanBookSnapshotPrune(id, 2)
	require.Error(t, err, "the dry run must fail the same way the prune does")
}

func TestPlanBookSnapshotPrune_MatchesWhatPruneDeletes(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 9)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", WinnerPreMergeTS: ts[0], LoserPreMergeTS: 0})
	f.journal(t, AutoMergeJournalEntry{WinnerID: "robin", LoserID: id, LoserPreMergeTS: ts[2], MergedAt: time.Now().UnixNano() + 1})

	plan, err := f.s.PlanBookSnapshotPrune(id, 3)
	require.NoError(t, err)
	require.Equal(t, BookSnapshotPrunePlan{Total: 9, Delete: 4, PinnedByJournal: 2}, plan)
	require.Len(t, f.timestamps(t, id), 9, "planning deletes nothing")

	deleted, err := f.s.PruneBookSnapshots(id, 3)
	require.NoError(t, err)
	require.Equal(t, plan.Delete, deleted)
	require.Len(t, f.timestamps(t, id), plan.Total-plan.Delete)
}

func TestPlanSnapshotPrune_PinnedSnapshotInsideTheKeepWindowIsNotCountedAsPinned(t *testing.T) {
	keys := [][]byte{[]byte("book_ver:b:1"), []byte("book_ver:b:2"), []byte("book_ver:b:3")}
	pins := &SnapshotPins{byBook: map[string]map[int64]struct{}{}}
	pins.add("b", 3)
	toDelete, plan := planSnapshotPrune("b", keys, 1, pins)
	require.Len(t, toDelete, 2)
	require.Equal(t, BookSnapshotPrunePlan{Total: 3, Delete: 2}, plan, "a pin the keep window already protects is not a reason a snapshot survived")
}

func TestSnapshotPins_BuiltOncePerGenerationNotOncePerBook(t *testing.T) {
	f := newPinFixture(t)
	f.journal(t, AutoMergeJournalEntry{WinnerID: "batman", LoserID: "robin", WinnerPreMergeTS: 5})

	first, err := f.s.snapshotPins()
	require.NoError(t, err)

	var wg sync.WaitGroup
	got := make([]*SnapshotPins, 16)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, perr := f.s.snapshotPins()
			if perr != nil {
				t.Error(perr)
			}
			got[i] = p
		}()
	}
	wg.Wait()
	for _, p := range got {
		require.Same(t, first, p, "every caller in one generation shares the one built set")
	}

	f.journal(t, AutoMergeJournalEntry{WinnerID: "batman", LoserID: "alfred", WinnerPreMergeTS: 6})
	second, err := f.s.snapshotPins()
	require.NoError(t, err)
	require.NotSame(t, first, second, "a journal write rebuilds the set")
	require.True(t, second.Has("batman", 6))
}

// dumpKV is every key and value in the store's DB, for "wrote nothing" checks.
func dumpKV(t *testing.T, s *PebbleStore) map[string]string {
	t.Helper()
	iter, err := s.DB().NewIter(&pebble.IterOptions{})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	out := map[string]string{}
	for iter.First(); iter.Valid(); iter.Next() {
		out[string(iter.Key())] = string(iter.Value())
	}
	require.NoError(t, iter.Error())
	return out
}
