// file: internal/plugins/maintenance/bounded_call_test.go
// version: 1.0.2
// guid: 78d82bf9-97a7-496f-90a2-1a1f6f8cffee
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedCall_ReturnsResult(t *testing.T) {
	var ab atomic.Int64
	v, err := boundedCall(context.Background(), time.Second, func() (int, error) { return 7, nil }, &ab)
	if v != 7 || err != nil || ab.Load() != 0 {
		t.Fatalf("got %d, %v, abandoned=%d", v, err, ab.Load())
	}
}

func TestBoundedCall_TimeoutAbandonsAndCountsUntilReturn(t *testing.T) {
	var ab atomic.Int64
	release, done := make(chan struct{}), make(chan struct{})
	var gauge atomic.Int64
	_, err := boundedCall(context.Background(), 10*time.Millisecond, func() (int, error) {
		defer close(done)
		<-release
		return 1, nil
	}, &ab, &gauge)
	if !errors.Is(err, errBoundedCallTimeout) {
		t.Fatalf("err = %v, want errBoundedCallTimeout", err)
	}
	if ab.Load() != 1 || gauge.Load() != 1 {
		t.Fatalf("abandoned = %d, gauge = %d, want 1 each while the call still runs", ab.Load(), gauge.Load())
	}
	close(release)
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for (ab.Load() != 0 || gauge.Load() != 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ab.Load() != 0 || gauge.Load() != 0 {
		t.Fatalf("abandoned = %d, gauge = %d after the call returned, want 0 each", ab.Load(), gauge.Load())
	}
}

func TestBoundedCall_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release := make(chan struct{})
	defer close(release)
	_, err := boundedCall(ctx, time.Minute, func() (int, error) { <-release; return 0, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// The timer fires after the goroutine has sent its result but before its CAS.
// The hooks pin that exact interleaving: fn cannot return until the waiter has
// already given up, and the goroutine cannot CAS until the waiter has decided.
// The waiter must return the good result, not a timeout, and nothing may be
// counted as abandoned.
func TestBoundedCall_ResultSentBeforeClaimIsNotATimeout(t *testing.T) {
	var ab atomic.Int64
	release, sent, proceed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	hooks := &boundedHooks{
		afterSend: func() { close(sent); <-proceed },
		onGiveUp:  func() { close(release); <-sent },
	}
	v, err := boundedCallHooked(context.Background(), time.Millisecond, hooks, func() (int, error) {
		<-release
		return 42, nil
	}, &ab)
	if err != nil || v != 42 {
		t.Fatalf("got %d, %v; want 42, nil (a result already sent is not a timeout)", v, err)
	}
	if ab.Load() != 0 {
		t.Fatalf("abandoned = %d before the goroutine's CAS, want 0", ab.Load())
	}
	// Let the goroutine CAS and exit. Its CAS (running -> finished) succeeds
	// because the waiter never claimed, so it must not uncount anything. Its
	// exit is not observable, hence the short settle before the final check.
	close(proceed)
	time.Sleep(10 * time.Millisecond)
	if ab.Load() != 0 {
		t.Fatalf("abandoned = %d after the goroutine exited, want 0", ab.Load())
	}
}
