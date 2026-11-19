// file: internal/plugins/dedup/label_refinement.go
// version: 1.4.0
// guid: 4b8e2d7a-91c3-4f05-b6a8-3d0e5c7f1a92
// last-edited: 2026-11-19

// dedup.label-refinement is the parent op behind the scheduled label_refinement
// task. It runs dedup.rebuild-gold-labels and then dedup.calibrate-composite as
// followed child ops, the way dedup.run-all follows its steps, so the run is one
// visible, cancellable row in Operations with two children instead of a detached
// scheduler goroutine that polled them.
//
// MODE: report only. Both children are enqueued with EMPTY params. With no apply
// key each falls back to its built-in dry-run default (Apply=false):
// rebuild_gold_labels.go takes the guarded `if !params.Apply` no-write return and
// calibrate_composite.go reports without persisting bands. This op has NO way to
// set apply=true; enabling an apply stays an operator decision made by running
// the child op directly. The Apply==false-on-empty-params guarantee is pinned by
// canaries in rebuild_gold_labels_test.go and calibrate_composite_test.go, and
// the empty bodies by TestLabelRefinementChildrenGetEmptyParams.
//
// DEDUPE. A child enqueue can join an already-queued or running manual `{}` run
// of the same op through params dedupe; that child then has no parent row of its
// own (it shows under whoever enqueued it first), though this op still follows it.
//
// CANCEL. Canceling this op stops the chain; like dedup.run-all it does NOT
// cancel the child running at that moment (the registry has no parent->child
// cancel cascade). Both children are dry-run, so a child left to finish writes
// nothing.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// LabelRefinementDefID is the def id the label_refinement scheduled task enqueues.
const LabelRefinementDefID = "dedup.label-refinement"

// labelRefinementKind is rule R3's native kind for dedup.label-refinement.
const labelRefinementKind = tasks.Kind("books.dedup.label-refinement")

// labelRefinementHelp is HEAD's Description, shared with the native kind (rule R5).
const labelRefinementHelp = "Runs dedup.rebuild-gold-labels then dedup.calibrate-composite as child operations, both in " +
	"report-only mode, and stops if either does not complete. Nothing is written: applying a rebuild or " +
	"a band change is a separate, operator-run step on the child operation."

// labelRefinementStepUnits is how many progress units each of the two steps
// spans, so the bar moves while a child runs (see runAllStepUnits).
const labelRefinementStepUnits = 1000

// labelRefinementStep is one child of the chain.
type labelRefinementStep struct {
	Label string
	DefID string
}

// labelRefinementSteps is the fixed order: rebuild first, so calibrate scores
// against the freshly derived gold labels.
var labelRefinementSteps = []labelRefinementStep{
	{Label: "Rebuilding gold labels", DefID: "dedup.rebuild-gold-labels"},
	{Label: "Calibrating composite scorer", DefID: "dedup.calibrate-composite"},
}

// labelRefinementChildParams is the body both children are enqueued with. It is
// EMPTY ON PURPOSE: see MODE in the file comment.
type labelRefinementChildParams struct{}

// LabelRefinementResult is the op's result payload (GET /operations/:id/result).
type LabelRefinementResult struct {
	// DryRun is always true: nothing is written by this op or its children.
	DryRun      bool   `json:"dry_run"`
	RebuildOp   string `json:"rebuild_op"`
	CalibrateOp string `json:"calibrate_op"`
}

func (p *Plugin) labelRefinementDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:             LabelRefinementDefID,
		Liveness:       sdk.LivenessManual,
		Plugin:         "dedup",
		DisplayName:    "Label refinement (report only)",
		Description:    labelRefinementHelp,
		ResumePolicy:   sdk.ResumeDrop,
		ConcurrencyKey: LabelRefinementDefID,
		Cancellable:    true,
		// The old scheduler chain's bound: it exceeds the sum of the children's
		// own timeouts (rebuild-gold-labels 60m + calibrate-composite 30m) plus
		// slack for queueing behind other work on their concurrency keys.
		Timeout:         2 * time.Hour,
		ProgressTimeout: 15 * time.Minute,
		// Same gate the generic operations endpoint falls back to for dedup ops
		// that declare none; explicit so it cannot drift.
		Permissions: []auth.Permission{auth.PermSettingsManage},
		// Read-only: the children are dry-run and declare their own capabilities.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runLabelRefinement,
	}
}

