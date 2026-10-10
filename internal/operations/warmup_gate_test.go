// file: internal/operations/warmup_gate_test.go
// version: 1.0.0
// guid: 8a4d2f61-7e03-4c9b-b5a8-1d6e3f0c7b92
// last-edited: 2026-10-09

package operations

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakeWaiter struct {
	done chan struct{}
}

func (f *fakeWaiter) WaitForWarmupCtx(ctx context.Context) error {
	select {
	case <-f.done:
		return nil
	default:
	}
	select {
	case <-f.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestWaitForWarmup_NoWaiterProceedsAtOnce(t *testing.T) {
	var waited atomic.Bool
	err := WaitForWarmup(context.Background(), nil, time.Minute, slog.Default(),
		func() { waited.Store(true) }, nil, 0)
	if err != nil || waited.Load() {
		t.Fatalf("err=%v waited=%v; want nil,false", err, waited.Load())
	}
}

func TestWaitForWarmup_AlreadyDoneDoesNotAnnounceAWait(t *testing.T) {
	w := &fakeWaiter{done: make(chan struct{})}
	close(w.done)
	var waited atomic.Bool
	err := WaitForWarmup(context.Background(), w, time.Minute, slog.Default(),
		func() { waited.Store(true) }, nil, 0)
	if err != nil || waited.Load() {
		t.Fatalf("err=%v waited=%v; want nil,false", err, waited.Load())
	}
}

func TestWaitForWarmup_BlocksUntilWarmupFinishes(t *testing.T) {
	w := &fakeWaiter{done: make(chan struct{})}
	var waited atomic.Int32
	var beats atomic.Int32
	result := make(chan error, 1)
	go func() {
		result <- WaitForWarmup(context.Background(), w, time.Minute, slog.Default(),
			func() { waited.Add(1) }, func() { beats.Add(1) }, 10*time.Millisecond)
	}()
	select {
	case err := <-result:
		t.Fatalf("returned before warmup finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if waited.Load() != 1 {
		t.Fatalf("onWait called %d times, want 1", waited.Load())
	}
	if beats.Load() == 0 {
		t.Fatal("heartbeat never fired during the wait")
	}
	close(w.done)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after warmup finished")
	}
}

func TestWaitForWarmup_CtxCancelReturnsPromptly(t *testing.T) {
	w := &fakeWaiter{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- WaitForWarmup(ctx, w, time.Minute, slog.Default(), nil, nil, 0)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after ctx cancel")
	}
}

// The bound: a warmup that never finishes must not block the op forever. The
// op proceeds (nil error) once the timeout elapses.
func TestWaitForWarmup_TimeoutProceeds(t *testing.T) {
	w := &fakeWaiter{done: make(chan struct{})}
	start := time.Now()
	err := WaitForWarmup(context.Background(), w, 80*time.Millisecond, slog.Default(), nil, nil, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil on timeout", err)
	}
	if d := time.Since(start); d < 60*time.Millisecond || d > 2*time.Second {
		t.Fatalf("waited %v, want about the 80ms timeout", d)
	}
}

func TestWarmupWaitTimeoutIsFiveMinutes(t *testing.T) {
	if WarmupWaitTimeout != 300*time.Second {
		t.Fatalf("WarmupWaitTimeout = %v, want 300s", WarmupWaitTimeout)
	}
}
