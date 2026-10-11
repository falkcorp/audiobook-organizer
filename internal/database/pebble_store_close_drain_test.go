// file: internal/database/pebble_store_close_drain_test.go
// version: 1.0.0
// guid: 7c2e9b14-5d3a-4f8e-9a61-2b0c4d8e7f35
// last-edited: 2026-10-11

package database

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
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
