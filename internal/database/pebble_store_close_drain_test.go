// file: internal/database/pebble_store_close_drain_test.go
// version: 1.3.0
// guid: 7c2e9b14-5d3a-4f8e-9a61-2b0c4d8e7f35
// last-edited: 2026-10-11

package database

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// Close must never run pebble's db.Close while a reader still holds a
// readState reference. Before the drain gate, PebbleStore.Close closed the DB
// immediately; the reader's later release (iterator Close, snapshot Close, Get
// closer Close) unref'd memtables Close had already unref'd and pebble
// panicked "inconsistent reference count: -1" (CI flake in
// TestOpsV2Timeline_CloseRacesReconcile). Each subtest holds one kind of
// reader open, starts Close in another goroutine, waits until the store
// refuses new work (proof Close has begun), then releases the reader. No
// sleeps: the probe polls a condition with a deadline, it does not race one.

// seedCloseDrainStore writes rows that stay in the memtable, so every reader
// pins memtables and the double-unref path is live.
func seedCloseDrainStore(t *testing.T) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 64 {
		if err := p.db.Set([]byte(fmt.Sprintf("closedrain:%03d", i)), []byte("v"), pebble.NoSync); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// probeRefusesNewWork reports whether a fresh read on p is refused as
// closed. A pre-gate store panics ErrClosed instead of returning it, so the
// panic is recovered and counted as a refusal too.
func probeRefusesNewWork(p *PebbleStore) (refused bool) {
	defer func() {
		if rec := recover(); rec != nil {
			if err, ok := rec.(error); ok && errors.Is(err, pebble.ErrClosed) {
				refused = true
				return
			}
			panic(rec)
		}
	}()
	_, closer, err := p.db.Get([]byte("closedrain:000"))
	if err == nil {
		_ = closer.Close()
		return false
	}
	return errors.Is(err, pebble.ErrClosed)
}

// releaseNoPanic runs release and converts a panic into a returned error.
func releaseNoPanic(release func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("release panicked: %v", rec)
		}
	}()
	return release()
}

