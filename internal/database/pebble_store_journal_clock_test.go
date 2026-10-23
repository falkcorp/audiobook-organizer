// file: internal/database/pebble_store_journal_clock_test.go
// version: 3.0.1
// guid: 7e2b94d1-35c8-4a60-9f17-d08a6b3c5e29
// last-edited: 2026-10-23

package database

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
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

// persistedClock reads the persisted ceiling.
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
	_, prev, _ := c.stamp(nil)
	for i := 0; i < 1000; i++ {
		switch {
		case i%100 == 50:
			wall.set(wall.now().Add(-time.Hour)) // stepped back
		case i%100 == 90:
			wall.set(wall.now().Add(2 * time.Hour)) // and forward past the mark
		}
		w, n, err := c.stamp(nil)
		if err != nil {
			t.Fatal(err)
		}
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
				_, n, _ := c.stamp(nil)
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

// The ceiling is persisted once per window of sequence time, always before a
// value at or past the previous one is handed out, and a failed persist hands
// nothing out.
func TestJournalClock_CeilingIsPersistedOncePerWindowAndBeforeAnyValueReachesIt(t *testing.T) {
	var c journalClock
	wall := &steppingClock{t: time.Unix(1_800_000_000, 0)}
	c.wall = wall.now
	var calls []int64
	var failNext bool
	persist := func(ceiling int64) error {
		if failNext {
			failNext = false
			return errors.New("disk full")
		}
		calls = append(calls, ceiling)
		return nil
	}
	var first int64
	for i := 0; i < 10_000; i++ { // a frozen wall clock: each value is one nanosecond on
		_, n, err := c.stamp(persist)
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) == 0 || n >= calls[len(calls)-1] {
			t.Fatalf("value %d was handed out at or past the persisted ceiling %v", n, calls)
		}
		if i == 0 {
			first = n
		}
	}
	if len(calls) != 1 {
		t.Fatalf("%d ceiling writes for 10,000 stamps inside one window, want 1", len(calls))
	}
	if calls[0] != first+int64(journalClockWindow) {
		t.Fatalf("the ceiling %d is not one window past the first value %d", calls[0], first)
	}
	// The wall clock moves on past the ceiling: one more write, before the value.
	wall.set(wall.now().Add(2 * journalClockWindow))
	_, n, err := c.stamp(persist)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || n >= calls[1] || calls[1] <= calls[0] {
		t.Fatalf("after the wall clock jumped past the window: value %d, ceilings %v", n, calls)
	}
	// A wall clock that steps back changes nothing: still inside the ceiling.
	wall.set(wall.now().Add(-5 * time.Hour))
	if _, m, err := c.stamp(persist); err != nil || m <= n || len(calls) != 2 {
		t.Fatalf("after a step back: value %d (was %d), %d writes, err %v", m, n, len(calls), err)
	}
	// A persist that fails hands out nothing and leaves the clock where it was.
	wall.set(time.Unix(1_800_000_000, 0).Add(24 * time.Hour))
	failNext = true
	before := c.high()
	if _, _, err := c.stamp(persist); err == nil {
		t.Fatal("a failed ceiling write must surface")
	}
	if c.high() != before {
		t.Fatalf("a failed stamp moved the clock: %d -> %d", before, c.high())
	}
	if _, k, err := c.stamp(persist); err != nil || k <= before {
		t.Fatalf("the retry after a failed write: %d, %v (before %d)", k, err, before)
	}
}

// A journal write stamps its rows from the clock: two rows written with the wall
// clock stepped back between them are still in the order they were written by
// sequence, while CreatedAt stays the wall time. The persisted ceiling is at or
// above both.
func TestJournalClock_WritesAreStampedAndCoveredByTheCeilingAcrossAStepBack(t *testing.T) {
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
	if v, ok := persistedClock(t, p); !ok || v < second.CreatedSeq {
		t.Fatalf("persisted ceiling = %d, %v; want at least the last sequence %d", v, ok, second.CreatedSeq)
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
	if v, _ := persistedClock(t, p); v < rows[0].RevertedSeq {
		t.Fatalf("persisted ceiling = %d, below the mark's %d", v, rows[0].RevertedSeq)
	}
}

// A row written reverted (a void) takes RevertedSeq from its write; a rewrite
// that carries an older RevertedSeq has it raised to the new CreatedSeq (no row
// is reverted before it was created), and one above it is kept.
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
	// A rewrite of the reverted row carrying its old, now smaller, RevertedSeq.
	rewrite := void
	rewrite.RevertedSeq = 42
	if err := p.CreateOperationChange(&rewrite); err != nil {
		t.Fatal(err)
	}
	if rewrite.RevertedSeq < rewrite.CreatedSeq {
		t.Fatalf("a rewrite reverted at %d before it was created at %d", rewrite.RevertedSeq, rewrite.CreatedSeq)
	}
	// A supplied RevertedSeq above the new CreatedSeq is the caller's to set.
	kept := *row
	kept.ID = "b"
	kept.RevertedAt, kept.RevertedSeq = &now, 1<<62
	if err := p.CreateOperationChange(&kept); err != nil {
		t.Fatal(err)
	}
	if kept.RevertedSeq != 1<<62 {
		t.Fatalf("a supplied RevertedSeq above CreatedSeq was changed: %d", kept.RevertedSeq)
	}
}

