// file: internal/plugins/dedup/kinds_chain.go
// version: 1.2.1
// guid: ca58ea3e-cc42-4a27-b1be-b532eb5a7b6e
// last-edited: 2026-11-19

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The chain group (F5-04): lsh-index-build, and the two definitions that run
// other definitions as followed children (run-all and label-refinement). HEAD
// follows a child by polling its operation row (childop.Follow); the native
// shells start it with rc.Enqueue and wait with rc.Await (05-tasks 5T.8.1,
// childop row), naming the child by its native books.dedup.* or books.acoustid.*
// kind (rule R3).

// ChainKindDeps is what the chain group's native kinds need from their owner.
type ChainKindDeps struct {
	// LSHReads is the read side of lsh-index-build (the index projection, the
	// already-indexed probe and the raw-fingerprint hydrate).
	LSHReads lshIndexBuildReads
	// NewLSHWriter builds lsh-index-build's writer over the run's gated writer.
	NewLSHWriter func(tasks.Writer) lshIndexBuildWriter
	// RescorePreview is dedup.run-all's read-only rescore (Engine.Rescore with
	// apply=false, which writes nothing, so it takes no writer).
	RescorePreview func(ctx context.Context) (dedupengine.RescoreResult, error)
}

// chainKinds lists the chain group's native kinds in HEAD file order.
func chainKinds(p *Plugin, d ChainKindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.lshIndexBuildNative(d, sched),
		p.runAllNative(d, sched),
		p.labelRefinementNative(d, sched),
	}
}

// lshIndexBuildNative is the native dedup.lsh-index-build kind. HEAD has no
// preview, so a native Preview run is refused.
func (p *Plugin) lshIndexBuildNative(d ChainKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(lshIndexBuildKind, sched, false)
	c.Title = "Build LSH fingerprint index"
	c.Help = "Builds the fpidx: secondary index over whole-file AcoustID fingerprints, enabling fast near-duplicate lookup without a full O(N) scan."
	// Writes found: PutLSHEntries writes the fpidx: and fpidx_meta: rows
	// (fingerprint_index) and SetLSHIndexBuilt writes the lsh_index_v1_done
	// setting (settings). It reads the BookFile projection and the raw print.
	c.Effects = tasks.Writes(res(ops.ResFingerprintIndex, ops.ResSettings)...).AlsoReads(res(ops.ResBookFiles)...)
	// HEAD Requeue: a restart runs it again from the start, and HasLSHIndex skips
	// the files an earlier attempt indexed, so FromZero loses nothing.
	c.Resume = tasks.ResumeFromZero
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.lsh-index")
	c.Timeout = 120 * time.Minute
	return tasks.Task(lshIndexBuildKind, tasks.TaskSpec[json.RawMessage, lshIndexBuildResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (lshIndexBuildResult, error) {
			if err := refuseKindModeKeys(lshIndexBuildKind, raw); err != nil {
				return lshIndexBuildResult{}, err
			}
			if err := kindLiveOnly(rc, lshIndexBuildKind); err != nil {
				return lshIndexBuildResult{}, err
			}
			if err := errNeed(lshIndexBuildKind, "Chain.LSHReads", d.LSHReads != nil); err != nil {
				return lshIndexBuildResult{}, err
			}
			if err := errNeed(lshIndexBuildKind, "Chain.NewLSHWriter", d.NewLSHWriter != nil); err != nil {
				return lshIndexBuildResult{}, err
			}
			w := d.NewLSHWriter(rc.Writer())
			if w == nil {
				return lshIndexBuildResult{}, errNeedWriter(lshIndexBuildKind)
			}
			return lshIndexBuildCore(rc.Context(), lshIndexBuildEnv{
				kindEnv: nativeEnv(rc),
				Reads:   d.LSHReads,
				Writer:  w,
				// The books whose files have no print are handed to the rescan as a
				// child run of this one (it is not awaited; HEAD did not wait). The
				// child runs in this run's mode, which is Live here.
				QueueRescan: func(_ context.Context, bookIDs []string) error {
					_, err := rc.Enqueue(kindFor("acoustid.fingerprint-rescan"), map[string]any{
						"scope":    "books",
						"book_ids": bookIDs,
					})
					return err
				},
			})
		},
	})
}

