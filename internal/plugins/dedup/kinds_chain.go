// file: internal/plugins/dedup/kinds_chain.go
// version: 1.1.0
// guid: ca58ea3e-cc42-4a27-b1be-b532eb5a7b6e
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// childEnv is what a chain core takes to run children: start one and wait for it.
type childEnv struct {
	// Enqueue starts child kind with params in mode m and returns its run id.
	Enqueue func(kind tasks.Kind, params any, m tasks.ModeRequest) (string, error)
	// Await waits for the run to end.
	Await func(ctx context.Context, id string) (tasks.Outcome, error)
}

func nativeChildEnv(rc *tasks.RC) childEnv {
	return childEnv{
		Enqueue: func(kind tasks.Kind, params any, m tasks.ModeRequest) (string, error) {
			ref, err := rc.Enqueue(kind, params, childModeOption(m))
			return string(ref.ID), err
		},
		Await: func(ctx context.Context, id string) (tasks.Outcome, error) { return awaitRun(ctx, rc, id) },
	}
}

// childOutcomeError is the error for a child that did not succeed, worded as
// HEAD's childEndedError is: label, run id, how it ended and why.
func childOutcomeError(label, id string, out tasks.Outcome) error {
	why := ""
	if out.Err != "" {
		why = ": " + out.Err
	}
	return fmt.Errorf("%s (%s) ended as %q%s", label, id, out.State, why)
}

// ---- dedup.run-all -------------------------------------------------------

// runAllNativeEnv is what the native run-all core takes from its shell.
type runAllNativeEnv struct {
	kindEnv
	childEnv
	Rescore func(context.Context) (dedupengine.RescoreResult, error)
}

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
			env := runAllNativeEnv{kindEnv: nativeEnv(rc), childEnv: nativeChildEnv(rc), Rescore: d.RescorePreview}
			var st runAllState
			resuming := false
			if env.Live {
				ok, err := rc.Restore(&st)
				if err != nil {
					return RunAllResult{}, fmt.Errorf("%s: restore checkpoint: %w", runAllKind, err)
				}
				resuming = ok && len(st.Plan) > 0
			}
			return runAllNativeCore(rc.Context(), env, st, resuming)
		},
	})
}

// runAllNativeCore is the native run-all body. It shares HEAD's step table, plan,
// plan validation, preview and result type; only the child driver differs, since
// HEAD reads each child's operation row and the native run starts and awaits
// runs. st is the restored checkpoint when resuming.
func runAllNativeCore(ctx context.Context, env runAllNativeEnv, st runAllState, resuming bool) (RunAllResult, error) {
	if !resuming {
		st = runAllState{}
		st.Plan, st.Skipped = runAllPlan()
	}
	if err := validateRunAllPlan(st); err != nil {
		return RunAllResult{}, err
	}
	if !env.Live {
		return runAllPreviewCore(ctx, env.kindEnv, st, env.Rescore)
	}

	total := len(st.Plan) * runAllStepUnits
	if resuming {
		env.Logger.Info("dedup.run-all: resuming", "step_index", st.StepIndex, "child_op_id", st.ChildOpID)
		env.Log(slog.LevelInfo, fmt.Sprintf("Resuming at step %d of %d", st.StepIndex+1, len(st.Plan)))
	} else {
		for _, s := range st.Skipped {
			env.Log(slog.LevelInfo, fmt.Sprintf("Skipping step %q: turned off in Settings", s))
		}
	}
	// Report before the first checkpoint, then freeze the plan before any child
	// exists, so a restart during the first enqueue resumes the same plan.
	env.Progress(st.StepIndex*runAllStepUnits, total, fmt.Sprintf("Starting at step %d of %d", st.StepIndex+1, len(st.Plan)))
	checkpoint := func() {
		if err := env.Checkpoint(st); err != nil {
			env.Logger.Warn("dedup.run-all: checkpoint failed", "error", err)
		}
	}
	checkpoint()

	var preview *dedupengine.RescoreResult
	for st.StepIndex < len(st.Plan) {
		if err := ctx.Err(); err != nil {
			return RunAllResult{}, err
		}
		i := st.StepIndex
		step, _ := lookupRunAllStep(st.Plan[i])
		head := fmt.Sprintf("Step %d of %d: %s", i+1, len(st.Plan), step.Label)
		env.Progress(i*runAllStepUnits, total, head)
		env.Log(slog.LevelInfo, head)

		var err error
		if step.DefID == "" {
			var res dedupengine.RescoreResult
			if res, err = env.Rescore(ctx); err != nil {
				err = fmt.Errorf("%s: %w", step.Label, err)
			} else {
				preview = &res
			}
		} else {
			err = runAllNativeChild(ctx, env, &st, step, head, total, checkpoint)
		}
		if err != nil {
			// A canceled context is the kernel's to classify, so hand it back
			// untouched and do not checkpoint: the last checkpoint already names
			// this step and its child.
			if ctx.Err() != nil {
				return RunAllResult{}, ctx.Err()
			}
			return RunAllResult{}, err
		}

		st.Finished = append(st.Finished, RunAllStepRecord{ID: step.ID, Label: step.Label, ChildOpID: st.ChildOpID})
		st.StepIndex++
		st.ChildOpID = ""
		env.Progress(st.StepIndex*runAllStepUnits, total, fmt.Sprintf("%s — done", head))
		checkpoint()
	}

	result := RunAllResult{PreviewSkipped: []RunAllPreviewSkip{}, Steps: st.Finished, Skipped: st.Skipped, RescorePreview: preview}
	if st.Finished == nil {
		result.Steps = []RunAllStepRecord{}
	}
	done := fmt.Sprintf("Finished %d of %d steps", len(st.Finished), len(st.Plan))
	if preview != nil {
		done += fmt.Sprintf("; %d of %d waiting pairs would score differently (nothing changed)", preview.Changed, preview.Inspected)
	}
	env.Progress(total, total, done)
	env.Log(slog.LevelInfo, done)
	return result, nil
}

