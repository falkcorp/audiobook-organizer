// file: internal/plugins/maintenance/bounded_call_test.go
// version: 1.0.0
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
	v, err := boundedCall(context.Background(), time.Second, &ab, func() (int, error) { return 7, nil })
	if v != 7 || err != nil || ab.Load() != 0 {
		t.Fatalf("got %d, %v, abandoned=%d", v, err, ab.Load())
	}
}

func TestBoundedCall_TimeoutAbandonsAndCountsUntilReturn(t *testing.T) {
	var ab atomic.Int64
	release, done := make(chan struct{}), make(chan struct{})
	_, err := boundedCall(context.Background(), 10*time.Millisecond, &ab, func() (int, error) {
		defer close(done)
		<-release
		return 1, nil
	})
	if !errors.Is(err, errBoundedCallTimeout) {
		t.Fatalf("err = %v, want errBoundedCallTimeout", err)
	}
	if ab.Load() != 1 {
		t.Fatalf("abandoned = %d, want 1 while the call still runs", ab.Load())
	}
	close(release)
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for ab.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ab.Load() != 0 {
		t.Fatalf("abandoned = %d after the call returned, want 0", ab.Load())
	}
}

func TestBoundedCall_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release := make(chan struct{})
	defer close(release)
	_, err := boundedCall(ctx, time.Minute, nil, func() (int, error) { <-release; return 0, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
