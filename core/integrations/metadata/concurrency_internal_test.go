// file: core/integrations/metadata/concurrency_internal_test.go
// version: 0.1.0
// guid: 3f8b2d95-6e41-4a07-9c5d-b1e7a0d36c82
// last-edited: 2026-11-22

package metadata

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func (g *gate) snapshot() (held, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held, len(g.waiters)
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// mustAcquire is an acquire that fails the test rather than wait forever.
func mustAcquire(t *testing.T, g *gate, limit int) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rel, err := g.acquire(ctx, limit)
	if err != nil {
		t.Fatalf("acquire at limit %d: %v", limit, err)
	}
	return rel
}

// acquireAsync starts an acquire and returns a channel that gets its release.
func acquireAsync(g *gate, limit int) <-chan func() {
	ch := make(chan func(), 1)
	go func() {
		rel, err := g.acquire(context.Background(), limit)
		if err != nil {
			close(ch)
			return
		}
		ch <- rel
	}()
	return ch
}

// Waiters are admitted in arrival order, one per freed slot.
func TestGate_AdmitsInArrivalOrder(t *testing.T) {
	g := &gate{}
	r1, _ := g.acquire(context.Background(), 1)
	var order []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range 3 {
		waitFor(t, "waiter to queue", func() bool { _, w := g.snapshot(); return w == i })
		wg.Go(func() {
			rel, err := g.acquire(context.Background(), 1)
			if err != nil {
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			rel()
		})
	}
	waitFor(t, "all three queued", func() bool { _, w := g.snapshot(); return w == 3 })
	r1()
	wg.Wait()
	if len(order) != 3 || order[0] != 0 || order[1] != 1 || order[2] != 2 {
		t.Errorf("admission order = %v, want [0 1 2]", order)
	}
	if h, w := g.snapshot(); h != 0 || w != 0 {
		t.Errorf("after all released: held=%d waiting=%d", h, w)
	}
}

// A release function that runs twice gives back one slot, not two.
func TestGate_ReleaseIsIdempotent(t *testing.T) {
	g := &gate{}
	a, _ := g.acquire(context.Background(), 2)
	_, _ = g.acquire(context.Background(), 2)
	a()
	a()
	if h, _ := g.snapshot(); h != 1 {
		t.Fatalf("held = %d after one slot was released twice, want 1", h)
	}
	_, _ = g.acquire(context.Background(), 2) // the freed slot
	third := acquireAsync(g, 2)
	time.Sleep(20 * time.Millisecond)
	select {
	case <-third:
		t.Fatal("a double release let a third holder in beside two")
	default:
	}
}

// Lowering the limit admits no one until holders are under it; raising it, or
// lifting it with 0, admits the waiters it now has room for.
func TestGate_LimitChangesApplyOnTheNextAcquire(t *testing.T) {
	g := &gate{}
	r := make([]func(), 3)
	for i := range r {
		r[i], _ = g.acquire(context.Background(), 3)
	}
	low := acquireAsync(g, 1)
	waitFor(t, "the lowered-limit waiter to queue", func() bool { _, w := g.snapshot(); return w == 1 })
	r[0]()
	r[1]()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-low:
		t.Fatal("admitted with one holder left under a limit of 1")
	default:
	}
	r[2]()
	select {
	case rel := <-low:
		rel()
	case <-time.After(2 * time.Second):
		t.Fatal("not admitted once holders were under the lowered limit")
	}

	// Raising: a limit-1 gate with a holder and a waiter; an acquire at limit 3 admits the waiter.
	hold, _ := g.acquire(context.Background(), 1)
	w := acquireAsync(g, 1)
	waitFor(t, "waiter to queue", func() bool { _, n := g.snapshot(); return n == 1 })
	other := mustAcquire(t, g, 3)
	select {
	case rel := <-w:
		rel()
	case <-time.After(2 * time.Second):
		t.Fatal("raising the limit did not admit the queued waiter")
	}
	other()
	hold()

	// Lifting: limit 0 admits every waiter and takes no slot itself.
	hold, _ = g.acquire(context.Background(), 1)
	w1, w2 := acquireAsync(g, 1), acquireAsync(g, 1)
	waitFor(t, "two waiters to queue", func() bool { _, n := g.snapshot(); return n == 2 })
	mustAcquire(t, g, 0)()
	for _, c := range []<-chan func(){w1, w2} {
		select {
		case r := <-c:
			r()
		case <-time.After(2 * time.Second):
			t.Fatal("lifting the limit did not admit a queued waiter")
		}
	}
	hold()
	if h, n := g.snapshot(); h != 0 || n != 0 {
		t.Errorf("end state: held=%d waiting=%d", h, n)
	}
}

// Callers that cancel at every moment of an acquire never leave a slot held or a
// waiter queued, and the holders never exceed the limit.
func TestGate_CancelRaceNeverLeaks(t *testing.T) {
	const limit, workers, rounds = 3, 24, 60
	g := &gate{}
	var holders, peak atomic.Int64
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for r := range rounds {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration((w+r)%5)*200*time.Microsecond)
				rel, err := g.acquire(ctx, limit)
				if err == nil {
					n := holders.Add(1)
					for {
						old := peak.Load()
						if n <= old || peak.CompareAndSwap(old, n) {
							break
						}
					}
					time.Sleep(100 * time.Microsecond)
					holders.Add(-1)
					rel()
				}
				cancel()
			}
		})
	}
	wg.Wait()
	if got := peak.Load(); got > limit {
		t.Errorf("peak holders = %d, want at most %d", got, limit)
	}
	if h, n := g.snapshot(); h != 0 || n != 0 {
		t.Errorf("leaked: held=%d waiting=%d", h, n)
	}
}
