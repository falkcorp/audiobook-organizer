// file: internal/operations/registry/run_items.go
// version: 1.5.1
// guid: a2b3c4d5-e6f7-8901-abcd-ef2345678901
// last-edited: 2026-08-30

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
	"sync/atomic"
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

	// CheckpointFn, if non-nil, is called after each item completes
	// successfully in SEQUENTIAL mode (Concurrency <= 1). The caller uses a
	// closure to capture the current item's ID or any other state needed to
	// build the checkpoint. Errors are logged but do not fail the item.
	//
	// For CONCURRENT mode use CheckpointStateFn below, which is serialized and
	// carries a watermark that is correct under out-of-order completion.
	CheckpointFn func(ctx context.Context) error

	// ResumeFrom skips the first N items because a previous run already
	// completed them. It slices items[ResumeFrom:] and shifts ProgressOffset by
	// the same amount, so the progress bar keeps reporting absolute position in
	// the original collection rather than restarting at 1.
	//
	// Pair it with CheckpointStateFn: the watermark handed to that callback is
	// exactly the value to store and feed back here on the next run.
	ResumeFrom int

	// CheckpointEvery throttles CheckpointStateFn to once per N completed
	// items. 0 means every item. Ignored when CheckpointStateFn is nil.
	CheckpointEvery int

	// CheckpointStateFn is the concurrent-safe checkpoint hook. It receives the
	// ABSOLUTE contiguous-completion watermark: the number of items from the
	// start of the original collection that are all finished, including any
	// skipped by ResumeFrom. Calls are serialized, so the callback may write
	// the shared state blob without further locking.
	//
	// The watermark is the unbroken completed PREFIX, not the completed count.
	// With workers finishing out of order, items 0,1,2,5 done means the
	// watermark is 3 — item 5 is re-run on resume. That is deliberate: every
	// item below the watermark is provably complete no matter what order the
	// pool finished in, which is the property that made concurrent
	// checkpointing unsafe before. Item-level idempotence covers the re-run.
	//
	// A FAILED item never advances the watermark, so a resume retries it rather
	// than skipping silently over it.
	CheckpointStateFn func(ctx context.Context, watermark int) error

	// ProgressOffset shifts UpdateProgress current values by this amount.
	// Use when iterating a sub-slice of a larger set so the progress bar
	// shows position within the full set rather than within the slice.
	// Example: startIdx=1000, total=5000 → item 0 reports 1001/5000.
	ProgressOffset int

	// ProgressTotal overrides the denominator passed to UpdateProgress.
	// Defaults to len(items) when zero. Set to the full collection size
	// when iterating a sub-slice (pairs with ProgressOffset).
	ProgressTotal int
}

// completionTracker records which item indices have finished successfully and
// exposes the contiguous completed prefix ("watermark").
//
// It exists because a completion COUNT is not a resume point under concurrency.
// Workers finish out of order, so "4 done" may mean indices {0,1,2,5}; resuming
// at 4 would skip 3 and 4 entirely. The watermark answers the only question a
// resume can safely act on: how many items from the start are ALL finished.
type completionTracker struct {
	mu        sync.Mutex
	done      []bool
	watermark int
}

func newCompletionTracker(n int) *completionTracker {
	return &completionTracker{done: make([]bool, n)}
}

