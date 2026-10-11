// file: internal/database/pebble_gate_shutdown_test.go
// version: 1.2.0
// guid: 9b3e5d70-2c1a-4f68-8e47-d5a0c6b1f293
// last-edited: 2026-10-11

package database

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// Tests for the shutdown half of the gate: the owned-engine close function,
// seal, the WAL sync on the timeout path, and the bounded WAL syncs.

// setReleaseHook installs (or, with nil, clears) the release hook. The hook
// is atomic, so this is safe while background goroutines release leases.
func (g *dbGate) setReleaseHook(f func(int64)) {
	if f == nil {
		g.releaseHook.Store(nil)
		return
	}
	g.releaseHook.Store(&f)
}

// setShutdownHook installs (or, with nil, clears) the drainAndSeal hook.
func (g *dbGate) setShutdownHook(f func(shutdownStage)) {
	if f == nil {
		g.shutdownHook.Store(nil)
		return
	}
	g.shutdownHook.Store(&f)
}

// stallFS wraps a filesystem so every file sync blocks while the FS is
// stalled: a disk that has stopped answering fsync.
type stallFS struct {
	vfs.FS
	mu      sync.Mutex
	stalled chan struct{} // non-nil while stalled; closed to release
	waiting atomic.Int64  // syncs currently blocked
}

func newStallFS(inner vfs.FS) *stallFS { return &stallFS{FS: inner} }

func (s *stallFS) stall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stalled == nil {
		s.stalled = make(chan struct{})
	}
}

func (s *stallFS) unstall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stalled != nil {
		close(s.stalled)
		s.stalled = nil
	}
}

func (s *stallFS) block() {
	s.mu.Lock()
	ch := s.stalled
	s.mu.Unlock()
	if ch == nil {
		return
	}
	s.waiting.Add(1)
	defer s.waiting.Add(-1)
	<-ch
}

func (s *stallFS) wrap(f vfs.File, err error) (vfs.File, error) {
	if err != nil {
		return f, err
	}
	return &stallFile{File: f, fs: s}, nil
}

func (s *stallFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return s.wrap(s.FS.Create(name, cat))
}

func (s *stallFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	return s.wrap(s.FS.Open(name, opts...))
}

func (s *stallFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	return s.wrap(s.FS.OpenReadWrite(name, cat, opts...))
}

func (s *stallFS) ReuseForWrite(oldname, newname string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return s.wrap(s.FS.ReuseForWrite(oldname, newname, cat))
}

type stallFile struct {
	vfs.File
	fs *stallFS
}

func (f *stallFile) Sync() error     { f.fs.block(); return f.File.Sync() }
func (f *stallFile) SyncData() error { f.fs.block(); return f.File.SyncData() }
func (f *stallFile) SyncTo(n int64) (fullSync bool, err error) {
	f.fs.block()
	return f.File.SyncTo(n)
}

// waitFor polls cond until it holds or 10 s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func openOwnedMem(t *testing.T, fs vfs.FS) (*GatedDB, func(time.Duration) error) {
	t.Helper()
	db, err := pebble.Open("db", &pebble.Options{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	return NewOwnedGatedDB(db)
}

func seedOwned(t *testing.T, g *GatedDB, n int) {
	t.Helper()
	for i := range n {
		if err := g.Set([]byte(fmt.Sprintf("owned:%03d", i)), []byte("v"), pebble.NoSync); err != nil {
			t.Fatal(err)
		}
	}
}

// countRows walks the iterator and reports how many rows it saw, turning a
// panic from a closed engine into an error.
func countRows(it *GatedIter) (n int, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("iterating panicked: %v", rec)
		}
	}()
	for it.First(); it.Valid(); it.Next() {
		n++
	}
	return n, it.Error()
}

