// file: internal/plugins/dedup/label_refinement.go
// version: 1.1.0
// guid: 4b8e2d7a-91c3-4f05-b6a8-3d0e5c7f1a92
// last-edited: 2026-10-15

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
	"github.com/falkcorp/audiobook-organizer/internal/operations/childop"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// LabelRefinementDefID is the def id the label_refinement scheduled task enqueues.
const LabelRefinementDefID = "dedup.label-refinement"

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
		ID:          LabelRefinementDefID,
		Liveness:    sdk.LivenessManual,
		Plugin:      "dedup",
		DisplayName: "Label refinement (report only)",
		Description: "Runs dedup.rebuild-gold-labels then dedup.calibrate-composite as child operations, both in " +
			"report-only mode, and stops if either does not complete. Nothing is written: applying a rebuild or " +
			"a band change is a separate, operator-run step on the child operation.",
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

func (p *Plugin) runLabelRefinement(ctx context.Context, _ json.RawMessage, rep sdk.Reporter) error {
	if p.registry == nil {
		return fmt.Errorf("%s: operation registry not available", LabelRefinementDefID)
	}
	if p.opStatus == nil {
		return fmt.Errorf("%s: operation store not available; cannot follow child operations", LabelRefinementDefID)
	}

	total := len(labelRefinementSteps) * labelRefinementStepUnits
	childIDs := make([]string, len(labelRefinementSteps))
	parentID := opsregistry.ReporterOpID(rep)
	for i, step := range labelRefinementSteps {
		// One phase per step, as dedup.run-all does, so the Operations page
		// shows which step is running.
		err := rep.RunPhase(ctx, step.DefID, func(ctx context.Context, _ opsregistry.Reporter) error {
			var opts []sdk.EnqueueOption
			if parentID != "" {
				// Lineage only: children show under this run in Operations.
				opts = append(opts, sdk.WithParent(parentID))
			}
			childID, err := p.registry.EnqueueOp(ctx, step.DefID, labelRefinementChildParams{}, opts...)
			if err != nil {
				return fmt.Errorf("%s: could not start %s: %w", step.Label, step.DefID, err)
			}
			if childID == "" {
				return fmt.Errorf("%s: the registry returned no operation id for %s", step.Label, step.DefID)
			}
			childIDs[i] = childID
			_ = rep.Log(slog.LevelInfo, fmt.Sprintf("%s: started %s (%s)", step.Label, step.DefID, childID))
			return p.followLabelRefinementChild(ctx, rep, step, childID, i*labelRefinementStepUnits, total)
		})
		if err != nil {
			return err
		}
	}

	_ = rep.UpdateProgress(total, total, "Finished (report only, nothing written)")
	_ = opsregistry.ReporterSetResult(rep, LabelRefinementResult{DryRun: true, RebuildOp: childIDs[0], CalibrateOp: childIDs[1]})
	_ = rep.Log(slog.LevelInfo, fmt.Sprintf("label refinement chain complete (report only, nothing written): rebuild %s, calibrate %s", childIDs[0], childIDs[1]))
	return nil
}

// followLabelRefinementChild follows one child to a terminal status, mirroring
// its progress onto this op (see LIVENESS in run_all.go: Follow reports a
// running child only when its row changed, so a wedged child leaves this op
// silent too). A child that is not completed ends the chain with an error.
func (p *Plugin) followLabelRefinementChild(ctx context.Context, rep sdk.Reporter, step labelRefinementStep, childID string, base, total int) error {
	// A recorded child counts as one unit before it reports any, so a queued
	// child does not leave the parent at progress 0.
	share := 1
	row, err := childop.Follow(ctx, p.opStatus, childID, childop.Options{
		Interval: p.runAllPoll(),
		Paused:   p.operationsPaused,
		OnObserve: func(o childop.Observation) {
			switch o.Event {
			case childop.EventQueued:
				_ = rep.UpdateProgress(base+share, total, step.Label+" — waiting to start (queued behind other work)")
			case childop.EventPaused:
				_ = rep.UpdateProgress(base+share, total, step.Label+" — paused by an operator")
			default:
				if o.Row.ProgressTotal > 0 {
					if s := min(o.Row.ProgressCurrent*labelRefinementStepUnits/o.Row.ProgressTotal, labelRefinementStepUnits-1); s > share {
						share = s
					}
				}
				msg := step.Label
				if o.Row.ProgressMessage != "" {
					msg += " — " + o.Row.ProgressMessage
				}
				_ = rep.UpdateProgress(base+share, total, msg)
			}
		},
	})
	if err != nil {
		return err
	}
	if row.Status != "completed" {
		why := ""
		if row.ErrorMessage != nil && *row.ErrorMessage != "" {
			why = ": " + *row.ErrorMessage
		}
		return fmt.Errorf("%s (%s) ended as %q%s; label refinement stopped", step.Label, row.ID, row.Status, why)
	}
	return nil
}
