// file: core/auth/password/password_concurrency_test.go
// version: 2.0.0
// guid: 6c2f94d1-7e03-4a58-b9d6-1a4e8f307c25
// last-edited: 2026-11-10

package password

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// overlapCounter is a Compute that records the peak number of derivations in
// flight; each one sleeps so concurrent calls overlap.
type overlapCounter struct{ running, peak, calls atomic.Int32 }

func (c *overlapCounter) compute(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
	n := c.running.Add(1)
	defer c.running.Add(-1)
	c.calls.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(15 * time.Millisecond)
	return make([]byte, keyLen)
}

// TestPasswordHash_BoundedConcurrency (05-authn §10, §4.5): many hashes and
// verifies at once never overlap more than Permits derivations inside the
// derivation; each real one allocates 64 MiB, so an uncapped login burst is a
// memory spike.
func TestPasswordHash_BoundedConcurrency(t *testing.T) {
	_, stored, err := New(Options{Time: 1, MemoryKiB: 8}).Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	const limit = 3
	var c overlapCounter
	h := New(Options{Permits: limit, Time: 1, MemoryKiB: 8, Compute: c.compute})
	if h.Permits() != limit {
		t.Fatalf("Permits = %d, want %d", h.Permits(), limit)
	}
	const workers = 8 * limit
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			if i%2 == 0 {
				if _, _, err := h.Hash("pw"); err != nil {
					t.Errorf("Hash: %v", err)
				}
				return
			}
			h.Verify(AlgoArgon2id, stored, "pw")
		})
	}
	wg.Wait()
	if got := c.calls.Load(); got != workers {
		t.Fatalf("derivations = %d, want %d (a path bypassed the instrumented derivation)", got, workers)
	}
	if got := c.peak.Load(); got > limit {
		t.Fatalf("peak concurrent argon2id derivations = %d, want <= %d", got, limit)
	}
	if got := c.peak.Load(); got < 2 {
		t.Fatalf("peak concurrent derivations = %d; the test never overlapped work, so it proves nothing", got)
	}
}

// TestPasswordHash_DefaultPermitsFollowGOMAXPROCS: the zero Options sizes the
// semaphore at GOMAXPROCS, at least one.
func TestPasswordHash_DefaultPermitsFollowGOMAXPROCS(t *testing.T) {
	if got := New(Options{}).Permits(); got < 1 {
		t.Fatalf("Permits = %d, want >= 1", got)
	}
}

// TestPasswordHash_BoundedWaitIsBusy (§6: 503 busy): with every permit held, a
// bounded hash, verify or login verify fails ErrBusy after its wait instead of
// queueing, and a cancelled context ends an unbounded wait.
func TestPasswordHash_BoundedWaitIsBusy(t *testing.T) {
	h := New(Options{Permits: 1, Time: 1, MemoryKiB: 8})
	_, stored, err := h.Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	h.sem <- struct{}{} // hold the only permit
	ctx := context.Background()
	if _, _, err := h.HashWithin(ctx, time.Millisecond, "pw"); !errors.Is(err, ErrBusy) {
		t.Fatalf("HashWithin = %v, want ErrBusy", err)
	}
	if ok, _, err := h.VerifyWithin(ctx, time.Millisecond, AlgoArgon2id, stored, "pw"); ok || !errors.Is(err, ErrBusy) {
		t.Fatalf("VerifyWithin = %v, %v; want false, ErrBusy", ok, err)
	}
	if ok, _, err := h.VerifyLoginWithin(ctx, 0, AlgoOAuth, "", "pw"); ok || !errors.Is(err, ErrBusy) {
		t.Fatalf("VerifyLoginWithin(no credential) = %v, %v; want false, ErrBusy (the dummy work waits too)", ok, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := h.HashWithin(cctx, -1, "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("HashWithin(cancelled, unbounded) = %v, want context.Canceled", err)
	}
	if _, _, err := h.HashWithin(cctx, time.Hour, "pw"); !errors.Is(err, context.Canceled) {
		t.Fatalf("HashWithin(cancelled, bounded) = %v, want context.Canceled", err)
	}
	<-h.sem
	if _, _, err := h.HashWithin(ctx, 0, "pw"); err != nil {
		t.Fatalf("HashWithin with a free permit: %v", err)
	}
}

// TestPasswordHash_PanickingDerivationFreesItsPermit: a derivation that
// panics (an allocation failure inside argon2, say) releases its permit on
// the way out, so the semaphore does not shrink by one for good. With one
// permit, a leak would make the next bounded call ErrBusy.
func TestPasswordHash_PanickingDerivationFreesItsPermit(t *testing.T) {
	var boom atomic.Bool
	h := New(Options{Permits: 1, Time: 1, MemoryKiB: 8, Compute: func(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		if boom.Load() {
			panic("the Joker's gas")
		}
		return make([]byte, keyLen)
	}})
	_, stored, err := h.Hash("riddle-me-this")
	if err != nil {
		t.Fatal(err)
	}
	panics := func(what string, call func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s did not panic", what)
			}
		}()
		call()
	}
	for name, call := range map[string]func(){
		"HashWithin":   func() { _, _, _ = h.HashWithin(context.Background(), 0, "riddle-me-this") },
		"VerifyWithin": func() { _, _, _ = h.VerifyWithin(context.Background(), 0, AlgoArgon2id, stored, "riddle-me-this") },
	} {
		boom.Store(true)
		panics(name, call)
		boom.Store(false)
		if _, _, err := h.HashWithin(context.Background(), 0, "riddle-me-this"); err != nil {
			t.Fatalf("after %s panicked, a bounded hash = %v; the permit leaked", name, err)
		}
	}
}
