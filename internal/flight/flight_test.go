// file: internal/flight/flight_test.go
// version: 1.1.0
// guid: 4c9e2a6b-7d13-4f58-a2b0-9e6d1c3f7a85
// last-edited: 2026-10-09

package flight

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// joinAsync starts f.do for key on its own request context and returns the
// context's cancel and a channel carrying do's result.
type flightResult struct {
	resp map[string]any
	err  error
}

func joinAsync(f *Group[map[string]any], key string, build func(context.Context) (map[string]any, error)) (context.CancelFunc, <-chan flightResult) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan flightResult, 1)
	go func() {
		r, err := f.Do(ctx, key, time.Minute, build)
		out <- flightResult{r, err}
	}()
	return cancel, out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Every waiter leaves: the shared build's context is cancelled, each caller
// gets its own context error back at once, and the key is free for a fresh
// build.
func TestGroup_AllWaitersLeaveCancelsBuild(t *testing.T) {
	var f Group[map[string]any]
	var builds atomic.Int32
	buildCtxDone := make(chan struct{})
	build := func(ctx context.Context) (map[string]any, error) {
		builds.Add(1)
		<-ctx.Done()
		close(buildCtxDone)
		return nil, ctx.Err()
	}
	cancel1, out1 := joinAsync(&f, "k", build)
	waitFor(t, "first waiter", func() bool { return f.Waiters("k") == 1 })
	cancel2, out2 := joinAsync(&f, "k", build)
	waitFor(t, "second waiter", func() bool { return f.Waiters("k") == 2 })

	cancel1()
	if r := <-out1; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("first caller: err %v, want its own context.Canceled", r.err)
	}
	select {
	case <-buildCtxDone:
		t.Fatal("build cancelled while a caller was still waiting on it")
	case <-time.After(50 * time.Millisecond):
	}
	cancel2()
	if r := <-out2; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("second caller: err %v, want context.Canceled", r.err)
	}
	select {
	case <-buildCtxDone:
	case <-time.After(5 * time.Second):
		t.Fatal("build context not cancelled after its last waiter left")
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("%d builds, want 1 shared build", n)
	}
	waitFor(t, "key released", func() bool { return !f.InFlight("k") })
}

// One waiter leaves, one stays: the build runs to completion and the caller
// that stayed gets its result.
func TestGroup_OneLeavesOtherGetsResult(t *testing.T) {
	var f Group[map[string]any]
	release := make(chan struct{})
	var cancelledEarly atomic.Bool
	build := func(ctx context.Context) (map[string]any, error) {
		select {
		case <-release:
		case <-ctx.Done():
			cancelledEarly.Store(true)
			return nil, ctx.Err()
		}
		return map[string]any{"count": 42}, nil
	}
	cancel1, out1 := joinAsync(&f, "k", build)
	waitFor(t, "first waiter", func() bool { return f.Waiters("k") == 1 })
	cancel2, out2 := joinAsync(&f, "k", build)
	defer cancel2()
	waitFor(t, "second waiter", func() bool { return f.Waiters("k") == 2 })

	cancel1()
	if r := <-out1; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("leaving caller: err %v, want context.Canceled", r.err)
	}
	close(release)
	r := <-out2
	if r.err != nil || r.resp["count"] != 42 {
		t.Fatalf("staying caller: resp %v err %v, want the build's result", r.resp, r.err)
	}
	if cancelledEarly.Load() {
		t.Fatal("build was cancelled although a caller was still waiting")
	}
}

// A build error goes to the callers that waited for it and is not kept: the
// next call runs a new build.
func TestGroup_ErrorIsNotKept(t *testing.T) {
	var f Group[map[string]any]
	var builds atomic.Int32
	boom := errors.New("boom")
	build := func(context.Context) (map[string]any, error) {
		if builds.Add(1) == 1 {
			return nil, boom
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := f.Do(context.Background(), "k", time.Minute, build); !errors.Is(err, boom) {
		t.Fatalf("first call: err %v, want boom", err)
	}
	r, err := f.Do(context.Background(), "k", time.Minute, build)
	if err != nil || r["ok"] != true {
		t.Fatalf("second call: %v %v, want a fresh successful build", r, err)
	}
	if n := builds.Load(); n != 2 {
		t.Fatalf("%d builds, want 2", n)
	}
}

// The shared build carries the given deadline even while callers wait.
func TestGroup_BuildHasDeadline(t *testing.T) {
	var f Group[map[string]any]
	var deadline time.Time
	var ok bool
	_, _ = f.Do(context.Background(), "k", time.Hour, func(ctx context.Context) (map[string]any, error) {
		deadline, ok = ctx.Deadline()
		return map[string]any{}, nil
	})
	if !ok || time.Until(deadline) > time.Hour || time.Until(deadline) < time.Hour-time.Minute {
		t.Fatalf("build deadline = %v (set %v), want about 1h from now", deadline, ok)
	}
}

// A caller whose context is already done gets its error back without a build
// being started or joined: there is nobody to wait for the result, and a
// build that ran anyway could finish before the caller's departure cancelled
// it (the flaky TestScopedTagFacets_CancelledCallerGetsErrorNothingCached).
func TestGroup_AlreadyCancelledCallerStartsNoBuild(t *testing.T) {
	var f Group[int]
	var builds atomic.Int32
	build := func(ctx context.Context) (int, error) {
		builds.Add(1)
		return 1, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 50; i++ {
		if _, err := f.Do(ctx, "k", 0, build); !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v, want context.Canceled", err)
		}
	}
	if n := builds.Load(); n != 0 {
		t.Fatalf("build ran %d times for a caller that had already left", n)
	}
	if f.InFlight("k") {
		t.Fatal("a build is registered for a caller that had already left")
	}
	// A live caller afterwards builds normally.
	if v, err := f.Do(context.Background(), "k", 0, build); err != nil || v != 1 {
		t.Fatalf("live caller: %v, %v", v, err)
	}
}
