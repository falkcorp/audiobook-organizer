// file: internal/database/pebble_store_journal_clock_test.go
// version: 2.0.0
// guid: 7e2b94d1-35c8-4a60-9f17-d08a6b3c5e29
// last-edited: 2026-10-22

package database

import (
	"encoding/binary"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// steppingClock is a wall clock the test moves by hand, backwards included.
type steppingClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *steppingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *steppingClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func persistedClock(t testing.TB, p *PebbleStore) (int64, bool) {
	t.Helper()
	v, closer, err := p.db.Get([]byte(journalClockKey))
	if err != nil {
		return 0, false
	}
	defer closer.Close()
	return int64(binary.BigEndian.Uint64(v)), true
}

// openOnDisk opens the store at dir through the real constructor.
func openOnDisk(t testing.TB, dir string) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStore(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatalf("open pebble at %s: %v", dir, err)
	}
	return p
}

func TestJournalClock_StrictlyIncreasesUnderAFrozenAndSteppedBackWallClock(t *testing.T) {
	var c journalClock
	wall := &steppingClock{t: time.Unix(1_800_000_000, 0)}
	c.wall = wall.now
	_, prev := c.stamp()
	for i := 0; i < 1000; i++ {
		switch {
		case i%100 == 50:
			wall.set(wall.now().Add(-time.Hour)) // stepped back
		case i%100 == 90:
			wall.set(wall.now().Add(2 * time.Hour)) // and forward past the mark
		}
		w, n := c.stamp()
		if n <= prev {
			t.Fatalf("step %d: sequence %d is not after %d", i, n, prev)
		}
		if !w.Equal(wall.now()) {
			t.Fatalf("step %d: the wall part %v is not the wall clock %v", i, w, wall.now())
		}
		prev = n
	}
}

func TestJournalClock_ConcurrentValuesAreDistinct(t *testing.T) {
	var c journalClock
	c.wall = (&steppingClock{t: time.Unix(1_800_000_000, 0)}).now // frozen: every call ties
	const workers, per = 16, 500
	out := make(chan int64, workers*per)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				_, n := c.stamp()
				out <- n
			}
		}()
	}
	wg.Wait()
	close(out)
	seen := map[int64]bool{}
	for v := range out {
		if seen[v] {
			t.Fatalf("value %d handed out twice", v)
		}
		seen[v] = true
	}
	if len(seen) != workers*per {
		t.Fatalf("%d distinct values, want %d", len(seen), workers*per)
	}
}

