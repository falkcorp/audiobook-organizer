// file: core/integrations/providerhttp/holds_review_test.go
// version: 1.0.0
// guid: 0d6f3b0a-6a5c-4a43-8c3e-7d9b5b1f2e44
// last-edited: 2026-11-11

package providerhttp_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// holdOn places a ClassAuth hold on ref's scope under p.
func holdOn(t testing.TB, r *providerhttp.Registry, ref integrations.Ref, p providerhttp.Policy, class integrations.Class) {
	t.Helper()
	g := r.GateFor(ref, p)
	tk, err := g.Acquire(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	tk.Done(&integrations.Error{Class: class, Status: 401})
}

// TestHoldFor_FindsCustomScopeAfterRestart: the scope a hold sits on is
// resolved from what the store recorded, never from the instances built since
// boot. A hold on a custom (shared) scope is found by ref and released by
// ReleaseAuthHold in a new Registry over the same store, before any Client or
// GateFor call; an instance that moved back to the default scope leaves no
// stale mapping behind.
func TestHoldFor_FindsCustomScopeAfterRestart(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	const shared = "google-books:0123456789ab"
	alfred, barbara := ref("alfred"), ref("barbara")
	custom := providerhttp.Policy{Limits: limits(100, 100), Scope: shared}

	r1 := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	r1.GateFor(barbara, custom) // a sharer that places no hold itself
	holdOn(t, r1, alfred, custom, integrations.ClassAuth)
	holdOn(t, r1, batcave, providerhttp.Policy{Limits: limits(100, 100)}, integrations.ClassAuth) // default scope
	if sc, err := gs.Scopes(ctx); err != nil || len(sc) != 2 || sc[alfred] != shared || sc[barbara] != shared {
		t.Fatalf("persisted scopes = %v, %v; want the two custom ones and no record for the default", sc, err)
	}

	r2 := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	for _, who := range []integrations.Ref{alfred, barbara} {
		h, ok := r2.HoldFor(who)
		if !ok || h.Scope != shared || h.Class != integrations.ClassAuth {
			t.Fatalf("HoldFor(%v) after a restart = %+v, %v; want the hold on the shared scope", who, h, ok)
		}
	}
	if h, ok := r2.HoldFor(batcave); !ok || h.Scope != batcave.String() {
		t.Fatalf("the default-scope hold: %+v, %v", h, ok)
	}
	if _, ok := r2.HoldFor(ref("selina")); ok {
		t.Fatal("an instance with no hold has one")
	}
	if err := r2.ReleaseAuthHold(ctx, barbara); err != nil { // via the SIBLING that never placed it
		t.Fatal(err)
	}
	if _, ok := r2.HoldFor(alfred); ok {
		t.Fatal("ReleaseAuthHold by ref must release the shared scope's hold")
	}
	if hs, _ := gs.Holds(ctx); len(hs) != 1 {
		t.Fatalf("store holds = %d, want only the default-scope one", len(hs))
	}

	// alfred is rebuilt on the default scope: its record goes, and a hold the
	// old scope still has is no longer "alfred's".
	r2.GateFor(alfred, providerhttp.Policy{Limits: limits(100, 100)})
	if sc, _ := gs.Scopes(ctx); len(sc) != 1 || sc[barbara] != shared {
		t.Fatalf("scopes after alfred moved to the default = %v", sc)
	}
	r3 := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	if h, ok := r3.HoldFor(alfred); ok {
		t.Fatalf("a stale mapping was followed: %+v", h)
	}
}

// blockingStore delays the removal of a hold until released.
type blockingStore struct {
	providerhttp.GateStore
	block   atomic.Bool
	scope   string // the scope whose release stalls
	entered chan struct{}
	release chan struct{}
}

func (b *blockingStore) PutHold(ctx context.Context, h integrations.Hold) error {
	if b.block.Load() && h.Until.IsZero() && h.Scope == b.scope {
		b.entered <- struct{}{}
		<-b.release
	}
	return b.GateStore.PutHold(ctx, h)
}

// within fails the test if f does not return in time.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return in %v: a stuck store write blocked it", what, d)
	}
}