func runCloseDrainCase(t *testing.T, open func(p *PebbleStore) (io.Closer, error)) {
	p := seedCloseDrainStore(t)
	held, err := open(p)
	if err != nil {
		t.Fatal(err)
	}

	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close() }()

	deadline := time.Now().Add(10 * time.Second)
	for !probeRefusesNewWork(p) {
		if time.Now().After(deadline) {
			t.Fatal("store never started refusing new work after Close began")
		}
		time.Sleep(time.Millisecond)
	}

	if err := releaseNoPanic(held.Close); err != nil {
		t.Fatalf("releasing a reader opened before Close: %v", err)
	}
	select {
	case err := <-closeErr:
		if err != nil {
			t.Fatalf("Close returned %v; want nil once the in-flight reader drained", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return after the in-flight reader was released")
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestPebbleStoreClose_WaitsForInFlightReaders(t *testing.T) {
	t.Run("iterator", func(t *testing.T) {
		runCloseDrainCase(t, func(p *PebbleStore) (io.Closer, error) {
			it, err := p.db.NewIter(&pebble.IterOptions{})
			if err != nil {
				return nil, err
			}
			it.First()
			return it, nil
		})
	})
	t.Run("snapshot iterator", func(t *testing.T) {
		runCloseDrainCase(t, func(p *PebbleStore) (io.Closer, error) {
			snap := p.db.NewSnapshot()
			it, err := snap.NewIter(&pebble.IterOptions{})
			if err != nil {
				_ = snap.Close()
				return nil, err
			}
			it.First()
			return closerFunc(func() error {
				ierr := it.Close()
				serr := snap.Close()
				if ierr != nil {
					return ierr
				}
				return serr
			}), nil
		})
	})
	t.Run("bare snapshot", func(t *testing.T) {
		runCloseDrainCase(t, func(p *PebbleStore) (io.Closer, error) {
			return p.db.NewSnapshot(), nil
		})
	})
	t.Run("snapshot iterator outliving its snapshot", func(t *testing.T) {
		// The iterator's own lease, not the snapshot's, must hold Close.
		runCloseDrainCase(t, func(p *PebbleStore) (io.Closer, error) {
			snap := p.db.NewSnapshot()
			it, err := snap.NewIter(&pebble.IterOptions{})
			if err != nil {
				_ = snap.Close()
				return nil, err
			}
			it.First()
			if err := snap.Close(); err != nil {
				_ = it.Close()
				return nil, err
			}
			return it, nil
		})
	})
	t.Run("get closer", func(t *testing.T) {
		runCloseDrainCase(t, func(p *PebbleStore) (io.Closer, error) {
			_, closer, err := p.db.Get([]byte("closedrain:001"))
			return closer, err
		})
	})
	t.Run("indexed batch iterator", func(t *testing.T) {
		runCloseDrainCase(t, func(p *PebbleStore) (io.Closer, error) {
			b := p.db.NewIndexedBatch()
			it, err := b.NewIter(&pebble.IterOptions{})
			if err != nil {
				_ = b.Close()
				return nil, err
			}
			it.First()
			return closerFunc(func() error {
				ierr := it.Close()
				berr := b.Close()
				if ierr != nil {
					return ierr
				}
				return berr
			}), nil
		})
	})
}

// A reader that never releases must not hang Close, and Close must not close
// the engine under it: Close returns an ErrClosed-wrapping error after the
// drain timeout, and the held iterator keeps working on the still-open
// engine.
func TestPebbleStoreClose_DrainTimeoutLeavesEngineOpen(t *testing.T) {
	p := seedCloseDrainStore(t)
	p.closeDrainTimeout = 50 * time.Millisecond
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte("closedrain:"), UpperBound: []byte("closedrain;")})
	if err != nil {
		t.Fatal(err)
	}
	cerr := p.Close()
	if cerr == nil || !errors.Is(cerr, pebble.ErrClosed) {
		t.Fatalf("Close with a held iterator returned %v; want an error wrapping pebble.ErrClosed", cerr)
	}
	n := 0
	for it.First(); it.Valid(); it.Next() {
		n++
	}
	if n != 64 {
		t.Fatalf("held iterator saw %d rows after a timed-out Close; want 64", n)
	}
	if err := releaseNoPanic(it.Close); err != nil {
		t.Fatalf("releasing after a timed-out Close: %v", err)
	}
	if err := p.db.closeEngine(); err != nil {
		t.Fatalf("engine left open by the timed-out Close did not close cleanly: %v", err)
	}
}

// Once Close has begun, every new access is refused with an error wrapping
// pebble.ErrClosed instead of panicking, and nested accesses made by a
// holder before Close began still work (acquire never blocks).
func TestPebbleStoreClose_RefusesNewWorkWithoutPanic(t *testing.T) {
	p := seedCloseDrainStore(t)
	it, err := p.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Nested lease while the outer one is held, before Close: must not block.
	if _, c, err := p.db.Get([]byte("closedrain:002")); err != nil {
		t.Fatal(err)
	} else {
		_ = c.Close()
	}

	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close() }()
	for !p.db.gate.closing() {
		runtime.Gosched()
	}

	refusals := map[string]error{
		"Set":         p.db.Set([]byte("x"), []byte("y"), pebble.Sync),
		"Delete":      p.db.Delete([]byte("x"), pebble.Sync),
		"DeleteRange": p.db.DeleteRange([]byte("a"), []byte("b"), pebble.Sync),
		"Flush":       p.db.Flush(),
	}
	_, _, refusals["Get"] = p.db.Get([]byte("closedrain:000"))
	_, refusals["NewIter"] = p.db.NewIter(&pebble.IterOptions{})
	_, refusals["Metrics"] = p.db.Metrics()
	snap := p.db.NewSnapshot()
	_, refusals["Snapshot.NewIter"] = snap.NewIter(&pebble.IterOptions{})
	_, _, refusals["Snapshot.Get"] = snap.Get([]byte("closedrain:000"))
	refusals["Snapshot.Close"] = snap.Close()
	b := p.db.NewBatch()
	_ = b.Set([]byte("x"), []byte("y"), nil)
	refusals["Batch.Commit"] = b.Commit(pebble.Sync)
	_ = b.Close()
	for name, err := range refusals {
		if name == "Snapshot.Close" {
			if err != nil {
				t.Errorf("Close of a refused snapshot returned %v; want nil", err)
			}
			continue
		}
		if !errors.Is(err, pebble.ErrClosed) {
			t.Errorf("%s after Close began returned %v; want an error wrapping pebble.ErrClosed", name, err)
		}
	}

	if err := releaseNoPanic(it.Close); err != nil {
		t.Fatal(err)
	}
	if err := releaseNoPanic(it.Close); err != nil {
		t.Fatalf("second Close of a gated iterator: %v", err)
	}
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// The gate under concurrency: many goroutines take and release leases while
// Close begins; Close's drain fires only after every lease taken before it
// is released, and the count never goes negative (release panics if it does).
func TestDBGate_ConcurrentAcquireRelease(t *testing.T) {
	for range 20 {
		g := newDBGate()
		var started, stop = make(chan struct{}), make(chan struct{})
		held := make(chan struct{})
		go func() {
			if err := g.acquire(); err != nil {
				panic(err)
			}
			close(held)
			<-stop
			g.release()
		}()
		<-held
		done := make(chan struct{})
		for range 8 {
			go func() {
				<-started
				for {
					select {
					case <-done:
						return
					default:
					}
					if g.acquire() == nil {
						g.release()
					}
				}
			}()
		}
		close(started)
		g.beginClose()
		if g.waitDrained(20 * time.Millisecond) {
			t.Fatal("drain signalled while a lease taken before beginClose was still held")
		}
		close(stop)
		if !g.waitDrained(10 * time.Second) {
			t.Fatal("drain never signalled after the last lease was released")
		}
		close(done)
		if g.acquire() == nil {
			t.Fatal("acquire succeeded after beginClose")
		}
	}
}

// A releaser preempted between its decrement and its drain check must not
// signal the drain on a stale count. The interleaving: R drops the last lease
// and parks; L acquires (Close not begun); Close begins and sees L; R resumes.
// R's decrement happened before Close began, so it must not signal, and the
// drain must wait for L.
func TestDBGate_StaleReleaseDoesNotSignalDrain(t *testing.T) {
	g := newDBGate()
	if err := g.acquire(); err != nil { // R's lease
		t.Fatal(err)
	}
	paused, resume := make(chan struct{}), make(chan struct{})
	g.setReleaseHook(func(int64) {
		close(paused)
		<-resume
	})
	rDone := make(chan struct{})
	go func() { defer close(rDone); g.release() }() // R parks after its decrement
	<-paused
	g.setReleaseHook(nil) // R already read the hook; L's release must not park
	if err := g.acquire(); err != nil {
		t.Fatalf("L acquire before Close began: %v", err)
	}
	g.beginClose()
	close(resume)
	<-rDone
	if g.waitDrained(100 * time.Millisecond) {
		t.Fatalf("drain signalled while L still holds a lease (in flight %d)", g.inFlight())
	}
	g.release()
	if !g.waitDrained(5 * time.Second) {
		t.Fatal("drain not signalled after L released")
	}
}

// crashCloneHasKey opens a store on a crash image of fs (only synced data
// survives) and reports whether key is there.
func crashCloneHasKey(t *testing.T, fs *vfs.MemFS, key []byte) bool {
	t.Helper()
	img := fs.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 0})
	p, err := newPebbleStore("db", img)
	if err != nil {
		t.Fatalf("reopen crash image: %v", err)
	}
	defer func() { _ = p.Close() }()
	_, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("read crash image: %v", err)
	}
	_ = closer.Close()
	return true
}

