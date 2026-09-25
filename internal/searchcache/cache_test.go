// file: internal/searchcache/cache_test.go
// version: 2.0.0
// guid: 88a06138-3f42-4544-a283-88594317ab8b
// last-edited: 2026-09-25

package searchcache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChangeLog_ChangedSinceAndOverflow(t *testing.T) {
	c := NewChangeLog(4)
	g0 := c.Generation()
	c.Record("a")
	c.Record("b", "a")
	ids, cur, ok := c.ChangedSince(g0)
	if !ok || cur != g0+2 {
		t.Fatalf("ChangedSince: ok=%v cur=%d", ok, cur)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("ids = %v, want [a b]", ids)
	}
	// Nothing changed since the current generation.
	if ids, _, ok := c.ChangedSince(cur); !ok || len(ids) != 0 {
		t.Fatalf("since current: ids=%v ok=%v", ids, ok)
	}
	// Two more records overflow the 4-slot ring: g0+1's record is evicted.
	c.Record("c", "d")
	if _, _, ok := c.ChangedSince(g0); ok {
		t.Fatal("ChangedSince(g0) after overflow reported ok")
	}
	if ids, _, ok := c.ChangedSince(g0 + 1); !ok || len(ids) != 4 {
		t.Fatalf("ChangedSince(g0+1) = %v ok=%v, want 4 ids", ids, ok)
	}
	// Empty IDs change nothing.
	before := c.Generation()
	c.Record("", "")
	if c.Generation() != before {
		t.Fatal("Record of empty IDs advanced the generation")
	}
	g := c.RecordAll()
	if _, _, ok := c.ChangedSince(g - 1); ok {
		t.Fatal("RecordAll left an older generation patchable")
	}
	if _, _, ok := c.ChangedSince(g); !ok {
		t.Fatal("ChangedSince(RecordAll gen) not ok")
	}
}

// fakeEval ranks the IDs of a shared corpus containing substr, in ID order.
type fakeEval struct {
	mu      *sync.Mutex
	corpus  map[string]string // id -> text
	substr  string
	block   chan struct{}
	builds  *atomic.Int64
	noPatch bool
}

