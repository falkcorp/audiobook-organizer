// file: internal/books/store/snapshot_pins_test.go
// version: 1.1.0
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

// pinnedAmong is the pinned subset of a book's snapshots as the prune sees it.
func (f *pinFixture) pinnedAmong(t *testing.T, id string) []int64 {
	t.Helper()
	pins, err := f.s.snapshotPins()
	require.NoError(t, err)
	existing := f.timestamps(t, id)
	got := pins.Pinned(id, existing)
	var out []int64
	for _, ts := range existing {
		if _, ok := got[ts]; ok {
			out = append(out, ts)
		}
	}
	return out
}

func TestEarliestSnapshotAbove(t *testing.T) {
	require.Equal(t, int64(0), EarliestSnapshotAbove(5, nil))
	require.Equal(t, int64(0), EarliestSnapshotAbove(9, []int64{3, 9, 5}), "nothing above the floor")
	require.Equal(t, int64(6), EarliestSnapshotAbove(5, []int64{9, 6, 3, 5, 12}), "strictly above, whatever the order")
	require.Equal(t, int64(3), EarliestSnapshotAbove(0, []int64{9, 3, 5}), "floor 0 is the oldest snapshot")
}

// A merge writes its journal entry BEFORE the merge and patches the snapshot
// timestamps in AFTER it, by finding "the earliest snapshot newer than the
// baseline". In between the entry names none, so it pins exactly that snapshot:
// the first one above its baseline, and nothing else of the book.
func TestPrune_ProvisionalEntryPinsOnlyTheFirstSnapshotAboveItsBaseline(t *testing.T) {
	f := newPinFixture(t)
	id, before := f.bookWithSnapshots(t, "Batman", 3)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, BaselinesRecorded: true,
		MergedAt: time.Now().UnixNano(), WinnerBaselineTS: before[2]})
	for i := range 3 { // the merge's own writes
		f.editBook(t, id, fmt.Sprintf("merged edit %d", i))
	}
	all := f.timestamps(t, id)
	require.Len(t, all, 6)

	deleted, err := f.s.PruneBookSnapshots(id, 1)
	require.NoError(t, err)
	require.Equal(t, 4, deleted, "the three at or below the baseline and the second merge write are deletable")
	require.Equal(t, []int64{all[3], all[5]}, f.timestamps(t, id),
		"the first snapshot above the baseline (what the patch will name) and the newest one survive, nothing more")
}

// The pin set is cached, but a floor is resolved against the snapshots the book has
// WHEN THE PRUNE DECIDES, not when the set was built: the merge's own writes land
// after the set was built, and the snapshot the patch names is one of them.
func TestPins_FloorResolvesAgainstSnapshotsWrittenAfterTheSetWasBuilt(t *testing.T) {
	f := newPinFixture(t)
	id, before := f.bookWithSnapshots(t, "Batman", 3)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, BaselinesRecorded: true,
		MergedAt: time.Now().UnixNano(), WinnerBaselineTS: before[2]})

	built, err := f.s.snapshotPins() // no snapshot above the baseline exists yet
	require.NoError(t, err)
	require.Zero(t, built.Len(), "no exact pin: the entry names no timestamp")
	require.Equal(t, []int64{before[2]}, built.Floors(id))
	require.Empty(t, f.pinnedAmong(t, id), "nothing above the floor yet, so nothing to pin yet")

	f.editBook(t, id, "the merge's write")
	f.editBook(t, id, "another")
	all := f.timestamps(t, id)

	again, err := f.s.snapshotPins()
	require.NoError(t, err)
	require.Same(t, built, again, "no journal write since, so the set was not rebuilt: the answer is computed on demand")
	require.Equal(t, []int64{all[3]}, f.pinnedAmong(t, id), "the first snapshot above the floor, written after the build")

	deleted, err := f.s.PruneBookSnapshots(id, 1)
	require.NoError(t, err)
	require.Equal(t, 3, deleted)
	require.Equal(t, []int64{all[3], all[4]}, f.timestamps(t, id))
}

// Two provisional entries on one book (a cluster merge shares its winner) each
// keep their OWN pin. Merging them to the lower floor would pin the first
// snapshot above the lower floor only and leave the newer in-flight merge's
// pre-merge snapshot unpinned.
func TestPrune_TwoProvisionalEntriesOnOneBookEachKeepTheirOwnPin(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)
	now := time.Now().UnixNano()
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, BaselinesRecorded: true, MergedAt: now, WinnerBaselineTS: ts[3]})
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "nightwing", Provisional: true, BaselinesRecorded: true, MergedAt: now + 1, WinnerBaselineTS: ts[1]})

	require.ElementsMatch(t, []int64{ts[2], ts[4]}, f.pinnedAmong(t, id), "the first snapshot above each row's own floor")
	deleted, err := f.s.PruneBookSnapshots(id, 1)
	require.NoError(t, err)
	require.Equal(t, 3, deleted, "ts[0], ts[1], ts[3]")
	require.Equal(t, []int64{ts[2], ts[4], ts[5]}, f.timestamps(t, id))
}

