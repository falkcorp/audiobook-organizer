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
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// A cancel of a run takes the runs it enqueued with it (F5-18 C6). HEAD's cancel
// of a batch job stopped the work the job had started; the kernel's parent run
// waits on its children (rc.Await) and, before this, a cancel ended only the
// parent: the child in flight kept writing until its own timeout. The cascade is
// the executor's, not an RC call, so it also covers a parent that never calls
// anything, and it reaches only the runs whose Parent is the canceled run, or
// the Parent of one of those. The cascade takes no run id from its caller, but a
// run's Parent is not proof that the parent enqueued it: rc.Enqueue refuses a
// Parent other than the enqueuing run, yet Client.Start takes a caller-supplied
// Parent (and, with it, whichever Actor the caller may start as). What keeps a
// caller from hanging a run under someone else's run, to be ended with it, is the
// actor check in reachCovers: a descendant is reached only when it carries the
// canceled run's own Actor.
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

// The cascade's reach is the canceling caller's own. Cancel evaluates mayCancel
// (the rule a direct cancel of a run applies) once per registered kind, against
// the canceled run's actor, and stores the result on the run (tasks.CancelReach,
// written in the same move as the cancel; All for a kernel-internal cancel). Every
// run the cascade ends carries a copy, so the walk, the dispatch guard and the
// boot pass all read one stored verdict and cannot diverge, and a child enqueued
// after the plan is judged by the kind it is. A run is out of reach when the
// verdict is missing (a record from before the field, a cancel that stored none),
// its kind is not in the set, or its Actor is not the one the verdict was made
// for: the live walk skips it and the runs beneath it, and the guard and the boot
// pass leave it alone, as before any cascade existed. They never fall back to
// "allowed".
//
// A live cancel's cascade is limited to the stored verdict narrowed by that
// caller's own (narrowReach): the first cancel stores its caller's verdict, and a
// repeat or racing cancel by someone with less authority ends no more than that
// caller could, while one with more is bounded by what the first stored. The guard
// and the boot pass have no caller, so they apply the stored verdict as it is.

// reachCovers reports whether a stored verdict lets the cascade end c.
func reachCovers(r *tasks.CancelReach, c tasks.RunRecord) bool {
	if r == nil || c.Actor != r.Actor {
		return false
	}
	if r.All {
		return true
	}
	_, found := slices.BinarySearch(r.Kinds, c.Kind)
	return found
}

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

// cascadeWorkers bounds the goroutines stopping the runs of one level of the
// cascade (the repo's concurrency rule: a bounded pool for per-run store work, not
// a plain loop over a tree that may be a whole batch's children).
const cascadeWorkers = 8

