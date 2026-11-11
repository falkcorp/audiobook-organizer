// file: core/integrations/metadata/cache_test.go
// version: 1.0.0
// guid: 6c13bd02-a41f-4d88-94ac-f88015f19873
// last-edited: 2026-11-11

package metadata_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
)

// cacheCfg is a cache over the harness's module keyspace.
func cacheOpts(h *harness, clk *coretest.ProviderClock, pos, neg time.Duration) metadata.CacheOptions[query] {
	return metadata.CacheOptions[query]{
		Keyspace: h.ks,
		QuestionKey: func(q query) (string, bool) {
			if q.Title == "" {
				return "", false // an empty question is uncacheable
			}
			return strings.ToLower(q.Title), true
		},
		Version: "v1",
		TTL:     func(integrations.Ref) metadata.TTL { return metadata.TTL{Positive: pos, Negative: neg} },
		Now:     clk.Now,
	}
}

func newCache(t testing.TB, h *harness, clk *coretest.ProviderClock, pos, neg time.Duration) *metadata.Cache[query] {
	t.Helper()
	c, err := metadata.NewCache(cacheOpts(h, clk, pos, neg))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func rows(t testing.TB, h *harness) int {
	t.Helper()
	n := 0
	for _, err := range h.ks.Scan(context.Background(), storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n
}

func newClock() *coretest.ProviderClock { return &coretest.ProviderClock{} }

func TestCache_NegativeTTL(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, 10*time.Minute)
	ref := ref("a")
	empty := coretest.NewFakeProvider[query, book](ref)
	ctx := context.Background()

	for range 3 {
		rs, _, err := metadata.Cached[query, book](ctx, cache, empty, query{"Hush"})
		if err != nil || len(rs) != 0 {
			t.Fatalf("rs=%v err=%v", rs, err)
		}
	}
	eq(t, "an empty answer is cached: one live call", empty.Calls(), 1)

	clk.Advance(9 * time.Minute)
	_, _, _ = metadata.Cached[query, book](ctx, cache, empty, query{"Hush"})
	eq(t, "inside the negative TTL", empty.Calls(), 1)
	clk.Advance(2 * time.Minute)
	_, _, _ = metadata.Cached[query, book](ctx, cache, empty, query{"Hush"})
	eq(t, "past the negative TTL the provider is asked again", empty.Calls(), 2)

	// A ClassNotFound is the same answer: empty, negative-cached, not an error.
	nf := coretest.NewFakeProvider[query, book](ref).Fail(integrations.ClassNotFound, 404)
	for range 2 {
		rs, _, err := metadata.Cached[query, book](ctx, cache, nf, query{"Knightfall"})
		if err != nil || len(rs) != 0 {
			t.Fatalf("not found: rs=%v err=%v", rs, err)
		}
	}
	eq(t, "not-found is negative-cached", nf.Calls(), 1)

	// Positive answers live for the positive TTL.
	full := coretest.NewFakeProvider[query, book](ref).Answer(answers("a", "Year One")...)
	for range 2 {
		rs, at, err := metadata.Cached[query, book](ctx, cache, full, query{"Year One"})
		if err != nil || len(rs) != 1 || at.IsZero() {
			t.Fatalf("rs=%v at=%v err=%v", rs, at, err)
		}
	}
	eq(t, "positive answer cached", full.Calls(), 1)
	clk.Advance(59 * time.Minute)
	_, _, _ = metadata.Cached[query, book](ctx, cache, full, query{"Year One"})
	eq(t, "inside the positive TTL", full.Calls(), 1)
	clk.Advance(2 * time.Minute)
	_, _, _ = metadata.Cached[query, book](ctx, cache, full, query{"Year One"})
	eq(t, "past the positive TTL", full.Calls(), 2)
}

func TestCache_KeepsAskedAtOfTheLiveAnswer(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Minute)
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...)
	ctx := context.Background()

	_, live, _ := metadata.Cached[query, book](ctx, cache, p, query{"x"})
	clk.Advance(30 * time.Minute)
	_, cachedAt, _ := metadata.Cached[query, book](ctx, cache, p, query{"x"})
	if !cachedAt.Equal(live) {
		t.Errorf("cached AskedAt = %v, want the live answer's %v", cachedAt, live)
	}
}