// The close function NewOwnedGatedDB returns must not close the engine when
// its drain times out: a held iterator keeps working, new work is refused, a
// retry while the iterator is still held fails again, and once the iterator
// is released the next call closes the engine cleanly. (An owned close that
// goes on to close the engine anyway after a timeout makes the later
// iterator release panic with a pebble refcount error.)
func TestOwnedGatedDB_TimedOutCloseLeavesEngineOpenThenRetryCloses(t *testing.T) {
	g, closeDB := openOwnedMem(t, vfs.NewMem())
	seedOwned(t, g, 16)
	it, err := g.NewIter(&pebble.IterOptions{LowerBound: []byte("owned:"), UpperBound: []byte("owned;")})
	if err != nil {
		t.Fatal(err)
	}

	cerr := closeDB(50 * time.Millisecond)
	if cerr == nil || !errors.Is(cerr, pebble.ErrClosed) {
		t.Fatalf("close with a held iterator returned %v; want an error wrapping pebble.ErrClosed", cerr)
	}
	n, ierr := countRows(it)
	if ierr != nil || n != 16 {
		t.Fatalf("held iterator after a timed-out close: %d rows, err %v; want 16 rows (the engine must still be open)", n, ierr)
	}
	if _, _, gerr := g.Get([]byte("owned:000")); !errors.Is(gerr, pebble.ErrClosed) {
		t.Fatalf("Get after a close began returned %v; want ErrClosed", gerr)
	}
	if cerr := closeDB(50 * time.Millisecond); cerr == nil {
		t.Fatal("a second close with the iterator still held returned nil")
	}

	if err := releaseNoPanic(it.Close); err != nil {
		t.Fatalf("releasing the iterator after timed-out closes: %v", err)
	}
	if err := closeDB(10 * time.Second); err != nil {
		t.Fatalf("close after the iterator was released: %v", err)
	}
	if err := closeDB(time.Second); err != nil {
		t.Fatalf("close after a successful close: %v", err)
	}
	if _, _, gerr := g.Get([]byte("owned:000")); !errors.Is(gerr, pebble.ErrClosed) {
		t.Fatalf("Get after a successful close returned %v; want ErrClosed", gerr)
	}
}

// Callers that arrive while a close attempt is running share it. N callers
// against a held straggler used to run N attempts back to back, each waiting
// its own timeout plus syncGrace (N x 11 s with the production default).
// Now they all get the result of the one attempt, so the whole herd costs one
// bound; a call made after that attempt finished is still a fresh retry, and
// a successful close stays idempotent.
func TestOwnedGatedDB_ConcurrentClosersShareOneAttempt(t *testing.T) {
	const callers = 8
	const timeout = 400 * time.Millisecond

	g, closeDB := openOwnedMem(t, vfs.NewMem())
	seedOwned(t, g, 4)
	it, err := g.NewIter(&pebble.IterOptions{LowerBound: []byte("owned:"), UpperBound: []byte("owned;")})
	if err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int32
	g.gate.setShutdownHook(func(st shutdownStage) {
		if st == stageRefusedAndSynced {
			attempts.Add(1)
		}
	})
	t.Cleanup(func() { g.gate.setShutdownHook(nil) })

	start := make(chan struct{})
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			<-start
			errs[i] = closeDB(timeout)
		})
	}
	begin := time.Now()
	close(start)
	wg.Wait()
	elapsed := time.Since(begin)

	for i, e := range errs {
		if e == nil || !errors.Is(e, pebble.ErrClosed) {
			t.Fatalf("caller %d: close with a held iterator returned %v; want an error wrapping pebble.ErrClosed", i, e)
		}
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("%d callers ran %d close attempts; want 1 shared attempt", callers, got)
	}
	// One attempt is ~timeout; serialised attempts would be ~callers x timeout.
	if limit := 3 * timeout; elapsed > limit {
		t.Fatalf("%d concurrent closers took %s; want about one bound (%s), not %d of them", callers, elapsed, timeout, callers)
	}

	// The failed attempt has finished, so a new call retries (and fails again
	// while the straggler is held), then succeeds once it lets go.
	if e := closeDB(50 * time.Millisecond); e == nil {
		t.Fatal("a close after the shared attempt, with the straggler still held, returned nil")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("a new call after a failed attempt ran %d total attempts; want 2 (it must retry)", got)
	}
	if e := releaseNoPanic(it.Close); e != nil {
		t.Fatalf("releasing the iterator: %v", e)
	}
	if e := closeDB(10 * time.Second); e != nil {
		t.Fatalf("close after the straggler released: %v", e)
	}
	if e := closeDB(time.Second); e != nil {
		t.Fatalf("close after a successful close: %v", e)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("%d total attempts; want 3 (a close after success must not run another)", got)
	}
}

