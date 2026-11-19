// file: internal/plugins/dedup/run_all.go
// version: 1.5.1
// guid: 428d4a76-207e-4f6e-8b0a-5f4d939690f2
// last-edited: 2026-11-19

// dedup.run-all is the Review page's one-button "Find all duplicates" run: every
// duplicate check, in order, as ONE server-side operation. Until 2026-09-28 the
// browser chained the same steps itself (web/src/components/review/
// dedupPipeline.ts), which stopped the moment the tab closed or reloaded, never
// survived a deploy, and showed up in Operations as four unrelated ops.
//
// ORDER, AND WHY (unchanged from the browser chain it replaces)
//
//  1. embeddings      dedup.embed-scan  -- the similarity data step 3 compares.
//                                          Skipped when dedup.embeddings_enabled
//                                          is false: there it can only fail, and
//                                          a failed step stops every later one.
//  2. acoustic        acoustid.scan     -- audio-match pairs from the stored
//                                          fingerprints, so they exist when
//                                          step 3 scores.
//  3. find            dedup.full-scan   -- exact + similarity checks, then the
//                                          unified score for every book. This is
//                                          the step that fills the candidate
//                                          queue the Review page's Dupes tab
//                                          reads.
//  4. ai-review       dedup.llm-review  -- reads the ambiguous pairs step 3 just
//                                          scored, so it must follow it.
//  5. rescore-preview Engine.Rescore(apply=false) -- writes nothing; its counts
//                                          are the op's result.
//
// Each op step is enqueued as a CHILD op (WithParent) and followed to a
// terminal status before the next starts. Anything other than `completed`
// stops the run: a later step built on a failed earlier one reports results
// that are not real.
//
// RESUME. ResumeRestart, with the position checkpointed after every enqueue and
// every finished step: the frozen plan, the index of the current step and the
// child op id. On restart the registry merges that checkpoint into params and
// Run re-enters at the recorded step (resumeChild):
//
//   - child completed      -> the step is done; move on.
//   - child queued/running -> keep following it.
//   - child interrupted_*  -> enqueue the step again. embed-scan and full-scan
//     are ResumeRequeue, so a deploy retires their row and queues a replacement
//     under a NEW id; the enqueue collapses onto that replacement (same def,
//     same `{}` params) instead of starting a second run. acoustid.scan and
//     llm-review are ResumeDrop, so theirs is simply started again.
//   - child failed/canceled -> the run stops with that as its error.
//
// Every checkpoint field is written on every checkpoint (no omitempty): the
// registry merges the blob into params KEY BY KEY, so a key left out of a later
// checkpoint would keep the stale value an earlier resume merged in -- a
// cleared child_op_id would come back as the old child.
//
// SCAN RULES. This op never takes a scan stand-down hold: it would park
// library.scan for the hours the run takes. It only enqueues through the
// registry, which never refuses a write op because a library.scan runs; each
// child keeps whatever stand-down behaviour it already has.
//
// LIVENESS (the watchdog). The parent does no work of its own while a child
// runs, so it reports progress from what it OBSERVES of the child, never from a
// timer:
//
//   - child running: report only when the child's row changed (status,
//     progress, message or last_progress_at). A healthy child moves its row at
//     least every flush interval (30s default, 5m cap) and is itself guarded by
//     its own watchdog, so a wedged child is killed and the parent then sees it
//     fail. ProgressTimeout is 15m to cover the 5m flush cap plus the child's
//     own 5m watchdog window.
//   - operator pause (POST /operations/pause) while the child runs: report
//     "paused" on each poll, mirroring the pause gate, which keeps a parked
//     child alive the same way.
//   - child queued: report "waiting to start" on each poll. A queued child is
//     owned by the dispatcher and is not wedged work -- acoustid.scan shares
//     the acoustid.fingerprint key with fingerprint rescans and can wait hours
//     behind one. Striking the parent there would kill a healthy run. The run's
//     24h Timeout still bounds a dispatcher that never picks the child up.
//
// MODE (owner rule 2026-09-25: a writing op previews unless told otherwise).
// The run writes through its children (full-scan writes candidates and may
// auto-link identical copies; llm-review may auto-merge; embed-scan writes
// embeddings), so it declares library.write and has a preview mode, resolved
// with opmode.ResolveDryRun: `{}` is a PREVIEW, `{"dry_run":false}` is live.
//
//   - Preview enqueues NO child. None of the four child ops has a preview mode
//     of its own (each is `no-mode` in write_op_modes.golden), so none of them
//     can run without writing; each is listed in the result's preview_skipped
//     with that reason. The only step that runs is the rescore preview, which
//     is read-only, so its counts (waiting pairs, and how many would score
//     differently) are what a preview can honestly offer. The Review page shows
//     them in its confirmation prompt.
//   - Live passes dry_run=false to every child explicitly. The children ignore
//     the key today; passing it means a child that later gains a preview mode
//     (and with it the preview-by-default rule) is still run live by a live
//     run, instead of silently turning the whole chain into a no-op.
//
// DEDUPE. A second request while a run is active returns that run only when its
// params are byte-identical (the registry default). DedupeQueuedRuns would
// collapse a live press onto a running PREVIEW, so it is off. The cost: once a
// live run has resumed after a restart its params carry the merged checkpoint,
// so a second live press queues a second run behind it (serialized by the
// ConcurrencyKey). The Review page remembers the run it started and does not
// press again while following it.
//
// CANCEL. Canceling this op stops the chain; it does NOT cancel the child that
// is running at that moment (the registry has no parent->child cancel cascade).
// That step finishes on its own and no later step starts.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/childop"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// RunAllDefID is the def id the Review page's one-button run enqueues.
const RunAllDefID = "dedup.run-all"

