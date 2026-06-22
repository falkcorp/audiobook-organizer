// file: internal/operations/registry/run_items.go
// version: 1.0.0
// guid: a2b3c4d5-e6f7-8901-abcd-ef2345678901
// last-edited: 2026-06-22

package registry

// Work-item execution contract — see docs/specs/2026-06-22-work-item-contract.md
//
// Generic methods cannot be declared on interfaces in Go, so RunItems is a
// package-level generic function that accepts a Reporter rather than a method.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrMode controls error collection behaviour in RunItems.
type ErrMode int

const (
	// ErrModeFail cancels remaining items on the first error (default).
	ErrModeFail ErrMode = iota
	// ErrModeCollect runs all items and returns a joined error of all failures.
	ErrModeCollect
)

// RunItemsOptions configures a RunItems call. All fields are optional.
type RunItemsOptions struct {
	// Concurrency is the worker-pool size. 0 or 1 = sequential (default).
	Concurrency int

	// PerItemTimeout wraps each item's context with this deadline when > 0.
	PerItemTimeout time.Duration

	// ErrMode controls whether the first error cancels remaining items
	// (ErrModeFail, default) or all items run and errors are joined
	// (ErrModeCollect).
	ErrMode ErrMode

	// Label returns the SetCurrentItem / UpdateProgress label for item i of
	// total. Defaults to "item <i+1>/<total>".
	Label func(i, total int) string
}

// RunItems fans out fn over items, managing:
//   - ctx.Done() polling between items
//   - reporter.SetCurrentItem label per item
//   - reporter.UpdateProgress after each item
//   - per-item timeout (RunItemsOptions.PerItemTimeout)
//   - worker-pool concurrency (RunItemsOptions.Concurrency)
//   - error semantics (ErrModeFail or ErrModeCollect)
//
// Returns nil when all items succeed.
// Returns ctx.Err() on context cancellation.
// Returns the first item error on ErrModeFail, or errors.Join on ErrModeCollect.
func RunItems[T any](ctx context.Context, r Reporter, items []T, fn func(ctx context.Context, item T) error, opts ...RunItemsOptions) error {
	var opt RunItemsOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.Concurrency < 1 {
		opt.Concurrency = 1
	}
	total := len(items)
	if total == 0 {
		return nil
	}

	lbl := opt.Label
	if lbl == nil {
		lbl = func(i, total int) string { return fmt.Sprintf("item %d/%d", i+1, total) }
	}

	runOne := func(ctx context.Context, i int, item T) error {
		itemCtx := ctx
		if opt.PerItemTimeout > 0 {
			var cancel context.CancelFunc
			itemCtx, cancel = context.WithTimeout(ctx, opt.PerItemTimeout)
			defer cancel()
		}
		l := lbl(i, total)
		r.SetCurrentItem(l)
		err := fn(itemCtx, item)
		_ = r.UpdateProgress(i+1, total, l)
		return err
	}

	if opt.Concurrency == 1 {
		return runItemsSeq(ctx, items, runOne, opt.ErrMode)
	}
	return runItemsPar(ctx, items, runOne, opt)
}

// runItemsSeq runs items sequentially.
func runItemsSeq[T any](ctx context.Context, items []T, runOne func(context.Context, int, T) error, mode ErrMode) error {
	var errs []error
	for i, item := range items {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := runOne(ctx, i, item); err != nil {
			if mode == ErrModeCollect {
				errs = append(errs, err)
				continue
			}
			return err
		}
	}
	return errors.Join(errs...)
}

// runItemsPar runs items with a worker pool of size opt.Concurrency.
func runItemsPar[T any](ctx context.Context, items []T, runOne func(context.Context, int, T) error, opt RunItemsOptions) error {
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, opt.Concurrency)
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup

	for i, item := range items {
		// Check for cancellation before acquiring a worker slot.
		select {
		case <-cancelCtx.Done():
			wg.Wait()
			if len(errs) > 0 {
				return errors.Join(errs...)
			}
			return ctx.Err()
		case sem <- struct{}{}:
		}

		wg.Add(1)
		i, item := i, item
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := runOne(cancelCtx, i, item); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				if opt.ErrMode == ErrModeFail {
					cancel()
				}
			}
		}()
	}

	wg.Wait()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return ctx.Err()
}
