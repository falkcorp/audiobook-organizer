// file: internal/plugins/dedup/auto_resolve.go
// version: 1.3.0
// guid: 3a7c1f08-9d24-4e6b-8b15-2c9f0a5d7e61
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// autoResolveParams are the JSON params for dedup.auto-resolve.
//
// apply defaults to false (dry-run report only). max_merges / sample_cap default
// to 0, which the engine coerces to 200 / 50 respectively.
type autoResolveParams struct {
	Apply     bool `json:"apply"`
	MaxMerges int  `json:"max_merges"`
	SampleCap int  `json:"sample_cap"`
}

// autoResolveDef defines the dedup.auto-resolve operation: the Tier-1 (Band
// CERTAIN) confidence-tiered auto-merge pass from the 02-dedup consultancy
// design. It is dry-run by default and — for apply=true — gated behind the
// dedup.auto_resolve_enabled kill switch and a max_merges cap. See
// dedup.Engine.AutoResolveCertain for the eligibility rules and safety rails.
func (p *Plugin) autoResolveDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.auto-resolve",
		Preview:         sdk.PreviewSpec{Params: autoResolveParams{}},
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Auto-resolve CERTAIN dedup candidates (Tier 1)",
		Description:     "Dry-run by default: reports which Band-CERTAIN dedup candidates WOULD auto-merge (≥2 primary signals or a whole-book-signature true_dup label). apply=true performs the merges, gated by the dedup.auto_resolve_enabled kill switch, and stops after max_merges successful merges (it is not bound to a reviewed dry run).",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh,
		ConcurrencyKey:  "dedup.auto-resolve",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runAutoResolve,
	}
}

// autoResolveKind is rule R3's native kind for dedup.auto-resolve.
const autoResolveKind = tasks.Kind("books.dedup.auto-resolve")

// autoResolveEngine is the one engine call the core makes.
type autoResolveEngine interface {
	AutoResolveCertain(ctx context.Context, apply bool, maxMerges, sampleCap int) (dedupengine.AutoResolveResult, error)
}

// autoResolveNativeParams are the native params: HEAD's Apply is the run's mode.
type autoResolveNativeParams struct {
	MaxMerges int `json:"max_merges"`
	SampleCap int `json:"sample_cap"`
}

// autoResolveEnv is what the core takes from its shell. Live is HEAD's apply=true;
// Enabled is the dedup.auto_resolve_enabled kill switch, read by the shell.
type autoResolveEnv struct {
	kindEnv
	Engine    autoResolveEngine
	MaxMerges int
	SampleCap int
	Enabled   bool
}

// autoResolveResult is the native run's result. Report is the dry run's
// reviewable plan: HEAD stores it through SetResult, a native run returns it.
type autoResolveResult struct {
	DryRun     bool                                 `json:"dry_run"`
	Checked    int                                  `json:"checked"`
	Eligible   int                                  `json:"eligible"`
	Merged     int                                  `json:"merged"`
	SkippedCap int                                  `json:"skipped_cap"`
	Report     *dedupengine.AutoResolveDryRunReport `json:"report,omitempty"`
}

// autoResolveCore is the body of runAutoResolve, shared by both shells.
//
// Concurrency: the pass is one Engine.AutoResolveCertain call whose merge loop
// (internal/dedup/auto_resolve.go) is sequential by design: candidates are
// snapshotted once, a book shared by two CERTAIN pairs would otherwise be
// merged twice, and the max_merges cap is loop-carried. No worker pool exists
// here, so no two workers can touch one book.
func autoResolveCore(ctx context.Context, env autoResolveEnv) (autoResolveResult, error) {
	// Fail fast at the op boundary too (the engine also enforces this) so a
	// misconfigured apply=true request is rejected before any candidate scan.
	if env.Live && !env.Enabled {
		return autoResolveResult{}, fmt.Errorf("auto-resolve apply=true requires dedup.auto_resolve_enabled=true (owner greenlight); refusing")
	}
	mode := "dry-run"
	if env.Live {
		mode = "APPLY"
	}
	env.Progress(0, 1, fmt.Sprintf("Scanning CERTAIN candidates for Tier-1 auto-resolution (%s)…", mode))
	env.Logger.Info("dedup auto-resolve start",
		"apply", env.Live, "max_merges", env.MaxMerges, "sample_cap", env.SampleCap)

	res, err := env.Engine.AutoResolveCertain(ctx, env.Live, env.MaxMerges, env.SampleCap)
	if err != nil {
		env.Logger.Error("dedup auto-resolve error", "error", err)
		return autoResolveResult{}, fmt.Errorf("auto-resolve: %w", err)
	}

	env.Progress(1, 1, fmt.Sprintf(
		"Auto-resolve complete (%s) — checked %d, eligible %d, merged %d, skipped(cap) %d",
		mode, res.Checked, res.Eligible, res.Merged, res.SkippedCap))
	env.Logger.Info("dedup auto-resolve complete",
		"dry_run", res.DryRun, "checked", res.Checked, "eligible", res.Eligible,
		"merged", res.Merged, "skipped_cap", res.SkippedCap, "samples", len(res.Samples),
		"plan", len(res.Plan), "plan_truncated", res.PlanTruncated)

	out := autoResolveResult{DryRun: res.DryRun, Checked: res.Checked, Eligible: res.Eligible, Merged: res.Merged, SkippedCap: res.SkippedCap}
	// A dry run's only product is the list of pairs a run would merge, so it is
	// stored as the op result and a failure to store it fails the op. An apply
	// run stores nothing: it already did its work.
	if !env.Live {
		report := dedupengine.NewAutoResolveDryRunReport(res)
		out.Report = &report
		if env.SetResult != nil {
			if err := env.SetResult(report); err != nil {
				return autoResolveResult{}, fmt.Errorf("auto-resolve: store the dry-run plan: %w", err)
			}
		}
	}
	return out, nil
}

// runAutoResolve is the v2 shell: HEAD's Run over autoResolveCore.
func (p *Plugin) runAutoResolve(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	var params autoResolveParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("parse auto-resolve params: %w", err)
		}
	}
	_, err := autoResolveCore(ctx, autoResolveEnv{
		kindEnv:   headEnv(reporter, params.Apply),
		Engine:    p.engine,
		MaxMerges: params.MaxMerges,
		SampleCap: params.SampleCap,
		Enabled:   config.AppConfig.Dedup.AutoResolveEnabled,
	})
	return err
}