// cancelDescendants stops every unfinished run under root that the canceling
// caller may cancel, breadth first, one level at a time. For each level it reads
// the unfinished runs afresh (the state index, bounded by the live backlog, not the
// history) and takes those whose Parent is a run of the level, after every run of
// the level has been stopped: a child that a run enqueued before its own stop
// committed is therefore seen, where a single read taken before the walk began
// would miss it and leave it running until the next restart (the dispatch guard
// ends one that is enqueued later, queued, but not one that already runs). The
// runs of a level are stopped by a pool of at most cascadeWorkers goroutines.
// Within a level the ids are distinct and each belongs to one parent, and the
// coordinator marks them seen before any worker starts, so no two workers touch
// the same run; the move that stops a run is a compare-and-swap in any case. A
// child that went terminal meanwhile is not an error. A child of a disabled module
// is left untouched (read-only while held, 5T.3.11), whether the caller could have
// canceled it or not, and is not reported as skipped: the dispatch guard and the
// boot pass end it when its module is enabled again, if the verdict covers it. A
// failure to stop one child does not skip the others; the errors are joined, the
// caller's cancel reports them, and a repeat cancel retries (stop).
//
// limit is what the walk may end (the stored verdict narrowed by the caller's, see
// stop); stamp is the verdict stored with the cancel, which every run the walk ends
// carries a copy of, so the guard and the boot pass judge a child that appears
// beneath it by the cancel's authority, not by the repeat's. A descendant limit does
// not cover, which is one the caller may not cancel by the very rule a direct cancel
// of that run applies (mayCancel: the run's own actor, core:tasks.manage, the kind's
// Perms.Cancel), is skipped: it keeps running, as it did before any cascade existed,
// and the runs beneath it are not walked, because the caller could not have reached
// them through it. The skips are handed to the cancel's CancelReport (the HTTP route
// records them in the core.task.cancel audit record) and counted in the parent's run
// log. The cancel still succeeds for the run and every descendant the caller may
// cancel.
func (e *executor) cancelDescendants(ctx context.Context, root tasks.RunID, by tasks.Actor, limit, stamp *tasks.CancelReach) error {
	var (
		mu      sync.Mutex // guards errs, which the workers add to (skipped is the coordinator's alone)
		errs    []error
		skipped []skippedRun
	)
	seen := map[tasks.RunID]bool{root: true}
	level := []tasks.RunID{root} // runs whose own stop has committed: their children are read next
	for len(level) > 0 {
		kids, err := e.childrenOf(ctx, level)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		var (
			g    errgroup.Group
			next []tasks.RunID
		)
		g.SetLimit(cascadeWorkers)
		for _, k := range kids {
			c, parent := k.rec, k.parent
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			covered := reachCovers(limit, c)
			if e.held(c.Kind) {
				if covered {
					next = append(next, c.ID)
				}
				continue // left for its module, never a skip
			}
			if !covered {
				skipped = append(skipped, skippedRun{parent: parent, id: c.ID, kind: c.Kind})
				continue // not queued: its own descendants are out of reach too
			}
			next = append(next, c.ID)
			g.Go(func() error {
				err := e.stopOne(ctx, c.ID, by, stamp, reasonCancel, parentCancelCause(parent))
				if err != nil && !errors.Is(err, tasks.ErrRunNotFound) {
					mu.Lock()
					errs = append(errs, fmt.Errorf("executor: cancel %s, a child of %s: %w", c.ID, parent, err))
					mu.Unlock()
					return nil
				}
				if err == nil {
					e.log.Info("child run canceled with its parent", "run", string(c.ID), "kind", string(c.Kind), "parent", string(parent))
				}
				return nil
			})
		}
		_ = g.Wait() // every job returns nil: its error is in errs
		level = next
	}
	if len(skipped) > 0 {
		if err := e.recordSkips(ctx, root, skipped); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// kid is an unfinished run and the run it hangs from.
type kid struct {
	parent tasks.RunID
	rec    tasks.RunRecord
}

// childrenOf reads the unfinished runs now and returns those whose Parent is one of
// parents, in the store's order. An undecodable record is hidden (5T.5) and counted.
func (e *executor) childrenOf(ctx context.Context, parents []tasks.RunID) ([]kid, error) {
	want := make(map[tasks.RunID]bool, len(parents))
	for _, p := range parents {
		want[p] = true
	}
	var out []kid
	for rec, err := range e.runs.Unfinished(ctx) {
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			e.ins.undecodable("run")
			continue // hidden (5T.5)
		}
		if want[rec.Parent] {
			out = append(out, kid{parent: rec.Parent, rec: rec})
		}
	}
	return out, nil
}

// skippedRun is a descendant a cancel left running because its caller may not
// cancel it.
type skippedRun struct {
	parent tasks.RunID // the run whose cancel reached it
	id     tasks.RunID
	kind   tasks.Kind
}

// recordSkips hands the skipped runs' ids to the cancel's CancelReport (the HTTP
// route names them in its audit record) and writes one line to the canceled run's
// log saying how many runs the cancel left running, and ids to slog. The log line
// names no run: the run's log is readable by whoever may view the canceled run's
// kind, which is not whoever may view the skipped run's, so a child's id or kind
// there would show a run its reader cannot see (the way RunView blanks BlockedBy).
// The line takes the top of the attempt's log page (below stageNotifyFailed's),
// where the attempt's own lines, which count up from 1, cannot collide with it. A
// repeat cancel writes the same sequence again, so the log carries the latest
// count, not one line per request. The report is filled first, so a log write that
// fails does not lose a skip from the audit record.
func (e *executor) recordSkips(ctx context.Context, root tasks.RunID, skipped []skippedRun) error {
	if rep, ok := ctx.Value(cancelReportKey{}).(*CancelReport); ok {
		for _, s := range skipped {
			rep.add(s.id)
		}
	}
	for _, s := range skipped {
		e.log.Warn("cancel did not reach a descendant its caller may not cancel", "run", string(s.id), "kind", string(s.kind), "parent", string(s.parent), "root", string(root))
	}
	rec, err := e.runs.Get(ctx, root)
	if err != nil {
		return fmt.Errorf("executor: record the runs the cancel of %s left running: %w", root, err)
	}
	line := tasks.LogLine{At: e.now(), Level: "warn", Message: fmt.Sprintf(
		"the cancel did not reach %d descendant run(s): the caller may not cancel them, so they keep running", len(skipped))}
	seq := uint64(rec.Attempt)<<32 | (math.MaxUint32 - 1)
	err = e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
		if err := e.runs.AppendLog(ctx, b, root, seq, line); err != nil {
			return err
		}
		e.emitLog(ctx, root, line, seq)
		return nil
	})
	if err != nil {
		return fmt.Errorf("executor: record the runs the cancel of %s left running: %w", root, err)
	}
	return nil
}