// pebble v2.1.7 keeps a NoSync WAL record in a process buffer until a sync, a
// full block, or engine close. A Close whose drain times out leaves the
// engine open, so without an explicit sync a row committed NoSync before
// shutdown never reaches disk. Close must sync the WAL on that path.
func TestPebbleStoreClose_DrainTimeoutStillSyncsCommittedRows(t *testing.T) {
	fs := vfs.NewCrashableMem()
	p, err := newPebbleStore("db", fs)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("closedrain:durable")
	if err := p.db.Set(key, []byte("v"), pebble.NoSync); err != nil {
		t.Fatal(err)
	}
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte("closedrain:"), UpperBound: []byte("closedrain;")})
	if err != nil {
		t.Fatal(err)
	}
	p.closeDrainTimeout = 50 * time.Millisecond
	if err := p.Close(); err == nil {
		t.Fatal("Close with a held reader returned nil; want the drain timeout error")
	}
	durable := crashCloneHasKey(t, fs, key)
	_ = it.Close()
	_ = p.db.closeEngine()
	if !durable {
		t.Fatal("a row committed NoSync before Close was lost by a crash after a timed-out Close")
	}
}

// A writer whose commit landed before Close began, and whose own WAL sync
// runs while Close is draining, must get that sync (it is what makes its
// already-visible rows durable), not a "closing" refusal.
func TestPebbleStoreClose_WriterSyncDuringDrainSucceeds(t *testing.T) {
	fs := vfs.NewCrashableMem()
	p, err := newPebbleStore("db", fs)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("closedrain:writer")
	if err := p.db.Set(key, []byte("v"), pebble.NoSync); err != nil {
		t.Fatal(err)
	}
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte("closedrain:"), UpperBound: []byte("closedrain;")})
	if err != nil {
		t.Fatal(err)
	}
	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	for !probeRefusesNewWork(p) {
		if time.Now().After(deadline) {
			t.Fatal("store never started refusing new work after Close began")
		}
		time.Sleep(time.Millisecond)
	}
	if err := p.syncBookFileWAL(); err != nil {
		t.Fatalf("book_file WAL sync while Close drains: %v", err)
	}
	if !crashCloneHasKey(t, fs, key) {
		t.Fatal("row not durable after the writer's sync during the drain")
	}
	_ = it.Close()
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A WAL sync admitted while Close drains holds a lease too: the engine must
// not close until it finishes, even if every ordinary lease has drained.
// After Close, SyncWAL is refused with an ErrClosed error, not a panic.
func TestPebbleStoreClose_WaitsForSyncAdmittedDuringDrain(t *testing.T) {
	p := seedCloseDrainStore(t)
	it, err := p.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close() }()
	for !p.db.gate.closing() {
		runtime.Gosched()
	}
	// Stand-in for a SyncWAL caught mid-fsync: its lease, taken while
	// closing, outlives the last ordinary lease.
	if err := p.db.gate.acquireSync(); err != nil {
		t.Fatalf("sync lease refused while draining: %v", err)
	}
	if err := releaseNoPanic(it.Close); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closeErr:
		t.Fatalf("Close returned (%v) while a WAL sync still held its lease", err)
	case <-time.After(100 * time.Millisecond):
	}
	p.db.gate.release()
	if err := <-closeErr; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.db.SyncWAL(); !errors.Is(err, pebble.ErrClosed) {
		t.Fatalf("SyncWAL after Close returned %v; want an error wrapping pebble.ErrClosed", err)
	}
}

