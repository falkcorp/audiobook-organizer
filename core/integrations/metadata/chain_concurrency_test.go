// file: core/integrations/metadata/chain_concurrency_test.go
// version: 0.1.0
// guid: 9a2d6e81-4c37-4f5b-b0e8-7d15a3c94f26
// last-edited: 2026-11-22

package metadata_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/metadata"
)

// limitFake stores an enabled hello.fake instance whose own Limits set
// MaxConcurrent (the instance override of an adapter that sets none).
func limitFake(h *harness, name string, priority, maxConcurrent int, f *fake) *fake {
	h.t.Helper()
	if f == nil {
		f = coretest.NewFakeProvider[query, book](ref(name))
	}
	h.mu.Lock()
	h.fakes[integrations.InstanceID(name)] = f
	h.mu.Unlock()
	h.putLimits(ref(name), fakeAdapter, priority, &integrations.Limits{MaxConcurrent: maxConcurrent})
	return f
}

// putLimits stores (or edits) ref with the given Limits override.
func (h *harness) putLimits(r integrations.Ref, adapter integrations.AdapterID, priority int, l *integrations.Limits) {
	h.t.Helper()
	var version uint64
	if cur, ok := h.reg.Config(r); ok {
		version = cur.Version
	}
	if _, err := h.reg.Put(admin(), integrations.InstanceConfig{
		Ref: r, Adapter: adapter, Name: string(r.Instance), Enabled: true, Priority: priority,
		Settings: map[string]any{}, Limits: l, Version: version,
	}, nil); err != nil {
		h.t.Fatalf("Put %s: %v", r, err)
	}
}

// searchN runs n searches of one chain at once and returns their outcomes of the
// one provider, in no order.
func searchN(ctx context.Context, t *testing.T, chain *metadata.Chain[query, book], n int) []metadata.OutcomeStatus {
	t.Helper()
	var wg sync.WaitGroup
	got := make([]metadata.OutcomeStatus, n)
	for i := range n {
		wg.Go(func() {
			_, outs, _ := chain.Search(ctx, query{Title: "Year One"})
			if len(outs) > 0 {
				got[i] = outs[0].Status
			}
		})
	}
	wg.Wait()
	return got
}

func allAnswered(t *testing.T, got []metadata.OutcomeStatus) {
	t.Helper()
	for i, st := range got {
		if st != metadata.Answered {
			t.Errorf("search %d: status %v, want Answered", i, st)
		}
	}
}

// Limits.MaxConcurrent on an instance bounds the searches in flight to it
// across every search of the chain (HEAD's rate_limit.max_concurrent).
func TestChain_MaxConcurrentBoundsSearchesInFlight(t *testing.T) {
	h := plainHarness(t)
	f := limitFake(h, "alfred", 1, 2, nil).Answer(answers("alfred", "Year One")...).Latency(30 * time.Millisecond)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	allAnswered(t, searchN(context.Background(), t, chain, 8))
	eq(t, "every search reached the provider", f.Calls(), 8)
	eq(t, "the most searches in flight at once", f.MaxConcurrent(), 2)
}

// No Limits.MaxConcurrent anywhere is no bound: core sets no default of its own.
func TestChain_NoMaxConcurrentIsUnbounded(t *testing.T) {
	h := plainHarness(t)
	f := h.addFake("alfred", 1, nil).Answer(answers("alfred", "Year One")...).Latency(30 * time.Millisecond)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	allAnswered(t, searchN(context.Background(), t, chain, 6))
	if got := f.MaxConcurrent(); got < 4 {
		t.Errorf("peak in flight = %d of 6 searches, want them mostly at once", got)
	}
}

// An adapter's Meta.Limits.MaxConcurrent binds its instances; an instance's own
// value replaces it, and a negative one lifts it.
func TestChain_MaxConcurrentInheritsFromTheAdapter(t *testing.T) {
	kit := coretest.ProviderHTTP(t)
	lim := httpLimits()
	lim.MaxConcurrent = 1
	h := newHarness(t, kit.Clock, kit.Registry.Outbound, lim)
	h.addHTTP("alfred", 1, kit.URL())
	var inflight, peak atomic.Int64
	kit.Handle(func(w http.ResponseWriter, _ *http.Request) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
		_ = json.NewEncoder(w).Encode([]string{"Year One"})
	})
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})
	run := func(label string, lo, hi int64) {
		t.Helper()
		peak.Store(0)
		allAnswered(t, searchN(context.Background(), t, chain, 5))
		if got := peak.Load(); got < lo || got > hi {
			t.Errorf("%s: peak in flight = %d, want %d to %d", label, got, lo, hi)
		}
	}
	run("the adapter's bound of 1", 1, 1)

	h.putLimits(ref("alfred"), httpAdapter, 1, &integrations.Limits{MaxConcurrent: 3})
	run("the instance's own bound of 3 replaces it", 2, 3)

	h.putLimits(ref("alfred"), httpAdapter, 1, &integrations.Limits{MaxConcurrent: -1})
	run("a negative instance value lifts the adapter's bound", 4, 5)
}