// A batch takes one sequence per row, in order, and the ceiling covers the last.
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
	if v, _ := persistedClock(t, p); v < rows[2].CreatedSeq {
		t.Fatalf("persisted ceiling = %d, below the last row's %d", v, rows[2].CreatedSeq)
	}
}

// ceilingWatcher samples the persisted ceiling while writers run and fails when
// it is below the largest sequence of a row whose write had already returned (a
// committed row) when the sample began.
type ceilingWatcher struct {
	committed atomic.Int64 // the largest sequence of a returned write
	bad       atomic.Value // string
	stop      chan struct{}
	done      chan struct{}
}

func (w *ceilingWatcher) record(seq int64) {
	for {
		cur := w.committed.Load()
		if seq <= cur || w.committed.CompareAndSwap(cur, seq) {
			return
		}
	}
}

func watchCeiling(p *PebbleStore) *ceilingWatcher {
	w := &ceilingWatcher{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			floor := w.committed.Load()
			v, closer, err := p.db.Get([]byte(journalClockKey))
			if err != nil {
				if floor > 0 {
					w.bad.Store(fmt.Sprintf("a row with sequence %d is committed and the ceiling key is missing: %v", floor, err))
					return
				}
				continue
			}
			got := int64(binary.BigEndian.Uint64(v))
			closer.Close()
			if got < floor {
				w.bad.Store(fmt.Sprintf("persisted ceiling %d is below the committed sequence %d", got, floor))
				return
			}
		}
	}()
	return w
}

func (w *ceilingWatcher) finish(t *testing.T) {
	t.Helper()
	close(w.stop)
	<-w.done
	if bad, ok := w.bad.Load().(string); ok {
		t.Fatal(bad)
	}
}

// randomDelay is a journalAfterStamp hook that holds each writer a random time
// between its stamp and its commit, so batches commit in any order.
func randomDelay(seed int64) func() error {
	var mu sync.Mutex
	rng := rand.New(rand.NewSource(seed))
	return func() error {
		mu.Lock()
		d := time.Duration(rng.Intn(3000)) * time.Microsecond
		mu.Unlock()
		time.Sleep(d)
		return nil
	}
}

