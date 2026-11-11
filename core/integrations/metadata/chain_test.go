// file: core/integrations/metadata/chain_test.go
// version: 1.0.0
// guid: 043ea600-bd34-4738-ad54-e15c5e90563c
// last-edited: 2026-11-11

package metadata_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/tasks"
)

var fixed4 = tasks.Fixed(4, "provider rate limits")

// plainHarness is a harness with fake providers only.
func plainHarness(t testing.TB) *harness {
	t.Helper()
	return newHarness(t, newClock(), nil, integrations.Limits{})
}

func answers(source string, titles ...string) []book {
	out := make([]book, len(titles))
	for i, t := range titles {
		out[i] = book{Title: t, Source: source}
	}
	return out
}

func statuses(outs []metadata.Outcome) map[string]metadata.OutcomeStatus {
	m := map[string]metadata.OutcomeStatus{}
	for _, o := range outs {
		m[string(o.Provider.Instance)] = o.Status
	}
	return m
}

func order(outs []metadata.Outcome) []string {
	var s []string
	for _, o := range outs {
		s = append(s, string(o.Provider.Instance))
	}
	return s
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func eqSlice(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

// httpLimits are the hello.http adapter's limits: no retries, a breaker that
// opens on two failures, and a 5xx hold that is over as soon as the test's
// clock moves, so the breaker (which sits behind the hold) is what refuses.
func httpLimits() integrations.Limits {
	return integrations.Limits{
		RPS: 1000, Burst: 1000, MaxRetries: -1, Timeout: 5 * time.Second,
		Breaker: integrations.BreakerPolicy{Threshold: 2, Cooldown: time.Minute},
		Holds:   integrations.HoldPolicy{Durations: map[integrations.Class]time.Duration{integrations.ClassTransient: time.Nanosecond}},
	}
}

func TestChain_SkipsHeldProvider(t *testing.T) {
	kit := coretest.ProviderHTTP(t)
	h := newHarness(t, kit.Clock, kit.Registry.Outbound, httpLimits())
	h.addHTTP("alfred", 1, kit.URL())
	robin := h.addFake("robin", 2, nil)
	robin.Answer(answers("robin", "Year One")...)
	kit.Handle(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})
	ctx := context.Background()

	rs, outs, err := chain.Search(ctx, query{"Year One"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "first search, alfred", outs[0].Status, metadata.Failed)
	eq(t, "first search, alfred's class", outs[0].Class, integrations.ClassRateLimited)
	eq(t, "first search, robin", outs[1].Status, metadata.Answered)
	eq(t, "results", len(rs), 1)
	hits := kit.Hits()

	rs, outs, err = chain.Search(ctx, query{"Year One"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "second search, alfred", outs[0].Status, metadata.SkippedHeld)
	eq(t, "second search, robin", outs[1].Status, metadata.Answered)
	eq(t, "results", len(rs), 1)
	eq(t, "the held provider was never dialled", kit.Hits(), hits)
	if outs[0].Err == nil || outs[0].Class != integrations.ClassRefusedLocal {
		t.Errorf("a skipped provider keeps its refusal: class %v err %v", outs[0].Class, outs[0].Err)
	}

	// When the hold ends the provider is asked again, first.
	kit.Clock.Advance(2 * time.Hour)
	kit.Handle(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]string{"The Long Halloween"})
	})
	rs, outs, err = chain.Search(ctx, query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "after the hold, alfred", outs[0].Status, metadata.Answered)
	eq(t, "FirstHit stops at alfred", len(outs), 1)
	eq(t, "result source", rs[0].Value.Source, "alfred")
}

func TestChain_FirstHitStopsInPriorityOrder(t *testing.T) {
	h := plainHarness(t)
	// Priority, not instance ID, orders the chain.
	third := h.addFake("alpha", 30, nil)
	third.Answer(answers("alpha", "c")...)
	first := h.addFake("zeta", 10, nil)
	first.Answer() // nothing
	second := h.addFake("mid", 20, nil)
	second.Answer(answers("mid", "b1", "b2")...)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	rs, outs, err := chain.Search(context.Background(), query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eqSlice(t, "outcomes", order(outs), []string{"zeta", "mid"})
	eq(t, "zeta", outs[0].Status, metadata.Empty)
	eq(t, "mid", outs[1].Status, metadata.Answered)
	eq(t, "results", len(rs), 2)
	eq(t, "the lower-priority provider is never called", third.Calls(), 0)
	eq(t, "zeta was asked once", first.Calls(), 1)
	for _, r := range rs {
		eq(t, "provenance", r.Provider, ref("mid"))
		eq(t, "live answer", r.FromCache, false)
		if r.AskedAt.IsZero() {
			t.Error("AskedAt is zero")
		}
	}
}

func TestChain_AllModeReturnsPriorityOrderNotCompletionOrder(t *testing.T) {
	h := plainHarness(t)
	slow := h.addFake("slow", 1, nil)
	slow.Answer(answers("slow", "s")...).Latency(60 * time.Millisecond)
	quick := h.addFake("quick", 2, nil)
	quick.Answer(answers("quick", "q")...)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, Mode: metadata.All})

	rs, outs, err := chain.Search(context.Background(), query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eqSlice(t, "outcomes", order(outs), []string{"slow", "quick"})
	eq(t, "first result", rs[0].Value.Source, "slow")
	eq(t, "second result", rs[1].Value.Source, "quick")
}