func TestCache_ErrorsNeverWritten(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	p := coretest.NewFakeProvider[query, book](ref("a"))
	p.Fail(integrations.ClassRateLimited, 429).Fail(integrations.ClassTransient, 503).Fail(integrations.ClassBadOutput, 200).Answer(answers("a", "x")...)
	ctx := context.Background()

	for i := range 3 {
		if _, _, err := metadata.Cached[query, book](ctx, cache, p, query{"x"}); err == nil {
			t.Fatalf("call %d: want the provider's error", i)
		}
		eq(t, "rows after an error", rows(t, h), 0)
	}
	rs, _, err := metadata.Cached[query, book](ctx, cache, p, query{"x"})
	if err != nil || len(rs) != 1 {
		t.Fatalf("the next call was served from an error: rs=%v err=%v", rs, err)
	}
	eq(t, "every call was live", p.Calls(), 4)
	eq(t, "the answer is stored", rows(t, h), 1)

	// A canceled call is not an answer either.
	ctx2, cancel := context.WithCancel(ctx)
	cancel()
	q2 := coretest.NewFakeProvider[query, book](ref("b")).Answer(answers("b", "y")...)
	if _, _, err := metadata.Cached[query, book](ctx2, cache, q2, query{"y"}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	eq(t, "rows after a canceled call", rows(t, h), 1)
}

func TestCache_ReadOnlyWritesNothing(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...)
	ro := metadata.WithCacheReadOnly(context.Background())

	for range 2 {
		if _, _, err := metadata.Cached[query, book](ro, cache, p, query{"x"}); err != nil {
			t.Fatal(err)
		}
	}
	eq(t, "nothing written", rows(t, h), 0)
	eq(t, "both calls were live", p.Calls(), 2)

	// It still reads what a normal call wrote.
	if _, _, err := metadata.Cached[query, book](context.Background(), cache, p, query{"x"}); err != nil {
		t.Fatal(err)
	}
	before := p.Calls()
	if _, _, err := metadata.Cached[query, book](ro, cache, p, query{"x"}); err != nil {
		t.Fatal(err)
	}
	eq(t, "read-only reads", p.Calls(), before)

	// The negative case writes nothing either.
	q := coretest.NewFakeProvider[query, book](ref("b"))
	_, _, _ = metadata.Cached[query, book](ro, cache, q, query{"none"})
	eq(t, "rows", rows(t, h), 1)
}

func TestCache_BypassSkipsReadButWrites(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "old")...).Answer(answers("a", "new")...)
	ctx := context.Background()

	rs, _, _ := metadata.Cached[query, book](ctx, cache, p, query{"x"})
	eq(t, "first", rs[0].Title, "old")
	rs, _, _ = metadata.Cached[query, book](metadata.WithCacheBypass(ctx), cache, p, query{"x"})
	eq(t, "bypass is live", rs[0].Title, "new")
	rs, _, _ = metadata.Cached[query, book](ctx, cache, p, query{"x"})
	eq(t, "and refreshed the cache", rs[0].Title, "new")
}

func TestCache_SingleFlight(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...).Latency(150 * time.Millisecond)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			<-start
			rs, _, err := metadata.Cached[query, book](context.Background(), cache, p, query{"x"})
			if err == nil && len(rs) != 1 {
				err = errors.New("wrong result")
			}
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	eq(t, "N goroutines, one live call", p.Calls(), 1)
}

func TestCache_SingleFlightSharesErrors(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	p := coretest.NewFakeProvider[query, book](ref("a")).Fail(integrations.ClassTransient, 503).Latency(100 * time.Millisecond)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, _, err := metadata.Cached[query, book](context.Background(), cache, p, query{"x"}); err == nil {
				t.Error("want the shared failure")
			}
		})
	}
	wg.Wait()
	eq(t, "one live call, one shared failure", p.Calls(), 1)
}