func (f *fakeEval) Build(ctx context.Context, progress func(int)) ([]string, error) {
	if f.builds != nil {
		f.builds.Add(1)
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, text := range f.corpus {
		if strings.Contains(text, f.substr) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	progress(len(out))
	return out, nil
}

func (f *fakeEval) Match(ctx context.Context, ids []string) ([]string, bool, error) {
	if f.noPatch {
		return nil, false, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, id := range ids {
		if strings.Contains(f.corpus[id], f.substr) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, true, nil
}

func (f *fakeEval) Less(ctx context.Context, a, b string) (bool, error) { return a < b, nil }

func newFake(corpus map[string]string, substr string) *fakeEval {
	return &fakeEval{mu: &sync.Mutex{}, corpus: corpus, substr: substr, builds: &atomic.Int64{}}
}

func (f *fakeEval) set(id, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if text == "" {
		delete(f.corpus, id)
		return
	}
	f.corpus[id] = text
}

func TestCache_HitPatchAndOverflow(t *testing.T) {
	changes := NewChangeLog(8)
	c := New(changes, Config{})
	ev := newFake(map[string]string{"b1": "red fox", "b2": "blue fox", "b3": "red hen"}, "red")
	ctx := context.Background()

	res, err := c.Lookup(ctx, "red", ev, webOpts(time.Second))
	if err != nil || !reflect.DeepEqual(res.IDs, []string{"b1", "b3"}) {
		t.Fatalf("first lookup = %v, %v", res.IDs, err)
	}
	res, _ = c.Lookup(ctx, "red", ev, webOpts(time.Second))
	if !res.Hit {
		t.Fatal("second lookup was not a hit")
	}

	// Incremental: b2 becomes red, b1 stops being red.
	ev.set("b2", "red fox")
	ev.set("b1", "grey fox")
	changes.Record("b2", "b1")
	res, err = c.Lookup(ctx, "red", ev, webOpts(time.Second))
	if err != nil || res.Stale || !reflect.DeepEqual(res.IDs, []string{"b2", "b3"}) {
		t.Fatalf("patched lookup = %v stale=%v err=%v", res.IDs, res.Stale, err)
	}
	if c.Stats().Patches != 1 {
		t.Fatalf("patches = %d, want 1", c.Stats().Patches)
	}
	builds := ev.builds.Load()

	// Overflow: more changes than the ring holds. Stale served, rebuild runs.
	ev.set("b0", "red ant")
	for i := 0; i < 10; i++ {
		changes.Record(fmt.Sprintf("x%d", i))
	}
	changes.Record("b0")
	res, err = c.Lookup(ctx, "red", ev, webOpts(time.Second))
	if err != nil || !res.Stale || !reflect.DeepEqual(res.IDs, []string{"b2", "b3"}) {
		t.Fatalf("overflow lookup = %v stale=%v err=%v", res.IDs, res.Stale, err)
	}
	waitFor(t, func() bool {
		r, _ := c.Lookup(ctx, "red", ev, webOpts(time.Second))
		return !r.Stale && reflect.DeepEqual(r.IDs, []string{"b0", "b2", "b3"})
	})
	if ev.builds.Load() <= builds {
		t.Fatal("overflow did not rebuild")
	}
}

func TestCache_UnpatchableEvaluatorRebuilds(t *testing.T) {
	changes := NewChangeLog(8)
	c := New(changes, Config{})
	ev := newFake(map[string]string{"b1": "red"}, "red")
	ev.noPatch = true
	ctx := context.Background()
	if _, err := c.Lookup(ctx, "k", ev, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	ev.set("b2", "red")
	changes.Record("b2")
	res, _ := c.Lookup(ctx, "k", ev, webOpts(time.Second))
	if !res.Stale {
		t.Fatal("unpatchable change was not served stale")
	}
	waitFor(t, func() bool {
		r, _ := c.Lookup(ctx, "k", ev, webOpts(time.Second))
		return !r.Stale && len(r.IDs) == 2
	})
}

func TestCache_DisconnectStillCaches(t *testing.T) {
	c := New(NewChangeLog(8), Config{})
	ev := newFake(map[string]string{"b1": "red"}, "red")
	ev.block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := c.Lookup(ctx, "k", ev, webOpts(time.Minute))
		errc <- err
	}()
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	close(ev.block)
	waitFor(t, func() bool { return c.Stats().Entries == 1 })
	res, err := c.Lookup(context.Background(), "k", ev, webOpts(time.Second))
	if err != nil || !res.Hit || len(res.IDs) != 1 {
		t.Fatalf("retry after disconnect = %+v, %v; want a hit", res, err)
	}
	if ev.builds.Load() != 1 {
		t.Fatalf("builds = %d, want 1", ev.builds.Load())
	}
}

func TestCache_PendingAndPoll(t *testing.T) {
	c := New(NewChangeLog(8), Config{})
	ev := newFake(map[string]string{"b1": "red", "b2": "red"}, "red")
	ev.block = make(chan struct{})
	_, err := c.Lookup(context.Background(), "k", ev, webOpts(20*time.Millisecond))
	var pe *PendingError
	if !errors.As(err, &pe) || pe.SearchID == "" {
		t.Fatalf("err = %v, want *PendingError", err)
	}
	st, ok := c.Job(pe.SearchID)
	if !ok || st.Status != "running" {
		t.Fatalf("job = %+v ok=%v, want running", st, ok)
	}
	// A second caller joins the same build rather than starting another.
	_, err = c.Lookup(context.Background(), "k", ev, webOpts(20*time.Millisecond))
	var pe2 *PendingError
	if !errors.As(err, &pe2) || pe2.SearchID != pe.SearchID {
		t.Fatalf("second caller got %v, want the same search ID", err)
	}
	close(ev.block)
	waitFor(t, func() bool { s, _ := c.Job(pe.SearchID); return s.Status == "done" })
	if st, _ := c.Job(pe.SearchID); st.MatchesSoFar != 2 {
		t.Fatalf("matches = %d, want 2", st.MatchesSoFar)
	}
	if ev.builds.Load() != 1 {
		t.Fatalf("builds = %d, want 1", ev.builds.Load())
	}
}

func TestCache_MemoryCapEvictsLRU(t *testing.T) {
	ctx := context.Background()
	corpus := map[string]string{}
	for i := 0; i < 10; i++ {
		corpus[fmt.Sprintf("b%02d", i)] = "red"
	}
	ev := newFake(corpus, "red")
	built, _ := ev.Build(ctx, func(int) {})
	one := entrySize("k1", built)
	c := New(NewChangeLog(8), Config{MaxBytes: 2*one + one/2})
	for _, k := range []string{"k1", "k2"} {
		if _, err := c.Lookup(ctx, k, ev, webOpts(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// Touch k1 so k2 is least recently used, then add k3.
	c.Lookup(ctx, "k1", ev, webOpts(time.Second)) //nolint:errcheck
	if _, err := c.Lookup(ctx, "k3", ev, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	st := c.Stats()
	if st.Entries != 2 || st.Bytes > 2*one+one/2 || st.Evicted != 1 {
		t.Fatalf("stats = %+v, want 2 entries within the cap after 1 eviction", st)
	}
	c.mu.Lock()
	_, hasK2 := c.entries["k2"]
	_, hasK1 := c.entries["k1"]
	c.mu.Unlock()
	if hasK2 || !hasK1 {
		t.Fatalf("evicted the wrong entry: k1=%v k2=%v", hasK1, hasK2)
	}
	// An entry bigger than the whole cap is served but not kept.
	small := New(NewChangeLog(8), Config{MaxBytes: 100})
	res, err := small.Lookup(ctx, "k", ev, webOpts(time.Second))
	if err != nil || len(res.IDs) != 10 || small.Stats().Entries != 0 {
		t.Fatalf("oversize: ids=%d err=%v entries=%d", len(res.IDs), err, small.Stats().Entries)
	}
}

func TestCache_ConcurrentReadsAndWrites(t *testing.T) {
	changes := NewChangeLog(64)
	c := New(changes, Config{})
	corpus := map[string]string{}
	for i := 0; i < 50; i++ {
		corpus[fmt.Sprintf("b%02d", i)] = "red"
	}
	ev := newFake(corpus, "red")
	ctx := context.Background()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if w%2 == 0 {
					id := fmt.Sprintf("b%02d", (i*7+w)%50)
					text := "red"
					if i%3 == 0 {
						text = "blue"
					}
					ev.set(id, text)
					changes.Record(id)
					continue
				}
				if _, err := c.Lookup(ctx, fmt.Sprintf("k%d", i%3), ev, webOpts(time.Second)); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	// Quiesced: every key must now converge on the truth.
	want, _ := ev.Build(ctx, func(int) {})
	for k := 0; k < 3; k++ {
		key := fmt.Sprintf("k%d", k)
		waitFor(t, func() bool {
			r, err := c.Lookup(ctx, key, ev, webOpts(time.Second))
			return err == nil && !r.Stale && reflect.DeepEqual(r.IDs, want)
		})
	}
}

func webOpts(wait time.Duration) LookupOptions {
	return LookupOptions{Wait: wait, AllowPending: true, AllowStale: true}
}

// exactOpts is what a caller that must see every change gets: no pending
// answer, no stale list.
var exactOpts = LookupOptions{}

// Finding 8/17: a caller that did not accept stale results never receives
// the pre-change list. On ring overflow, or a change the evaluator cannot
// patch, it gets ErrNotCurrent (and runs the search itself); a rebuild is
// started for everyone after it.
func TestCache_ExactCallerNeverGetsStale(t *testing.T) {
	changes := NewChangeLog(4)
	c := New(changes, Config{})
	ev := newFake(map[string]string{"b1": "red"}, "red")
	ctx := context.Background()
	if _, err := c.Lookup(ctx, "k", ev, exactOpts); err != nil {
		t.Fatal(err)
	}
	ev.set("b2", "red")
	for i := 0; i < 10; i++ {
		changes.Record(fmt.Sprintf("x%d", i))
	}
	changes.Record("b2")
	res, err := c.Lookup(ctx, "k", ev, exactOpts)
	if !errors.Is(err, ErrNotCurrent) {
		t.Fatalf("overflow: exact lookup = %+v, %v; want ErrNotCurrent", res, err)
	}
	waitFor(t, func() bool {
		r, err := c.Lookup(ctx, "k", ev, exactOpts)
		return err == nil && reflect.DeepEqual(r.IDs, []string{"b1", "b2"})
	})

	// Unpatchable evaluator: same contract.
	ev.noPatch = true
	ev.set("b3", "red")
	changes.Record("b3")
	if _, err := c.Lookup(ctx, "k", ev, exactOpts); !errors.Is(err, ErrNotCurrent) {
		t.Fatalf("unpatchable: err = %v, want ErrNotCurrent", err)
	}
}

// An exact caller blocks on a new build past the configured wait instead of
// receiving a PendingError.
func TestCache_ExactCallerBlocksPastWait(t *testing.T) {
	c := New(NewChangeLog(8), Config{Wait: 10 * time.Millisecond})
	ev := newFake(map[string]string{"b1": "red"}, "red")
	ev.block = make(chan struct{})
	go func() { time.Sleep(60 * time.Millisecond); close(ev.block) }()
	res, err := c.Lookup(context.Background(), "k", ev, exactOpts)
	if err != nil || len(res.IDs) != 1 {
		t.Fatalf("exact miss = %+v, %v; want the built list", res, err)
	}
}

// Finding 10b/21: the eviction fallback in refresh uses the CALLER's options,
// so a blocking caller never gets a PendingError from it.
func TestCache_EvictedDuringRefreshKeepsCallerOptions(t *testing.T) {
	changes := NewChangeLog(8)
	c := New(changes, Config{Wait: 10 * time.Millisecond})
	ev := newFake(map[string]string{"b1": "red"}, "red")
	ev.block = make(chan struct{})
	go func() { time.Sleep(60 * time.Millisecond); close(ev.block) }()
	// Straight into refresh with no entry: the state Lookup sees when the
	// entry is evicted between its read and the patch.
	res, err := c.refresh(context.Background(), "k", ev, exactOpts, 0)
	if err != nil || len(res.IDs) != 1 {
		t.Fatalf("refresh after eviction = %+v, %v; want the built list, not a pending error", res, err)
	}
}

// Finding 10a: a patch that outlives the caller's wait does not hold the
// request. A caller that accepts stale results gets the old list flagged
// stale; the patch finishes and is stored.
func TestCache_SlowPatchIsBoundedByWait(t *testing.T) {
	changes := NewChangeLog(8)
	c := New(changes, Config{})
	ev := &slowLessEval{fakeEval: newFake(map[string]string{"b1": "red", "b3": "red"}, "red"), delay: 30 * time.Millisecond}
	ctx := context.Background()
	if _, err := c.Lookup(ctx, "k", ev, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	ev.set("b2", "red")
	changes.Record("b2")
	start := time.Now()
	res, err := c.Lookup(ctx, "k", ev, webOpts(5*time.Millisecond))
	if err != nil || !res.Stale || len(res.IDs) != 2 {
		t.Fatalf("slow patch = %+v, %v; want the old list, stale", res, err)
	}
	if d := time.Since(start); d > 25*time.Millisecond {
		t.Fatalf("lookup took %v, want about the 5ms wait", d)
	}
	waitFor(t, func() bool {
		r, err := c.Lookup(ctx, "k", ev, webOpts(time.Second))
		return err == nil && r.Hit && reflect.DeepEqual(r.IDs, []string{"b1", "b2", "b3"})
	})
}

type slowLessEval struct {
	*fakeEval
	delay time.Duration
}

func (s *slowLessEval) Less(ctx context.Context, a, b string) (bool, error) {
	time.Sleep(s.delay)
	return a < b, nil
}

// Finding 1/11: a finished job keeps no ID list once its waiters have read
// it, whether they read it or timed out.
func TestCache_FinishedJobsDoNotPinIDs(t *testing.T) {
	c := New(NewChangeLog(8), Config{})
	ev := newFake(map[string]string{"b1": "red", "b2": "red"}, "red")
	if _, err := c.Lookup(context.Background(), "k1", ev, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	var ids []string
	for id := range c.jobs {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		if c.jobHoldsIDs(id) {
			t.Fatalf("job %s still holds its result after the waiter read it", id)
		}
	}
	ev.block = make(chan struct{})
	_, err := c.Lookup(context.Background(), "k2", ev, webOpts(5*time.Millisecond))
	var pe *PendingError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want pending", err)
	}
	close(ev.block)
	waitFor(t, func() bool { s, _ := c.Job(pe.SearchID); return s.Status == "done" })
	if c.jobHoldsIDs(pe.SearchID) {
		t.Fatal("a job whose only waiter timed out pinned its result")
	}
}

// Finding 12: builds are bounded. At most MaxConcurrentBuilds run at once,
// and past MaxQueuedBuilds a new key gets ErrBusy instead of a goroutine.
func TestCache_BuildsAreBounded(t *testing.T) {
	c := New(NewChangeLog(8), Config{MaxConcurrentBuilds: 2, MaxQueuedBuilds: 3})
	var running, peak atomic.Int64
	gate := make(chan struct{})
	mk := func() Evaluator {
		return &countingEval{running: &running, peak: &peak, gate: gate}
	}
	for i := 0; i < 3; i++ {
		_, err := c.Lookup(context.Background(), fmt.Sprintf("k%d", i), mk(), webOpts(time.Millisecond))
		var pe *PendingError
		if !errors.As(err, &pe) {
			t.Fatalf("lookup %d: %v, want pending", i, err)
		}
	}
	if _, err := c.Lookup(context.Background(), "k3", mk(), webOpts(time.Millisecond)); !errors.Is(err, ErrBusy) {
		t.Fatalf("4th distinct key: err = %v, want ErrBusy", err)
	}
	close(gate)
	waitFor(t, func() bool { return c.Stats().Building == 0 })
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrent builds = %d, want <= 2", p)
	}
}

type countingEval struct {
	running, peak *atomic.Int64
	gate          chan struct{}
}

func (e *countingEval) Build(ctx context.Context, progress func(int)) ([]string, error) {
	n := e.running.Add(1)
	for {
		p := e.peak.Load()
		if n <= p || e.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-e.gate
	e.running.Add(-1)
	return []string{"x"}, nil
}
func (e *countingEval) Match(ctx context.Context, ids []string) ([]string, bool, error) {
	return nil, false, nil
}
func (e *countingEval) Less(ctx context.Context, a, b string) (bool, error) { return a < b, nil }

// Finding 5: an ID is charged at its allocation size class, and the backing
// array by capacity.
func TestCache_EntrySizeChargesAllocation(t *testing.T) {
	ids := make([]string, 0, 4)
	for i := 0; i < 2; i++ {
		ids = append(ids, fmt.Sprintf("01J%023d", i)) // 26 bytes: 32-byte class
	}
	got := entrySize("k", ids)
	want := int64(entryOverheadBytes+1) + 4*16 + 2*32
	if got != want {
		t.Fatalf("entrySize = %d, want %d", got, want)
	}
}

// Finding 29: for an evaluator whose order drifts under other books' writes
// (Bleve relevance), a successful patch also schedules a rebuild, which
// restores the fresh order.
func TestCache_DriftingOrderRebuildsAfterPatch(t *testing.T) {
	changes := NewChangeLog(8)
	c := New(changes, Config{})
	ev := &driftEval{fakeEval: newFake(map[string]string{"b1": "red", "b3": "red"}, "red")}
	ctx := context.Background()
	if _, err := c.Lookup(ctx, "k", ev, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	before := ev.builds.Load()
	ev.set("b2", "red")
	changes.Record("b2")
	res, err := c.Lookup(ctx, "k", ev, webOpts(time.Second))
	if err != nil || res.Stale || len(res.IDs) != 3 {
		t.Fatalf("patched = %+v, %v", res, err)
	}
	waitFor(t, func() bool { return ev.builds.Load() > before && c.Stats().Building == 0 })
}

type driftEval struct{ *fakeEval }

func (driftEval) OrderDriftsOnPatch() bool { return true }

// Finding 13: a lookup that joins a build started before a change reports the
// generation the IDs are current to, and an exact caller gets the change
// patched in.
func TestCache_JoinedOlderBuildIsBroughtForward(t *testing.T) {
	changes := NewChangeLog(8)
	c := New(changes, Config{})
	ev := newFake(map[string]string{"b1": "red"}, "red")
	ev.block = make(chan struct{})
	_, err := c.Lookup(context.Background(), "k", ev, webOpts(time.Millisecond))
	var pe *PendingError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want pending", err)
	}
	waitFor(t, func() bool { s, _ := c.Job(pe.SearchID); return s.Status == "running" })
	ev.set("b2", "red")
	changes.Record("b2")
	need := changes.Generation()
	done := make(chan Result, 1)
	go func() {
		r, err := c.Lookup(context.Background(), "k", ev, exactOpts)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	time.Sleep(10 * time.Millisecond)
	close(ev.block)
	r := <-done
	if r.Gen < need || !reflect.DeepEqual(r.IDs, []string{"b1", "b2"}) {
		t.Fatalf("joined build = %+v; want gen >= %d with b2 patched in", r, need)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}