// labelRefinementEnv is what the label-refinement core takes from its shell.
type labelRefinementEnv struct {
	kindEnv
	Children childDriver
	Phase    phaseFunc
	// StepFrame emits a progress frame as each step starts, before its phase. It
	// is native-only: HEAD's chain emitted none (its first frame for a step was
	// the child's own queued or running frame at base+1), so HEAD's shell leaves
	// it false and its output stays what it was.
	StepFrame bool
}

// runLabelRefinement is HEAD's v2 shell over labelRefinementCore.
func (p *Plugin) runLabelRefinement(ctx context.Context, _ json.RawMessage, rep sdk.Reporter) error {
	if p.registry == nil {
		return fmt.Errorf("%s: operation registry not available", LabelRefinementDefID)
	}
	if p.opStatus == nil {
		return fmt.Errorf("%s: operation store not available; cannot follow child operations", LabelRefinementDefID)
	}
	env := headEnv(rep, true)
	_, err := labelRefinementCore(ctx, labelRefinementEnv{kindEnv: env, Children: p.headChildDriver(env.OpID), Phase: headOuterPhase(rep, env)})
	return err
}

// headOuterPhase is HEAD's label-refinement phase wrapper: Reporter.RunPhase for
// the Operations page's per-step phase, but fn gets the OUTER env, not one over
// the phase's reporter. HEAD's label-refinement ignored the phase reporter and
// reported and logged through the outer one, so a step's lines carry no `phase`
// attribute (dedup.run-all, by contrast, logs through its phase reporters).
func headOuterPhase(rep sdk.Reporter, outer kindEnv) phaseFunc {
	return func(ctx context.Context, id string, fn func(ctx context.Context, env kindEnv) error) error {
		return rep.RunPhase(ctx, id, func(ctx context.Context, _ opsregistry.Reporter) error {
			return fn(ctx, outer)
		})
	}
}

// labelRefinementCore is the label-refinement body, shared by both shells: each
// step's child runs report-only and is followed to the end, in HEAD's order, and
// a child that does not complete ends the chain (the one child driver, chain.go).
func labelRefinementCore(ctx context.Context, env labelRefinementEnv) (LabelRefinementResult, error) {
	total := len(labelRefinementSteps) * labelRefinementStepUnits
	childIDs := make([]string, len(labelRefinementSteps))
	for i, step := range labelRefinementSteps {
		if env.StepFrame {
			env.Progress(i*labelRefinementStepUnits, total, step.Label)
		}
		// One phase per step, as dedup.run-all does, so the Operations page
		// shows which step is running.
		err := env.Phase(ctx, step.DefID, func(ctx context.Context, e kindEnv) error {
			return runChainChild(ctx, chainEnv{kindEnv: e, Children: env.Children}, chainChild{
				Label: step.Label, DefID: step.DefID, StepID: step.DefID, Head: step.Label,
				Base: i * labelRefinementStepUnits, Total: total, Units: labelRefinementStepUnits,
				Mode: childReportOnly, StopNote: "; label refinement stopped", Chain: "dedup.label-refinement",
			}, &childIDs[i], nil)
		})
		if err != nil {
			return LabelRefinementResult{}, err
		}
	}

	env.Progress(total, total, "Finished (report only, nothing written)")
	result := LabelRefinementResult{DryRun: true, RebuildOp: childIDs[0], CalibrateOp: childIDs[1]}
	if env.SetResult != nil {
		_ = env.SetResult(result)
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("label refinement chain complete (report only, nothing written): rebuild %s, calibrate %s", childIDs[0], childIDs[1]))
	return result, nil
}