// A second Close, concurrent or later, returns the first one's result
// instead of panicking on pebble's double close.
func TestPebbleStoreClose_Idempotent(t *testing.T) {
	p := seedCloseDrainStore(t)
	errs := make(chan error, 4)
	for range 4 {
		go func() { errs <- releaseNoPanic(p.Close) }()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}
	if err := releaseNoPanic(p.Close); err != nil {
		t.Fatalf("Close after Close: %v", err)
	}
}

// Every engine call takes and returns a lease, so Close's drain can see it.
// Short calls (a write, a commit, a metrics read) finish too fast to hold
// open from a test, so this checks the accounting directly: each call must
// release exactly the leases it took, at least one, and leave none held.
func TestGatedDB_EveryEngineCallIsLeased(t *testing.T) {
	p := seedCloseDrainStore(t)
	if p.warmupCancel != nil {
		p.warmupCancel()
	}
	if p.warmupDone != nil {
		<-p.warmupDone // no background gate traffic while the hook counts
	}
	t.Cleanup(func() { _ = p.Close() })
	g := p.db.gate
	calls := map[string]func() error{
		"Set":         func() error { return p.db.Set([]byte("lease:a"), []byte("v"), pebble.NoSync) },
		"Delete":      func() error { return p.db.Delete([]byte("lease:a"), pebble.NoSync) },
		"DeleteRange": func() error { return p.db.DeleteRange([]byte("lease:a"), []byte("lease:b"), pebble.NoSync) },
		"LogData":     func() error { return p.db.LogData([]byte("x"), pebble.NoSync) },
		"SyncWAL":     p.db.SyncWAL,
		"Flush":       p.db.Flush,
		"Metrics":     func() error { _, err := p.db.Metrics(); return err },
		"EstimateDiskUsage": func() error {
			_, err := p.db.EstimateDiskUsage([]byte("a"), []byte("z"))
			return err
		},
		"SSTables": func() error { _, err := p.db.SSTables(); return err },
		"Batch.Commit": func() error {
			b := p.db.NewBatch()
			defer func() { _ = b.Close() }()
			if err := b.Set([]byte("lease:b"), []byte("v"), nil); err != nil {
				return err
			}
			return b.Commit(pebble.NoSync)
		},
		"IndexedBatch.Get": func() error {
			b := p.db.NewIndexedBatch()
			defer func() { _ = b.Close() }()
			_, c, err := b.Get([]byte("closedrain:000"))
			if err != nil {
				return err
			}
			return c.Close()
		},
		"IndexedBatch.NewIter": func() error {
			b := p.db.NewIndexedBatch()
			defer func() { _ = b.Close() }()
			it, err := b.NewIter(&pebble.IterOptions{})
			if err != nil {
				return err
			}
			return it.Close()
		},
		"Iter.Clone": func() error {
			it, err := p.db.NewIter(&pebble.IterOptions{})
			if err != nil {
				return err
			}
			c, err := it.Clone(pebble.CloneOptions{})
			if err != nil {
				_ = it.Close()
				return err
			}
			return errors.Join(c.Close(), it.Close())
		},
	}
	for name, call := range calls {
		var releases atomic.Int64
		g.setReleaseHook(func(int64) { releases.Add(1) })
		err := call()
		g.setReleaseHook(nil)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if releases.Load() == 0 {
			t.Errorf("%s took no lease, so Close's drain cannot see it", name)
		}
		if n := g.inFlight(); n != 0 {
			t.Errorf("%s left %d leases held", name, n)
		}
	}
}

// A refused acquire must not touch the lease count at all. If it counted
// itself and backed out, callers retrying in a tight loop after Close began
// would keep the count above zero between them and starve the drain (seen
// under -race -cpu 8 as a drain that never signalled). The back-out would
// show up here as a release.
func TestDBGate_RefusedAcquireLeavesCountAlone(t *testing.T) {
	g := newDBGate()
	if err := g.acquire(); err != nil {
		t.Fatal(err)
	}
	g.beginClose()
	var releases atomic.Int64
	g.setReleaseHook(func(int64) { releases.Add(1) })
	for range 100 {
		if err := g.acquire(); !errors.Is(err, pebble.ErrClosed) {
			t.Fatalf("acquire after beginClose returned %v; want ErrClosed", err)
		}
	}
	g.setReleaseHook(nil)
	if n := releases.Load(); n != 0 {
		t.Fatalf("refused acquires touched the count (%d back-out releases)", n)
	}
	if n := g.inFlight(); n != 1 {
		t.Fatalf("in flight %d after refused acquires; want 1", n)
	}
	g.release()
	if !g.waitDrained(5 * time.Second) {
		t.Fatal("drain not signalled after the last real lease was released")
	}
}