// A journal write persists the clock in its own batch and stamps its rows from
// it: two rows written with the wall clock stepped back between them are still
// in the order they were written by sequence, while CreatedAt stays the wall
// time.
func TestJournalClock_WritesAreStampedAndPersistedInOrderAcrossAStepBack(t *testing.T) {
	p := newOpChangeTestStore(t)
	wall := &steppingClock{t: time.Unix(1_800_000_000, 0)}
	p.SetJournalWallClock(wall.now)
	first := listRow("op1", "a", "S", "collection-held:L")
	if err := p.CreateOperationChange(first); err != nil {
		t.Fatal(err)
	}
	wall.set(wall.now().Add(-24 * time.Hour))
	second := listRow("op2", "b", "R", "collection:L")
	if err := p.CreateOperationChange(second); err != nil {
		t.Fatal(err)
	}
	if second.CreatedSeq <= first.CreatedSeq {
		t.Fatalf("second row's sequence %d is not after the first's %d", second.CreatedSeq, first.CreatedSeq)
	}
	if !second.CreatedAt.Before(first.CreatedAt) || !second.CreatedAt.Equal(wall.now()) {
		t.Fatalf("CreatedAt must stay the wall time: first %v, second %v, wall %v", first.CreatedAt, second.CreatedAt, wall.now())
	}
	if v, ok := persistedClock(t, p); !ok || v != second.CreatedSeq {
		t.Fatalf("persisted clock = %d, %v; want the last sequence %d", v, ok, second.CreatedSeq)
	}
	// A mark takes its sequence from the same clock: after both rows although
	// the wall clock is a day behind them.
	if err := p.MarkOperationChangesReverted("op1", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	rows, err := p.GetBookChanges("S")
	if err != nil || len(rows) != 1 || rows[0].RevertedAt == nil {
		t.Fatalf("GetBookChanges = %v, %v", rows, err)
	}
	if rows[0].RevertedSeq <= second.CreatedSeq {
		t.Fatalf("RevertedSeq %d is not after the last write %d", rows[0].RevertedSeq, second.CreatedSeq)
	}
	if !rows[0].RevertedAt.Equal(wall.now()) {
		t.Fatalf("RevertedAt %v must stay the wall time %v", rows[0].RevertedAt, wall.now())
	}
	if v, _ := persistedClock(t, p); v != rows[0].RevertedSeq {
		t.Fatalf("persisted clock = %d, want the mark's %d", v, rows[0].RevertedSeq)
	}
}

// A row written reverted (a void) takes RevertedSeq from its write unless it
// carries one.
func TestJournalClock_ABornRevertedRowIsStampedWithARevertedSeq(t *testing.T) {
	p := newOpChangeTestStore(t)
	row := listRow("op1", "a", "S", "collection:L")
	if err := p.CreateOperationChange(row); err != nil {
		t.Fatal(err)
	}
	void := *row
	now := time.Now()
	void.RevertedAt, void.Voided = &now, true
	if err := p.CreateOperationChange(&void); err != nil {
		t.Fatal(err)
	}
	if void.RevertedSeq == 0 || void.RevertedSeq < row.CreatedSeq {
		t.Fatalf("void RevertedSeq = %d, want a stamp after the row's %d", void.RevertedSeq, row.CreatedSeq)
	}
	kept := *row
	kept.ID = "b"
	kept.RevertedAt, kept.RevertedSeq = &now, 42
	if err := p.CreateOperationChange(&kept); err != nil {
		t.Fatal(err)
	}
	if kept.RevertedSeq != 42 {
		t.Fatalf("a supplied RevertedSeq was overwritten: %d", kept.RevertedSeq)
	}
}

// A batch takes one sequence per row, in order, and persists the last.
func TestJournalClock_BatchRowsAreStampedInOrder(t *testing.T) {
	p := newOpChangeTestStore(t)
	p.SetJournalWallClock((&steppingClock{t: time.Unix(1_800_000_000, 0)}).now)
	rows := []*OperationChange{listRow("op1", "a", "S", "collection:L"), listRow("op1", "b", "S", "collection:L"), listRow("op1", "c", "S", "collection:L")}
	if err := p.CreateOperationChanges(rows); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].CreatedSeq <= rows[i-1].CreatedSeq {
			t.Fatalf("row %d not after row %d", i, i-1)
		}
	}
	if v, _ := persistedClock(t, p); v != rows[2].CreatedSeq {
		t.Fatalf("persisted = %d, want %d", v, rows[2].CreatedSeq)
	}
}

// Two writers whose batches would commit out of order must not leave the
// persisted mark behind a committed row: A stamps first and is held before its
// commit while B runs. The clock is held through the commit, so B waits for A
// (without the hold B commits first and A then persists the smaller value).
func TestJournalClock_ConcurrentWritersNeverLeaveThePersistedMarkBehindACommittedRow(t *testing.T) {
	p := newOpChangeTestStore(t)
	p.SetJournalWallClock((&steppingClock{t: time.Unix(1_800_000_000, 0)}).now)
	var calls int
	var gate sync.Mutex
	release := make(chan struct{})
	held := make(chan struct{})
	journalAfterStamp = func() error {
		gate.Lock()
		calls++
		first := calls == 1
		gate.Unlock()
		if first {
			close(held)
			<-release
		}
		return nil
	}
	defer func() { journalAfterStamp = nil }()
	a := listRow("op1", "a", "S", "collection:L")
	b := listRow("op2", "b", "S", "collection:L")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := p.CreateOperationChange(a); err != nil {
			t.Error(err)
		}
	}()
	<-held
	bDone := make(chan struct{})
	go func() {
		defer wg.Done()
		defer close(bDone)
		if err := p.CreateOperationChange(b); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-bDone: // not held back: B committed ahead of A
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	top := max(a.CreatedSeq, b.CreatedSeq)
	if v, ok := persistedClock(t, p); !ok || v < top {
		t.Fatalf("persisted mark %d is behind a committed row's sequence %d (a=%d b=%d)", v, top, a.CreatedSeq, b.CreatedSeq)
	}
}

