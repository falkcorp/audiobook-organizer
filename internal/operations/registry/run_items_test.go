// file: internal/operations/registry/run_items_test.go
// version: 1.0.0
// guid: b3c4d5e6-f7a8-9012-bcde-f34567890123
// last-edited: 2026-06-22

package registry_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// stubReporterForItems is a minimal Reporter for RunItems tests.
type stubReporterForItems struct {
	progressCalls atomic.Int32
	currentItems  []string
	mu            sync.Mutex
}

func (s *stubReporterForItems) UpdateProgress(current, total int, message string) error {
	s.progressCalls.Add(1)
	return nil
}
func (s *stubReporterForItems) SetCurrentItem(label string) {
	s.mu.Lock()
	s.currentItems = append(s.currentItems, label)
	s.mu.Unlock()
}
func (s *stubReporterForItems) Log(_ slog.Level, _ string, _ ...slog.Attr) error { return nil }
func (s *stubReporterForItems) Logger() *slog.Logger                             { return slog.Default() }
func (s *stubReporterForItems) Checkpoint(_ any) error                           { return nil }
func (s *stubReporterForItems) IsCanceled() bool                                 { return false }
func (s *stubReporterForItems) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, s)
}
func (s *stubReporterForItems) Trigger(_ context.Context, _ string, _ any) error { return nil }

func TestRunItems_Sequential_AllSucceed(t *testing.T) {
	r := &stubReporterForItems{}
	items := []int{1, 2, 3}
	var processed []int
	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, n int) error {
		processed = append(processed, n)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(processed) != 3 {
		t.Fatalf("expected 3 items processed, got %d", len(processed))
	}
	if int(r.progressCalls.Load()) != 3 {
		t.Fatalf("expected 3 progress calls, got %d", r.progressCalls.Load())
	}
}

func TestRunItems_Sequential_FailFast(t *testing.T) {
	r := &stubReporterForItems{}
	items := []int{1, 2, 3}
	var processed []int
	errStop := errors.New("stop")
	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, n int) error {
		processed = append(processed, n)
		if n == 2 {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("expected errStop, got %v", err)
	}
	if len(processed) != 2 {
		t.Fatalf("expected 2 items processed (fail on 2nd), got %d", len(processed))
	}
}

func TestRunItems_Sequential_CollectErrors(t *testing.T) {
	r := &stubReporterForItems{}
	items := []int{1, 2, 3}
	errA := errors.New("err-1")
	errC := errors.New("err-3")
	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, n int) error {
		if n == 1 {
			return errA
		}
		if n == 3 {
			return errC
		}
		return nil
	}, registry.RunItemsOptions{ErrMode: registry.ErrModeCollect})
	if err == nil {
		t.Fatal("expected a joined error")
	}
	if !errors.Is(err, errA) {
		t.Errorf("expected errA in joined error")
	}
	if !errors.Is(err, errC) {
		t.Errorf("expected errC in joined error")
	}
	if int(r.progressCalls.Load()) != 3 {
		t.Fatalf("all 3 items should have been processed, got %d progress calls", r.progressCalls.Load())
	}
}

func TestRunItems_Parallel_AllSucceed(t *testing.T) {
	r := &stubReporterForItems{}
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}
	var count atomic.Int32
	err := registry.RunItems(context.Background(), r, items, func(_ context.Context, n int) error {
		count.Add(1)
		return nil
	}, registry.RunItemsOptions{Concurrency: 4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int(count.Load()) != 20 {
		t.Fatalf("expected 20 items, got %d", count.Load())
	}
}

func TestRunItems_Parallel_FailFast_CancelsRemaining(t *testing.T) {
	r := &stubReporterForItems{}
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	errBoom := errors.New("boom")
	var count atomic.Int32
	err := registry.RunItems(context.Background(), r, items, func(ctx context.Context, n int) error {
		count.Add(1)
		if n == 0 {
			return errBoom
		}
		// Simulate slow work — will be cancelled by ctx.Done()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			return nil
		}
	}, registry.RunItemsOptions{Concurrency: 4, ErrMode: registry.ErrModeFail})
	if err == nil {
		t.Fatal("expected error")
	}
	if count.Load() >= 100 {
		t.Fatalf("expected fewer than 100 items to run; got %d", count.Load())
	}
}

func TestRunItems_PerItemTimeout(t *testing.T) {
	r := &stubReporterForItems{}
	items := []int{1}
	err := registry.RunItems(context.Background(), r, items, func(ctx context.Context, n int) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return nil
		}
	}, registry.RunItemsOptions{PerItemTimeout: 10 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestRunItems_ContextCancellation(t *testing.T) {
	r := &stubReporterForItems{}
	ctx, cancel := context.WithCancel(context.Background())
	items := make([]int, 100)
	var count atomic.Int32
	cancel() // pre-cancel
	err := registry.RunItems(ctx, r, items, func(_ context.Context, _ int) error {
		count.Add(1)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if count.Load() > 0 {
		t.Fatalf("no items should have run with a pre-cancelled ctx, got %d", count.Load())
	}
}

func TestRunItems_EmptyItems(t *testing.T) {
	r := &stubReporterForItems{}
	err := registry.RunItems(context.Background(), r, []string{}, func(_ context.Context, _ string) error {
		return errors.New("should not be called")
	})
	if err != nil {
		t.Fatalf("unexpected error for empty slice: %v", err)
	}
}

func TestRunItems_CustomLabel(t *testing.T) {
	r := &stubReporterForItems{}
	items := []string{"a", "b"}
	_ = registry.RunItems(context.Background(), r, items, func(_ context.Context, _ string) error {
		return nil
	}, registry.RunItemsOptions{
		Label: func(i, total int) string { return fmt.Sprintf("processing %d of %d", i+1, total) },
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.currentItems) != 2 {
		t.Fatalf("expected 2 SetCurrentItem calls, got %d", len(r.currentItems))
	}
	if r.currentItems[0] != "processing 1 of 2" {
		t.Errorf("unexpected label: %q", r.currentItems[0])
	}
}
