// file: core/internal/bleveindex/reconcile.go
// version: 1.0.2
// guid: 7d5cb0b4-ce7f-4ece-bf97-ab406d9fbee5
// last-edited: 2026-11-11

package bleveindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/mediacore/storage/index"
)

// The reconciler (05-storage §5S.3.8) drains every open index's durable dirty
// set. It is an in-process loop shorter than the task scheduler's one-minute
// poll, so it is not a tasks.Schedule: it wakes every
// core.index.reconcile.interval and after a MarkDirty nudge, and applies marks
// with a bounded pool of core.index.reconcile.workers. Run and Stop are the
// shape the kernel's other loops have (scheduler.Run, scheduler.Stop); the
// kernel starts it after module hooks and stops it before them (06 §6.5.2),
// which is F7-09's wiring.

// Run reconciles until Stop or ctx ends. It returns nil on either.
func (e *Engine) Run(ctx context.Context) error {
	e.runMu.Lock()
	if e.stopped || e.running {
		e.runMu.Unlock()
		return errors.New("bleveindex: Run after Stop, or twice")
	}
	e.running = true
	e.runMu.Unlock()
	defer close(e.done)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-e.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	timer := time.NewTimer(e.interval())
	defer timer.Stop()
	for {
		if err := e.Reconcile(ctx); err != nil && ctx.Err() == nil {
			e.log.Error("index reconcile pass failed", "err", err)
		}
		if e.yielded.Swap(false) {
			// An index used up its pass budget with marks left: take the next pass
			// now rather than after the interval.
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-e.wake:
		case <-timer.C:
		}
		timer.Reset(e.interval())
	}
}

// Stop ends Run and waits for it to return, up to ctx. A Stop before Run, or a
// second Stop, returns at once; a Run after Stop refuses to start.
func (e *Engine) Stop(ctx context.Context) error {
	e.runMu.Lock()
	if e.stopped {
		e.runMu.Unlock()
		return nil
	}
	e.stopped = true
	running := e.running
	e.runMu.Unlock()
	close(e.stop)
	if !running {
		return nil
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("bleveindex: stop: %w", context.Cause(ctx))
	}
}

// Reconcile runs one pass over every open index and returns the errors of the
// passes that failed. Run calls it; a test or a rebuild's tail may call it
// directly.
func (e *Engine) Reconcile(ctx context.Context) error {
	e.passes.Add(1)
	var errs []error
	for _, i := range e.snapshot() {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := e.drain(ctx, i); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", i.module, i.name, err))
		}
	}
	return errors.Join(errs...)
}

// drain applies i's pending marks, page by page. It is skipped, not queued,
// while a rebuild holds the index (the writer lock), and for a Degraded or closed one.
func (e *Engine) drain(ctx context.Context, i *idx) error {
	if i.State() == index.Degraded {
		return nil
	}
	if !i.op.TryLock() {
		return nil
	}
	defer i.op.Unlock()
	i.mu.RLock()
	closed := i.closed
	i.mu.RUnlock()
	if closed {
		return nil
	}
	started := time.Now()
	budget := e.passBudget()
	// A pass resumes where the previous one yielded (i.resumeAfter, kept in
	// memory, touched only under the writer lock): marks that fail stay at the
	// head of the set, so a pass that restarted from the first mark every time
	// would spend its budget on the same poison head and never reach the healthy
	// marks behind it.
	start := i.resumeAfter
	after := start
	now := e.clock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		marks, err := i.dirty.PendingAfter(ctx, after, pageSize)
		if err != nil {
			return err
		}
		if len(marks) == 0 {
			break
		}
		if after != "" && bytes.Compare([]byte(marks[0].ID), []byte(after)) <= 0 {
			// PendingAfter starts strictly after the cursor; a first mark at or
			// before it means the cursor does not advance, and looping would spin.
			return fmt.Errorf("dirty set cursor did not advance past %q (first mark %q)", after, marks[0].ID)
		}
		i.dirtySince.CompareAndSwap(0, time.Now().UnixNano())
		last := marks[len(marks)-1].ID
		if due := i.due(marks, now); len(due) > 0 {
			if err := e.applyPage(ctx, i, due); err != nil {
				return err
			}
		}
		after = last
		if len(marks) < pageSize {
			break
		}
		if time.Since(started) >= budget {
			// One index with a huge backlog must not starve the others: yield after
			// at least one page, remember where we were, and let the loop take the
			// next pass at once.
			i.resumeAfter = after
			e.yielded.Store(true)
			return nil
		}
	}
	i.resumeAfter = ""
	if start != "" {
		// This pass began mid-set, so the marks before the cursor (the ones that
		// failed, and the ones that arrived behind it) were not looked at: take
		// the next pass at once, from the head.
		e.yielded.Store(true)
		return nil
	}
	rest, err := i.dirty.Pending(ctx, 1)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return nil // marks arrived behind the cursor, failed, or are backing off: the next pass takes them
	}
	i.dirtySince.Store(0)
	i.forgetFailures()
	if i.State() == index.Rebuilding && i.rebuilt.Load() {
		return i.markReady()
	}
	return nil
}