// The seed through the real open path: write rows, close, reopen with the
// constructor, and the mark is at least the persisted value plus the margin.
func TestJournalClock_RealOpenSeedsFromTheKey(t *testing.T) {
	dir := t.TempDir()
	p := openOnDisk(t, dir)
	p.SetJournalWallClock((&steppingClock{t: time.Unix(1_800_000_000, 0)}).now)
	row := listRow("op1", "a", "S", "collection:L")
	if err := p.CreateOperationChange(row); err != nil {
		t.Fatal(err)
	}
	persisted, ok := persistedClock(t, p)
	if !ok || persisted != row.CreatedSeq {
		t.Fatalf("persisted = %d, %v; want %d", persisted, ok, row.CreatedSeq)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	re := openOnDisk(t, dir)
	defer re.Close()
	if got, want := re.journalClock.hw.Load(), persisted+int64(journalClockMargin); got < want {
		t.Fatalf("after reopen the mark = %d, want at least the persisted %d plus the margin = %d", got, persisted, want)
	}
	// Whatever the wall clock reads after the restart, the next stamp is after it.
	re.SetJournalWallClock((&steppingClock{t: time.Unix(1_700_000_000, 0)}).now)
	next := listRow("op2", "b", "S", "collection:L")
	if err := re.CreateOperationChange(next); err != nil {
		t.Fatal(err)
	}
	if next.CreatedSeq <= persisted+int64(journalClockMargin) {
		t.Fatalf("first stamp after a restart = %d, want after %d", next.CreatedSeq, persisted+int64(journalClockMargin))
	}
}

// The missing-key variant of the real open: the key is gone, so the reopen scans
// the journal and the mark is at least the largest stamp of any row.
func TestJournalClock_RealOpenScansTheRowsWhenTheKeyIsMissing(t *testing.T) {
	dir := t.TempDir()
	p := openOnDisk(t, dir)
	p.SetJournalWallClock((&steppingClock{t: time.Unix(1_800_000_000, 0)}).now)
	a := listRow("op1", "a", "S", "collection:L")
	b := listRow("op1", "b", "S", "collection:L")
	if err := p.CreateOperationChanges([]*OperationChange{a, b}); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkOperationChangesReverted("op1", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	rows, _ := p.GetOperationChanges("op1")
	var top int64
	for _, r := range rows {
		top = max(top, r.CreatedSeq, r.RevertedSeq)
	}
	if err := p.db.Delete([]byte(journalClockKey), nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	re := openOnDisk(t, dir)
	defer re.Close()
	if got := re.journalClock.hw.Load(); got < top {
		t.Fatalf("after a reopen with no key the mark = %d, want at least the largest row stamp %d", got, top)
	}
	if v, ok := persistedClock(t, re); !ok || v < top {
		t.Fatalf("the scan must persist the mark: %d, %v (want >= %d)", v, ok, top)
	}
}

// With no key (a journal written before the clock existed), the mark is the
// largest stamp of the rows already recorded: sequence or wall time, created or
// reverted.
func TestJournalClock_SeededFromTheRowsWhenTheKeyIsMissing(t *testing.T) {
	p := newOpChangeTestStore(t)
	base := time.Unix(1_800_000_000, 0)
	reverted := base.Add(3 * time.Hour)
	rawPutOpChange(t, p, &OperationChange{ID: "a", OperationID: "op1", BookID: "S", CreatedAt: base})
	rawPutOpChange(t, p, &OperationChange{ID: "b", OperationID: "op1", BookID: "S", CreatedAt: base.Add(time.Hour), RevertedAt: &reverted})
	if _, ok := persistedClock(t, p); ok {
		t.Fatal("a store with no journal write has no persisted clock")
	}
	fresh := &PebbleStore{db: p.db}
	fresh.SetJournalWallClock((&steppingClock{t: base.Add(-time.Hour)}).now)
	if err := fresh.seedJournalClock(); err != nil {
		t.Fatal(err)
	}
	if _, n := fresh.journalClock.stamp(); n <= reverted.UnixNano() {
		t.Fatalf("first sequence %d is not after the largest recorded time %d", n, reverted.UnixNano())
	}
	rawPutOpChange(t, p, &OperationChange{ID: "c", OperationID: "op2", BookID: "S", CreatedAt: base, CreatedSeq: reverted.UnixNano() + 5_000})
	fresh2 := &PebbleStore{db: p.db}
	if err := p.db.Delete([]byte(journalClockKey), nil); err != nil {
		t.Fatal(err)
	}
	if err := fresh2.seedJournalClock(); err != nil {
		t.Fatal(err)
	}
	if got := fresh2.journalClock.hw.Load(); got < reverted.UnixNano()+5_000 {
		t.Fatalf("a row's CreatedSeq must count: mark %d", got)
	}
}

// A row that does not decode must not keep the store from opening.
func TestJournalClock_SeedSkipsAnUndecodableRow(t *testing.T) {
	p := newOpChangeTestStore(t)
	if err := p.db.Set(opChangeKey("op1", "junk"), []byte("not json"), nil); err != nil {
		t.Fatal(err)
	}
	fresh := &PebbleStore{db: p.db}
	if err := fresh.seedJournalClock(); err != nil {
		t.Fatalf("seed over an undecodable row: %v", err)
	}
}

// A malformed key does not fail the open: the seed falls back to the row scan.
func TestJournalClock_AMalformedKeyFallsBackToTheRowScan(t *testing.T) {
	p := newOpChangeTestStore(t)
	base := time.Unix(1_800_000_000, 0)
	rawPutOpChange(t, p, &OperationChange{ID: "a", OperationID: "op1", BookID: "S", CreatedAt: base, CreatedSeq: base.UnixNano() + 7})
	if err := p.db.Set([]byte(journalClockKey), []byte{1, 2, 3}, nil); err != nil {
		t.Fatal(err)
	}
	fresh := &PebbleStore{db: p.db}
	if err := fresh.seedJournalClock(); err != nil {
		t.Fatalf("a malformed key must not fail the open: %v", err)
	}
	if got := fresh.journalClock.hw.Load(); got < base.UnixNano()+7 {
		t.Fatalf("the row scan did not run: mark %d", got)
	}
	if v, ok := persistedClock(t, fresh); !ok || v < base.UnixNano()+7 {
		t.Fatalf("the scan must replace the malformed key: %d, %v", v, ok)
	}
}

// The first-open scan also stages the opchange_by_list: entry of every
// user_content_follow row that lacks one (rows an earlier build wrote), and is
// idempotent.
func TestJournalClock_TheFirstOpenScanStagesMissingListEntriesAndIsIdempotent(t *testing.T) {
	p := newOpChangeTestStore(t)
	base := time.Unix(1_800_000_000, 0)
	rawPutOpChange(t, p, &OperationChange{ID: "a", OperationID: "op1", BookID: "S", ChangeType: "user_content_follow", FieldName: "collection-took:L1", CreatedAt: base})
	rawPutOpChange(t, p, &OperationChange{ID: "b", OperationID: "op1", BookID: "B", ChangeType: "user_content_follow", FieldName: "collection:L1", CreatedAt: base})
	rawPutOpChange(t, p, &OperationChange{ID: "c", OperationID: "op2", BookID: "B", ChangeType: "user_content_follow", FieldName: "playlist:P1", CreatedAt: base})
	rawPutOpChange(t, p, &OperationChange{ID: "d", OperationID: "op2", BookID: "B", ChangeType: "user_content_follow", FieldName: "rating:S", CreatedAt: base})
	rawPutOpChange(t, p, &OperationChange{ID: "e", OperationID: "op2", BookID: "B", ChangeType: "metadata_update", FieldName: "collection:L1", CreatedAt: base})
	// One row's entry already exists: it must not be duplicated or disturbed.
	if err := p.db.Set(append(opChangeByListListPrefix("collection", "L1"), "op1:b"...), nil, nil); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		fresh := &PebbleStore{db: p.db}
		if pass == 2 {
			if err := p.db.Delete([]byte(journalClockKey), nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := fresh.seedJournalClock(); err != nil {
			t.Fatal(err)
		}
		if got := listIDs(t, p, "collection", "L1"); len(got) != 2 || got[0] != "op1/a" || got[1] != "op1/b" {
			t.Fatalf("pass %d: collection L1 reads %v", pass, got)
		}
		if got := listIDs(t, p, "playlist", "P1"); len(got) != 1 || got[0] != "op2/c" {
			t.Fatalf("pass %d: playlist P1 reads %v", pass, got)
		}
		for _, id := range []string{"d", "e"} {
			if e := listEntries(t, p, "op2", id); len(e) != 0 {
				t.Fatalf("pass %d: row %s must have no list entry, has %v", pass, id, e)
			}
		}
	}
}

// BenchmarkJournalWrite_Serialized measures one journal write (row, indexes,
// clock key, Sync commit) with the clock held through the commit, sequentially
// and from several writers at once, so the cost of serializing is visible.
func BenchmarkJournalWrite_Serialized(b *testing.B) {
	for _, workers := range []int{1, 8} {
		b.Run(map[int]string{1: "one-writer", 8: "eight-writers"}[workers], func(b *testing.B) {
			p := newOpChangeTestStore(b)
			b.SetParallelism(workers)
			b.ResetTimer()
			var n int64
			var mu sync.Mutex
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					mu.Lock()
					n++
					id := n
					mu.Unlock()
					r := listRow("op", "r"+time.Unix(0, id).Format("150405.000000000"), "S", "collection:L")
					if err := p.CreateOperationChange(r); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