// runAllKind is rule R3's native kind for dedup.run-all.
const runAllKind = tasks.Kind("books.dedup.run-all")

// runAllHelp is HEAD's Description, shared with the native kind (rule R5).
const runAllHelp = "Runs every duplicate check in order (similarity data, audio fingerprints, find and score, AI review) as child operations, then previews how many waiting pairs would score differently. Preview by default: {} runs only the read-only score check and lists the steps a live run ({\"dry_run\":false}) would start. Resumes at the step it was on after a restart."

// runAllStepUnits is how many progress units each step spans. The parent's
// progress is (step index * units + the running child's share of units) over
// (steps * units): a real count of thousandths of steps, so the bar moves
// during a multi-hour step instead of sitting still between step boundaries.
const runAllStepUnits = 1000

// runAllCheckpointEvery bounds how often a progressing wait re-checkpoints. The
// state itself does not change mid-step; the point is high_water_progress,
// which the registry's boot-loop guard (checkInfiniteRestart) reads.
const runAllCheckpointEvery = time.Minute

// defaultRunAllPollInterval is how often a running step's child row is read.
const defaultRunAllPollInterval = 5 * time.Second

// runAllStep is one step of the chain. DefID is empty for the in-process
// rescore preview.
type runAllStep struct {
	ID    string
	Label string
	DefID string
}

// runAllSteps is the canonical order. See the file comment for why.
var runAllSteps = []runAllStep{
	{ID: "embeddings", Label: "Preparing similarity data", DefID: "dedup.embed-scan"},
	{ID: "acoustic", Label: "Comparing audio fingerprints", DefID: "acoustid.scan"},
	{ID: "find", Label: "Finding and scoring duplicates", DefID: "dedup.full-scan"},
	{ID: "ai-review", Label: "AI review of unclear pairs", DefID: "dedup.llm-review"},
	{ID: "rescore-preview", Label: "Checking scores"},
}

func lookupRunAllStep(id string) (runAllStep, bool) {
	for _, s := range runAllSteps {
		if s.ID == id {
			return s, true
		}
	}
	return runAllStep{}, false
}

// RunAllStepRecord is one finished step, as recorded in the checkpoint and the
// op result.
type RunAllStepRecord struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	ChildOpID string `json:"child_op_id"`
}

