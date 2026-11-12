// file: core/integrations/providerhttp/holds_review_test.go
// version: 1.2.2
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

// eventually polls f until it is true; the background retry of the boot read
// finishes within milliseconds against a local store.
func eventually(t testing.TB, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

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
	var h integrations.Hold
	eventually(t, "the retried read", func() bool { var ok bool; h, ok = r.HoldFor(batcave); return ok })
	if h.Class != integrations.ClassAuth {
		t.Fatalf("after the period: %+v; want the persisted hold", h)
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

// TestHolds_LateReadKeepsWhatThisProcessBuilt (N5): a store unreadable at boot
// and read later never overwrites what this process did meanwhile. A hold
// placed during the outage that outlasts the persisted one on the same scope
// stays; an instance built during the outage keeps the scope it was built with
// over a stale record, and the record is rewritten to match.
func TestHolds_LateReadKeepsWhatThisProcessBuilt(t *testing.T) {
	ctx := context.Background()
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	persistedUntil := clk.Now().Add(40 * time.Second)
	if err := gs.PutHold(ctx, integrations.Hold{Scope: batcave.String(), Class: integrations.ClassQuota,
		Until: persistedUntil, SetAt: clk.Now()}); err != nil {
		t.Fatal(err)
	}
	alfred := ref("alfred")
	if err := gs.PutScope(ctx, alfred, "old:scope"); err != nil {
		t.Fatal(err)
	}
	// The outage swallows hold writes too, so the persisted hold stays the old
	// one while the table moves on.
	mute := &muteHolds{GateStore: gs}
	mute.on.Store(true)
	fs := &failingStore{GateStore: mute}
	fs.failHolds.Store(true)
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = fs })

	pol := providerhttp.Policy{Limits: limits(100, 100)}
	holdOn(t, r, batcave, pol, integrations.ClassAuth) // longer than the persisted one, same scope
	holdOn(t, r, alfred, providerhttp.Policy{Limits: limits(100, 100), Scope: "new:scope"}, integrations.ClassAuth)

	fs.failHolds.Store(false)
	mute.on.Store(false)
	clk.Advance(31 * time.Second)
	eventually(t, "the retried read", func() bool {
		r.HoldFor(batcave)
		sc, err := gs.Scopes(ctx)
		return err == nil && sc[alfred] == "new:scope"
	})
	h, ok := r.HoldFor(batcave)
	if !ok || h.Class != integrations.ClassAuth || !h.Until.After(persistedUntil) {
		t.Fatalf("the late read replaced a longer hold: %+v, %v (persisted until %v)", h, ok, persistedUntil)
	}
	h, ok = r.HoldFor(alfred)
	if !ok || h.Scope != "new:scope" {
		t.Fatalf("the late read replaced the scope the instance was built with: %+v, %v", h, ok)
	}
	if sc, err := gs.Scopes(ctx); err != nil || sc[alfred] != "new:scope" {
		t.Fatalf("the stale record must be rewritten: %v, %v", sc, err)
	}
}

// muteHolds accepts hold writes and drops them while on.
type muteHolds struct {
	providerhttp.GateStore
	on atomic.Bool
}

func (m *muteHolds) PutHold(ctx context.Context, h integrations.Hold) error {
	if m.on.Load() {
		return nil
	}
	return m.GateStore.PutHold(ctx, h)
}

// countingScopes counts PutScope calls.
type countingScopes struct {
	providerhttp.GateStore
	puts atomic.Int64
}

func (c *countingScopes) PutScope(ctx context.Context, ref integrations.Ref, scope string) error {
	c.puts.Add(1)
	return c.GateStore.PutScope(ctx, ref, scope)
}

