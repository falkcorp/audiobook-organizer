// file: core/internal/pebble/gate.go
// version: 1.0.0
// guid: 33e2f330-f4a0-4797-ac8d-38b8cd434243
// last-edited: 2026-11-07

package pebble

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosing: the engine is closing or closed; no new lease is admitted.
var ErrClosing = errors.New("pebble: engine closing")

// Gate state bits. The lease count and both flags live in one atomic word,
// so every transition reads the count and the flags it changes together (the
// design of HEAD's GatedDB, internal/database/pebble_gate.go, re-implemented):
// with the count and the closing flag in separate words a releaser can
// decrement to zero before Close begins, be preempted, and read the flag after
// Close began while a new lease was taken; it then signals the drain on a
// stale zero and the engine closes under the new lease.
const (
	closingBit = int64(1) << 62 // Close began: ordinary acquires are refused
	sealedBit  = int64(1) << 61 // drained and about to close: sync acquires refused too
	countMask  = sealedBit - 1
)

// syncGrace bounds the WAL syncs after the drain and seal's wait for a sync
// lease, past the drain deadline. A WAL fsync is milliseconds on a healthy
// disk; this only bounds a stalled one.
const syncGrace = time.Second

// walEngine is what the gate closes: the WAL sync barrier and the engine's
// own Close. *pebble.DB satisfies it; the gate's tests use a fake.
type walEngine interface {
	SyncWAL() error
	Close() error
}

// gate counts the leases held on one engine and lets Close wait for them.
// Every engine call holds a lease (05-storage §5S.6), so closing under a
// lease, a use-after-free in Pebble, cannot happen: a Close that times out
// leaves the engine open and returns an error (§5S.5 "Close timeout").
type gate struct {
	state     atomic.Int64
	drained   chan struct{}
	drainOnce sync.Once

	mu     sync.Mutex // serialises Close calls
	closed bool
	eng    walEngine
	// syncs is false for a read-only engine, which has no WAL to sync.
	syncs bool
}

func newGate(eng walEngine, syncs bool) *gate {
	return &gate{drained: make(chan struct{}), eng: eng, syncs: syncs}
}

// acquire takes one ordinary lease, or refuses with ErrClosing once Close
// began. It never blocks, and a refused acquire never touches the count: a
// caller retrying in a loop after Close began must not keep the count above
// zero and hold the drain open.
func (g *gate) acquire() error { return g.acquireUnless(closingBit | sealedBit) }

// acquireSync takes a lease for a WAL sync: admitted while Close drains,
// refused once the gate is sealed.
func (g *gate) acquireSync() error { return g.acquireUnless(sealedBit) }

func (g *gate) acquireUnless(refuse int64) error {
	for {
		s := g.state.Load()
		if s&refuse != 0 {
			return ErrClosing
		}
		if g.state.CompareAndSwap(s, s+1) {
			return nil
		}
	}
}

// release returns one lease. The release that leaves a closing gate with no
// lease signals the drain.
func (g *gate) release() {
	n := g.state.Add(-1)
	if n < 0 || n&countMask == countMask {
		panic("pebble: gate lease released more times than acquired")
	}
	if n&countMask == 0 && n&closingBit != 0 {
		g.drainOnce.Do(func() { close(g.drained) })
	}
}

// beginClose refuses every later ordinary acquire; leases already held stay
// valid. Idempotent.
func (g *gate) beginClose() {
	if g.state.Or(closingBit)&countMask == 0 {
		g.drainOnce.Do(func() { close(g.drained) })
	}
}

func (g *gate) inFlight() int64 { return g.state.Load() & countMask }

// waitDrainedUntil waits for the drain signal until deadline.
func (g *gate) waitDrainedUntil(deadline time.Time) bool {
	select {
	case <-g.drained:
		return true
	default:
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	t := time.NewTimer(remaining)
	defer t.Stop()
	select {
	case <-g.drained:
		return true
	case <-t.C:
		return false
	}
}

// seal moves a drained gate to sealed, refusing sync acquires too, waiting
// until deadline for a sync admitted after the drain. It reports whether the
// gate is sealed with no lease, the only state in which the engine may close.
func (g *gate) seal(deadline time.Time) bool {
	for {
		if g.state.CompareAndSwap(closingBit, closingBit|sealedBit) {
			return true
		}
		if s := g.state.Load(); s&sealedBit != 0 && s&countMask == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// syncWAL runs one WAL sync under a sync lease, bounded by deadline. A sync
// that outlives its deadline keeps its lease until it returns, so the engine
// is never closed under it.
func (g *gate) syncWAL(stage string, deadline time.Time) error {
	if !g.syncs {
		return nil
	}
	if err := g.acquireSync(); err != nil {
		return fmt.Errorf("pebble: close: WAL sync %s: %w", stage, err)
	}
	res := make(chan error, 1)
	go func() {
		defer g.release()
		res <- g.eng.SyncWAL()
	}()
	t := time.NewTimer(max(time.Until(deadline), 0))
	defer t.Stop()
	select {
	case err := <-res:
		if err != nil {
			return fmt.Errorf("pebble: close: WAL sync %s: %w", stage, err)
		}
		return nil
	case <-t.C:
		return fmt.Errorf("pebble: close: WAL sync %s did not return in time: %w", stage, ErrClosing)
	}
}

// close is the engine close (05-storage §5S.4 "App.Run ends"): refuse new
// work, sync the WAL so every row committed before Close is durable even if
// the process dies during the drain, wait for held leases up to timeout,
// sync again, seal, then close the engine. On timeout the engine stays open
// and an error names the leases still held; close may be called again.
func (g *gate) close(timeout time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.beginClose()
	deadline := time.Now().Add(timeout)
	limit := deadline.Add(syncGrace)
	syncErr := g.syncWAL("after refusing new work", deadline)
	if !g.waitDrainedUntil(deadline) {
		n := g.inFlight()
		return errors.Join(
			fmt.Errorf("pebble: close: %d engine accesses still in flight after %s; the engine stays open: %w", n, timeout, ErrClosing),
			syncErr)
	}
	if err := g.syncWAL("after the drain", limit); err != nil && syncErr == nil {
		syncErr = err
	}
	if !g.seal(limit) {
		return errors.Join(
			fmt.Errorf("pebble: close: %d WAL syncs still in flight after the drain; the engine stays open: %w", g.inFlight(), ErrClosing),
			syncErr)
	}
	g.closed = true
	return errors.Join(syncErr, g.eng.Close())
}