func TestCache_CanceledLeaderDoesNotCancelFollowers(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	var mu sync.Mutex
	var started int
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	p := coretest.NewFakeProvider[query, book](ref("a"))
	p.Script(func(ctx context.Context, _ query) ([]book, error) {
		mu.Lock()
		started++
		first := started == 1
		mu.Unlock()
		entered <- struct{}{}
		if first { // the leader waits for its own cancellation
			<-ctx.Done()
			return nil, ctx.Err()
		}
		<-release
		return answers("a", "x"), nil
	})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, _, err := metadata.Cached[query, book](leaderCtx, cache, p, query{"x"})
		leaderErr <- err
	}()
	<-entered // the leader's live call is running

	followerErr := make(chan error, 1)
	go func() {
		rs, _, err := metadata.Cached[query, book](context.Background(), cache, p, query{"x"})
		if err == nil && len(rs) != 1 {
			err = errors.New("wrong result")
		}
		followerErr <- err
	}()
	time.Sleep(50 * time.Millisecond) // the follower is waiting on the leader's flight
	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Errorf("leader: %v", err)
	}
	<-entered // the follower retried the call itself
	close(release)
	if err := <-followerErr; err != nil {
		t.Errorf("follower: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	eq(t, "the follower made its own call", started, 2)
}

func TestCache_FollowerCancelWhileWaiting(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...).Latency(300 * time.Millisecond)
	go func() { _, _, _ = metadata.Cached[query, book](context.Background(), cache, p, query{"x"}) }()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, _, err := metadata.Cached[query, book](ctx, cache, p, query{"x"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a waiting follower honours its own deadline: %v", err)
	}
}

func TestCache_VersionBumpInvalidates(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	v1 := newCache(t, h, clk, time.Hour, time.Hour)
	o2 := cacheOpts(h, clk, time.Hour, time.Hour)
	o2.Version = "v2"
	v2, err := metadata.NewCache(o2)
	if err != nil {
		t.Fatal(err)
	}
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...)
	ctx := context.Background()

	_, _, _ = metadata.Cached[query, book](ctx, v1, p, query{"x"})
	_, _, _ = metadata.Cached[query, book](ctx, v1, p, query{"x"})
	eq(t, "v1 caches", p.Calls(), 1)
	_, _, _ = metadata.Cached[query, book](ctx, v2, p, query{"x"})
	eq(t, "a bumped Version is a miss", p.Calls(), 2)
	eq(t, "both versions' rows exist", rows(t, h), 2)

	removed, err := v2.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "sweep removes the old version", removed, 1)
	eq(t, "and keeps the current one", rows(t, h), 1)
	_, _, _ = metadata.Cached[query, book](ctx, v2, p, query{"x"})
	eq(t, "current entry still served", p.Calls(), 2)
}

func TestCache_UndecodableIsMiss(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...)
	ctx := context.Background()
	_, _, _ = metadata.Cached[query, book](ctx, cache, p, query{"x"})
	corrupt := func(value []byte) {
		t.Helper()
		var ks [][]byte
		for kv, err := range h.ks.Scan(ctx, storage.ScanOptions{}) {
			if err != nil {
				t.Fatal(err)
			}
			ks = append(ks, kv.Clone().Key)
		}
		for _, k := range ks {
			if err := h.ks.Set(ctx, k, value); err != nil {
				t.Fatal(err)
			}
		}
	}

	corrupt([]byte("not an entry"))
	rs, _, err := metadata.Cached[query, book](ctx, cache, p, query{"x"})
	if err != nil || len(rs) != 1 {
		t.Fatalf("an undecodable entry blocked a live answer: rs=%v err=%v", rs, err)
	}
	eq(t, "it went live", p.Calls(), 2)
	eq(t, "and the entry was rewritten", rows(t, h), 1)
	_, _, _ = metadata.Cached[query, book](ctx, cache, p, query{"x"})
	eq(t, "the rewritten entry serves", p.Calls(), 2)

	// A structurally valid entry whose payload is not the result type is a miss too.
	type wire struct {
		Version string          `json:"ver"`
		At      time.Time       `json:"at"`
		Expires time.Time       `json:"exp"`
		Data    json.RawMessage `json:"data"`
	}
	raw, err := codec.JSON[wire](1).Encode(wire{Version: "v1", At: clk.Now(), Expires: clk.Now().Add(time.Hour), Data: json.RawMessage(`{"not":"a list"}`)})
	if err != nil {
		t.Fatal(err)
	}
	corrupt(raw)
	if _, _, err := metadata.Cached[query, book](ctx, cache, p, query{"x"}); err != nil {
		t.Fatal(err)
	}
	eq(t, "wrong payload shape is a miss", p.Calls(), 3)
}