// CancelReport collects what one cancel did beyond answering nil or an error: the
// descendants its caller may not cancel (Skipped), and whether it committed any
// stop at all (Acted). A caller that wants them (the HTTP route, for the
// core.task.cancel audit record) puts one on the context it passes to
// tasks.Client.Cancel with WithCancelReport and reads it after Cancel returns,
// whatever Cancel returned: an error that comes after the run's own stop committed
// (a child that could not be stopped, a repeat cancel's "not found" while it
// finished a cascade) still leaves an action to audit. tasks.Client's signature
// has no room for either.
type CancelReport struct {
	mu      sync.Mutex
	skipped []tasks.RunID
	acted   bool
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
	r.acted = true
	r.mu.Unlock()
}

// reportActed marks the context's CancelReport (if any): a cancel move committed.
func reportActed(ctx context.Context) {
	if r, ok := ctx.Value(cancelReportKey{}).(*CancelReport); ok {
		r.mu.Lock()
		r.acted = true
		r.mu.Unlock()
	}
}

// Skipped is the descendants the cancel left running because its caller may not
// cancel them, in the order the cascade met them. A repeat of the cascade (a retry
// after a store failure) may list a run twice.
func (r *CancelReport) Skipped() []tasks.RunID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tasks.RunID(nil), r.skipped...)
}

// Acted reports whether this cancel changed anything: it stopped the run or a
// descendant, or it left a descendant running that its caller may not cancel. A
// repeat cancel that finds the run already ended and nothing to do reports false.
func (r *CancelReport) Acted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acted
}

// cancelOrphan is the dispatcher's guard before a claim: a queued run whose
// parent was canceled is canceled itself, never started, when the parent's stored
// verdict covers it (reachCovers). A run out of reach is dispatched as on HEAD. It
// reports whether the run was an orphan (the caller skips the claim either way: a
// run whose stop failed is not started, and the next pass tries again).
func (e *executor) cancelOrphan(ctx context.Context, rec tasks.RunRecord) bool {
	if rec.Parent == "" {
		return false
	}
	p, err := e.runs.Get(ctx, rec.Parent)
	if err != nil || !canceledByCancel(p) {
		return false // a parent that is gone (pruned) or unreadable does not hold its child back
	}
	if !reachCovers(p.CancelReach, rec) {
		return false // out of the cancel's reach (or no verdict stored): dispatched as on HEAD
	}
	if err := e.stop(ctx, rec.ID, tasks.Actor{}, p.CancelReach, reasonCancel, parentCancelCause(p.ID)); err != nil && !errors.Is(err, tasks.ErrRunNotFound) && ctx.Err() == nil {
		e.log.Warn("child of a canceled run not canceled at dispatch; it is tried again next pass", "run", string(rec.ID), "parent", string(p.ID), "err", err)
	}
	return true
}