// seal alone: it must refuse to seal while a sync lease is held, and seal
// once the lease is released.
func TestDBGate_SealWaitsForSyncLease(t *testing.T) {
	g := newDBGate()
	g.beginClose()
	if err := g.acquireSync(); err != nil {
		t.Fatalf("acquireSync while draining: %v", err)
	}
	if g.seal(time.Now().Add(20 * time.Millisecond)) {
		t.Fatal("seal succeeded while a sync lease was held")
	}
	g.release()
	if !g.seal(time.Now().Add(5 * time.Second)) {
		t.Fatal("seal did not succeed after the sync lease was released")
	}
	if err := g.acquireSync(); !errors.Is(err, pebble.ErrClosed) {
		t.Fatalf("acquireSync after seal returned %v; want ErrClosed", err)
	}
}

// A SyncWAL admitted after the drain fired but before seal holds a lease, so
// the engine must not close until it finishes.
func TestSeal_WaitsForSyncAdmittedBetweenDrainAndSeal(t *testing.T) {
	p := seedCloseDrainStore(t)
	releaseSync := make(chan struct{})
	var once sync.Once
	releaseNow := func() { once.Do(func() { close(releaseSync) }) }
	defer releaseNow()
	var held atomic.Bool
	p.db.gate.setShutdownHook(func(st shutdownStage) {
		if st != stageDrained {
			return
		}
		if err := p.db.gate.acquireSync(); err != nil {
			t.Errorf("SyncWAL lease refused between the drain and seal: %v", err)
			return
		}
		held.Store(true)
		go func() { <-releaseSync; p.db.gate.release() }()
	})

	closeErr := make(chan error, 1)
	go func() { closeErr <- p.Close() }()
	waitFor(t, "the sync lease to be taken between the drain and seal", held.Load)

	select {
	case err := <-closeErr:
		t.Fatalf("Close returned (%v) while a WAL sync admitted after the drain still held its lease", err)
	case <-time.After(150 * time.Millisecond):
	}
	releaseNow()
	select {
	case err := <-closeErr:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return after the sync lease was released")
	}
	if err := p.db.SyncWAL(); !errors.Is(err, pebble.ErrClosed) {
		t.Fatalf("SyncWAL after Close returned %v; want an error wrapping pebble.ErrClosed", err)
	}
}

// A sync lease that is never released must make Close give up with an error
// and leave the engine open, not close it under the lease and not hang.
func TestSeal_TimesOutWhileSyncLeaseHeldAndLeavesEngineOpen(t *testing.T) {
	p := seedCloseDrainStore(t)
	p.closeDrainTimeout = 50 * time.Millisecond
	var held atomic.Bool
	p.db.gate.setShutdownHook(func(st shutdownStage) {
		if st == stageDrained && p.db.gate.acquireSync() == nil {
			held.Store(true)
		}
	})

	start := time.Now()
	cerr := p.Close()
	if !held.Load() {
		t.Fatal("the hook never took its sync lease")
	}
	if cerr == nil || !errors.Is(cerr, pebble.ErrClosed) {
		t.Fatalf("Close with a stuck sync lease returned %v; want an error wrapping pebble.ErrClosed", cerr)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("Close took %s with a stuck sync lease; it must be bounded", el)
	}
	// The engine is still open: release the lease and close it ourselves.
	p.db.gate.release()
	if err := releaseNoPanic(p.db.closeEngine); err != nil {
		t.Fatalf("engine left open by the timed-out Close did not close cleanly: %v", err)
	}
}

