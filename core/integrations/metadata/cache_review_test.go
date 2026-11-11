// file: core/integrations/metadata/cache_review_test.go
// version: 1.0.0
// guid: 3b7a1c0e-9f6d-4f0b-a1d2-5e8c4b6a7f90
// last-edited: 2026-11-11

package metadata_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// gatedProvider answers every call from a script that blocks until released and
// tells the test each time a live call starts.
func gatedProvider(name string) (p *coretest.FakeProvider[query, book], entered chan struct{}, release chan struct{}) {
	entered = make(chan struct{}, 8)
	release = make(chan struct{})
	p = coretest.NewFakeProvider[query, book](ref(name))
	p.Script(func(context.Context, query) ([]book, error) {
		entered <- struct{}{}
		<-release
		return answers(name, "x"), nil
	})
	return p, entered, release
}

// TestCache_FlightsAreKeptApart (N6, N7): callers share one live call only when
// the gate would treat them alike. A Background leader that the gate may refuse
// must not hand its refusal to an interactive, hold-bypassing or
// differently-counted follower; a read-only leader writes nothing so cannot
// serve a writing caller; and a question key that contains the old joiner
// cannot run into another flight.
func TestCache_FlightsAreKeptApart(t *testing.T) {
	type variant struct {
		name     string
		leader   func(context.Context) context.Context
		follower func(context.Context) context.Context
		fq, lq   string // the questions, when they differ
	}
	id := func(ctx context.Context) context.Context { return ctx }
	for _, v := range []variant{
		{name: "interactive follower", follower: providerhttp.WithInteractive},
		{name: "interactive leader", leader: providerhttp.WithInteractive},
		{name: "hold-bypassing follower", follower: providerhttp.WithBypassHold},
		{name: "hold-bypassing leader", leader: providerhttp.WithBypassHold},
		{name: "another counter", leader: func(c context.Context) context.Context { return providerhttp.WithCounter(c, "query") },
			follower: func(c context.Context) context.Context { return providerhttp.WithCounter(c, "grab") }},
		{name: "counter vs none", follower: func(c context.Context) context.Context { return providerhttp.WithCounter(c, "grab") }},
		{name: "read-only leader, writing follower", leader: metadata.WithCacheReadOnly},
		{name: "key crafted to run into the read-only suffix", leader: metadata.WithCacheReadOnly, lq: "x", fq: "x\x00ro"},
	} {
		t.Run(v.name, func(t *testing.T) {
			clk := newClock()
			h := newHarness(t, clk, nil, integrations.Limits{})
			cache := newCache(t, h, clk, time.Hour, time.Hour)
			p, entered, release := gatedProvider("a")
			lead, follow := v.leader, v.follower
			if lead == nil {
				lead = id
			}
			if follow == nil {
				follow = id
			}
			lq, fq := v.lq, v.fq
			if lq == "" {
				lq = "x"
			}
			if fq == "" {
				fq = "x"
			}
			var wg sync.WaitGroup
			wg.Go(func() { _, _, _ = metadata.Cached[query, book](lead(context.Background()), cache, p, query{lq}) })
			<-entered
			wg.Go(func() { _, _, _ = metadata.Cached[query, book](follow(context.Background()), cache, p, query{fq}) })
			select {
			case <-entered: // the follower went live for itself
			case <-time.After(3 * time.Second):
				t.Fatal("the follower joined a flight the gate would have treated differently")
			}
			close(release)
			wg.Wait()
			eq(t, "two live calls", p.Calls(), 2)
		})
	}

	t.Run("same flags still share", func(t *testing.T) {
		clk := newClock()
		h := newHarness(t, clk, nil, integrations.Limits{})
		cache := newCache(t, h, clk, time.Hour, time.Hour)
		p, entered, release := gatedProvider("a")
		ctx := providerhttp.WithCounter(providerhttp.WithInteractive(context.Background()), "grab")
		var wg sync.WaitGroup
		wg.Go(func() { _, _, _ = metadata.Cached[query, book](ctx, cache, p, query{"x"}) })
		<-entered
		var joined atomic.Bool
		wg.Go(func() {
			_, _, _ = metadata.Cached[query, book](ctx, cache, p, query{"x"})
			joined.Store(true)
		})
		select {
		case <-entered:
			t.Fatal("identical callers must share one live call")
		case <-time.After(200 * time.Millisecond):
		}
		close(release)
		wg.Wait()
		eq(t, "one live call", p.Calls(), 1)
	})
}

// TestCache_FingerprintSeparatesConfigurations (N8): answers cached under one
// effective configuration are never read under another, a live call is not
// shared across them, and returning to the first configuration reads its
// answers again until they expire.
func TestCache_FingerprintSeparatesConfigurations(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	var mu sync.Mutex
	fp := "region=us"
	o := cacheOpts(h, clk, time.Hour, time.Hour)
	o.Fingerprint = func(integrations.Ref) string {
		mu.Lock()
		defer mu.Unlock()
		return fp
	}
	cache, err := metadata.NewCache(o)
	if err != nil {
		t.Fatal(err)
	}
	set := func(v string) { mu.Lock(); fp = v; mu.Unlock() }
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...)
	ask := func() {
		t.Helper()
		if _, _, err := metadata.Cached[query, book](context.Background(), cache, p, query{"x"}); err != nil {
			t.Fatal(err)
		}
	}
	ask()
	ask()
	eq(t, "cached under region=us", p.Calls(), 1)
	set("region=uk")
	ask()
	eq(t, "another configuration asks the provider", p.Calls(), 2)
	ask()
	eq(t, "and caches under its own fingerprint", p.Calls(), 2)
	set("region=us")
	ask()
	eq(t, "the first configuration's answer is still there", p.Calls(), 2)
	if n := rows(t, h); n != 2 {
		t.Fatalf("rows = %d, want one per fingerprint", n)
	}

	// An in-flight call under one configuration is not joined by a caller of another.
	gp, entered, release := gatedProvider("b")
	var wg sync.WaitGroup
	set("region=us")
	wg.Go(func() { _, _, _ = metadata.Cached[query, book](context.Background(), cache, gp, query{"y"}) })
	<-entered
	set("region=uk")
	wg.Go(func() { _, _, _ = metadata.Cached[query, book](context.Background(), cache, gp, query{"y"}) })
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("a caller of another configuration joined the flight")
	}
	close(release)
	wg.Wait()

	// No fingerprint: nothing changes for a cache that does not set one.
	plain := newCache(t, h, clk, time.Hour, time.Hour)
	q := coretest.NewFakeProvider[query, book](ref("c")).Answer(answers("c", "z")...)
	for range 2 {
		if _, _, err := metadata.Cached[query, book](context.Background(), plain, q, query{"z"}); err != nil {
			t.Fatal(err)
		}
	}
	eq(t, "no fingerprint", q.Calls(), 1)
}
