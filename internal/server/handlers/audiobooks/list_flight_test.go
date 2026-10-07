// file: internal/server/handlers/audiobooks/list_flight_test.go
// version: 1.0.0
// guid: 1f8d3b62-9a47-4c05-b7e2-6c4a0e9d5f13
// last-edited: 2026-10-06

package audiobookshandler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// joinAsync starts f.do for key on its own request context and returns the
// context's cancel and a channel carrying do's result.
type flightResult struct {
	resp gin.H
	err  error
}

func joinAsync(f *listFlight, key string, build func(context.Context) (gin.H, error)) (context.CancelFunc, <-chan flightResult) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan flightResult, 1)
	go func() {
		r, err := f.do(ctx, key, build)
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

func (f *listFlight) waitersFor(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.calls[key]; ok {
		return c.waiters
	}
	return 0
}

// Every waiter leaves: the shared build's context is cancelled, each caller
// gets its own context error back at once, and the key is free for a fresh
// build.
func TestListFlight_AllWaitersLeaveCancelsBuild(t *testing.T) {
	var f listFlight
	var builds atomic.Int32
	buildCtxDone := make(chan struct{})
	build := func(ctx context.Context) (gin.H, error) {
		builds.Add(1)
		<-ctx.Done()
		close(buildCtxDone)
		return nil, ctx.Err()
	}
	cancel1, out1 := joinAsync(&f, "k", build)
	waitFor(t, "first waiter", func() bool { return f.waitersFor("k") == 1 })
	cancel2, out2 := joinAsync(&f, "k", build)
	waitFor(t, "second waiter", func() bool { return f.waitersFor("k") == 2 })

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
	waitFor(t, "key released", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, held := f.calls["k"]
		return !held
	})
}

// One waiter leaves, one stays: the build runs to completion and the caller
// that stayed gets its result.
func TestListFlight_OneLeavesOtherGetsResult(t *testing.T) {
	var f listFlight
	release := make(chan struct{})
	var cancelledEarly atomic.Bool
	build := func(ctx context.Context) (gin.H, error) {
		select {
		case <-release:
		case <-ctx.Done():
			cancelledEarly.Store(true)
			return nil, ctx.Err()
		}
		return gin.H{"count": 42}, nil
	}
	cancel1, out1 := joinAsync(&f, "k", build)
	waitFor(t, "first waiter", func() bool { return f.waitersFor("k") == 1 })
	cancel2, out2 := joinAsync(&f, "k", build)
	defer cancel2()
	waitFor(t, "second waiter", func() bool { return f.waitersFor("k") == 2 })

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
func TestListFlight_ErrorIsNotKept(t *testing.T) {
	var f listFlight
	var builds atomic.Int32
	boom := errors.New("boom")
	build := func(context.Context) (gin.H, error) {
		if builds.Add(1) == 1 {
			return nil, boom
		}
		return gin.H{"ok": true}, nil
	}
	if _, err := f.do(context.Background(), "k", build); !errors.Is(err, boom) {
		t.Fatalf("first call: err %v, want boom", err)
	}
	r, err := f.do(context.Background(), "k", build)
	if err != nil || r["ok"] != true {
		t.Fatalf("second call: %v %v, want a fresh successful build", r, err)
	}
	if n := builds.Load(); n != 2 {
		t.Fatalf("%d builds, want 2", n)
	}
}

// The shared build carries a hard deadline even while callers wait.
func TestListFlight_BuildHasDeadline(t *testing.T) {
	var f listFlight
	var deadline time.Time
	var ok bool
	_, _ = f.do(context.Background(), "k", func(ctx context.Context) (gin.H, error) {
		deadline, ok = ctx.Deadline()
		return gin.H{}, nil
	})
	if !ok || time.Until(deadline) > listFlightBuildTimeout || time.Until(deadline) < listFlightBuildTimeout-time.Minute {
		t.Fatalf("build deadline = %v (set %v), want about %s from now", deadline, ok, listFlightBuildTimeout)
	}
}