// An edit of the bound takes effect on the next search.
func TestChain_MaxConcurrentEditTakesEffect(t *testing.T) {
	h := plainHarness(t)
	f := limitFake(h, "alfred", 1, 1, nil).Answer(answers("alfred", "Year One")...).Latency(25 * time.Millisecond)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})

	allAnswered(t, searchN(context.Background(), t, chain, 4))
	eq(t, "bound 1", f.MaxConcurrent(), 1)
	h.putLimits(ref("alfred"), fakeAdapter, 1, &integrations.Limits{MaxConcurrent: 3})
	allAnswered(t, searchN(context.Background(), t, chain, 6))
	if got := f.MaxConcurrent(); got < 2 || got > 3 {
		t.Errorf("bound raised to 3: peak in flight = %d, want 2 or 3", got)
	}
}

// Waiting for a slot is not the provider's time: PerProvider starts once the
// slot is held. Four 60ms searches queued behind one slot take 240ms in all,
// well past the 100ms deadline, and every one must still be answered.
func TestChain_QueueingDoesNotSpendTheProviderDeadline(t *testing.T) {
	h := plainHarness(t)
	limitFake(h, "alfred", 1, 1, nil).Answer(answers("alfred", "Year One")...).Latency(60 * time.Millisecond)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, PerProvider: 100 * time.Millisecond})

	allAnswered(t, searchN(context.Background(), t, chain, 4))
}

// The deadline still bounds the call itself once the slot is held, and a slow
// call gives its slot back so the next search runs.
func TestChain_DeadlineStillBoundsAHeldSlot(t *testing.T) {
	h := plainHarness(t)
	f := limitFake(h, "alfred", 1, 1, nil).Answer(answers("alfred", "Year One")...).Latency(time.Second)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, PerProvider: 30 * time.Millisecond})

	start := time.Now()
	for i := range 3 {
		_, outs, _ := chain.Search(context.Background(), query{Title: "Year One"})
		if len(outs) != 1 || outs[0].Status != metadata.Failed || outs[0].Class != integrations.ClassTransient {
			t.Fatalf("search %d: %+v, want Failed/Transient (the provider's own deadline)", i, outs)
		}
	}
	if time.Since(start) > 700*time.Millisecond {
		t.Error("a timed-out search kept the slot")
	}
	eq(t, "each search ran", f.Calls(), 3)
}

// A search queued for a slot leaves cleanly when its caller cancels: it is the
// caller's cancellation (never the provider's failure), it never reaches the
// provider, and the slot it did not take is not lost.
func TestChain_CancelWhileQueuedHoldsNothing(t *testing.T) {
	h := plainHarness(t)
	hold := make(chan struct{})
	f := limitFake(h, "alfred", 1, 1, nil)
	f.Script(func(ctx context.Context, q query) ([]book, error) {
		if q.Title == "hold" {
			select {
			case <-hold:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return answers("alfred", q.Title), nil
	})
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4})
	first := make(chan []metadata.Outcome, 1)
	go func() {
		_, outs, _ := chain.Search(context.Background(), query{Title: "hold"})
		first <- outs
	}()
	for f.Calls() < 1 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	_, outs, err := chain.Search(ctx, query{Title: "queued"})
	if err == nil || len(outs) != 1 || outs[0].Status != metadata.Canceled || outs[0].Class != integrations.ClassCanceled {
		t.Fatalf("queued search: outs=%+v err=%v, want one Canceled", outs, err)
	}
	eq(t, "the queued search never reached the provider", f.Calls(), 1)

	close(hold)
	if got := <-first; len(got) != 1 || got[0].Status != metadata.Answered {
		t.Fatalf("the holder: %+v", got)
	}
	// The slot is free: a new search runs at once.
	done := make(chan metadata.OutcomeStatus, 1)
	go func() {
		_, o, _ := chain.Search(context.Background(), query{Title: "after"})
		done <- o[0].Status
	}()
	select {
	case st := <-done:
		eq(t, "the search after the cancel", st, metadata.Answered)
	case <-time.After(2 * time.Second):
		t.Fatal("a canceled waiter leaked the slot: the next search never ran")
	}
}

// A cached answer never reaches the provider, so it never waits for a slot:
// with the only slot held by another question, the cached one is answered.
func TestChain_CachedAnswerTakesNoSlot(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	hold := make(chan struct{})
	f := limitFake(h, "alfred", 1, 1, nil)
	f.Script(func(ctx context.Context, q query) ([]book, error) {
		if q.Title == "hold" {
			select {
			case <-hold:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return answers("alfred", q.Title), nil
	})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	chain := newChain(t, h, metadata.ChainOptions[query]{Parallelism: fixed4, Cache: cache})
	ctx := context.Background()

	if _, outs, _ := chain.Search(ctx, query{Title: "Year One"}); outs[0].Status != metadata.Answered {
		t.Fatalf("priming search: %+v", outs)
	}
	go func() { _, _, _ = chain.Search(ctx, query{Title: "hold"}) }()
	for f.Calls() < 2 {
		time.Sleep(time.Millisecond)
	}
	defer close(hold)

	done := make(chan metadata.Outcome, 1)
	go func() {
		_, o, _ := chain.Search(ctx, query{Title: "Year One"})
		done <- o[0]
	}()
	select {
	case o := <-done:
		eq(t, "the cached search", o.Status, metadata.Answered)
		eq(t, "the cached search's provider calls", f.Calls(), 2)
	case <-time.After(2 * time.Second):
		t.Fatal("a cached answer waited for the slot a live search held")
	}
}
