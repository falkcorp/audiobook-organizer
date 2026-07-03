// file: internal/plugins/dedup/drain_stale.go
// version: 1.0.0
// guid: a6103a90-d68c-4db5-ace4-e2a9fb2a51e1
// last-edited: 2026-07-03

// Package dedup — op dedup.drain-stale (DEDUP-1 / CONS-16 / CONS-17).
//
// Wraps Engine.DrainStaleCandidates as a UOS operation. Re-evaluates pending
// exact-layer dedup candidates that were emitted before the duration-ms
// (CONS-16) and multi-file title-leak (CONS-17) importer bugs were fixed,
// against today's emission gates, and reports counts/samples by rejection
// reason.
//
// Dry-run by default. Pass {"apply":true} to soft-reclassify would-purge rows
// to "stale-drain" (never a hard delete). The apply path is gated behind a
// versioned Settings done-flag (dedup_stale_drain_v1_done) so a second apply
// run after completion is a safe no-op — the M0 purge_legacy_fp precedent.
//
// DATA-LOSS GATE: this op ships the tool only. Running apply=true against
// production requires a separate, explicit owner greenlight after the dry-run
// counts/samples have been reviewed (TODO.md CONS-10).
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// drainStaleDoneFlag is the versioned Settings key that prevents re-running the
// apply path after it has completed once. Bump to v2 if the drain criteria ever
// change and a re-run is required.
const drainStaleDoneFlag = "dedup_stale_drain_v1_done"

// drainStaleCheckpointID is the stable checkpoint key for the op's resumable
// scan. A constant is safe because ConcurrencyKey serialises runs, so no two
// drain-stale runs ever checkpoint concurrently.
const drainStaleCheckpointID = "dedup.drain-stale"

type drainStaleParams struct {
	// Apply, if true, soft-reclassifies would-purge candidates to "stale-drain".
	// Default false (dry-run) — the op only reports counts/samples.
	Apply bool `json:"apply"`
}

func (p *Plugin) drainStaleDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.drain-stale",
		Plugin:      "dedup",
		DisplayName: "Drain stale exact candidates",
		Description: "Re-evaluates pending exact-layer dedup candidates emitted before the CONS-16 " +
			"(duration-ms) and CONS-17 (title-leak) importer bugs were fixed against today's emission " +
			"gates, and reports counts/samples by rejection reason. Dry-run by default; pass apply=true " +
			"to soft-reclassify would-purge rows to stale-drain. Idempotent: a versioned flag prevents " +
			"re-running after apply.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.drain-stale",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runDrainStale,
	}
}

func (p *Plugin) runDrainStale(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	var params drainStaleParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	reporter.Logger().Info("dedup.drain-stale start", "apply", params.Apply)

	// Guard: skip the apply path if it has already completed once.
	if params.Apply {
		if done, err := p.isFlagSet(drainStaleDoneFlag); err != nil {
			reporter.Logger().Warn("drain-stale: flag check error (proceeding)", "error", err)
		} else if done {
			reporter.Logger().Info("drain-stale: already completed; skipping (flag set)", "flag", drainStaleDoneFlag)
			_ = reporter.UpdateProgress(1, 1, "Already completed (flag set); nothing to do.")
			return nil
		}
	}

	_ = reporter.UpdateProgress(0, 1, "Scanning pending exact candidates…")

	result, err := p.engine.DrainStaleCandidates(ctx, drainStaleCheckpointID, params.Apply)
	if err != nil {
		return fmt.Errorf("drain-stale: %w", err)
	}

	// Log the full reason breakdown (deterministic order) plus totals.
	reasons := make([]string, 0, len(result.ReasonCounts))
	for r := range result.ReasonCounts {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		reporter.Logger().Info("drain-stale: reason breakdown", "reason", r, "count", result.ReasonCounts[r])
	}
	reporter.Logger().Info("drain-stale: scan complete",
		"inspected", result.Inspected,
		"would_purge", result.WouldPurge,
		"kept", result.Kept,
		"apply", params.Apply)

	summary := fmt.Sprintf("inspected=%d would_purge=%d kept=%d reasons=%s",
		result.Inspected, result.WouldPurge, result.Kept, formatReasonCounts(result.ReasonCounts, reasons))

	if !params.Apply {
		_ = reporter.UpdateProgress(1, 1, fmt.Sprintf(
			"Dry-run — %d candidate(s) would be reclassified stale-drain. %s. Review counts/samples, then pass apply=true (owner greenlight required).",
			result.WouldPurge, summary))
		reporter.Logger().Info("drain-stale: dry-run only; nothing written", "would_purge", result.WouldPurge)
		return nil
	}

	// Apply completed — set the versioned done-flag so a second apply is a no-op.
	if err := p.store.SetSetting(drainStaleDoneFlag, "true", "bool", false); err != nil {
		reporter.Logger().Warn("drain-stale: could not set done flag", "flag", drainStaleDoneFlag, "error", err)
	} else {
		reporter.Logger().Info("drain-stale: set done flag", "flag", drainStaleDoneFlag)
	}

	_ = reporter.UpdateProgress(1, 1, fmt.Sprintf("Complete — %d candidate(s) reclassified stale-drain. %s", result.WouldPurge, summary))
	return nil
}

// formatReasonCounts renders the reason buckets in the given (sorted) order as a
// compact "reason=count" string for the progress summary.
func formatReasonCounts(counts map[string]int, order []string) string {
	if len(order) == 0 {
		return "none"
	}
	s := ""
	for i, r := range order {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%s=%d", r, counts[r])
	}
	return s
}