func TestChain_NoAnswerIsError(t *testing.T) {
	h := plainHarness(t)
	h.addFake("a", 1, nil).Fail(integrations.ClassTransient, 503)
	h.addFake("b", 2, nil).Fail(integrations.ClassBadOutput, 200)
	h.addFake("c", 3, nil).Accepting(func(query) bool { return false })
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	rs, outs, err := chain.Search(context.Background(), query{"x"})
	if !errors.Is(err, metadata.ErrNoProviderAnswered) {
		t.Fatalf("err = %v, want ErrNoProviderAnswered", err)
	}
	if len(rs) != 0 {
		t.Errorf("results = %v", rs)
	}
	eq(t, "a", statuses(outs)["a"], metadata.Failed)
	eq(t, "b", statuses(outs)["b"], metadata.Failed)
	eq(t, "c", statuses(outs)["c"], metadata.SkippedCaps)
	// The causes are joined: each provider's error stays reachable.
	var ie *integrations.Error
	if !errors.As(err, &ie) || ie.Class != integrations.ClassTransient {
		t.Errorf("the first cause is not joined: %v", err)
	}
	eq(t, "the error is not an answer: ClassOf", integrations.ClassOf(context.Background(), err), integrations.ClassTransient)
}

func TestChain_NoInstancesIsNoAnswer(t *testing.T) {
	h := plainHarness(t)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})
	rs, outs, err := chain.Search(context.Background(), query{"x"})
	if !errors.Is(err, metadata.ErrNoProviderAnswered) || len(rs) != 0 || len(outs) != 0 {
		t.Errorf("rs=%v outs=%v err=%v", rs, outs, err)
	}
}

func TestChain_NotFoundIsEmpty(t *testing.T) {
	h := plainHarness(t)
	nf := h.addFake("a", 1, nil)
	nf.Fail(integrations.ClassNotFound, 404)
	h.addFake("b", 2, nil).Answer(answers("b", "x")...)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, Mode: metadata.All})

	rs, outs, err := chain.Search(context.Background(), query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "a", outs[0].Status, metadata.Empty)
	if outs[0].Err != nil {
		t.Errorf("an answer about the query is not an error: %v", outs[0].Err)
	}
	eq(t, "b", outs[1].Status, metadata.Answered)
	eq(t, "results", len(rs), 1)

	// Alone, a not-found provider is an answer: no results, no error.
	h2 := plainHarness(t)
	h2.addFake("only", 1, nil).Fail(integrations.ClassNotFound, 404)
	rs, outs, err = newChain(t, h2, metadata.ChainOptions[query]{Parallelism: fixed4}).Search(context.Background(), query{"x"})
	if err != nil || len(rs) != 0 || outs[0].Status != metadata.Empty {
		t.Errorf("rs=%v outs=%v err=%v", rs, outs, err)
	}
}

func TestChain_FanOutBounded(t *testing.T) {
	h := plainHarness(t)
	var now, peak atomic.Int64
	script := func(ctx context.Context, _ query) ([]book, error) {
		n := now.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer now.Add(-1)
		select {
		case <-time.After(30 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return answers("p", "x"), nil
	}
	for i := range 7 {
		f := coretest.NewFakeProvider[query, book](ref("p" + string(rune('a'+i))))
		f.Script(script)
		h.addFake("p"+string(rune('a'+i)), i, f)
	}
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: tasks.Fixed(2, "test bound"), Mode: metadata.All})

	rs, outs, err := chain.Search(context.Background(), query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "every provider answered", len(outs), 7)
	eq(t, "results", len(rs), 7)
	eq(t, "peak concurrent provider calls", peak.Load(), int64(2))
}