func TestCache_UncacheableQuestionGoesLive(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Hour)
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...)
	for range 2 {
		if _, _, err := metadata.Cached[query, book](context.Background(), cache, p, query{}); err != nil {
			t.Fatal(err)
		}
	}
	eq(t, "no caching", p.Calls(), 2)
	eq(t, "no rows", rows(t, h), 0)
}

func TestCache_ZeroTTLDoesNotCacheThatKind(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, 0)
	p := coretest.NewFakeProvider[query, book](ref("a"))
	for range 2 {
		_, _, _ = metadata.Cached[query, book](context.Background(), cache, p, query{"x"})
	}
	eq(t, "empty answers are not cached without a negative TTL", p.Calls(), 2)
	eq(t, "rows", rows(t, h), 0)
}

func TestCache_NilCacheIsLive(t *testing.T) {
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...)
	rs, _, err := metadata.Cached[query, book](context.Background(), nil, p, query{"x"})
	if err != nil || len(rs) != 1 {
		t.Fatalf("rs=%v err=%v", rs, err)
	}
}

func TestCache_InvalidateAndSweep(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	cache := newCache(t, h, clk, time.Hour, time.Minute)
	ctx := context.Background()
	p := coretest.NewFakeProvider[query, book](ref("a")).Answer(answers("a", "x")...)
	_, _, _ = metadata.Cached[query, book](ctx, cache, p, query{"keep"})
	_, _, _ = metadata.Cached[query, book](ctx, cache, p, query{"drop"})
	eq(t, "rows", rows(t, h), 2)

	if err := cache.Invalidate(ctx, ref("a"), "drop"); err != nil {
		t.Fatal(err)
	}
	if err := cache.Invalidate(ctx, ref("a"), "never-was"); err != nil {
		t.Errorf("invalidating a missing key: %v", err)
	}
	eq(t, "invalidated", rows(t, h), 1)
	_, _, _ = metadata.Cached[query, book](ctx, cache, p, query{"drop"})
	eq(t, "the dropped question goes live", p.Calls(), 3)

	// A row that is not a cache entry is left alone; expired entries go.
	if err := h.ks.Set(ctx, []byte("legacy"), []byte("pcache row of another shape")); err != nil {
		t.Fatal(err)
	}
	removed, err := cache.Sweep(ctx)
	if err != nil || removed != 0 {
		t.Fatalf("nothing is expired yet: removed=%d err=%v", removed, err)
	}
	clk.Advance(2 * time.Hour)
	removed, err = cache.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "expired entries are swept", removed, 2)
	eq(t, "the foreign row survives", rows(t, h), 1)
}

func TestNewCache_Validates(t *testing.T) {
	clk := newClock()
	h := newHarness(t, clk, nil, integrations.Limits{})
	good := cacheOpts(h, clk, time.Hour, time.Hour)
	for name, mut := range map[string]func(*metadata.CacheOptions[query]){
		"no keyspace":    func(o *metadata.CacheOptions[query]) { o.Keyspace = storage.Keyspace{} },
		"no QuestionKey": func(o *metadata.CacheOptions[query]) { o.QuestionKey = nil },
		"no TTL":         func(o *metadata.CacheOptions[query]) { o.TTL = nil },
	} {
		o := good
		mut(&o)
		if _, err := metadata.NewCache(o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