// applyPage applies marks with a bounded pool. The marks of one page are
// distinct ids, hence distinct dirty rows and distinct bleve documents, and
// each worker takes a disjoint slice of them, so no two workers ever touch the
// same row or document; the index's one writer at a time is guaranteed by
// the writer lock, which drain holds.
func (e *Engine) applyPage(ctx context.Context, i *idx, marks []index.Mark) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(e.workers())
	chunk := e.chunk()
	for lo := 0; lo < len(marks); lo += chunk {
		part := marks[lo:min(lo+chunk, len(marks))]
		g.Go(func() error { return i.applyMarks(gctx, part) })
	}
	return g.Wait()
}

// applyMarks indexes one slice of marks as one bleve batch and then clears
// them. The order is the invariant: the mark's token was read BEFORE the
// document is loaded, the document is indexed BEFORE the mark is cleared, and
// the clear is conditional on the token. An entity written again after the
// token was read re-marked it in the same batch as the write, so the clear
// finds another token and leaves the id pending; a mark is never removed
// unless an index state at least as new as its write was applied.
func (i *idx) applyMarks(ctx context.Context, marks []index.Mark) error {
	entries := make([]entry, 0, len(marks))
	kept := make([]index.Mark, 0, len(marks))
	for _, m := range marks {
		doc, found, err := i.loadDoc(ctx, m.ID)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			i.failures.Add(1)
			i.recordFailure(m.ID)
			i.log.Warn("index document failed to load; its mark stays pending and is retried with backoff",
				"module", i.module, "index", i.name, "id", m.ID, "err", err)
			continue
		}
		e := entry{id: m.ID}
		if found {
			if doc == nil {
				doc = index.Doc{}
			}
			e.doc = doc
		}
		entries = append(entries, e)
		kept = append(kept, m)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := i.applyBatch(entries); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// One document the index refuses must not keep its neighbours out: apply
		// the chunk one document at a time, leave the refused ones pending (counted
		// and logged) and clear the rest. If every document is refused the fault is
		// the index's, not a poison mark's, and the pass reports it.
		kept, err = i.applyEach(ctx, entries, kept, err)
		if err != nil {
			return err
		}
	}
	for _, m := range kept {
		if _, err := i.dirty.Clear(ctx, m); err != nil {
			return err
		}
		i.recordSuccess(m.ID)
	}
	return nil
}

// applyEach is applyMarks' fallback for a chunk whose batch failed: it applies
// each entry alone and returns the marks of those that went in. It returns
// batchErr when none did.
func (i *idx) applyEach(ctx context.Context, entries []entry, marks []index.Mark, batchErr error) ([]index.Mark, error) {
	var ok []index.Mark
	var refused []string
	for n, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := i.applyBatch([]entry{e}); err != nil {
			i.failures.Add(1)
			refused = append(refused, e.id)
			i.log.Warn("index refused a document; its mark stays pending and is retried with backoff",
				"module", i.module, "index", i.name, "id", e.id, "err", err)
			continue
		}
		ok = append(ok, marks[n])
	}
	if len(ok) == 0 {
		return nil, batchErr // the index is failing, not one document: no backoff for any of them
	}
	for _, id := range refused {
		i.recordFailure(id)
	}
	return ok, nil
}