// An entry written before baselines were recorded has no baseline, so its floor is
// MergedAt: set after the baselines would have been read and before the merge
// wrote anything. The old behaviour for it (floor 0) pinned every snapshot.
func TestPrune_LegacyProvisionalEntryUsesMergedAtAsItsFloor(t *testing.T) {
	f := newPinFixture(t)
	w, ts := f.bookWithSnapshots(t, "Batman", 6)
	l, _ := f.bookWithSnapshots(t, "Robin", 3)
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, Provisional: true, MergedAt: ts[2] + 1}) // no BaselinesRecorded

	require.Equal(t, []int64{ts[3]}, f.pinnedAmong(t, w), "the first snapshot written after MergedAt")
	deleted, err := f.s.PruneBookSnapshots(w, 1)
	require.NoError(t, err)
	require.Equal(t, 4, deleted, "not 0: a legacy row no longer pins the whole history")
	require.Equal(t, []int64{ts[3], ts[5]}, f.timestamps(t, w))
}

// With no recorded baseline AND no MergedAt there is nothing to bound the entry,
// and the first snapshot above 0 is the book's oldest, the same answer the patch
// itself would give for a baseline of 0.
func TestPrune_ProvisionalEntryWithNoFloorAtAllPinsTheOldestSnapshot(t *testing.T) {
	f := newPinFixture(t)
	w, ts := f.bookWithSnapshots(t, "Batman", 5)
	pins := &SnapshotPins{byBook: map[string]map[int64]struct{}{}, floors: map[string][]int64{}}
	e := AutoMergeJournalEntry{WinnerID: w, LoserID: "robin", Provisional: true}
	wf, lf := e.provisionalFloors()
	pins.addFloor(e.WinnerID, wf)
	pins.addFloor(e.LoserID, lf)
	got := pins.Pinned(w, ts)
	require.Len(t, got, 1)
	require.Contains(t, got, ts[0])
}

// A recorded baseline of 0 is a real baseline (the book had no snapshot when the
// merge began), not "unknown", so it is not replaced by MergedAt.
func TestPrune_RecordedZeroBaselineIsARealBaseline(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 4)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, BaselinesRecorded: true, MergedAt: time.Now().UnixNano()})
	require.Equal(t, []int64{ts[0]}, f.pinnedAmong(t, id), "the first snapshot above 0 is the oldest")
}

// A provisional entry whose merge failed in a way that does not prove nothing was
// written stays provisional. The row is the evidence the merge may have run, so it
// keeps pinning for as long as it exists, however old, and the dry run counts it.
// What it pins is one snapshot per book.
func TestPrune_StaleProvisionalEntryStillPinsOneSnapshotAndIsCounted(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)
	old := time.Now().Add(-48 * time.Hour).UnixNano()
	key := f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, BaselinesRecorded: true, MergedAt: old, WinnerBaselineTS: ts[1]})

	deleted, err := f.s.PruneBookSnapshots(id, 1)
	require.NoError(t, err)
	require.Equal(t, 4, deleted, "ts[0], ts[1], ts[3], ts[4]; ts[2] is the pin and ts[5] the kept one")
	require.Equal(t, []int64{ts[2], ts[5]}, f.timestamps(t, id))

	a, err := f.s.AuditMergeJournalSnapshots(10)
	require.NoError(t, err)
	require.Equal(t, 1, a.StaleProvisional)
	require.Equal(t, []string{key}, a.StaleProvisionalIDs)
}

// A closed (aborted) entry never merged anything: it pins nothing and is not a
// stale provisional row.
func TestPrune_AbortedProvisionalEntryPinsNothingAndIsNotStale(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)
	f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", Provisional: true, BaselinesRecorded: true,
		MergedAt: time.Now().Add(-48 * time.Hour).UnixNano(), WinnerBaselineTS: ts[1], AbortedAt: time.Now().UnixNano()})

	require.Empty(t, f.pinnedAmong(t, id))
	deleted, err := f.s.PruneBookSnapshots(id, 2)
	require.NoError(t, err)
	require.Equal(t, 4, deleted)

	a, err := f.s.AuditMergeJournalSnapshots(10)
	require.NoError(t, err)
	require.Zero(t, a.StaleProvisional, "closed rows are not counted")
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
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, Provisional: true, UndoneAt: now, MergedAt: now - int64(4*time.Hour)})  // undone: not stale
	f.journal(t, AutoMergeJournalEntry{WinnerID: w, LoserID: l, Provisional: true, AbortedAt: now, MergedAt: now - int64(5*time.Hour)}) // closed: not stale
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