// TestScopeRecord_WrittenOnceUntilItChanges: building an instance again with
// the scope the store already records writes nothing, and a change writes once.
func TestScopeRecord_WrittenOnceUntilItChanges(t *testing.T) {
	cs := &countingScopes{GateStore: providerhttp.NativeGateStore(store(t))}
	r := newRegistry(t, newClock(), func(o *providerhttp.Options) { o.Store = cs })
	alfred := ref("alfred")
	custom := providerhttp.Policy{Limits: limits(100, 100), Scope: "shared:scope"}
	for range 5 {
		r.GateFor(alfred, custom)
	}
	if n := cs.puts.Load(); n != 1 {
		t.Fatalf("PutScope called %d times for one unchanged scope, want 1", n)
	}
	r.GateFor(alfred, providerhttp.Policy{Limits: limits(100, 100), Scope: "other:scope"})
	r.GateFor(alfred, providerhttp.Policy{Limits: limits(100, 100), Scope: "other:scope"})
	if n := cs.puts.Load(); n != 2 {
		t.Fatalf("PutScope called %d times after one change, want 2", n)
	}
	r.GateFor(alfred, providerhttp.Policy{Limits: limits(100, 100)}) // back to the default: the record goes
	if n := cs.puts.Load(); n != 3 {
		t.Fatalf("PutScope called %d times after the move to the default, want 3", n)
	}
	if sc, err := cs.Scopes(context.Background()); err != nil || len(sc) != 0 {
		t.Fatalf("the default scope needs no record: %v, %v", sc, err)
	}
}

// slowHolds fails or stalls Holds on demand.
type slowHolds struct {
	providerhttp.GateStore
	fail, block atomic.Bool
	release     chan struct{}
	started     chan struct{}
}

func (s *slowHolds) Holds(ctx context.Context) ([]integrations.Hold, error) {
	if s.fail.Load() {
		return nil, errStore
	}
	if s.block.Load() {
		select {
		case s.started <- struct{}{}:
		default:
		}
		<-s.release
	}
	return s.GateStore.Holds(ctx)
}

// TestHolds_RetryNeverBlocksARequest (MINOR 5): the retry of a boot read runs in
// the background, so a store that has stalled costs the request that found the
// retry due nothing, and Close waits for the reader.
func TestHolds_RetryNeverBlocksARequest(t *testing.T) {
	clk := newClock()
	ss := &slowHolds{GateStore: providerhttp.NativeGateStore(store(t)), release: make(chan struct{}), started: make(chan struct{}, 1)}
	if err := ss.PutHold(context.Background(), integrations.Hold{Scope: batcave.String(), Class: integrations.ClassAuth,
		Until: clk.Now().Add(time.Hour), SetAt: clk.Now()}); err != nil {
		t.Fatal(err)
	}
	ss.fail.Store(true)
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = ss })
	ss.fail.Store(false)
	ss.block.Store(true)
	clk.Advance(31 * time.Second)
	within(t, 2*time.Second, "HoldFor while the retried read is stalled", func() {
		if _, ok := r.HoldFor(batcave); ok {
			t.Error("found a hold the stalled read has not delivered")
		}
	})
	<-ss.started // the read is under way in the background
	within(t, 2*time.Second, "a second HoldFor", func() { r.HoldFor(batcave) })
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a background read was still under way")
	case <-time.After(150 * time.Millisecond):
	}
	close(ss.release)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return after the read ended")
	}
	eventually(t, "the stalled read to land", func() bool { _, ok := r.HoldFor(batcave); return ok })
}

// TestHolds_NoRetryAfterClose: a closed Registry starts no more background
// reads of the store.
func TestHolds_NoRetryAfterClose(t *testing.T) {
	clk := newClock()
	fs := &failingStore{GateStore: providerhttp.NativeGateStore(store(t))}
	fs.failHolds.Store(true)
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = fs })
	r.Close()
	fs.failHolds.Store(false)
	clk.Advance(31 * time.Second)
	for range 20 {
		r.HoldFor(batcave)
		time.Sleep(time.Millisecond)
	}
	if n := fs.holdReads.Load(); n != 1 {
		t.Fatalf("the store was read %d times; a closed Registry must read it only at boot", n)
	}
}
