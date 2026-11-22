// file: core/startprogress/startprogress_test.go
// version: 1.0.0
// guid: 1f8c3d0e-6b53-4c6e-a2b1-9a3f5e0d4c21
// last-edited: 2026-11-22

package startprogress

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestRun_ExtendsBeforeAndPerUnitOnTheCallersGoroutine: the
// extension precedes the work and follows its progress units even when they
// are raised from several worker goroutines, and extend is never running
// twice at once (the concurrency ExtendStart does not support).
func TestRun_ExtendsBeforeAndPerUnitOnTheCallersGoroutine(t *testing.T) {
	var inExtend atomic.Int32
	var calls atomic.Int32
	extend := func(step string) {
		if inExtend.Add(1) != 1 {
			t.Error("extend called concurrently")
		}
		if step != "gotham" {
			t.Errorf("step = %q", step)
		}
		calls.Add(1)
		inExtend.Add(-1)
	}
	err := Run(context.Background(), "gotham", extend, func(ctx context.Context) error {
		if calls.Load() != 1 {
			t.Errorf("work started after %d extensions, want 1", calls.Load())
		}
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for range 50 {
					Report(ctx)
				}
			})
		}
		wg.Wait()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Coalescing may merge signals, but the last one is always answered: at
	// least the start and one for the units, never more than 1 + 200.
	if n := calls.Load(); n < 2 || n > 201 {
		t.Fatalf("extensions = %d, want between 2 and 201", n)
	}
}

func TestRun_NoProgressIsOneExtension(t *testing.T) {
	var calls int
	err := Run(context.Background(), "robin", func(string) { calls++ }, func(context.Context) error { return nil })
	if err != nil || calls != 1 {
		t.Fatalf("err %v, extensions %d, want nil and 1", err, calls)
	}
}

func TestRun_ReturnsWorksError(t *testing.T) {
	boom := errors.New("riddler")
	if err := Run(context.Background(), "s", func(string) {}, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestRun_NilExtenderRunsWorkAndReportIsANoOp(t *testing.T) {
	ran := false
	err := Run(context.Background(), "s", nil, func(ctx context.Context) error {
		Report(ctx)
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("err %v ran %v", err, ran)
	}
	Report(context.Background()) // no hook under a bare ctx
}
