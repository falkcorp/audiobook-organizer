// file: internal/plugins/maintenance/repairs_ops.go
// version: 1.0.0
// guid: 6f1a8d37-2e59-4b0c-8a74-3d9e5b1c7f82
// last-edited: 2026-09-27

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// The two operations behind the Repairs lane of /review. They are generic:
// every fixer in p.Repairs() runs through them, and the framework in
// internal/repairs owns the guards, the fingerprint check, the history writer
// and the scan stand-down, so a fixer cannot skip them.

// The apply op reads the plan through a type assertion on the queue store.
// Production hands it a database.Store, so pin that database.Store has the
// method: an assertion whose method is missing there would fail only in prod.
var _ repairs.OpReader = database.Store(nil)

func (p *Plugin) repairsPlanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          repairs.PlanOpID,
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Repairs: plan",
		Description: "Plans one Repairs-lane fixer (params: fixer_id, params) and stores every row in the op " +
			"result for paging. Read-only: runs during a library.scan, takes no stand-down. Rows touching " +
			"books/itunes/** or Doctor Who / Big Finish / Torchwood are marked skipped.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  repairs.PlanOpID,
		Cancellable:     true,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runRepairsPlan,
	}
}

func (p *Plugin) repairsApplyDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          repairs.ApplyOpID,
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Repairs: apply",
		Description: "Applies explicit row_ids of a stored repairs.plan (params: fixer_id, plan_op_id, row_ids). " +
			"PREVIEW BY DEFAULT: only dry_run=false writes. Each row is re-planned and refused as " +
			"changed_since_plan when its fingerprint moved; hands-off rows are refused again on fresh reads. " +
			"A write pauses a running library.scan through the scan stand-down (waiting until it parks) " +
			"instead of refusing, and records metadata history for every changed field. Nothing is deleted.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		// One apply at a time: two applies of overlapping plans would race.
		ConcurrencyKey: repairs.ApplyOpID,
		Cancellable:    true,
		Timeout:        6 * time.Hour,
		Capabilities:   []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:            p.runRepairsApply,
	}
}

func (p *Plugin) runRepairsPlan(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params repairs.PlanParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: parse params: %w", repairs.PlanOpID, err)
		}
	}
	f, ok := p.Repairs().Get(params.FixerID)
	if !ok {
		return fmt.Errorf("%s: unknown fixer %q", repairs.PlanOpID, params.FixerID)
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	all, err := store.GetAllSeries()
	if err != nil {
		// Without series names the guard cannot see a Doctor Who book whose
		// path does not say so: fail rather than plan past it.
		return fmt.Errorf("%s: list series: %w", repairs.PlanOpID, err)
	}
	res, err := repairs.RunPlan(ctx, f, params.Params, repairs.PlanDeps{
		Guard: store, Series: repairs.SeriesNamesFrom(all),
	}, reporter)
	if err != nil {
		return err
	}
	if err := registry.ReporterSetResult(reporter, res); err != nil {
		return fmt.Errorf("%s: plan not persisted: %w", repairs.PlanOpID, err)
	}
	_ = reporter.UpdateProgress(res.Total, res.Total, fmt.Sprintf("PLANNED %s — rows=%d applicable=%d skipped=%v",
		f.ID(), res.Total, res.Applicable, res.SkippedByKind))
	return nil
}

func (p *Plugin) runRepairsApply(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params repairs.ApplyParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: parse params: %w", repairs.ApplyOpID, err)
		}
	}
	dryRun, derr := opmode.ResolveDryRun(repairs.ApplyOpID, params.DryRun, params.DryRunC)
	if derr != nil {
		return derr
	}
	f, ok := p.Repairs().Get(params.FixerID)
	if !ok {
		return fmt.Errorf("%s: unknown fixer %q", repairs.ApplyOpID, params.FixerID)
	}
	store := p.deps.OpsStore()
	vps := p.deps.VersionPrimaryStore()
	if store == nil || vps == nil {
		return fmt.Errorf("database not initialized")
	}
	// GetOperationV2 is on database.Store, which the production queue store
	// is; a store without it cannot load the plan, so fail loudly.
	opReader, ok := p.deps.OperationQueueStore().(repairs.OpReader)
	if !ok {
		return fmt.Errorf("%s: operation store cannot read the plan op", repairs.ApplyOpID)
	}
	plan, err := repairs.LoadPlan(opReader, params.PlanOpID, f.ID())
	if err != nil {
		return err
	}
	all, err := store.GetAllSeries()
	if err != nil {
		return fmt.Errorf("%s: list series: %w", repairs.ApplyOpID, err)
	}
	opID := registry.ReporterOpID(reporter)
	if !dryRun && opID == "" {
		return repairs.ErrNoHolderID
	}
	deps := repairs.ApplyDeps{
		Guard: store, Series: repairs.SeriesNamesFrom(all),
		StandDown: p.deps, OpID: opID, Wait: p.standDownWait,
	}
	if !dryRun {
		deps.Writer = repairs.NewWriter(store, vps, f.ID(), "bulk_update", "repairs-", reporter.Logger())
	}
	res, runErr := repairs.RunApply(ctx, f, plan, params.PlanOpID, params.RowIDs, dryRun, deps, reporter)
	// The per-row report is written before any error returns, the lost-lease
	// abort included, so the operator sees exactly which rows were written.
	if res != nil {
		if serr := registry.ReporterSetResult(reporter, res); serr != nil {
			if runErr == nil {
				return fmt.Errorf("%s: result not persisted: %w", repairs.ApplyOpID, serr)
			}
			reporter.Logger().Warn("repairs.apply: result not persisted", "err", serr)
		}
	}
	if runErr != nil {
		if errors.Is(runErr, repairs.ErrStandDownLost) {
			return fmt.Errorf("%s %s: %w", repairs.ApplyOpID, f.ID(), runErr)
		}
		return runErr
	}
	prefix := "DRY RUN (nothing written) — "
	if !dryRun {
		prefix = "APPLIED — "
	}
	_ = reporter.UpdateProgress(len(res.Rows), len(res.Rows), fmt.Sprintf("%s%s rows=%d %v not_in_plan=%d history_rows=%d history_failed=%d",
		prefix, f.ID(), len(res.Rows), res.ByOutcome, len(res.NotInPlan), res.HistoryRows, res.HistoryFailed))
	return nil
}