// Many writers with a random delay between stamp and commit (so their commits
// land in any order): the persisted ceiling is never below the sequence of a
// committed row, and after a close and a reopen it is at or above every row and
// the next stamp is past all of them.
func TestJournalClock_ConcurrentCreatesNeverLeaveTheCeilingBehindACommittedRow(t *testing.T) {
	dir := t.TempDir()
	p := openOnDisk(t, dir)
	// A wall clock that moves forward in big steps, so ceilings are written
	// repeatedly while the writers run.
	wall := &steppingClock{t: time.Unix(1_800_000_000, 0)}
	p.SetJournalWallClock(wall.now)
	journalAfterStamp = randomDelay(1)
	defer func() { journalAfterStamp = nil }()
	w := watchCeiling(p)
	const writers, per = 12, 25
	var wg sync.WaitGroup
	var maxSeq atomic.Int64
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if i%5 == 0 {
					wall.set(wall.now().Add(40 * time.Second)) // reaches the ceiling now and then
				}
				rows := []*OperationChange{
					listRow(fmt.Sprintf("op%d", g), fmt.Sprintf("m%d", i), "S", "collection-took:L"),
					listRow(fmt.Sprintf("op%d", g), fmt.Sprintf("r%d", i), fmt.Sprintf("R%d", g), "collection:L"),
				}
				if err := p.CreateOperationChanges(rows); err != nil {
					t.Error(err)
					return
				}
				for _, r := range rows {
					w.record(r.CreatedSeq)
				}
				for _, r := range rows {
					for {
						cur := maxSeq.Load()
						if r.CreatedSeq <= cur || maxSeq.CompareAndSwap(cur, r.CreatedSeq) {
							break
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	w.finish(t)
	top := maxSeq.Load()
	if v, ok := persistedClock(t, p); !ok || v < top {
		t.Fatalf("after the writers: persisted ceiling %d, %v; the largest committed sequence is %d", v, ok, top)
	}
	journalAfterStamp = nil
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	re := openOnDisk(t, dir)
	defer re.Close()
	if got := re.journalClock.high(); got < top {
		t.Fatalf("after the reopen the clock starts at %d, below the largest committed sequence %d", got, top)
	}
	re.SetJournalWallClock((&steppingClock{t: time.Unix(1_700_000_000, 0)}).now) // and a wall clock far behind
	next := listRow("opNext", "n", "S", "collection:L")
	if err := re.CreateOperationChange(next); err != nil {
		t.Fatal(err)
	}
	if next.CreatedSeq <= top {
		t.Fatalf("the first stamp after the reopen, %d, is not past the committed %d", next.CreatedSeq, top)
	}
}

// The same invariant for the mark path: writers mark their own operations
// reverted with a random delay between the stamp and the commit.
func TestJournalClock_ConcurrentMarksNeverLeaveTheCeilingBehindACommittedMark(t *testing.T) {
	dir := t.TempDir()
	p := openOnDisk(t, dir)
	wall := &steppingClock{t: time.Unix(1_800_000_000, 0)}
	p.SetJournalWallClock(wall.now)
	const writers, per = 12, 20
	for g := 0; g < writers; g++ {
		for i := 0; i < per; i++ {
			if err := p.CreateOperationChange(listRow(fmt.Sprintf("op%d", g), fmt.Sprintf("c%d", i), "S", "collection-held:L")); err != nil {
				t.Fatal(err)
			}
		}
	}
	journalAfterStamp = randomDelay(2)
	defer func() { journalAfterStamp = nil }()
	w := watchCeiling(p)
	var wg sync.WaitGroup
	var maxSeq atomic.Int64
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if i%4 == 0 {
					wall.set(wall.now().Add(45 * time.Second))
				}
				op, id := fmt.Sprintf("op%d", g), fmt.Sprintf("c%d", i)
				if err := p.MarkOperationChangesReverted(op, []string{id}); err != nil {
					t.Error(err)
					return
				}
				rows, err := p.GetOperationChanges(op)
				if err != nil {
					t.Error(err)
					return
				}
				for _, r := range rows {
					if r.ID == id && r.RevertedSeq > 0 {
						w.record(r.RevertedSeq)
						for {
							cur := maxSeq.Load()
							if r.RevertedSeq <= cur || maxSeq.CompareAndSwap(cur, r.RevertedSeq) {
								break
							}
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	w.finish(t)
	top := maxSeq.Load()
	if top == 0 {
		t.Fatal("no mark was recorded")
	}
	if v, ok := persistedClock(t, p); !ok || v < top {
		t.Fatalf("after the marks: persisted ceiling %d, %v; the largest committed RevertedSeq is %d", v, ok, top)
	}
	journalAfterStamp = nil
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	re := openOnDisk(t, dir)
	defer re.Close()
	if got := re.journalClock.high(); got < top {
		t.Fatalf("after the reopen the clock starts at %d, below the largest committed RevertedSeq %d", got, top)
	}
}

// The open seeds the clock from the persisted ceiling exactly: no margin, and no
// scan of the rows (a row above the ceiling is not looked at).
func TestJournalClock_OpenSeedsFromTheCeilingWithNoMarginAndNoScan(t *testing.T) {
	p := newOpChangeTestStore(t)
	base := time.Unix(1_800_000_000, 0)
	ceiling := base.UnixNano() + 12345
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], uint64(ceiling))
	if err := p.db.Set([]byte(journalClockKey), v[:], pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Set([]byte(opChangeByListBackfillKey), []byte("1"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	// A row stamped above the ceiling: only a scan would see it.
	rawPutOpChange(t, p, &OperationChange{ID: "x", OperationID: "op1", BookID: "S", CreatedAt: base, CreatedSeq: ceiling + 1_000_000})
	fresh := &PebbleStore{db: p.db}
	if err := fresh.seedJournalClock(); err != nil {
		t.Fatal(err)
	}
	if got := fresh.journalClock.high(); got != ceiling {
		t.Fatalf("seeded at %d, want exactly the ceiling %d (no margin, no scan)", got, ceiling)
	}
	fresh.SetJournalWallClock((&steppingClock{t: base.Add(-time.Hour)}).now)
	_, n, err := fresh.stampJournal()
	if err != nil {
		t.Fatal(err)
	}
	if n != ceiling+1 {
		t.Fatalf("first stamp %d, want the ceiling plus one (%d)", n, ceiling+1)
	}
	if got, _ := persistedClock(t, fresh); got <= n {
		t.Fatalf("the ceiling %d was not moved past the stamp %d before it was handed out", got, n)
	}
}

// A clean Close lowers the ceiling to the actual high-water mark, so a restart
// does not begin a window ahead; a store reopened from it starts at that mark.
func TestJournalClock_CloseSettlesTheCeilingToTheHighWaterMark(t *testing.T) {
	dir := t.TempDir()
	p := openOnDisk(t, dir)
	p.SetJournalWallClock((&steppingClock{t: time.Unix(1_800_000_000, 0)}).now)
	row := listRow("op1", "a", "S", "collection:L")
	if err := p.CreateOperationChange(row); err != nil {
		t.Fatal(err)
	}
	ahead, _ := persistedClock(t, p)
	if ahead < row.CreatedSeq+int64(journalClockWindow)-1 {
		t.Fatalf("a running store keeps its ceiling a window ahead: %d for a sequence of %d", ahead, row.CreatedSeq)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	re := openOnDisk(t, dir)
	defer re.Close()
	if got := re.journalClock.high(); got != row.CreatedSeq {
		t.Fatalf("after a clean close and reopen the clock starts at %d, want the last sequence %d", got, row.CreatedSeq)
	}
	if v, _ := persistedClock(t, re); v != row.CreatedSeq {
		t.Fatalf("persisted %d after a clean close, want %d", v, row.CreatedSeq)
	}
}

// Close while a writer sits between its stamp and its commit: the ceiling the
// close settles to is the high-water mark, which includes the writer's
// sequence, so whether that row went on to commit or not, a reopened store
// stamps past it.
func TestJournalClock_CloseWithAWriterBetweenStampAndCommitStampsPastItAfterReopen(t *testing.T) {
	dir := t.TempDir()
	p := openOnDisk(t, dir)
	p.closeDrainTimeout = 100 * time.Millisecond
	p.SetJournalWallClock((&steppingClock{t: time.Unix(1_800_000_000, 0)}).now)
	before := listRow("op0", "z", "S", "collection:L")
	if err := p.CreateOperationChange(before); err != nil {
		t.Fatal(err)
	}
	stamped, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	journalAfterStamp = func() error {
		once.Do(func() { close(stamped) })
		<-release
		return nil
	}
	defer func() { journalAfterStamp = nil }()
	inflight := listRow("op1", "a", "S", "collection:L")
	writerDone := make(chan error, 1)
	go func() { writerDone <- p.CreateOperationChange(inflight) }()
	<-stamped
	inflightSeq := inflight.CreatedSeq // set before the hook ran
	if inflightSeq <= before.CreatedSeq {
		t.Fatalf("the in-flight row's sequence %d is not past the earlier row's %d", inflightSeq, before.CreatedSeq)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()
	time.Sleep(50 * time.Millisecond) // let Close reach and pass the settle
	close(release)
	writeErr := <-writerDone // the row may commit or be refused by the closing store
	if err := <-closeDone; err != nil {
		t.Logf("close returned %v (the writer's outcome was %v)", err, writeErr)
	}
	journalAfterStamp = nil

	re := openOnDisk(t, dir)
	defer re.Close()
	if got := re.journalClock.high(); got < inflightSeq {
		t.Fatalf("the reopened clock starts at %d, below the in-flight sequence %d", got, inflightSeq)
	}
	_, next, err := re.stampJournal()
	if err != nil {
		t.Fatal(err)
	}
	if next <= inflightSeq {
		t.Fatalf("the first stamp after the reopen is %d, not past the in-flight sequence %d (the write's outcome: %v)", next, inflightSeq, writeErr)
	}
	if rows, err := re.GetOperationChanges("op1"); err == nil && len(rows) == 1 && rows[0].CreatedSeq != inflightSeq {
		t.Fatalf("the committed row carries %d, want %d", rows[0].CreatedSeq, inflightSeq)
	}
}

// After a crash (no settle) the clock starts at the ceiling, a window ahead of
// the last sequence, and every later stamp is past every row the dead process
// could have written.
func TestJournalClock_AfterACrashTheClockStartsAtTheCeiling(t *testing.T) {
	p := newOpChangeTestStore(t)
	p.SetJournalWallClock((&steppingClock{t: time.Unix(1_800_000_000, 0)}).now)
	row := listRow("op1", "a", "S", "collection:L")
	if err := p.CreateOperationChange(row); err != nil {
		t.Fatal(err)
	}
	ceiling, _ := persistedClock(t, p)
	fresh := &PebbleStore{db: p.db} // a new process over the same files, no clean close
	if err := fresh.seedJournalClock(); err != nil {
		t.Fatal(err)
	}
	if got := fresh.journalClock.high(); got != ceiling || got <= row.CreatedSeq {
		t.Fatalf("after a crash the clock starts at %d, want the ceiling %d (> %d)", got, ceiling, row.CreatedSeq)
	}
}

// The missing-key variant of the real open: the key is gone, so the reopen scans
// the journal, the clock starts at the largest stamp of any row and the key is
// persisted.
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
	// Closing settles the ceiling into the key; take the key out after.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openOnDisk(t, dir)
	if err := reopened.db.Delete([]byte(journalClockKey), nil); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	re := openOnDisk(t, dir)
	defer re.Close()
	if got := re.journalClock.high(); got < top {
		t.Fatalf("after a reopen with no key the clock starts at %d, want at least the largest row stamp %d", got, top)
	}
	if v, ok := persistedClock(t, re); !ok || v < top {
		t.Fatalf("the scan must persist the clock: %d, %v (want >= %d)", v, ok, top)
	}
}

// With no key (a journal written before the clock existed), the clock starts at
// the largest stamp of the rows already recorded: sequence or wall time, created
// or reverted.
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
	if _, n, err := fresh.stampJournal(); err != nil || n <= reverted.UnixNano() {
		t.Fatalf("first sequence %d (err %v) is not after the largest recorded time %d", n, err, reverted.UnixNano())
	}
	rawPutOpChange(t, p, &OperationChange{ID: "c", OperationID: "op2", BookID: "S", CreatedAt: base, CreatedSeq: reverted.UnixNano() + 5_000})
	fresh2 := &PebbleStore{db: p.db}
	if err := p.db.Delete([]byte(journalClockKey), nil); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Delete([]byte(opChangeByListBackfillKey), nil); err != nil {
		t.Fatal(err)
	}
	if err := fresh2.seedJournalClock(); err != nil {
		t.Fatal(err)
	}
	if got := fresh2.journalClock.high(); got < reverted.UnixNano()+5_000 {
		t.Fatalf("a row's CreatedSeq must count: clock %d", got)
	}
}

// A store that has never held a journal row opens without a clock key and starts
// at the wall clock.
func TestJournalClock_AFreshStoreHasNoClockKey(t *testing.T) {
	p := newOpChangeTestStore(t)
	if _, ok := persistedClock(t, p); ok {
		t.Fatal("a fresh store must not carry a clock key")
	}
	if got := p.journalClock.high(); got != 0 {
		t.Fatalf("a fresh store's clock starts at %d, want 0 (the wall clock decides)", got)
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

// A malformed key does not fail the open: the seed falls back to the row scan
// and the key is rewritten, with rows to scan or with none.
func TestJournalClock_AMalformedKeyFallsBackToTheRowScanAndIsRewritten(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	for _, withRows := range []bool{true, false} {
		p := newOpChangeTestStore(t)
		if withRows {
			rawPutOpChange(t, p, &OperationChange{ID: "a", OperationID: "op1", BookID: "S", CreatedAt: base, CreatedSeq: base.UnixNano() + 7})
		}
		if err := p.db.Set([]byte(journalClockKey), []byte{1, 2, 3}, nil); err != nil {
			t.Fatal(err)
		}
		// The list index backfill is already done, so only the clock decides.
		if err := p.db.Set([]byte(opChangeByListBackfillKey), []byte("1"), nil); err != nil {
			t.Fatal(err)
		}
		fresh := &PebbleStore{db: p.db}
		if err := fresh.seedJournalClock(); err != nil {
			t.Fatalf("a malformed key must not fail the open: %v", err)
		}
		if withRows {
			if got := fresh.journalClock.high(); got < base.UnixNano()+7 {
				t.Fatalf("the row scan did not run: clock %d", got)
			}
		}
		v, closer, err := p.db.Get([]byte(journalClockKey))
		if err != nil {
			t.Fatalf("withRows=%v: key gone after the open: %v", withRows, err)
		}
		n := len(v)
		closer.Close()
		if n != 8 {
			t.Fatalf("withRows=%v: the malformed key (%d bytes) was not rewritten", withRows, n)
		}
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
		// The store was opened fresh (so it wrote its completion keys); a store
		// from an earlier build has neither. A second pass finds every entry.
		if err := p.db.Delete([]byte(journalClockKey), nil); err != nil {
			t.Fatal(err)
		}
		if err := p.db.Delete([]byte(opChangeByListBackfillKey), nil); err != nil {
			t.Fatal(err)
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

// The list-index backfill is gated on its own completion key, not on the clock
// key: a store whose clock key exists (written by a build that did not stage the
// entries) still gets its entries; and once the backfill key is written, a later
// open does not scan again.
func TestJournalClock_TheListIndexBackfillIsGatedOnItsOwnKey(t *testing.T) {
	p := newOpChangeTestStore(t)
	base := time.Unix(1_800_000_000, 0)
	rawPutOpChange(t, p, &OperationChange{ID: "b", OperationID: "op1", BookID: "B", ChangeType: "user_content_follow", FieldName: "collection:L1", CreatedAt: base, CreatedSeq: base.UnixNano()})
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], uint64(base.UnixNano()+int64(time.Minute)))
	if err := p.db.Set([]byte(journalClockKey), v[:], pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Delete([]byte(opChangeByListBackfillKey), nil); err != nil { // as in a store an earlier build wrote
		t.Fatal(err)
	}
	if got := listEntries(t, p, "op1", "b"); len(got) != 0 {
		t.Fatalf("setup: the row already has entries %v", got)
	}
	// The clock key is present, the backfill key is not: the scan runs.
	fresh := &PebbleStore{db: p.db}
	if err := fresh.seedJournalClock(); err != nil {
		t.Fatal(err)
	}
	if got := listEntries(t, p, "op1", "b"); len(got) != 1 {
		t.Fatalf("a store with a clock key but no backfill key must get its list entries, has %v", got)
	}
	if _, closer, err := p.db.Get([]byte(opChangeByListBackfillKey)); err != nil {
		t.Fatalf("the backfill completion key was not written: %v", err)
	} else {
		closer.Close()
	}
	if got, _ := persistedClock(t, p); got != base.UnixNano()+int64(time.Minute) {
		t.Fatalf("the scan changed a good clock key: %d", got)
	}
	// Both keys present: a later open reads two keys and scans nothing, so an
	// entry removed by hand stays removed.
	if err := p.db.Delete(append(opChangeByListListPrefix("collection", "L1"), "op1:b"...), nil); err != nil {
		t.Fatal(err)
	}
	fresh2 := &PebbleStore{db: p.db}
	if err := fresh2.seedJournalClock(); err != nil {
		t.Fatal(err)
	}
	if got := listEntries(t, p, "op1", "b"); len(got) != 0 {
		t.Fatalf("a second open scanned again: %v", got)
	}
}

// BenchmarkJournalWrite measures one journal write (row, indexes, Sync commit)
// on an ON-DISK store from exactly N goroutines (N = 1, 8, 32): ns/op is the
// wall time per write across all writers, so a lock that serializes the commit
// shows as ns/op that does not fall with N. The "-serialized" variants wrap
// every write in one outside mutex, which is what holding a clock lock across
// the commit did: the contrast is the cost the ceiling design removes.
func BenchmarkJournalWrite(b *testing.B) {
	for _, tc := range []struct {
		workers    int
		serialized bool
	}{{1, false}, {8, false}, {32, false}, {32, true}} {
		name := fmt.Sprintf("writers-%d", tc.workers)
		if tc.serialized {
			name += "-serialized"
		}
		b.Run(name, func(b *testing.B) {
			p := openOnDisk(b, b.TempDir())
			b.Cleanup(func() { _ = p.Close() })
			var seq atomic.Int64
			var outside sync.Mutex
			b.ResetTimer()
			var wg sync.WaitGroup
			for g := 0; g < tc.workers; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						i := seq.Add(1)
						if i > int64(b.N) {
							return
						}
						r := listRow(fmt.Sprintf("op%d", i%64), fmt.Sprintf("r%d", i), "S", "collection:L")
						if tc.serialized {
							outside.Lock()
						}
						err := p.CreateOperationChange(r)
						if tc.serialized {
							outside.Unlock()
						}
						if err != nil {
							b.Error(err)
							return
						}
					}
				}()
			}
			wg.Wait()
		})
	}
}
