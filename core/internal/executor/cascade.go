// file: core/internal/executor/cascade.go
// version: 0.1.0
// guid: 3f7a91c2-5d0e-4b86-a1c4-9e2d7b6053f8
// last-edited: 2026-11-22

package executor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
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

// The cascade's reach is the canceling caller's own: cancelDescendants skips a
// descendant the caller may not cancel (mayCancel, the rule a direct cancel
// applies) and the runs beneath it. The dispatch guard and the boot pass apply NO
// such check yet: they hold only the parent's stored cancel event, which carries
// the canceling Actor's identity and no rbac.Checker, and a checker cannot be
// rebuilt from an identity without a resolver the executor does not have
// (Options.Principal resolves from a live request context only). Until the lead
// rules on where that verdict comes from, a skipped child that is still queued is
// ended by cancelOrphan at its next dispatch, and one still unfinished at a
// restart is ended by orphansAtBoot.

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

// cancelDescendants stops every unfinished run under root that the canceling
// caller may cancel, breadth first: one read of the unfinished runs (the state
// index, bounded by the live backlog, not the history) builds the
// parent-to-children map, then each child is stopped with stopOne, sequentially,
// so the cascade spawns no goroutines and walks each run once. A child that went
// terminal meanwhile is not an error. A child of a disabled module is left
// untouched (read-only while held, 5T.3.11): the dispatch guard and the boot pass
// end it when its module is enabled again. A failure to stop one child does not
// skip the others; the errors are joined, the caller's cancel reports them, and a
// repeat cancel retries (stop).
//
// who is the caller's checker (nil: a kernel-internal cancel, which no checker
// limits). A descendant who may not cancel it, by the very rule a direct cancel
// of that run applies (mayCancel: the run's own actor, core:tasks.manage, the
// kind's Perms.Cancel), is skipped: it keeps running, as it did before any
// cascade existed, and the runs beneath it are not walked, because the caller
// could not have reached them through it. Each skip is written to the parent's
// run log and handed to the cancel's CancelReport (the HTTP route records it in
// the core.task.cancel audit record). The cancel still succeeds for the run and
// every descendant the caller may cancel.
func (e *executor) cancelDescendants(ctx context.Context, root tasks.RunID, by tasks.Actor, who rbac.Checker) error {
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
	var skipped []skippedRun
	seen := map[tasks.RunID]bool{root: true}
	for queue := []tasks.RunID{root}; len(queue) > 0; queue = queue[1:] {
		parent := queue[0]
		for _, c := range kids[parent] {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if who != nil && !e.mayCancel(c, by, who) {
				skipped = append(skipped, skippedRun{parent: parent, id: c.ID, kind: c.Kind})
				continue // not queued: its own descendants are out of reach too
			}
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
	if len(skipped) > 0 {
		if err := e.recordSkips(ctx, root, by, skipped); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// skippedRun is a descendant a cancel left running because its caller may not
// cancel it.
type skippedRun struct {
	parent tasks.RunID // the run whose cancel reached it
	id     tasks.RunID
	kind   tasks.Kind
}

// recordSkips writes one log line per skipped run to the canceled run's log,
// naming the skipped run's id and kind, and hands the ids to the cancel's
// CancelReport. The lines take the top of the attempt's log page (below
// stageNotifyFailed's), where the attempt's own lines, which count up from 1,
// cannot collide with them. The report is filled first, so an audit record never
// misses a skip because the log write failed.
func (e *executor) recordSkips(ctx context.Context, root tasks.RunID, by tasks.Actor, skipped []skippedRun) error {
	if rep, ok := ctx.Value(cancelReportKey{}).(*CancelReport); ok {
		for _, s := range skipped {
			rep.add(s.id)
		}
	}
	rec, err := e.runs.Get(ctx, root)
	if err != nil {
		return fmt.Errorf("executor: record the runs the cancel of %s left running: %w", root, err)
	}
	now := e.now()
	err = e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
		for i, s := range skipped {
			line := tasks.LogLine{At: now, Level: "warn", Message: fmt.Sprintf(
				"the cancel did not reach run %s (%s): %s may not cancel it, so it keeps running", s.id, s.kind, actorName(by))}
			seq := uint64(rec.Attempt)<<32 | (math.MaxUint32 - 1 - uint64(i))
			if err := e.runs.AppendLog(ctx, b, root, seq, line); err != nil {
				return err
			}
			e.emitLog(ctx, root, line, seq)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("executor: record the runs the cancel of %s left running: %w", root, err)
	}
	for _, s := range skipped {
		e.log.Warn("cancel did not reach a descendant its caller may not cancel", "run", string(s.id), "kind", string(s.kind), "parent", string(s.parent), "root", string(root))
	}
	return nil
}

// actorName is an actor as a log line names it.
func actorName(a tasks.Actor) string {
	switch {
	case a.UserID != "":
		return "user " + a.UserID
	case a.System != "":
		return "system actor " + a.System
	default:
		return "the caller"
	}
}

// CancelReport collects what one cancel's cascade left running: the descendants
// its caller may not cancel. A caller that wants the list (the HTTP route, for the
// core.task.cancel audit record) puts one on the context it passes to
// tasks.Client.Cancel with WithCancelReport and reads it after Cancel returns;
// tasks.Client's signature has no room for it.
type CancelReport struct {
	mu      sync.Mutex
	skipped []tasks.RunID
}

type cancelReportKey struct{}

// WithCancelReport returns ctx carrying a fresh CancelReport.
func WithCancelReport(ctx context.Context) (context.Context, *CancelReport) {
	r := &CancelReport{}
	return context.WithValue(ctx, cancelReportKey{}, r), r
}

func (r *CancelReport) add(id tasks.RunID) {
	r.mu.Lock()
	r.skipped = append(r.skipped, id)
	r.mu.Unlock()
}

// Skipped is the descendants the cancel left running because its caller may not
// cancel them, in the order the cascade met them. A repeat of the cascade (a retry
// after a store failure) may list a run twice.
func (r *CancelReport) Skipped() []tasks.RunID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tasks.RunID(nil), r.skipped...)
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
	if err := e.stop(ctx, rec.ID, tasks.Actor{}, nil, reasonCancel, parentCancelCause(p.ID)); err != nil && !errors.Is(err, tasks.ErrRunNotFound) && ctx.Err() == nil {
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
