// file: core/internal/executor/cascade.go
// version: 0.1.0
// guid: 3f7a91c2-5d0e-4b86-a1c4-9e2d7b6053f8
// last-edited: 2026-11-22

package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/tasks"
)

// A cancel of a run takes the runs it enqueued with it (F5-18 C6). HEAD's cancel
// of a batch job stopped the work the job had started; the kernel's parent run
// waits on its children (rc.Await) and, before this, a cancel ended only the
// parent: the child in flight kept writing until its own timeout. The cascade is
// the executor's, not an RC call, so it also covers a parent that never calls
// anything, and it reaches only the runs whose Parent is the canceled run, or
// the Parent of one of those: no caller can name another run, because nothing
// here takes a run id from a caller.
//
// Three places keep it true:
//
//   - cancelDescendants, at the cancel: every unfinished descendant is stopped
//     the way Cancel stops a run (its context is canceled with a cause that
//     wraps tasks.ErrCanceled, its writer refuses, the supervisor abandons it
//     after its grace). It runs after the run's own stop (see stop).
//   - cancelOrphan, at dispatch: a child enqueued while the cancel was being
//     applied (after the descendants were read) is canceled instead of claimed.
//   - orphansAtBoot, at resume: a restart between the cancel and the cascade, or
//     partway through it, leaves no child running (nothing is running after a
//     restart) and no child resumable: each is ended canceled.
//
// Only a cancel cascades. A run the watchdog stopped, one that timed out or one a
// shutdown interrupted keeps its children's lifecycle as it was; a child's
// Reason "cancel" is what the dispatch guard and the boot pass read, so a child
// canceled with its parent passes the cancel on to its own children the same way.

// canceledByCancel reports a run a cancel ended or is ending. The watchdog's
// canceled state carries Reason "watchdog", so it does not match.
func canceledByCancel(rec tasks.RunRecord) bool {
	return rec.Reason == reasonCancel && (rec.State == tasks.StateCanceled || rec.State == tasks.StateStopping)
}

// parentCancelCause is the cause a child's context is canceled with when its
// parent was canceled. It wraps tasks.ErrCanceled, so every errors.Is check on
// a cancel holds and the run ends canceled (attempt.outcome).
func parentCancelCause(parent tasks.RunID) error {
	return fmt.Errorf("%w: its parent run %s was canceled", tasks.ErrCanceled, parent)
}

// cancelDescendants stops every unfinished run under root, breadth first: one
// read of the unfinished runs (the state index, bounded by the live backlog, not
// the history) builds the parent-to-children map, then each child is stopped with
// stopOne, sequentially, so the cascade spawns no goroutines and walks each run
// once. A child that went terminal meanwhile is not an error. A child of a
// disabled module is left untouched (read-only while held, 5T.3.11): the
// dispatch guard and the boot pass end it when its module is enabled again. A
// failure to stop one child does not skip the others; the errors are joined, the
// caller's cancel reports them, and a repeat cancel retries (stop).
func (e *executor) cancelDescendants(ctx context.Context, root tasks.RunID, by tasks.Actor) error {
	kids := map[tasks.RunID][]tasks.RunRecord{}
	for rec, err := range e.runs.Unfinished(ctx) {
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.ins.undecodable("run")
			continue // hidden (5T.5)
		}
		if rec.Parent != "" {
			kids[rec.Parent] = append(kids[rec.Parent], rec)
		}
	}
	var errs []error
	seen := map[tasks.RunID]bool{root: true}
	for queue := []tasks.RunID{root}; len(queue) > 0; queue = queue[1:] {
		parent := queue[0]
		for _, c := range kids[parent] {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			queue = append(queue, c.ID)
			if e.held(c.Kind) {
				continue
			}
			err := e.stopOne(ctx, c.ID, by, reasonCancel, parentCancelCause(parent))
			if err != nil && !errors.Is(err, tasks.ErrRunNotFound) {
				errs = append(errs, fmt.Errorf("executor: cancel %s, a child of %s: %w", c.ID, parent, err))
				continue
			}
			if err == nil {
				e.log.Info("child run canceled with its parent", "run", string(c.ID), "kind", string(c.Kind), "parent", string(parent))
			}
		}
	}
	return errors.Join(errs...)
}

// cancelOrphan is the dispatcher's guard before a claim: a queued run whose
// parent was canceled is canceled itself, never started. It reports whether the
// run was an orphan (the caller skips the claim either way: a run whose stop
// failed is not started, and the next pass tries again).
func (e *executor) cancelOrphan(ctx context.Context, rec tasks.RunRecord) bool {
	if rec.Parent == "" {
		return false
	}
	p, err := e.runs.Get(ctx, rec.Parent)
	if err != nil || !canceledByCancel(p) {
		return false // a parent that is gone (pruned) or unreadable does not hold its child back
	}
	if err := e.stop(ctx, rec.ID, tasks.Actor{}, reasonCancel, parentCancelCause(p.ID)); err != nil && !errors.Is(err, tasks.ErrRunNotFound) && ctx.Err() == nil {
		e.log.Warn("child of a canceled run not canceled at dispatch; it is tried again next pass", "run", string(rec.ID), "parent", string(p.ID), "err", err)
	}
	return true
}

// orphansAtBoot picks, from the unfinished runs the resume sweep read, those that
// hang from a canceled run: the child of a run a cancel ended, of one still
// stopping for a cancel (the sweep ends it canceled), or of another such orphan.
// The value is the ancestor whose cancel orphans it. A run of a disabled module is
// never picked (the sweep leaves it untouched).
func (e *executor) orphansAtBoot(ctx context.Context, recs []tasks.RunRecord) map[tasks.RunID]tasks.RunID {
	byID := make(map[tasks.RunID]tasks.RunRecord, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	via := map[tasks.RunID]tasks.RunID{} // decided runs: "" = not an orphan
	var orphanOf func(r tasks.RunRecord) tasks.RunID
	orphanOf = func(r tasks.RunRecord) tasks.RunID {
		if v, ok := via[r.ID]; ok {
			return v
		}
		via[r.ID] = "" // a cycle (impossible: a parent exists before its child) reads as not an orphan
		var out tasks.RunID
		switch p, ok := byID[r.Parent]; {
		case r.Parent == "":
		case ok:
			if canceledByCancel(p) || orphanOf(p) != "" {
				out = p.ID
			}
		default:
			if p, err := e.runs.Get(ctx, r.Parent); err == nil && canceledByCancel(p) {
				out = p.ID
			}
		}
		via[r.ID] = out
		return out
	}
	out := map[tasks.RunID]tasks.RunID{}
	for _, r := range recs {
		if e.held(r.Kind) {
			continue
		}
		if p := orphanOf(r); p != "" {
			out[r.ID] = p
		}
	}
	return out
}

// cancelAtBoot ends one orphan canceled, from the state the sweep read it in.
func (e *executor) cancelAtBoot(ctx context.Context, rec tasks.RunRecord, parent tasks.RunID) error {
	_, err := e.move(ctx, rec.ID, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State != rec.State || cur.Attempt != rec.Attempt || cur.FenceEpoch != rec.FenceEpoch {
			return cur, tasks.RunEvent{}, errStale(cur, "cancel with parent")
		}
		next := cur
		next.State, next.Reason, next.Finished = tasks.StateCanceled, reasonCancel, e.now()
		return next, tasks.RunEvent{Event: "resume", Reason: reasonCancel}, nil
	}, nil)
	if err == nil {
		e.log.Info("child of a canceled run ended canceled at boot", "run", string(rec.ID), "kind", string(rec.Kind), "parent", string(parent))
	}
	return err
}