// bootOrphan is a run the boot pass ends: the ancestor whose cancel orphans it
// and the verdict of that cancel (the root's, carried down through orphans of
// orphans, never the child's own).
type bootOrphan struct {
	parent tasks.RunID
	reach  *tasks.CancelReach
}

// orphansAtBoot picks, from the unfinished runs the resume sweep read, those that
// hang from a canceled run and that its stored verdict covers: the child of a run
// a cancel ended, of one still stopping for a cancel (the sweep ends it canceled),
// or of another such orphan. A child the verdict does not cover is left alone, and
// so are the runs beneath it (nothing canceled reaches them through it). A run of
// a disabled module is never picked (the sweep leaves it untouched).
func (e *executor) orphansAtBoot(ctx context.Context, recs []tasks.RunRecord) map[tasks.RunID]bootOrphan {
	byID := make(map[tasks.RunID]tasks.RunRecord, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	via := map[tasks.RunID]bootOrphan{} // decided runs: the zero value = not an orphan
	var orphanOf func(r tasks.RunRecord) bootOrphan
	orphanOf = func(r tasks.RunRecord) bootOrphan {
		if v, ok := via[r.ID]; ok {
			return v
		}
		via[r.ID] = bootOrphan{} // a cycle (impossible: a parent exists before its child) reads as not an orphan
		var out bootOrphan
		p, unfinished := byID[r.Parent] // a parent the sweep read can pass a cancel down to its own children
		if r.Parent != "" && !unfinished {
			if got, err := e.runs.Get(ctx, r.Parent); err == nil {
				p = got
			}
		}
		switch {
		case p.ID == "":
		case canceledByCancel(p):
			out = bootOrphan{parent: p.ID, reach: p.CancelReach}
		case unfinished:
			if up := orphanOf(p); up.parent != "" {
				out = bootOrphan{parent: p.ID, reach: up.reach}
			}
		}
		if out.parent != "" && !reachCovers(out.reach, r) {
			out = bootOrphan{}
		}
		via[r.ID] = out
		return out
	}
	out := map[tasks.RunID]bootOrphan{}
	for _, r := range recs {
		if e.held(r.Kind) {
			continue
		}
		if o := orphanOf(r); o.parent != "" {
			out[r.ID] = o
		}
	}
	return out
}

// cancelAtBoot ends one orphan canceled, from the state the sweep read it in. The
// orphan keeps the verdict that ended it, so a child enqueued under it later is
// judged by the original caller's authority.
func (e *executor) cancelAtBoot(ctx context.Context, rec tasks.RunRecord, o bootOrphan) error {
	_, err := e.move(ctx, rec.ID, func(cur tasks.RunRecord) (tasks.RunRecord, tasks.RunEvent, error) {
		if cur.State != rec.State || cur.Attempt != rec.Attempt || cur.FenceEpoch != rec.FenceEpoch {
			return cur, tasks.RunEvent{}, errStale(cur, "cancel with parent")
		}
		next := cur
		next.State, next.Reason, next.Finished = tasks.StateCanceled, reasonCancel, e.now()
		next.CancelReach = o.reach
		return next, tasks.RunEvent{Event: "resume", Reason: reasonCancel}, nil
	}, nil)
	if err == nil {
		e.log.Info("child of a canceled run ended canceled at boot", "run", string(rec.ID), "kind", string(rec.Kind), "parent", string(o.parent))
	}
	return err
}