func TestAuditMergeJournalSnapshots_WritesNothing(t *testing.T) {
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
	pins := &SnapshotPins{byBook: map[string]map[int64]struct{}{}, floors: map[string][]int64{}}
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
	require.Contains(t, second.Pinned("batman", []int64{5, 6}), int64(6))
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

// The re-plan loop. A journal row written between the plan and the commit makes
// the plan stale, so it is redone; a journal that changes on every attempt ends
// the call with an error and deletes nothing.
func TestPruneBookSnapshots_ReplansWhenAJournalRowLandsBeforeTheCommit(t *testing.T) {
	f := newPinFixture(t)
	id, ts := f.bookWithSnapshots(t, "Batman", 6)
	var attempts []int
	pruneBeforeCommitHook = func(attempt int) {
		attempts = append(attempts, attempt)
		if attempt == 0 { // a merge's entry lands after the first plan
			f.journal(t, AutoMergeJournalEntry{WinnerID: id, LoserID: "robin", WinnerPreMergeTS: ts[0]})
		}
	}
	t.Cleanup(func() { pruneBeforeCommitHook = nil })

	deleted, err := f.s.PruneBookSnapshots(id, 2)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, attempts, "planned twice: the stale plan was not committed")
	require.Equal(t, 3, deleted)
	require.Contains(t, f.timestamps(t, id), ts[0], "the entry that landed mid-call is honoured")
}

func TestPruneBookSnapshots_GivesUpAfterFiveChangedJournalsAndDeletesNothing(t *testing.T) {
	f := newPinFixture(t)
	id, _ := f.bookWithSnapshots(t, "Batman", 6)
	n := 0
	pruneBeforeCommitHook = func(attempt int) {
		n++
		f.journal(t, AutoMergeJournalEntry{WinnerID: "alfred", LoserID: "robin", WinnerPreMergeTS: int64(100 + attempt), MergedAt: time.Now().UnixNano() + int64(attempt)})
	}
	t.Cleanup(func() { pruneBeforeCommitHook = nil })

	deleted, err := f.s.PruneBookSnapshots(id, 2)
	require.Error(t, err)
	require.Zero(t, deleted)
	require.Equal(t, pruneStablePinsAttempts, n)
	require.Len(t, f.timestamps(t, id), 6, "nothing was deleted")
}

// One unreadable journal row: the pin build fails, names the row, and the failure
// is remembered for pinFailureTTL so a whole-library prune does not rescan the
// journal once per book. A journal write, or the TTL, ends the memory.
func TestSnapshotPins_UnreadableRowIsNamedAndTheFailureIsRememberedForAWhile(t *testing.T) {
	f := newPinFixture(t)
	id, _ := f.bookWithSnapshots(t, "Batman", 3)
	badKey := dedupAutoMergeJournalPfx + "ffffffffffffffff"
	require.NoError(t, f.s.SetRaw(badKey, []byte("{not json")))
	autoMergeJournalGen.Add(1)

	now := time.Now()
	pinClock = func() time.Time { return now }
	t.Cleanup(func() { pinClock = time.Now })

	err := f.s.CheckSnapshotPins()
	var unreadable *MergeJournalRowUnreadableError
	require.ErrorAs(t, err, &unreadable)
	require.Equal(t, badKey, unreadable.Key)
	require.Contains(t, err.Error(), "prune is blocked until it is repaired")

	// Repair the row behind the store's back (no journal write, so no new
	// generation): inside the TTL the failure is answered from memory.
	require.NoError(t, f.s.SetRaw(badKey, []byte(`{"winner_id":"a","loser_id":"b"}`)))
	require.ErrorAs(t, f.s.CheckSnapshotPins(), &unreadable, "remembered: the journal is not rescanned per call")
	_, err = f.s.PlanBookSnapshotPrune(id, 1)
	require.ErrorAs(t, err, &unreadable)

	now = now.Add(pinFailureTTL + time.Second)
	require.NoError(t, f.s.CheckSnapshotPins(), "after the TTL the repaired row reads fine")
}