// TestHoldStoreWrite_BlocksOnlyItsOwnScope (N4): while one scope's hold is
// being removed from a store that has stalled, admission, listing, reading and
// the other scopes' writers carry on; the stalled release finishes when the
// store does.
func TestHoldStoreWrite_BlocksOnlyItsOwnScope(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	bs := &blockingStore{GateStore: providerhttp.NativeGateStore(store(t)), entered: make(chan struct{}, 1), release: make(chan struct{})}
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = bs })
	alfred, barbara, selina := ref("alfred"), ref("barbara"), ref("selina")
	pol := providerhttp.Policy{Limits: limits(100, 100)}
	holdOn(t, r, alfred, pol, integrations.ClassAuth)
	holdOn(t, r, barbara, pol, integrations.ClassAuth)
	held, _ := r.HoldFor(alfred)

	bs.scope = held.Scope
	bs.block.Store(true)
	errc := make(chan error, 1)
	go func() { errc <- r.ReleaseHoldIf(ctx, held) }()
	<-bs.entered // the release is inside the store, holding alfred's scope

	within(t, 3*time.Second, "HoldFor of the stalled scope", func() {
		if _, ok := r.HoldFor(alfred); !ok {
			t.Error("the hold must stay in force until the store has released it")
		}
	})
	within(t, 3*time.Second, "Holds", func() {
		n := 0
		for range r.Holds() {
			n++
		}
		if n != 2 {
			t.Errorf("Holds = %d, want 2", n)
		}
	})
	within(t, 3*time.Second, "admission of the stalled scope", func() {
		if _, err := r.GateFor(alfred, pol).Acquire(ctx, ""); !refusedFor(err, providerhttp.ReasonHold) {
			t.Errorf("Acquire = %v, want the hold refusal", err)
		}
	})
	within(t, 3*time.Second, "a writer of another scope", func() {
		holdOn(t, r, selina, pol, integrations.ClassQuota)
		if _, ok := r.HoldFor(selina); !ok {
			t.Error("the other scope's hold was not placed")
		}
	})
	within(t, 3*time.Second, "a release of another scope", func() {
		other, _ := r.HoldFor(barbara)
		if err := r.ReleaseHoldIf(ctx, other); err != nil {
			t.Error(err)
		}
	})
	close(bs.release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if _, ok := r.HoldFor(alfred); ok {
		t.Fatal("the release did not take effect")
	}
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestHolds_BootReadRetriedLazily (N5): a store that cannot be read at boot is
// logged and read again, at most once per period and never per request, until a
// read succeeds; holds placed meanwhile are kept.
func TestHolds_BootReadRetriedLazily(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	if err := gs.PutHold(ctx, integrations.Hold{Scope: batcave.String(), Class: integrations.ClassAuth,
		Until: clk.Now().Add(time.Hour), SetAt: clk.Now()}); err != nil {
		t.Fatal(err)
	}
	fs := &failingStore{GateStore: gs}
	fs.failHolds.Store(true)
	var logs syncBuf
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = fs; o.Log = logger })
	if !bytes.Contains([]byte(logs.String()), []byte("could not be read")) {
		t.Fatalf("a failed boot read must be logged: %q", logs.String())
	}
	// A hold placed during the outage is kept across the late read.
	holdOn(t, r, ref("selina"), providerhttp.Policy{Limits: limits(100, 100)}, integrations.ClassQuota)
	for range 50 {
		if _, ok := r.HoldFor(batcave); ok {
			t.Fatal("found a hold that was never read")
		}
	}
	if n := fs.holdReads.Load(); n != 1 {
		t.Fatalf("the store was read %d times within the period, want only the boot read", n)
	}
	fs.failHolds.Store(false)
	if _, ok := r.HoldFor(batcave); ok {
		t.Fatal("retried before the period elapsed")
	}
	clk.Advance(31 * time.Second)
	h, ok := r.HoldFor(batcave)
	if !ok || h.Class != integrations.ClassAuth {
		t.Fatalf("after the period: %+v, %v; want the persisted hold", h, ok)
	}
	if _, ok := r.HoldFor(ref("selina")); !ok {
		t.Fatal("the hold placed during the outage was lost by the late read")
	}
	if n := fs.holdReads.Load(); n != 2 {
		t.Fatalf("reads = %d, want 2", n)
	}
	for range 20 {
		r.HoldFor(batcave)
	}
	if n := fs.holdReads.Load(); n != 2 {
		t.Fatalf("a loaded Registry kept reading: %d", n)
	}
	if !bytes.Contains([]byte(logs.String()), []byte("after an earlier failure")) {
		t.Fatalf("the recovery must be logged: %q", logs.String())
	}
}