func TestChain_EditKeepsBreaker(t *testing.T) {
	kit := coretest.ProviderHTTP(t)
	h := newHarness(t, kit.Clock, kit.Registry.Outbound, httpLimits())
	h.addHTTP("alfred", 1, kit.URL())
	h.addFake("robin", 2, nil).Answer(answers("robin", "x")...)
	kit.Handle(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, Mode: metadata.All})
	ctx := context.Background()
	alfred := ref("alfred")

	for range 2 { // the breaker's threshold
		_, outs, err := chain.Search(ctx, query{"x"})
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "failing upstream", outs[0].Status, metadata.Failed)
		kit.Clock.Advance(time.Millisecond) // the 5xx hold is over; the breaker is not
	}
	_, outs, _ := chain.Search(ctx, query{"x"})
	eq(t, "breaker open", outs[0].Status, metadata.SkippedBreaker)
	hits, builds := kit.Hits(), h.buildCount(alfred)

	// Editing the instance swaps it for a new build; the chain is not rebuilt
	// and the gate's state, which lives per Ref, survives.
	h.edit(alfred, 9, true)
	_, outs, err := chain.Search(ctx, query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eqSlice(t, "the edit's new priority took effect on the next search", order(outs), []string{"robin", "alfred"})
	eq(t, "edited instance, breaker still open", statuses(outs)["alfred"], metadata.SkippedBreaker)
	eq(t, "the edit swapped the build", h.buildCount(alfred), builds+1)
	eq(t, "no request reached the upstream", kit.Hits(), hits)

	// The cooldown passes and the upstream recovers: the breaker's probe lets
	// the edited instance through.
	kit.Clock.Advance(2 * time.Minute)
	kit.Handle(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode([]string{"Zero Year"}) })
	_, outs, err = chain.Search(ctx, query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "after the cooldown", statuses(outs)["alfred"], metadata.Answered)
}

func TestChain_EditedInstancesApplyOnNextSearch(t *testing.T) {
	h := plainHarness(t)
	a := h.addFake("a", 1, nil)
	a.Answer(answers("a", "x")...)
	b := h.addFake("b", 2, nil)
	b.Answer(answers("b", "x")...)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})
	ctx := context.Background()

	rs, _, _ := chain.Search(ctx, query{"x"})
	eq(t, "before", rs[0].Value.Source, "a")
	h.edit(ref("a"), 3, true) // reorder
	rs, _, _ = chain.Search(ctx, query{"x"})
	eq(t, "after the reorder", rs[0].Value.Source, "b")
	h.edit(ref("b"), 2, false) // disable
	rs, _, _ = chain.Search(ctx, query{"x"})
	eq(t, "after b is disabled", rs[0].Value.Source, "a")
	h.put(ref("c"), fakeAdapter, 0, true, nil) // an instance with no fake: its build fails, the chain goes on
	rs, outs, err := chain.Search(ctx, query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "a broken instance is a Failed outcome", statuses(outs)["c"], metadata.Failed)
	eq(t, "and the chain goes on", rs[0].Value.Source, "a")
}

func TestChain_SkipsProvidersThatCannotUseTheQuery(t *testing.T) {
	h := plainHarness(t)
	picky := h.addFake("picky", 1, nil)
	picky.Answer(answers("picky", "x")...).Accepting(func(q query) bool { return q.Title != "" })
	h.addFake("any", 2, nil).Answer(answers("any", "y")...)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	rs, outs, err := chain.Search(context.Background(), query{})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "picky", outs[0].Status, metadata.SkippedCaps)
	eq(t, "picky was not called", picky.Calls(), 0)
	eq(t, "any answered", rs[0].Value.Source, "any")
}

func TestChain_IgnoresInstancesOfOtherQueryTypes(t *testing.T) {
	h := plainHarness(t)
	h.addFake("mine", 2, nil).Answer(answers("mine", "x")...)
	// A metadata adapter of another app type (a TV lookup) is not this chain's.
	other := coretest.NewFakeProvider[tvQuery, string](ref("tv"))
	h.addTV("tv", 1, other)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	rs, outs, err := chain.Search(context.Background(), query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eqSlice(t, "only this chain's instances are considered", order(outs), []string{"mine"})
	eq(t, "results", len(rs), 1)
	eq(t, "the other type's provider is never asked", other.Calls(), 0)

	// Named explicitly, a mismatch is an error, not a silent skip.
	named := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, Instances: func() []integrations.Ref { return []integrations.Ref{ref("tv"), ref("mine")} }})
	_, outs, err = named.Search(context.Background(), query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "named mismatch", outs[0].Status, metadata.Failed)
	eq(t, "named mismatch, class", outs[0].Class, integrations.ClassUnsupported)
	eq(t, "named match", outs[1].Status, metadata.Answered)
}

func TestChain_PerProviderDeadline(t *testing.T) {
	h := plainHarness(t)
	h.addFake("slow", 1, nil).Answer(answers("slow", "x")...).Latency(time.Second)
	h.addFake("fast", 2, nil).Answer(answers("fast", "y")...)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, PerProvider: 20 * time.Millisecond})

	start := time.Now()
	rs, outs, err := chain.Search(context.Background(), query{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("the slow provider held the chain past its deadline")
	}
	eq(t, "slow", outs[0].Status, metadata.Failed)
	eq(t, "the deadline is the provider's failure, not the caller's", outs[0].Class, integrations.ClassTransient)
	eq(t, "fast", rs[0].Value.Source, "fast")
}

