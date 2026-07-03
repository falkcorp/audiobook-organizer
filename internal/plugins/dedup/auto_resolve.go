// file: internal/plugins/dedup/auto_resolve.go
// version: 1.0.0
// guid: 3a7c1f08-9d24-4e6b-8b15-2c9f0a5d7e61
// last-edited: 2026-07-03

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
		Plugin:          "dedup",
		DisplayName:     "Auto-resolve CERTAIN dedup candidates (Tier 1)",
		Description:     "Dry-run by default: reports which Band-CERTAIN dedup candidates WOULD auto-merge (≥2 primary signals or a whole-book-signature true_dup label). apply=true performs the merges, gated by the dedup.auto_resolve_enabled kill switch and a max_merges cap.",
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

	// Fail fast at the op boundary too (the engine also enforces this) so a
	// misconfigured apply=true request is rejected before any candidate scan.
	if params.Apply && !config.AppConfig.Dedup.AutoResolveEnabled {
		return fmt.Errorf("auto-resolve apply=true requires dedup.auto_resolve_enabled=true (owner greenlight); refusing")
	}

	mode := "dry-run"
	if params.Apply {
		mode = "APPLY"
	}
	_ = reporter.UpdateProgress(0, 1,
		fmt.Sprintf("Scanning CERTAIN candidates for Tier-1 auto-resolution (%s)…", mode))
	reporter.Logger().Info("dedup auto-resolve start",
		"apply", params.Apply, "max_merges", params.MaxMerges, "sample_cap", params.SampleCap)

	res, err := p.engine.AutoResolveCertain(ctx, params.Apply, params.MaxMerges, params.SampleCap)
	if err != nil {
		reporter.Logger().Error("dedup auto-resolve error", "error", err)
		return fmt.Errorf("auto-resolve: %w", err)
	}

	_ = reporter.UpdateProgress(1, 1, fmt.Sprintf(
		"Auto-resolve complete (%s) — checked %d, eligible %d, merged %d, skipped(cap) %d",
		mode, res.Checked, res.Eligible, res.Merged, res.SkippedCap))
	reporter.Logger().Info("dedup auto-resolve complete",
		"dry_run", res.DryRun, "checked", res.Checked, "eligible", res.Eligible,
		"merged", res.Merged, "skipped_cap", res.SkippedCap, "samples", len(res.Samples))
	return nil
}