// pebble keeps a NoSync WAL record in a process buffer until a later sync. A
// row an in-flight writer commits NoSync AFTER Close's first sync, while a
// lease is still held, is only made durable by the sync on the timeout path
// (the engine stays open, so no close-time flush follows).
func TestPebbleStoreClose_TimeoutPathSyncsRowsCommittedDuringDrain(t *testing.T) {
	fs := vfs.NewCrashableMem()
	p, err := newPebbleStore("db", fs)
	if err != nil {
		t.Fatal(err)
	}
	it, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte("closedrain:"), UpperBound: []byte("closedrain;")})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("closedrain:committed-during-drain")
	p.db.gate.setShutdownHook(func(st shutdownStage) {
		if st != stageRefusedAndSynced {
			return
		}
		// A writer that took its lease before Close began and commits now.
		// The gate refuses a new Set, so write through the raw engine, the
		// way that in-flight commit reaches it.
		if err := p.db.db.Set(key, []byte("v"), pebble.NoSync); err != nil {
			t.Errorf("simulated in-flight commit: %v", err)
		}
	})
	p.closeDrainTimeout = 50 * time.Millisecond
	if err := p.Close(); err == nil {
		t.Fatal("Close with a held reader returned nil; want the drain timeout error")
	}
	durable := crashCloneHasKey(t, fs, key)
	_ = it.Close()
	_ = p.db.closeEngine()
	if !durable {
		t.Fatal("a row committed NoSync during the drain was lost by a crash after a timed-out Close")
	}
}

// A WAL fsync that never returns (a stalled disk) must not hang Close. Close
// returns an error after the drain timeout plus syncGrace, the engine stays
// open for as long as the fsync is in flight, and it closes cleanly once the
// disk answers.
func TestPebbleStoreClose_StalledWALSyncDoesNotHangClose(t *testing.T) {
	sfs := newStallFS(vfs.NewMem())
	p, err := newPebbleStore("db", sfs)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if err := p.db.Set([]byte(fmt.Sprintf("closedrain:%03d", i)), []byte("v"), pebble.NoSync); err != nil {
			t.Fatal(err)
		}
	}
	p.closeDrainTimeout = 100 * time.Millisecond
	sfs.stall()
	defer sfs.unstall()

	done := make(chan error, 1)
	go func() { done <- p.Close() }()
	select {
	case cerr := <-done:
		if cerr == nil || !errors.Is(cerr, pebble.ErrClosed) {
			t.Fatalf("Close on a stalled disk returned %v; want an error wrapping pebble.ErrClosed", cerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung on a stalled WAL sync")
	}
	if n := p.db.gate.inFlight(); n < 1 {
		t.Fatalf("no lease held for the stalled fsync (in flight %d); the engine could close under it", n)
	}

	sfs.unstall()
	waitFor(t, "the stalled fsync to release its lease", func() bool { return p.db.gate.inFlight() == 0 })
	if err := releaseNoPanic(p.db.closeEngine); err != nil {
		t.Fatalf("engine left open by the timed-out Close did not close cleanly: %v", err)
	}
}

// The owned close function retries after a stalled sync: once the disk
// answers, the next call closes the engine.
func TestOwnedGatedDB_StalledSyncThenRetryCloses(t *testing.T) {
	sfs := newStallFS(vfs.NewMem())
	g, closeDB := openOwnedMem(t, sfs)
	seedOwned(t, g, 8)
	sfs.stall()
	defer sfs.unstall()

	done := make(chan error, 1)
	go func() { done <- closeDB(100 * time.Millisecond) }()
	select {
	case cerr := <-done:
		if cerr == nil {
			t.Fatal("close on a stalled disk returned nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("close hung on a stalled WAL sync")
	}

	sfs.unstall()
	waitFor(t, "a retried close to succeed once the disk answers", func() bool {
		return closeDB(time.Second) == nil
	})
}

// The test hooks are atomic: a test can install and clear them while the
// store is live and goroutines are taking and releasing leases (under -race,
// a plain field read in release would be a data race with the writes here).
func TestDBGate_HooksSettableWhileLive(t *testing.T) {
	g := newDBGate()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if g.acquire() == nil {
					g.release()
				}
			}
		})
	}
	var fired atomic.Int64
	for range 2000 {
		g.setReleaseHook(func(int64) { fired.Add(1) })
		g.setShutdownHook(func(shutdownStage) {})
		g.setReleaseHook(nil)
		g.setShutdownHook(nil)
	}
	close(stop)
	wg.Wait()
	_ = fired.Load()
}