func TestChain_CallerCancelIsNotAProviderFailure(t *testing.T) {
	h := plainHarness(t)
	h.addFake("slow", 1, nil).Answer(answers("slow", "x")...).Latency(5 * time.Second)
	h.addFake("next", 2, nil).Answer(answers("next", "y")...)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	rs, outs, err := chain.Search(ctx, query{"x"})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, metadata.ErrNoProviderAnswered) {
		t.Fatalf("err = %v", err)
	}
	eq(t, "ClassOf", integrations.ClassOf(ctx, err), integrations.ClassCanceled)
	eq(t, "slow", outs[0].Status, metadata.Canceled)
	eq(t, "eq class", outs[0].Class, integrations.ClassCanceled)
	if len(rs) != 0 {
		t.Errorf("results = %v", rs)
	}
	eq(t, "the provider never reached is canceled too, never asked", outs[1].Status, metadata.Canceled)
}

func TestNewChain_Validates(t *testing.T) {
	h := plainHarness(t)
	if _, err := metadata.NewChain[query, book](h.reg, metadata.ChainOptions[query]{}); !errors.Is(err, tasks.ErrNoParallelism) {
		t.Errorf("zero Parallelism: %v", err)
	}
	if _, err := metadata.NewChain[query, book](nil, metadata.ChainOptions[query]{Parallelism: fixed4}); err == nil {
		t.Error("nil Registry accepted")
	}
	if _, err := metadata.NewChain[query, book](h.reg, metadata.ChainOptions[query]{Parallelism: fixed4, Mode: 9}); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestChain_Stream(t *testing.T) {
	h := plainHarness(t)
	h.addFake("slow", 1, nil).Answer(answers("slow", "s")...).Latency(80 * time.Millisecond)
	h.addFake("quick", 2, nil).Answer(answers("quick", "q1", "q2")...)
	h.addFake("broken", 3, nil).Fail(integrations.ClassAuth, 401)
	h.addFake("empty", 4, nil).Answer()
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	var got []string
	var failed []string
	for r, err := range chain.Stream(context.Background(), query{"x"}) {
		if err != nil {
			failed = append(failed, string(r.Provider.Instance))
			continue
		}
		got = append(got, r.Value.Source+":"+r.Value.Title)
	}
	eqSlice(t, "completion order", got, []string{"quick:q1", "quick:q2", "slow:s"})
	eqSlice(t, "failures are yielded", failed, []string{"broken"})
}

func TestChain_StreamStopsEarly(t *testing.T) {
	h := plainHarness(t)
	var canceled atomic.Int64
	started := make(chan struct{})
	slow := coretest.NewFakeProvider[query, book](ref("slow"))
	slow.Script(func(ctx context.Context, _ query) ([]book, error) {
		close(started)
		select {
		case <-time.After(5 * time.Second):
			return answers("slow", "s"), nil
		case <-ctx.Done():
			canceled.Add(1)
			return nil, ctx.Err()
		}
	})
	h.addFake("slow", 2, slow)
	h.addFake("quick", 1, nil).Answer(answers("quick", "q")...)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	start := time.Now()
	for range chain.Stream(context.Background(), query{"x"}) {
		<-started // the slow provider is running
		break     // one answer is enough
	}
	if time.Since(start) > 2*time.Second {
		t.Error("stopping the stream did not cancel the running provider")
	}
	eq(t, "the running provider was canceled and awaited", canceled.Load(), int64(1))
}

func TestChain_StreamReportsCancellation(t *testing.T) {
	h := plainHarness(t)
	h.addFake("slow", 1, nil).Answer(answers("slow", "s")...).Latency(5 * time.Second)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	var last error
	for _, err := range chain.Stream(ctx, query{"x"}) {
		last = err
	}
	if !errors.Is(last, context.Canceled) {
		t.Errorf("last = %v", last)
	}
}

func TestChain_UsesCache(t *testing.T) {
	h := plainHarness(t)
	f := h.addFake("a", 1, nil)
	f.Answer(answers("a", "x")...)
	cache := newCache(t, h, h.clk, time.Hour, time.Minute)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, Cache: cache})
	ctx := context.Background()

	rs, _, _ := chain.Search(ctx, query{"x"})
	eq(t, "first search is live", rs[0].FromCache, false)
	rs, _, _ = chain.Search(ctx, query{"x"})
	eq(t, "second search is cached", rs[0].FromCache, true)
	eq(t, "one live call", f.Calls(), 1)
	rs, _, _ = chain.Search(metadata.WithCacheBypass(ctx), query{"x"})
	eq(t, "bypass goes live", rs[0].FromCache, false)
	eq(t, "two live calls", f.Calls(), 2)
}