// runAllState is both the op's params and its checkpoint. A fresh request is
// `{}`; the first checkpoint freezes Plan, and a resume reads the rest back.
// No omitempty: see the file comment.
type runAllState struct {
	Plan      []string           `json:"plan"`
	Skipped   []string           `json:"skipped"`
	StepIndex int                `json:"step_index"`
	ChildOpID string             `json:"child_op_id"`
	Finished  []RunAllStepRecord `json:"finished"`

	// Mode, as sent. *bool so an omitted key stays distinguishable from false
	// (opmode). Never rewritten by a checkpoint, so a resume keeps the mode.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// RunAllPreviewSkip is a step a preview did not run, and why.
type RunAllPreviewSkip struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	DefID  string `json:"def_id"`
	Reason string `json:"reason"`
}

// RunAllResult is the op's result payload (GET /operations/:id/result).
type RunAllResult struct {
	DryRun bool `json:"dry_run"`
	// PreviewSkipped lists the steps a preview left out (every child op; see
	// MODE in the file comment). Empty on a live run.
	PreviewSkipped []RunAllPreviewSkip        `json:"preview_skipped"`
	Steps          []RunAllStepRecord         `json:"steps"`
	Skipped        []string                   `json:"skipped"`
	RescorePreview *dedupengine.RescoreResult `json:"rescore_preview"`
}

// opStatusReader is the one store method this op needs to follow its children
// (childop.Reader). *database.PebbleStore satisfies it; register.go wires it
// from the plugin store.
type opStatusReader = childop.Reader

// The production store must keep satisfying it, or register.go's assertion
// fails and every run errors with "operation store not available".
var _ opStatusReader = (*database.PebbleStore)(nil)

// runAllPlan builds a fresh run's step list from the current config.
func runAllPlan() (plan, skipped []string) {
	embeddingsOn := config.AppConfig.Dedup.EmbeddingsEnabled
	for _, s := range runAllSteps {
		if s.ID == "embeddings" && !embeddingsOn {
			skipped = append(skipped, s.ID)
			continue
		}
		plan = append(plan, s.ID)
	}
	return plan, skipped
}

func (p *Plugin) runAllDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:           RunAllDefID,
		Preview:      sdk.PreviewSpec{Params: runAllState{}},
		Liveness:     sdk.LivenessManual,
		Plugin:       "dedup",
		DisplayName:  "Find all duplicates",
		Description:  runAllHelp,
		ResumePolicy: sdk.ResumeRestart,
		// One run at a time. No DedupeQueuedRuns: see DEDUPE in the file comment.
		ConcurrencyKey:  RunAllDefID,
		DefaultPriority: sdk.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		// Sum of the children's own timeouts (2h + 6h + 2h + 2h) plus time
		// queued behind other work on their concurrency keys.
		Timeout:         24 * time.Hour,
		ProgressTimeout: 15 * time.Minute,
		// Writes through its children; see MODE in the file comment.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runRunAll,
	}
}

func (p *Plugin) runAllPoll() time.Duration {
	if p.runAllPollInterval > 0 {
		return p.runAllPollInterval
	}
	return defaultRunAllPollInterval
}

func (p *Plugin) operationsPaused() bool {
	if p.operationsPausedFn != nil {
		return p.operationsPausedFn()
	}
	return opsregistry.OperationsPaused()
}

func (p *Plugin) rescorePreview(ctx context.Context) (dedupengine.RescoreResult, error) {
	if p.rescorePreviewFn != nil {
		return p.rescorePreviewFn(ctx)
	}
	if p.engine == nil {
		return dedupengine.RescoreResult{}, fmt.Errorf("dedup engine not available")
	}
	// apply=false: counts only, writes nothing.
	return p.engine.Rescore(ctx, false)
}

// runAllEnv is what the run-all core takes from its shell (rules R2 and R10): the
// run's env, the driver that starts and waits for a child, the phase wrapper, and
// the read-only rescore preview. HEAD's shell resolves the dry-run switch from the
// params (ResolveLive); the native shell's mode is the start request's, which is
// already in kindEnv.Live.
type runAllEnv struct {
	kindEnv
	Children childDriver
	Phase    phaseFunc
	Rescore  func(context.Context) (dedupengine.RescoreResult, error)
	// ResolveLive, when set, decides live-or-preview from the plan-validated state
	// (HEAD: opmode.ResolveDryRun over dry_run/dryRun). It runs after the plan is
	// validated, as HEAD's did.
	ResolveLive func(st runAllState) (bool, error)
}

