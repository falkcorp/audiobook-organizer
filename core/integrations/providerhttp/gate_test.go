// file: core/integrations/providerhttp/gate_test.go
// version: 1.1.0
// guid: ed269bb8-8872-4b1a-8da7-6673ea480c40
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// TestBucketSharedAcrossClientsOfScope: two Clients of one Scope draw from ONE
// bucket (HEAD defect 1).
func TestBucketSharedAcrossClientsOfScope(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	srv, _ := server(t, ok)
	p := providerhttp.Policy{Limits: limits(1, 1), Trust: lanTrust, Scope: "gotham"}
	a, b := r.Client(ref("alfred"), p), r.Client(ref("barbara"), p)
	for _, c := range []*providerhttp.Client{a, b, a, b} {
		if _, err := get(t, c, srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	// The first request takes the one token; the other three each wait for one.
	if got := clk.Sleeps(); len(got) != 3 || got[0] != time.Second {
		t.Fatalf("waits = %v, want three of 1s from one shared bucket", got)
	}
	// Another scope has its own bucket.
	c := r.Client(ref("selina"), providerhttp.Policy{Limits: limits(1, 1), Trust: lanTrust, Scope: "metropolis"})
	if _, err := get(t, c, srv.URL); err != nil || len(clk.Sleeps()) != 3 {
		t.Fatalf("a client of another scope must not wait: %v, waits %v", err, clk.Sleeps())
	}
}

// TestSetLimitsMutatesInPlace: a limit change resizes the one bucket; no second
// bucket appears (HEAD defect 1).
func TestSetLimitsMutatesInPlace(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	srv, _ := server(t, ok)
	p := providerhttp.Policy{Limits: limits(1, 1), Trust: lanTrust, Scope: "gotham"}
	a, b := r.Client(ref("alfred"), p), r.Client(ref("barbara"), p)
	if _, err := get(t, a, srv.URL); err != nil { // spends the one token
		t.Fatal(err)
	}
	b.SetLimits(limits(1000, 1000))
	clk.Advance(time.Second) // 1000 tokens earned at the new rate, if the bucket was resized
	for range 20 {
		if _, err := get(t, a, srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	if got := clk.Sleeps(); len(got) != 0 {
		t.Fatalf("waits = %v after SetLimits through the other client, want none", got)
	}
	if sa, sb := a.State().BucketTokens, b.State().BucketTokens; sa > 1000 || sb > 1000 || sa < 970 {
		t.Fatalf("tokens a=%v b=%v, want the one resized bucket (about 980)", sa, sb)
	}
}

// TestRebuildWithNewLimitsResizesInPlace: a Client call for a scope that
// exists (an instance rebuilt after a settings edit) applies its limits to the
// existing bucket.
func TestRebuildWithNewLimitsResizesInPlace(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	srv, _ := server(t, ok)
	old := r.Client(batcave, providerhttp.Policy{Limits: limits(1, 1), Trust: lanTrust})
	_ = r.Client(batcave, providerhttp.Policy{Limits: limits(500, 500), Trust: lanTrust})
	clk.Advance(time.Second)
	for range 10 {
		if _, err := get(t, old, srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	if len(clk.Sleeps()) != 0 {
		t.Fatalf("the old client still waits: %v", clk.Sleeps())
	}
}

func rateLimited(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusTooManyRequests)
}

// TestHoldPersistsAcrossRegistryRestart: a hold earned by a 429 is in the store
// and a new Registry over it refuses without sending.
func TestHoldPersistsAcrossRegistryRestart(t *testing.T) {
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	r1 := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	srv, hits := server(t, rateLimited)
	p := providerhttp.Policy{Limits: limits(100, 100), Trust: lanTrust}
	if resp, err := get(t, r1.Client(batcave, p), srv.URL); err != nil || resp.StatusCode != 429 {
		t.Fatalf("first request = %v, %v", resp, err)
	}
	var held []integrations.Hold
	for h := range r1.Holds() {
		held = append(held, h)
	}
	if len(held) != 1 || held[0].Class != integrations.ClassRateLimited || held[0].Until.Sub(clk.Now()) != 15*time.Minute {
		t.Fatalf("holds = %+v, want one 15m rate-limit hold", held)
	}
	r2 := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	n := hits.Load()
	_, err := get(t, r2.Client(batcave, p), srv.URL)
	if reason, ok := providerhttp.ReasonOf(err); !ok || reason != providerhttp.ReasonHold {
		t.Fatalf("after restart err = %v, want a hold refusal", err)
	}
	if integrations.ClassOf(context.Background(), err) != integrations.ClassRefusedLocal || hits.Load() != n {
		t.Fatalf("the refused request must send nothing (class %v, hits %d -> %d)", integrations.ClassOf(context.Background(), err), n, hits.Load())
	}
	if h, ok := r2.HoldFor(batcave); !ok || h.Scope != batcave.String() {
		t.Fatalf("HoldFor = %+v, %v", h, ok)
	}
}

// TestHoldClampedToMaxOnLoad: a persisted hold beyond the maximum is clamped;
// an expired one is dropped.
func TestHoldClampedToMaxOnLoad(t *testing.T) {
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	ctx := context.Background()
	far := integrations.Hold{Scope: "far", Class: integrations.ClassAuth, Until: clk.Now().Add(14 * 365 * 24 * time.Hour), SetAt: clk.Now()}
	gone := integrations.Hold{Scope: "gone", Class: integrations.ClassAuth, Until: clk.Now().Add(-time.Minute), SetAt: clk.Now().Add(-time.Hour)}
	for _, h := range []integrations.Hold{far, gone} {
		if err := gs.PutHold(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	r := newRegistry(t, clk, func(o *providerhttp.Options) {
		o.Store = gs
		o.Settings = func() providerhttp.Settings { return providerhttp.Settings{HoldMax: 6 * time.Hour} }
	})
	var got []integrations.Hold
	for h := range r.Holds() {
		got = append(got, h)
	}
	if len(got) != 1 || got[0].Scope != "far" || got[0].Until.Sub(clk.Now()) != 6*time.Hour {
		t.Fatalf("holds = %+v, want only far, clamped to 6h", got)
	}
	left, err := gs.Holds(ctx)
	if err != nil || len(left) != 1 || left[0].Until.Sub(clk.Now()) != 6*time.Hour {
		t.Fatalf("store holds = %+v, %v; want the clamp persisted and the expired record gone", left, err)
	}
}

// TestHold_EarlySuccessKeepsHold: a success whose attempt started BEFORE the
// hold was set does not release it; one that started after does (the
// RecordSuccess rule).
func TestHold_EarlySuccessKeepsHold(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	srv, _ := server(t, ok)
	g := r.GateFor(batcave, providerhttp.Policy{Limits: limits(100, 100)})
	early, err := g.Acquire(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	late, err := g.Acquire(context.Background(), "") // started before the hold, too
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	late.Done(&integrations.Error{Class: integrations.ClassAuth, Status: 401}) // sets the 6h hold
	if _, ok := r.HoldFor(batcave); !ok {
		t.Fatal("the auth failure must hold the scope")
	}
	early.Done(nil) // started before the hold existed
	if _, ok := r.HoldFor(batcave); !ok {
		t.Fatal("an early success released a hold set after its attempt started")
	}
	// A bypassing lookup that starts after the hold and succeeds releases it.
	clk.Advance(time.Second)
	t3, err := g.Acquire(providerhttp.WithBypassHold(context.Background()), "")
	if err != nil {
		t.Fatal(err)
	}
	t3.Done(nil)
	if _, ok := r.HoldFor(batcave); ok {
		t.Fatal("a success that started after the hold must release it")
	}
	_ = srv
}

// TestBreaker_LeaseExpiryReopens: a half-open probe that never reports back
// reopens the breaker when its lease expires (HEAD defect 2).
func TestBreaker_LeaseExpiryReopens(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	lim := limits(100, 100)
	lim.Breaker = integrations.BreakerPolicy{Threshold: 2, Cooldown: 10 * time.Second, ProbeTimeout: 5 * time.Second}
	lim.Holds = integrations.HoldPolicy{Durations: map[integrations.Class]time.Duration{integrations.ClassTransient: time.Millisecond}}
	g := r.GateFor(batcave, providerhttp.Policy{Limits: lim})
	ctx := context.Background()
	fail := func() {
		tk, err := g.Acquire(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		tk.Done(&integrations.Error{Class: integrations.ClassTransient, Status: 503})
		clk.Advance(time.Second) // past the 1ms hold
	}
	fail()
	fail()
	if st := g.State().Breaker; st != integrations.BreakerOpen {
		t.Fatalf("breaker = %v after 2 failures, want open", st)
	}
	if _, err := g.Acquire(ctx, ""); !refusedFor(err, providerhttp.ReasonBreaker) {
		t.Fatalf("during cooldown err = %v, want a breaker refusal", err)
	}
	clk.Advance(10 * time.Second)
	probe, err := g.Acquire(ctx, "") // takes the single lease
	if err != nil {
		t.Fatalf("the probe was refused: %v", err)
	}
	_ = probe
	if _, err := g.Acquire(ctx, ""); !refusedFor(err, providerhttp.ReasonBreaker) {
		t.Fatalf("a second request during the probe must be refused, got %v", err)
	}
	clk.Advance(6 * time.Second) // the lease (5s) expires with no outcome
	if _, err := g.Acquire(ctx, ""); !refusedFor(err, providerhttp.ReasonBreaker) {
		t.Fatalf("after the lease expired the breaker must be open again, got %v", err)
	}
	if st := g.State().Breaker; st != integrations.BreakerOpen {
		t.Fatalf("breaker = %v, want open", st)
	}
	probe.Done(nil) // the stale probe's late answer must not close it
	if st := g.State().Breaker; st != integrations.BreakerOpen {
		t.Fatalf("a stale probe closed the breaker: %v", st)
	}
	clk.Advance(10 * time.Second)
	p2, err := g.Acquire(ctx, "")
	if err != nil {
		t.Fatalf("a new probe must be possible after the cooldown: %v", err)
	}
	p2.Done(nil)
	if st := g.State().Breaker; st != integrations.BreakerClosed {
		t.Fatalf("a successful probe must close the breaker, got %v", st)
	}
}

func refusedFor(err error, why providerhttp.Reason) bool {
	r, ok := providerhttp.ReasonOf(err)
	return ok && r == why && integrations.ClassOf(context.Background(), err) == integrations.ClassRefusedLocal
}

// TestBreaker_StaleProbeCannotTouchNewLease: probe A's lease expires, the
// breaker reopens and probe B takes a new lease; A's late success must not
// close the breaker under B (the epoch names the lease).
func TestBreaker_StaleProbeCannotTouchNewLease(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	lim := limits(100, 100)
	lim.Breaker = integrations.BreakerPolicy{Threshold: 1, Cooldown: 10 * time.Second, ProbeTimeout: 5 * time.Second}
	lim.Holds = integrations.HoldPolicy{Durations: map[integrations.Class]time.Duration{integrations.ClassTransient: time.Millisecond}}
	g := r.GateFor(batcave, providerhttp.Policy{Limits: lim})
	ctx := context.Background()
	tk, err := g.Acquire(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	tk.Done(&integrations.Error{Class: integrations.ClassTransient, Status: 503})
	clk.Advance(11 * time.Second)
	a, err := g.Acquire(ctx, "") // probe A
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(6 * time.Second)  // A's lease expires: open again
	clk.Advance(10 * time.Second) // and the cooldown after that passes
	b, err := g.Acquire(ctx, "")  // probe B holds the new lease
	if err != nil {
		t.Fatalf("probe B was refused: %v", err)
	}
	a.Done(nil) // A answers late
	if st := g.State().Breaker; st != integrations.BreakerHalfOpen {
		t.Fatalf("A's stale success moved the breaker to %v while B holds the lease", st)
	}
	if _, err := g.Acquire(ctx, ""); !refusedFor(err, providerhttp.ReasonBreaker) {
		t.Fatalf("B's lease was given away: %v", err)
	}
	b.Done(nil)
	if st := g.State().Breaker; st != integrations.BreakerClosed {
		t.Fatalf("B's success left the breaker %v", st)
	}
}

// TestBreaker_IgnoresCanceled: our own cancellation and refusals count for
// nothing (HEAD defect 3), and a probe that is canceled releases its lease.
func TestBreaker_IgnoresCanceled(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	lim := limits(100, 100)
	lim.Breaker = integrations.BreakerPolicy{Threshold: 2, Cooldown: 10 * time.Second}
	g := r.GateFor(batcave, providerhttp.Policy{Limits: lim})
	for range 10 {
		ctx, cancel := context.WithCancel(context.Background())
		tk, err := g.Acquire(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		tk.Done(context.Canceled)
	}
	if st := g.State().Breaker; st != integrations.BreakerClosed {
		t.Fatalf("breaker = %v after ten cancellations, want closed", st)
	}
	// Open it, then cancel the probe: the lease is released, not stranded.
	for range 2 {
		tk, _ := g.Acquire(context.Background(), "")
		tk.Done(&integrations.Error{Class: integrations.ClassTransient, Status: 500})
		clk.Advance(6 * time.Hour) // past every hold
	}
	clk.Advance(10 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	tk, err := g.Acquire(ctx, "")
	if err != nil {
		t.Fatalf("probe refused: %v", err)
	}
	cancel()
	tk.Done(context.Canceled)
	again, err := g.Acquire(context.Background(), "")
	if err != nil {
		t.Fatalf("a canceled probe stranded the half-open breaker: %v", err)
	}
	again.Done(nil)
}

func daily(total, background int, counters ...string) *integrations.DailyLimits {
	return &integrations.DailyLimits{Total: total, Background: background, Zone: time.UTC, Counters: counters}
}

// TestQuotaTiers: Background stops at Background, Interactive continues to
// Total.
func TestQuotaTiers(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	lim := limits(1000, 1000)
	lim.Daily = daily(5, 3)
	g := r.GateFor(batcave, providerhttp.Policy{Limits: lim})
	bg, ia := context.Background(), providerhttp.WithInteractive(context.Background())
	for i := range 3 {
		if tk, err := g.Acquire(bg, ""); err != nil {
			t.Fatalf("background %d: %v", i, err)
		} else {
			tk.Done(nil)
		}
	}
	if _, err := g.Acquire(bg, ""); !refusedFor(err, providerhttp.ReasonQuota) || !errors.Is(err, providerhttp.ErrQuotaSpent) {
		t.Fatalf("background past its tier = %v, want a spent-quota refusal", err)
	}
	for i := range 2 {
		if _, err := g.Acquire(ia, ""); err != nil {
			t.Fatalf("interactive %d: %v", i, err)
		}
	}
	if _, err := g.Acquire(ia, ""); !errors.Is(err, providerhttp.ErrQuotaSpent) {
		t.Fatalf("interactive past Total = %v, want spent", err)
	}
	if st := g.State(); st.QuotaUsed["requests"] != 5 || st.QuotaLimit["requests"] != 5 {
		t.Fatalf("state quota = %v / %v", st.QuotaUsed, st.QuotaLimit)
	}
	// A quota spent is a refusal, not a remote failure: nothing is held.
	if _, held := r.HoldFor(batcave); held {
		t.Fatal("a local quota refusal must not hold the scope")
	}
}

// TestQuotaDayRollsInZone: the quota day rolls at midnight of the limit's zone,
// across a daylight-saving boundary.
func TestQuotaDayRollsInZone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	clk := newClock()
	// 2039-03-13 is the US spring-forward Sunday: 08:30 UTC is 00:30 PST that day.
	clk.t = time.Date(2039, 3, 13, 7, 30, 0, 0, time.UTC) // 23:30 PST on the 12th
	gs := providerhttp.NativeGateStore(store(t))
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	lim := limits(1000, 1000)
	lim.Daily = &integrations.DailyLimits{Total: 2, Background: 2, Zone: la}
	g := r.GateFor(batcave, providerhttp.Policy{Limits: lim})
	ctx := context.Background()
	for range 2 {
		if _, err := g.Acquire(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Acquire(ctx, ""); !errors.Is(err, providerhttp.ErrQuotaSpent) {
		t.Fatalf("third on the 12th = %v, want spent", err)
	}
	clk.Advance(time.Hour) // 08:30 UTC = 00:30 PST on the 13th: a new quota day
	if _, err := g.Acquire(ctx, ""); err != nil {
		t.Fatalf("first of the 13th = %v, want admitted", err)
	}
	day, used, err := gs.Quota(ctx, batcave.String(), "requests")
	if err != nil || day != "2039-03-13" || used != 1 {
		t.Fatalf("stored = %q %d, %v; want 2039-03-13 1", day, used, err)
	}
	// The same UTC clock reads the day differently in another zone.
	g2 := r.GateFor(ref("alfred"), providerhttp.Policy{Limits: func() integrations.Limits {
		l := limits(1000, 1000)
		l.Daily = &integrations.DailyLimits{Total: 1, Background: 1, Zone: time.UTC}
		return l
	}()})
	if _, err := g2.Acquire(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// A restart reads the day back: the count survives.
	r2 := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	g3 := r2.GateFor(batcave, providerhttp.Policy{Limits: lim})
	if _, err := g3.Acquire(ctx, ""); err != nil {
		t.Fatalf("second of the 13th after restart: %v", err)
	}
	if _, err := g3.Acquire(ctx, ""); !errors.Is(err, providerhttp.ErrQuotaSpent) {
		t.Fatalf("third of the 13th after restart = %v, want spent: the count was lost", err)
	}
}

// failingStore wraps a GateStore with switchable failures.
type failingStore struct {
	providerhttp.GateStore
	failQuota, failPut, failHolds atomic.Bool
	holdReads                     atomic.Int64 // Holds calls, failed or not
}

var errStore = errors.New("the keyspace is gone")

func (f *failingStore) Quota(ctx context.Context, s, c string) (string, int, error) {
	if f.failQuota.Load() {
		return "", 0, errStore
	}
	return f.GateStore.Quota(ctx, s, c)
}

func (f *failingStore) PutQuota(ctx context.Context, s, c, d string, n int) error {
	if f.failPut.Load() {
		return errStore
	}
	return f.GateStore.PutQuota(ctx, s, c, d, n)
}

func (f *failingStore) Holds(ctx context.Context) ([]integrations.Hold, error) {
	f.holdReads.Add(1)
	if f.failHolds.Load() {
		return nil, errStore
	}
	return f.GateStore.Holds(ctx)
}

// TestQuotaStoreReadFailureFailsClosed: a counter that cannot be read refuses,
// before anything is sent; it is no hold.
func TestQuotaStoreReadFailureFailsClosed(t *testing.T) {
	clk := newClock()
	fs := &failingStore{GateStore: providerhttp.NativeGateStore(store(t))}
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = fs })
	srv, hits := server(t, ok)
	lim := limits(1000, 1000)
	lim.Daily = daily(100, 100)
	c := r.Client(batcave, providerhttp.Policy{Limits: lim, Trust: lanTrust})
	fs.failQuota.Store(true)
	_, err := get(t, c, srv.URL)
	if !errors.Is(err, providerhttp.ErrBudgetUnavailable) || !refusedFor(err, providerhttp.ReasonQuota) {
		t.Fatalf("read failure = %v, want an unavailable-budget refusal", err)
	}
	if hits.Load() != 0 {
		t.Fatal("a request was sent although the quota could not be read")
	}
	fs.failQuota.Store(false) // the failure is not cached: the next read recovers
	if _, err := get(t, c, srv.URL); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
	fs.failPut.Store(true) // a count that cannot be persisted is not spent
	if _, err := get(t, c, srv.URL); !errors.Is(err, providerhttp.ErrBudgetUnavailable) {
		t.Fatalf("write failure = %v, want refusal", err)
	}
	fs.failPut.Store(false)
	if _, err := get(t, c, srv.URL); err != nil {
		t.Fatal(err)
	}
	if st := c.State(); st.QuotaUsed["requests"] != 2 {
		t.Fatalf("used = %v, want 2: a refused reservation must not count", st.QuotaUsed)
	}
}

// TestHoldStoreReadFailureFailsOpen: holds that cannot be loaded are in memory
// only (M2.5) and the Registry still works.
func TestHoldStoreReadFailureFailsOpen(t *testing.T) {
	clk := newClock()
	fs := &failingStore{GateStore: providerhttp.NativeGateStore(store(t))}
	fs.failHolds.Store(true)
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = fs })
	srv, _ := server(t, rateLimited)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Trust: lanTrust})
	if _, err := get(t, c, srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, held := r.HoldFor(batcave); !held {
		t.Fatal("the hold must stay in memory")
	}
}

// TestQuota_ConcurrentReservationsNoLostCount: N goroutines reserving on one
// counter persist exactly N (-race).
func TestQuota_ConcurrentReservationsNoLostCount(t *testing.T) {
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	lim := limits(1e6, 1e6)
	lim.Daily = daily(1000, 1000)
	g := r.GateFor(batcave, providerhttp.Policy{Limits: lim})
	const n = 64
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, err := g.Acquire(context.Background(), ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	day, used, err := gs.Quota(context.Background(), batcave.String(), "requests")
	if err != nil || used != n || day == "" {
		t.Fatalf("persisted = %q %d, %v; want exactly %d", day, used, err, n)
	}
}

// TestQuotaUnknownCounterRefused: a counter the limits do not declare is
// refused, not counted nowhere.
func TestQuotaUnknownCounterRefused(t *testing.T) {
	r := newRegistry(t, newClock())
	lim := limits(100, 100)
	lim.Daily = daily(10, 10, "query", "grab")
	g := r.GateFor(batcave, providerhttp.Policy{Limits: lim})
	if _, err := g.Acquire(context.Background(), "download"); !errors.Is(err, providerhttp.ErrUnknownCounter) {
		t.Fatalf("err = %v, want unknown counter", err)
	}
	for _, c := range []string{"query", "grab", ""} {
		if _, err := g.Acquire(context.Background(), c); err != nil {
			t.Fatalf("counter %q: %v", c, err)
		}
	}
	st := g.State()
	if st.QuotaUsed["query"] != 2 || st.QuotaUsed["grab"] != 1 {
		t.Fatalf("used = %v; the unnamed counter is the first declared", st.QuotaUsed)
	}
}

// TestGate_DisabledRefusesBeforeDial: core.integrations.disabled refuses
// before any dial and writes nothing: no hold, breaker or quota record.
func TestGate_DisabledRefusesBeforeDial(t *testing.T) {
	clk := newClock()
	ks := store(t)
	gs := providerhttp.NativeGateStore(ks)
	var disabled atomic.Bool
	r := newRegistry(t, clk, func(o *providerhttp.Options) {
		o.Store = gs
		o.Disabled = disabled.Load
	})
	srv, hits := server(t, ok)
	lim := limits(100, 100)
	lim.Daily = daily(10, 10)
	p := providerhttp.Policy{Limits: lim, Trust: lanTrust}
	c := r.Client(batcave, p)
	disabled.Store(true)
	for range 3 {
		_, err := get(t, c, srv.URL)
		if !refusedFor(err, providerhttp.ReasonDisabled) {
			t.Fatalf("disabled err = %v", err)
		}
		if _, err := c.Acquire(context.Background(), ""); !refusedFor(err, providerhttp.ReasonDisabled) {
			t.Fatalf("disabled Acquire = %v", err)
		}
	}
	// The copied *http.Client is refused too.
	cp := *c.Client()
	if resp, err := cp.Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a copied client got through while disabled")
	}
	if hits.Load() != 0 {
		t.Fatal("a request was dialled while disabled")
	}
	if day, used, _ := gs.Quota(context.Background(), batcave.String(), "requests"); day != "" || used != 0 {
		t.Fatalf("quota written while disabled: %q %d", day, used)
	}
	if hs, _ := gs.Holds(context.Background()); len(hs) != 0 {
		t.Fatalf("holds written while disabled: %+v", hs)
	}
	if st := c.State(); st.Breaker != integrations.BreakerClosed || st.Hold != nil {
		t.Fatalf("state changed while disabled: %+v", st)
	}
	disabled.Store(false)
	if _, err := get(t, c, srv.URL); err != nil {
		t.Fatalf("after re-enable: %v", err)
	}
}

// TestHoldFor_NeedsNoClient: a hold is visible and releasable by ref with no
// Client built since the boot (the F7-10 hold-route seam).
func TestHoldFor_NeedsNoClient(t *testing.T) {
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	ctx := context.Background()
	if err := gs.PutHold(ctx, integrations.Hold{Scope: batcave.String(), Class: integrations.ClassQuota,
		Until: clk.Now().Add(time.Hour), SetAt: clk.Now()}); err != nil {
		t.Fatal(err)
	}
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	h, held := r.HoldFor(batcave)
	if !held || h.Scope != batcave.String() {
		t.Fatalf("HoldFor = %+v, %v; want the persisted hold with no client built", h, held)
	}
	if _, held := r.HoldFor(ref("alfred")); held {
		t.Fatal("an unrelated ref reports a hold")
	}
	if err := r.ReleaseHoldIf(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, held := r.HoldFor(batcave); held {
		t.Fatal("the hold is still held")
	}
	if hs, _ := gs.Holds(ctx); len(hs) != 0 {
		t.Fatalf("store still has %+v", hs)
	}
	if err := r.ReleaseHoldIf(ctx, h); err != nil { // releasing nothing is not an error
		t.Fatal(err)
	}
}

// TestReleaseHoldIf_ChangedBetweenReadAndRelease: F7-10 reads a hold (HoldFor)
// and releases it a moment later. A hold that changed in between is not
// released unseen: a longer hold set after the read survives, a hold that
// ended meets the goal, a store that cannot delete keeps the hold.
func TestReleaseHoldIf_ChangedBetweenReadAndRelease(t *testing.T) {
	clk := newClock()
	gs := providerhttp.NativeGateStore(store(t))
	r := newRegistry(t, clk, func(o *providerhttp.Options) { o.Store = gs })
	g := r.GateFor(batcave, providerhttp.Policy{Limits: limits(100, 100)})
	ctx := context.Background()
	place := func(class integrations.Class, retryAfter time.Duration) {
		t.Helper()
		tk, err := g.Acquire(providerhttp.WithBypassHold(ctx), "")
		if err != nil {
			t.Fatal(err)
		}
		tk.Done(&integrations.Error{Class: class, Status: 429, RetryAfter: retryAfter})
	}
	place(integrations.ClassRateLimited, 10*time.Minute)
	seen, ok := r.HoldFor(batcave)
	if !ok || seen.Scope != batcave.String() {
		t.Fatalf("HoldFor = %+v, %v", seen, ok)
	}

	// Between the read and the release a longer hold replaces it.
	clk.Advance(time.Minute)
	place(integrations.ClassQuota, 2*time.Hour)
	now, _ := r.HoldFor(batcave)
	if now.Until.Equal(seen.Until) {
		t.Fatalf("the test did not replace the hold: %+v", now)
	}
	if err := r.ReleaseHoldIf(ctx, seen); !errors.Is(err, providerhttp.ErrHoldChanged) {
		t.Fatalf("ReleaseHoldIf(stale) = %v, want ErrHoldChanged", err)
	}
	if got, held := r.HoldFor(batcave); !held || !got.Until.Equal(now.Until) {
		t.Fatalf("the newer hold was released unseen: %+v, %v", got, held)
	}
	if hs, _ := gs.Holds(ctx); len(hs) != 1 || !hs[0].Until.Equal(now.Until) {
		t.Fatalf("the store lost the newer hold: %+v", hs)
	}

	// A store that cannot delete leaves the hold in force and says so.
	// (PutHold is the delete path, so the store is one whose PutHold fails.)
	r2 := newRegistry(t, clk, func(o *providerhttp.Options) {
		o.Store = &holdPutFailing{GateStore: providerhttp.NativeGateStore(store(t))}
	})
	g2 := r2.GateFor(batcave, providerhttp.Policy{Limits: limits(100, 100)})
	tk, _ := g2.Acquire(ctx, "")
	tk.Done(&integrations.Error{Class: integrations.ClassQuota, Status: 429})
	h2, _ := r2.HoldFor(batcave)
	if err := r2.ReleaseHoldIf(ctx, h2); err == nil {
		t.Fatal("a release the store could not delete reported success")
	}
	if _, held := r2.HoldFor(batcave); !held {
		t.Fatal("the hold was dropped though the store kept it")
	}

	// The current hold, seen again, is released; releasing it again is a no-op.
	if err := r.ReleaseHoldIf(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, held := r.HoldFor(batcave); held {
		t.Fatal("the hold survived its own release")
	}
	if err := r.ReleaseHoldIf(ctx, now); err != nil {
		t.Fatalf("releasing a gone hold: %v", err)
	}
	// A hold that ended meets the goal too.
	place(integrations.ClassRateLimited, time.Minute)
	ended, _ := r.HoldFor(batcave)
	clk.Advance(time.Hour)
	if err := r.ReleaseHoldIf(ctx, ended); err != nil {
		t.Fatalf("an ended hold: %v", err)
	}
}

// holdPutFailing is a GateStore whose PutHold (the write and the delete of a
// hold) always fails.
type holdPutFailing struct{ providerhttp.GateStore }

func (holdPutFailing) PutHold(context.Context, integrations.Hold) error { return errStore }

// TestReleaseAuthHold: only a ClassAuth hold of the ref's scope is released.
func TestReleaseAuthHold(t *testing.T) {
	clk := newClock()
	r := newRegistry(t, clk)
	g := r.GateFor(batcave, providerhttp.Policy{Limits: limits(100, 100)})
	tk, _ := g.Acquire(context.Background(), "")
	tk.Done(&integrations.Error{Class: integrations.ClassQuota, Status: 429})
	if err := r.ReleaseAuthHold(context.Background(), batcave); err != nil {
		t.Fatal(err)
	}
	if _, held := r.HoldFor(batcave); !held {
		t.Fatal("a quota hold was released as an auth hold")
	}
	if h, ok := r.HoldFor(batcave); ok {
		if err := r.ReleaseHoldIf(context.Background(), h); err != nil {
			t.Fatal(err)
		}
	}
	tk, _ = g.Acquire(context.Background(), "")
	tk.Done(&integrations.Error{Class: integrations.ClassAuth, Status: 401})
	if _, held := r.HoldFor(batcave); !held {
		t.Fatal("no auth hold")
	}
	if err := r.ReleaseAuthHold(context.Background(), batcave); err != nil {
		t.Fatal(err)
	}
	if _, held := r.HoldFor(batcave); held {
		t.Fatal("the auth hold survived ReleaseAuthHold")
	}
}

// the shape F7-10's hold route declares (internal/integrationhttp is internal
// to core, so its assertion is F7-09's; see seams.d/F7-02.md). It is the only
// hold-release pair the Registry has: a release by scope alone is not atomic
// with the read that found the hold, so it does not exist.
var _ interface {
	HoldFor(integrations.Ref) (integrations.Hold, bool)
	ReleaseHoldIf(context.Context, integrations.Hold) error
} = (*providerhttp.Registry)(nil)
