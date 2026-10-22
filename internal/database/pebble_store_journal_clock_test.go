// file: internal/database/pebble_store_journal_clock_test.go
// version: 1.0.0
// guid: 7e2b94d1-35c8-4a60-9f17-d08a6b3c5e29
// last-edited: 2026-10-22

package database

import (
	"encoding/binary"
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

func persistedClock(t *testing.T, p *PebbleStore) (int64, bool) {
	t.Helper()
	v, closer, err := p.db.Get([]byte(journalClockKey))
	if err != nil {
		return 0, false
	}
	defer closer.Close()
	return int64(binary.BigEndian.Uint64(v)), true
}

func TestJournalClock_StrictlyIncreasesUnderAFrozenAndSteppedBackWallClock(t *testing.T) {
	var c journalClock
	wall := &steppingClock{t: time.Unix(1_800_000_000, 0)}
	c.wall = wall.now
	prev := c.next()
	for i := 0; i < 1000; i++ {
		switch {
		case i%100 == 50:
			wall.set(wall.now().Add(-time.Hour)) // stepped back
		case i%100 == 90:
			wall.set(wall.now().Add(2 * time.Hour)) // and forward past the mark
		}
		n := c.next()
		if !n.After(prev) {
			t.Fatalf("step %d: %v is not after %v", i, n, prev)
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
				out <- c.next().UnixNano()
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
// in the order they were written.
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
	if !second.CreatedAt.After(first.CreatedAt) {
		t.Fatalf("second row %v is not after the first %v", second.CreatedAt, first.CreatedAt)
	}
	if v, ok := persistedClock(t, p); !ok || v != second.CreatedAt.UnixNano() {
		t.Fatalf("persisted clock = %d, %v; want the last stamp %d", v, ok, second.CreatedAt.UnixNano())
	}
	// A mark takes its time from the same clock: after both rows although the
	// wall clock is a day behind them.
	if err := p.MarkOperationChangesReverted("op1", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	rows, err := p.GetBookChanges("S")
	if err != nil || len(rows) != 1 || rows[0].RevertedAt == nil {
		t.Fatalf("GetBookChanges = %v, %v", rows, err)
	}
	if !rows[0].RevertedAt.After(second.CreatedAt) {
		t.Fatalf("RevertedAt %v is not after the last write %v", rows[0].RevertedAt, second.CreatedAt)
	}
	if v, _ := persistedClock(t, p); v != rows[0].RevertedAt.UnixNano() {
		t.Fatalf("persisted clock = %d, want the mark's %d", v, rows[0].RevertedAt.UnixNano())
	}
}

// A batch takes one value per row, in order, and persists the last.
func TestJournalClock_BatchRowsAreStampedInOrder(t *testing.T) {
	p := newOpChangeTestStore(t)
	p.SetJournalWallClock((&steppingClock{t: time.Unix(1_800_000_000, 0)}).now)
	rows := []*OperationChange{listRow("op1", "a", "S", "collection:L"), listRow("op1", "b", "S", "collection:L"), listRow("op1", "c", "S", "collection:L")}
	if err := p.CreateOperationChanges(rows); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(rows); i++ {
		if !rows[i].CreatedAt.After(rows[i-1].CreatedAt) {
			t.Fatalf("row %d not after row %d", i, i-1)
		}
	}
	if v, _ := persistedClock(t, p); v != rows[2].CreatedAt.UnixNano() {
		t.Fatalf("persisted = %d, want %d", v, rows[2].CreatedAt.UnixNano())
	}
}

// A restart after the wall clock stepped back: the key seeds the mark (plus the
// margin), so the first stamp is after everything recorded.
func TestJournalClock_SeededFromTheKeyAtOpen(t *testing.T) {
	p := newOpChangeTestStore(t)
	wall := &steppingClock{t: time.Unix(1_800_000_000, 0)}
	p.SetJournalWallClock(wall.now)
	last := listRow("op1", "a", "S", "collection:L")
	if err := p.CreateOperationChange(last); err != nil {
		t.Fatal(err)
	}
	fresh := &PebbleStore{db: p.db}
	wall2 := &steppingClock{t: time.Unix(1_800_000_000, 0).Add(-time.Hour)} // the clock restarted an hour behind
	fresh.SetJournalWallClock(wall2.now)
	if err := fresh.seedJournalClock(); err != nil {
		t.Fatal(err)
	}
	n := fresh.journalNow()
	if want := last.CreatedAt.Add(journalClockMargin); n.Before(want) {
		t.Fatalf("first stamp after a restart = %v, want at least the recorded %v plus the margin", n, want)
	}
}

// With no key (a journal written before the clock existed), the mark is the
// largest time of the rows already recorded, CreatedAt and RevertedAt alike.
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
	if n := fresh.journalNow(); !n.After(reverted) {
		t.Fatalf("first stamp %v is not after the largest recorded time %v", n, reverted)
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

func TestJournalClock_BadKeyLengthIsAnError(t *testing.T) {
	p := newOpChangeTestStore(t)
	if err := p.db.Set([]byte(journalClockKey), []byte{1, 2, 3}, nil); err != nil {
		t.Fatal(err)
	}
	fresh := &PebbleStore{db: p.db}
	if err := fresh.seedJournalClock(); err == nil {
		t.Fatal("a malformed key must fail the seed, not be read as zero")
	}
}