// runRunAll is HEAD's v2 shell: decode the params and hand runAllCore the run's
// reporter, the registry-backed child driver and the rescore preview.
func (p *Plugin) runRunAll(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	if p.registry == nil {
		return fmt.Errorf("%s: operation registry not available", RunAllDefID)
	}
	if p.opStatus == nil {
		return fmt.Errorf("%s: operation store not available; cannot follow child operations", RunAllDefID)
	}

	var st runAllState
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &st); err != nil {
			return fmt.Errorf("%s: decode params: %w", RunAllDefID, err)
		}
	}
	env := headEnv(reporter, true)
	env.Checkpoint = reporter.Checkpoint
	_, err := runAllCore(ctx, runAllEnv{
		kindEnv:  env,
		Children: p.headChildDriver(env.OpID),
		Phase:    headPhase(reporter),
		Rescore:  p.rescorePreview,
		ResolveLive: func(st runAllState) (bool, error) {
			dryRun, err := opmode.ResolveDryRun(RunAllDefID, st.DryRun, st.DryRunCamel)
			return !dryRun, err
		},
	}, st, len(st.Plan) > 0)
	return err
}

// runAllCore is the run-all body, shared by HEAD's v2 shell and the native kind
// (the one driver of the live loop: see chain.go). st is the checkpoint the run
// resumes from, resuming says whether it names a plan; a run that is not resuming
// builds the plan from Settings.
func runAllCore(ctx context.Context, env runAllEnv, st runAllState, resuming bool) (RunAllResult, error) {
	if !resuming {
		st.Plan, st.Skipped = runAllPlan()
		st.StepIndex, st.ChildOpID, st.Finished = 0, "", nil
	}
	if err := validateRunAllPlan(st); err != nil {
		return RunAllResult{}, err
	}
	if env.ResolveLive != nil {
		live, err := env.ResolveLive(st)
		if err != nil {
			return RunAllResult{}, err
		}
		env.Live = live
	}
	if !env.Live {
		return runAllPreviewCore(ctx, env.kindEnv, st, env.Rescore)
	}

	total := len(st.Plan) * runAllStepUnits
	log := env.Logger
	if resuming {
		log.Info("dedup.run-all: resuming", "step_index", st.StepIndex, "plan", strings.Join(st.Plan, ","), "child_op_id", st.ChildOpID)
		env.Log(slog.LevelInfo, fmt.Sprintf("Resuming at step %d of %d", st.StepIndex+1, len(st.Plan)))
	} else {
		for _, s := range st.Skipped {
			env.Log(slog.LevelInfo, fmt.Sprintf("Skipping step %q: turned off in Settings", s))
		}
	}
	// Report before the first checkpoint: Checkpoint records the reporter's
	// current progress as high_water_progress, which the boot-loop guard reads.
	env.Progress(st.StepIndex*runAllStepUnits, total,
		fmt.Sprintf("Starting at step %d of %d", st.StepIndex+1, len(st.Plan)))
	save := func(e kindEnv) {
		if err := e.Checkpoint(st); err != nil {
			e.Logger.Warn("dedup.run-all: checkpoint failed", "error", err)
		}
	}
	// Freeze the plan before any child exists, so a restart during the first
	// enqueue resumes the same plan rather than re-reading config.
	save(env.kindEnv)

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

		err := env.Phase(ctx, step.ID, func(ctx context.Context, e kindEnv) error {
			if step.DefID == "" {
				res, err := env.Rescore(ctx)
				if err != nil {
					return fmt.Errorf("%s: %w", step.Label, err)
				}
				preview = &res
				return nil
			}
			// The checkpoint a running child's progress triggers is throttled to one
			// a minute (runAllCheckpointEvery); the one after its enqueue is not.
			var last time.Time
			record := func(force bool) {
				if !force {
					if time.Since(last) < runAllCheckpointEvery {
						return
					}
					last = time.Now()
				}
				save(e)
			}
			return runChainChild(ctx, chainEnv{kindEnv: e, Children: env.Children}, chainChild{
				Label: step.Label, DefID: step.DefID, StepID: step.ID, Head: head,
				Base: i * runAllStepUnits, Total: total, Units: runAllStepUnits,
				Mode: childLive, Chain: "dedup.run-all",
			}, &st.ChildOpID, record)
		})
		if err != nil {
			// A canceled context is the registry's to classify (restart vs.
			// user cancel), so hand it back untouched and do NOT checkpoint:
			// the last checkpoint already names this step and its child.
			if ctx.Err() != nil {
				return RunAllResult{}, ctx.Err()
			}
			return RunAllResult{}, err
		}

		st.Finished = append(st.Finished, RunAllStepRecord{ID: step.ID, Label: step.Label, ChildOpID: st.ChildOpID})
		st.StepIndex++
		st.ChildOpID = ""
		env.Progress(st.StepIndex*runAllStepUnits, total, fmt.Sprintf("%s — done", head))
		save(env.kindEnv)
	}

	result := RunAllResult{PreviewSkipped: []RunAllPreviewSkip{}, Steps: st.Finished, Skipped: st.Skipped, RescorePreview: preview}
	if st.Finished == nil {
		result.Steps = []RunAllStepRecord{}
	}
	if env.SetResult != nil {
		if err := env.SetResult(result); err != nil {
			log.Warn("dedup.run-all: could not store result", "error", err)
		}
	}
	done := fmt.Sprintf("Finished %d of %d steps", len(st.Finished), len(st.Plan))
	if preview != nil {
		done += fmt.Sprintf("; %d of %d waiting pairs would score differently (nothing changed)", preview.Changed, preview.Inspected)
	}
	env.Progress(total, total, done)
	env.Log(slog.LevelInfo, done)
	return result, nil
}