// runAllNativeChild runs one op step: re-attach to the recorded child if there is
// one, otherwise start it, then wait for it. The child starts in Live mode,
// explicitly, as HEAD passes dry_run=false (MODE in run_all.go's comment): the
// kernel's default is this run's mode, which is Live here, but a step must not
// change meaning with a default.
func runAllNativeChild(ctx context.Context, env runAllNativeEnv, st *runAllState, step runAllStep, head string, total int, checkpoint func()) error {
	child := kindFor(step.DefID)
	if st.ChildOpID != "" {
		out, err := env.Await(ctx, st.ChildOpID)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			// The recorded run is gone or unreadable: start the step again (the
			// enqueue folds into a live run of the same kind and params).
			env.Logger.Warn("dedup.run-all: recorded child unreadable; re-enqueueing step",
				"step", step.ID, "child_op_id", st.ChildOpID, "error", err)
			st.ChildOpID = ""
		case out.State == tasks.StateSucceeded:
			return nil
		default:
			return childOutcomeError(step.Label, st.ChildOpID, out)
		}
	}
	base := st.StepIndex * runAllStepUnits
	if st.ChildOpID == "" {
		id, err := env.Enqueue(child, struct{}{}, tasks.ModeRequestLive)
		if err != nil {
			return fmt.Errorf("%s: could not start %s: %w", step.Label, step.DefID, err)
		}
		if id == "" {
			return fmt.Errorf("%s: the kernel returned no run id for %s", step.Label, step.DefID)
		}
		st.ChildOpID = id
		checkpoint()
		env.Log(slog.LevelInfo, fmt.Sprintf("%s: started %s (%s)", step.Label, step.DefID, id))
	}
	// A recorded child counts as one unit of progress before it reports any (the
	// boot-loop guard reads a run that never moved).
	env.Progress(base+1, total, head+" — waiting for "+step.DefID)
	out, err := env.Await(ctx, st.ChildOpID)
	if err != nil {
		return err
	}
	if out.State != tasks.StateSucceeded {
		return childOutcomeError(step.Label, st.ChildOpID, out)
	}
	return nil
}

// ---- dedup.label-refinement ---------------------------------------------

// labelRefinementNativeEnv is what the native label-refinement core takes.
type labelRefinementNativeEnv struct {
	kindEnv
	childEnv
}

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
			return labelRefinementNativeCore(rc.Context(), labelRefinementNativeEnv{kindEnv: nativeEnv(rc), childEnv: nativeChildEnv(rc)})
		},
	})
}

// labelRefinementNativeCore starts each step's child in Preview mode and waits
// for it, in HEAD's order.
func labelRefinementNativeCore(ctx context.Context, env labelRefinementNativeEnv) (LabelRefinementResult, error) {
	total := len(labelRefinementSteps) * labelRefinementStepUnits
	childIDs := make([]string, len(labelRefinementSteps))
	for i, step := range labelRefinementSteps {
		if err := ctx.Err(); err != nil {
			return LabelRefinementResult{}, err
		}
		base := i * labelRefinementStepUnits
		env.Progress(base, total, step.Label)
		id, err := env.Enqueue(kindFor(step.DefID), labelRefinementChildParams{}, tasks.ModeRequestPreview)
		if err != nil {
			return LabelRefinementResult{}, fmt.Errorf("%s: could not start %s: %w", step.Label, step.DefID, err)
		}
		if id == "" {
			return LabelRefinementResult{}, fmt.Errorf("%s: the kernel returned no run id for %s", step.Label, step.DefID)
		}
		childIDs[i] = id
		env.Log(slog.LevelInfo, fmt.Sprintf("%s: started %s (%s)", step.Label, step.DefID, id))
		env.Progress(base+1, total, step.Label+" — waiting to finish")
		out, err := env.Await(ctx, id)
		if err != nil {
			return LabelRefinementResult{}, err
		}
		if out.State != tasks.StateSucceeded {
			return LabelRefinementResult{}, fmt.Errorf("%w; label refinement stopped", childOutcomeError(step.Label, id, out))
		}
	}
	env.Progress(total, total, "Finished (report only, nothing written)")
	env.Log(slog.LevelInfo, fmt.Sprintf("label refinement chain complete (report only, nothing written): rebuild %s, calibrate %s", childIDs[0], childIDs[1]))
	return LabelRefinementResult{DryRun: true, RebuildOp: childIDs[0], CalibrateOp: childIDs[1]}, nil
}