// childModeOption states the mode a child starts in, instead of inheriting this
// run's (the kernel's default for rc.Enqueue).
func childModeOption(m tasks.ModeRequest) tasks.StartOption {
	return func(r *tasks.StartRequest) { r.Mode = m }
}

// awaitRun waits for a child run. rc.Await takes no context, so the wait runs in
// its own goroutine and ctx ends it without ending the child. The goroutine
// returns when the attempt's own context ends, which a cancel does. The wait
// stamps no liveness itself: the kernel's await relays it to this run while the
// child is alive (core/internal/executor, pinned by core's
// TestAwait_ParentSurvivesChildLongerThanItsWatchdogLimit).
func awaitRun(ctx context.Context, rc *tasks.RC, id string) (tasks.Outcome, error) {
	type awaited struct {
		out tasks.Outcome
		err error
	}
	done := make(chan awaited, 1)
	go func() {
		// A panic here is outside the run body's recover and would end the
		// process: report it as the wait's error.
		defer func() {
			if r := recover(); r != nil {
				done <- awaited{err: fmt.Errorf("await child run %s panicked: %v", id, r)}
			}
		}()
		out, err := rc.Await(tasks.RunRef{ID: tasks.RunID(id)})
		done <- awaited{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-ctx.Done():
		return tasks.Outcome{}, context.Cause(ctx)
	}
}

// nativeChildDriver is the native child driver: a child is started with
// rc.Enqueue on its native kind and waited for with rc.Await. A native child has
// no operation row to read progress from, so the wait reports one frame (the
// shared driver's "waiting for" note) and nothing more.
func nativeChildDriver(rc *tasks.RC) childDriver {
	return childDriver{
		NoID: "the kernel returned no run id",
		Start: func(_ context.Context, defID string, mode childMode) (string, error) {
			// Each child starts in an explicit mode, instead of inheriting this
			// run's (the kernel's default for rc.Enqueue): a step must not change
			// meaning with a default. run-all's children are Live with empty
			// params (HEAD passes dry_run=false); label-refinement's are Preview,
			// since HEAD enqueues them with empty params for each child's
			// report-only default, and a native child's mode is the start
			// request's, not params.
			var (
				ref tasks.RunRef
				err error
			)
			if mode == childLive {
				ref, err = rc.Enqueue(kindFor(defID), struct{}{}, childModeOption(tasks.ModeRequestLive))
			} else {
				ref, err = rc.Enqueue(kindFor(defID), labelRefinementChildParams{}, childModeOption(tasks.ModeRequestPreview))
			}
			return string(ref.ID), err
		},
		Wait: func(ctx context.Context, id string, reattach bool, observe func(childObservation)) (childEnd, error) {
			// Reported for a re-attach too (it is the only frame the wait has):
			// the bar would otherwise read "Starting at step N" for the whole wait.
			observe(childObservation{Event: childWaiting})
			out, err := awaitRun(ctx, rc, id)
			if err != nil {
				if reattach && ctx.Err() == nil {
					// The recorded run is gone or unreadable.
					return childEnd{}, childGoneError{cause: err}
				}
				return childEnd{}, err
			}
			return childEnd{
				Status:      string(out.State),
				Why:         out.Err,
				Completed:   out.State == tasks.StateSucceeded,
				Interrupted: nativeChildInterrupted(out.State),
			}, nil
		},
	}
}

// nativeChildInterrupted says a run state is an interruption: a restart, a
// stand-down, or the kernel dropping a ResumeDrop child after one.
func nativeChildInterrupted(s tasks.RunState) bool {
	switch s {
	case tasks.StateInterrupted, tasks.StateInterruptedQuiesced, tasks.StateInterruptedDropped:
		return true
	}
	return false
}

// nativePhase runs a phase on the run's own env: a native run has no phases, but
// a canceled run stops before it starts the step.
func nativePhase(env kindEnv) phaseFunc {
	return func(ctx context.Context, _ string, fn func(ctx context.Context, env kindEnv) error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(ctx, env)
	}
}

// ---- dedup.run-all -------------------------------------------------------

// runAllNative is the native dedup.run-all kind. Preview is HEAD's dry run (no
// child starts; the read-only rescore preview is the result); Live starts each
// step as a child run in Live mode and waits for it. The run writes nothing
// itself, so it is ReadOnly: a write set overlapping a child's would make the
// kernel refuse the wait as self-blocked.
func (p *Plugin) runAllNative(d ChainKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(runAllKind, sched, false)
	c.Title = "Find all duplicates"
	c.Help = runAllHelp
	c.Effects = tasks.ReadOnly()
	// HEAD Restart, and its Run reads the checkpoint on restart to re-enter at the
	// recorded step: Continue.
	c.Resume = tasks.ResumeContinue
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.run-all")
	c.Timeout = 24 * time.Hour
	c.ProgressTimeout = 15 * time.Minute
	return tasks.Task(runAllKind, tasks.TaskSpec[json.RawMessage, RunAllResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (RunAllResult, error) {
			if err := refuseKindModeKeys(runAllKind, raw); err != nil {
				return RunAllResult{}, err
			}
			if err := errNeed(runAllKind, "Chain.RescorePreview", d.RescorePreview != nil); err != nil {
				return RunAllResult{}, err
			}
			base := nativeEnv(rc)
			env := runAllEnv{kindEnv: base, Children: nativeChildDriver(rc), Phase: nativePhase(base), Rescore: d.RescorePreview}
			var st runAllState
			resuming := false
			if env.Live {
				ok, err := rc.Restore(&st)
				if err != nil {
					return RunAllResult{}, fmt.Errorf("%s: restore checkpoint: %w", runAllKind, err)
				}
				resuming = ok && len(st.Plan) > 0
			}
			return runAllCore(rc.Context(), env, st, resuming)
		},
	})
}

// ---- dedup.label-refinement ---------------------------------------------

// labelRefinementNative is the native dedup.label-refinement kind. It is a
// report-only chain: HEAD enqueues both children with empty params, so each
// takes its built-in dry-run default and writes nothing. A native child's mode
// comes from the start request, not params, so the same guarantee is stated
// here as Preview on every child start; this op has no way to start one Live.
// HEAD has no preview of its own, so a Preview run of this op is refused.
func (p *Plugin) labelRefinementNative(_ ChainKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(labelRefinementKind, sched, false)
	c.Title = "Label refinement (report only)"
	c.Help = labelRefinementHelp
	c.Effects = tasks.ReadOnly()
	c.Resume = tasks.ResumeDrop
	// HEAD sets no DefaultPriority, and the registry's zero value is Low.
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books.dedup.label-refinement")
	c.Timeout = 2 * time.Hour
	c.ProgressTimeout = 15 * time.Minute
	return tasks.Task(labelRefinementKind, tasks.TaskSpec[json.RawMessage, LabelRefinementResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (LabelRefinementResult, error) {
			if err := refuseKindModeKeys(labelRefinementKind, raw); err != nil {
				return LabelRefinementResult{}, err
			}
			if err := kindLiveOnly(rc, labelRefinementKind); err != nil {
				return LabelRefinementResult{}, err
			}
			base := nativeEnv(rc)
			return labelRefinementCore(rc.Context(), labelRefinementEnv{kindEnv: base, Children: nativeChildDriver(rc), Phase: nativePhase(base), StepFrame: true})
		},
	})
}