// runAllPreviewCore is the dry run, shared by both shells: no child op is
// enqueued, the read-only rescore preview runs if it is in the plan, and every
// op step is reported as skipped. Writes nothing but the op's own state and
// result (env.SetResult is HEAD's result store; the native shell returns the
// result instead).
func runAllPreviewCore(ctx context.Context, env kindEnv, st runAllState, rescore func(context.Context) (dedupengine.RescoreResult, error)) (RunAllResult, error) {
	total := len(st.Plan)
	result := RunAllResult{DryRun: true, PreviewSkipped: []RunAllPreviewSkip{}, Steps: []RunAllStepRecord{}, Skipped: st.Skipped}
	env.Progress(0, total, "Preview: nothing will be changed")
	for i, id := range st.Plan {
		if err := ctx.Err(); err != nil {
			return RunAllResult{}, err
		}
		step, _ := lookupRunAllStep(id)
		if step.DefID != "" {
			result.PreviewSkipped = append(result.PreviewSkipped, RunAllPreviewSkip{
				ID: step.ID, Label: step.Label, DefID: step.DefID,
				Reason: "has no preview mode, so a preview cannot run it without writing; a live run starts it",
			})
			env.Progress(i+1, total, fmt.Sprintf("Preview: %s would run %s (skipped)", step.Label, step.DefID))
			continue
		}
		res, err := rescore(ctx)
		if err != nil {
			return RunAllResult{}, fmt.Errorf("%s: %w", step.Label, err)
		}
		result.RescorePreview = &res
		result.Steps = append(result.Steps, RunAllStepRecord{ID: step.ID, Label: step.Label})
		env.Progress(i+1, total, fmt.Sprintf("Preview: %s done", step.Label))
	}
	if env.SetResult != nil {
		if err := env.SetResult(result); err != nil {
			env.Logger.Warn("dedup.run-all: could not store preview result", "error", err)
		}
	}
	msg := fmt.Sprintf("Preview finished: %d step(s) would start child operations; nothing was changed", len(result.PreviewSkipped))
	if result.RescorePreview != nil {
		msg += fmt.Sprintf("; %d of %d waiting pairs would score differently", result.RescorePreview.Changed, result.RescorePreview.Inspected)
	}
	env.Progress(total, total, msg)
	env.Log(slog.LevelInfo, msg)
	return result, nil
}