// complete marks index i finished and returns the new watermark. Advancing is
// O(1) amortised: the scan resumes from the previous watermark and each index is
// stepped over at most once across the whole run.
func (c *completionTracker) complete(i int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= 0 && i < len(c.done) {
		c.done[i] = true
	}
	for c.watermark < len(c.done) && c.done[c.watermark] {
		c.watermark++
	}
	return c.watermark
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

	// Resume: drop the already-completed prefix. ProgressTotal is pinned to the
	// ORIGINAL length first, so a resumed run still reports "4200/14000" rather
	// than restarting the denominator at the size of the remainder — a resumed
	// op that appears to start over is exactly the confusion this whole change
	// exists to remove.
	resumeFrom := opt.ResumeFrom
	if resumeFrom < 0 {
		resumeFrom = 0
	}
	if resumeFrom > 0 {
		if opt.ProgressTotal == 0 {
			opt.ProgressTotal = total
		}
		if resumeFrom >= total {
			// Everything was already done in the previous run. Report the work
			// as fully complete so the bar does not sit at a stale value.
			_ = r.UpdateProgress(opt.ProgressTotal, opt.ProgressTotal, "resumed: all items already complete")
			return nil
		}
		opt.ProgressOffset += resumeFrom
		items = items[resumeFrom:]
		total = len(items)
	}

	progTotal := total
	if opt.ProgressTotal > 0 {
		progTotal = opt.ProgressTotal
	}

	// Concurrent-safe checkpointing. tracker is nil when the caller did not ask
	// for it, so the zero-value options path allocates nothing.
	var tracker *completionTracker
	var ckptMu sync.Mutex
	lastCkpt := 0
	if opt.CheckpointStateFn != nil {
		tracker = newCompletionTracker(total)
	}
	maybeCheckpoint := func(ctx context.Context, i int) {
		if tracker == nil {
			return
		}
		mark := tracker.complete(i)
		every := opt.CheckpointEvery
		if every < 1 {
			every = 1
		}
		ckptMu.Lock()
		defer ckptMu.Unlock()
		// Guard on the watermark, not the completion count: with a gap open the
		// watermark stalls, and checkpointing the same value repeatedly is
		// pointless write amplification.
		if mark <= lastCkpt || mark-lastCkpt < every && mark < total {
			return
		}
		lastCkpt = mark
		_ = opt.CheckpointStateFn(ctx, resumeFrom+mark)
	}

	lbl := opt.Label
	if lbl == nil {
		lbl = func(i, total int) string { return fmt.Sprintf("item %d/%d", opt.ProgressOffset+i+1, total) }
	}

	// P-2: report a monotonic completion count, not the item index. In parallel
	// mode items finish out of order, so reporting ProgressOffset+i+1 made the
	// progress bar jump backwards (item 5 finishing before item 2 reported 6
	// then 3). An atomic counter incremented as each item completes is monotonic
	// in both sequential and parallel modes. The label still carries the item's
	// own index for identity.
	var completed atomic.Int64
	runOne := func(ctx context.Context, i int, item T) error {
		itemCtx := ctx
		if opt.PerItemTimeout > 0 {
			var cancel context.CancelFunc
			itemCtx, cancel = context.WithTimeout(ctx, opt.PerItemTimeout)
			defer cancel()
		}
		r.SetCurrentItem(lbl(i, progTotal))
		err := fn(itemCtx, item)
		done := int(completed.Add(1))
		// Re-render the label AFTER fn rather than reusing the pre-work string.
		// Labels commonly close over running tallies, and with Concurrency = N
		// all N workers snapshot their label at dispatch — before any of them
		// has finished — so a reused string reports state from before its own
		// item ran. Measured on chapters-backfill: a 12-book apply that wrote
		// all 12 printed "persist=0" on ten lines and never exceeded 2, which
		// reads as total failure for the hours a whole-library run takes.
		// SetCurrentItem above still gets the pre-work label: it names the item
		// being STARTED, which is what it is for.
		_ = r.UpdateProgress(opt.ProgressOffset+done, progTotal, lbl(i, progTotal))
		// Only a SUCCESS may advance the watermark. Marking a failed item done
		// would let a resume skip straight past it, turning one failure into
		// permanently unprocessed work that nothing ever revisits.
		if err == nil {
			maybeCheckpoint(ctx, i)
		}
		return err
	}

	if opt.Concurrency == 1 {
		return runItemsSeq(ctx, items, runOne, opt)
	}
	return runItemsPar(ctx, items, runOne, opt)
}

// runItemsSeq runs items sequentially, calling opt.CheckpointFn after each
// successful item when set.
func runItemsSeq[T any](ctx context.Context, items []T, runOne func(context.Context, int, T) error, opt RunItemsOptions) error {
	var errs []error
	for i, item := range items {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := runOne(ctx, i, item); err != nil {
			if opt.ErrMode == ErrModeCollect {
				errs = append(errs, err)
				continue
			}
			return err
		}
		if opt.CheckpointFn != nil {
			_ = opt.CheckpointFn(ctx) // errors are non-fatal; op continues
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

		i, item := i, item
		wg.Go(func() {
			defer func() { <-sem }()
			if err := runOne(cancelCtx, i, item); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				if opt.ErrMode == ErrModeFail {
					cancel()
				}
			}
		})
	}

	wg.Wait()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return ctx.Err()
}