// validateRunAllPlan rejects a plan this build cannot run: an unknown or
// repeated step id, or a position past the end. Refusing is safer than
// skipping -- a silently dropped step reads as a finished run.
func validateRunAllPlan(st runAllState) error {
	if len(st.Plan) == 0 {
		return fmt.Errorf("%s: nothing to run (every step is turned off)", RunAllDefID)
	}
	seen := make(map[string]bool, len(st.Plan))
	for _, id := range st.Plan {
		if _, ok := lookupRunAllStep(id); !ok {
			return fmt.Errorf("%s: unknown step %q in plan", RunAllDefID, id)
		}
		if seen[id] {
			return fmt.Errorf("%s: step %q listed twice in plan", RunAllDefID, id)
		}
		seen[id] = true
	}
	if st.StepIndex < 0 || st.StepIndex > len(st.Plan) {
		return fmt.Errorf("%s: step_index %d out of range for %d steps", RunAllDefID, st.StepIndex, len(st.Plan))
	}
	return nil
}

// headChildDriver is HEAD's child driver: a child is started with
// registry.EnqueueOp under the run's operation (lineage only: children show under
// it in Operations) and followed by polling its operation row with
// childop.Follow. See LIVENESS in this file's comment: Follow reports a running
// child only when its row changed, and a queued or operator-paused child on every
// poll, so this op's watchdog sees exactly what the child shows and a wedged
// child leaves this op silent too.
func (p *Plugin) headChildDriver(parentID string) childDriver {
	return childDriver{
		NoID: "the registry returned no operation id",
		Start: func(ctx context.Context, defID string, mode childMode) (string, error) {
			var opts []sdk.EnqueueOption
			if parentID != "" {
				opts = append(opts, sdk.WithParent(parentID))
			}
			// Live, explicitly, for run-all (see MODE in the file comment); the
			// report-only chain enqueues with empty params, which each child reads
			// as its own dry-run default.
			var params any = labelRefinementChildParams{}
			if mode == childLive {
				params = opmode.DryRunParams{DryRun: opmode.Live()}
			}
			return p.registry.EnqueueOp(ctx, defID, params, opts...)
		},
		Wait: func(ctx context.Context, id string, reattach bool, observe func(childObservation)) (childEnd, error) {
			if reattach {
				row, err := p.opStatus.GetOperationV2(id)
				if err != nil || row == nil {
					return childEnd{}, childGoneError{cause: err}
				}
				switch {
				case row.Status == "completed" || row.Status == "failed" || row.Status == "canceled":
					return headChildEnd(row, false), nil
				case state.IsInterrupted(row.Status):
					return headChildEnd(row, true), nil
				}
			}
			row, err := childop.Follow(ctx, p.opStatus, id, childop.Options{
				Interval: p.runAllPoll(),
				Paused:   p.operationsPaused,
				OnObserve: func(o childop.Observation) {
					switch o.Event {
					case childop.EventQueued:
						observe(childObservation{Event: childQueued})
					case childop.EventPaused:
						observe(childObservation{Event: childPaused})
					default:
						observe(childObservation{Event: childRunning, Current: o.Row.ProgressCurrent, Total: o.Row.ProgressTotal, Message: o.Row.ProgressMessage})
					}
				},
			})
			if err != nil {
				return childEnd{}, err
			}
			// A child that was alive and then ends interrupted is the registry
			// force-dropping it, not a restart: this op would have been stopped too.
			return headChildEnd(row, false), nil
		},
	}
}

// headChildEnd reads how a child's operation row ended.
func headChildEnd(row *database.OperationV2Row, interrupted bool) childEnd {
	why := ""
	if row.ErrorMessage != nil {
		why = *row.ErrorMessage
	}
	return childEnd{Status: row.Status, Why: why, Completed: row.Status == "completed", Interrupted: interrupted}
}

// headPhase is HEAD's phase wrapper: Reporter.RunPhase, so the Operations page
// shows which step is running, with the env over that phase's reporter.
func headPhase(reporter sdk.Reporter) phaseFunc {
	return func(ctx context.Context, id string, fn func(ctx context.Context, env kindEnv) error) error {
		return reporter.RunPhase(ctx, id, func(ctx context.Context, rep opsregistry.Reporter) error {
			env := headEnv(rep, true)
			env.Checkpoint = rep.Checkpoint
			return fn(ctx, env)
		})
	}
}
