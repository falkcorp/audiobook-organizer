// file: internal/plugins/acoustid/duration_backfill.go
// version: 1.3.0
// guid: e5f6a7b8-c9d0-4e1f-9a2b-3c4d5e6f7a8b
// last-edited: 2026-09-21

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// DurationBackfillParams controls the acoustid.fingerprint-duration-repair operation.
// Live defaults to false (the Go zero value), so triggering the op with no
// params — or any params JSON that omits the field — is always a safe,
// read-only dry run. Callers must explicitly pass {"live": true} to write.
type DurationBackfillParams struct {
	Live bool `json:"live,omitempty"`
}

func (p *Plugin) durationBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		// Renamed from "acoustid.duration-backfill" on 2026-09-21. That name sat
		// next to maintenance.duration-backfill and read as its twin, when the
		// two share nothing: this one re-runs fpcalc (a full audio DECODE) to
		// repopulate BookFile.AcoustIDFingerprintDurationSec, while the
		// maintenance op fills in book durations from values already stored.
		// They are sequential stages — this one produces the field the
		// maintenance op prefers as its first source of truth — not duplicates.
		ID:              "acoustid.fingerprint-duration-repair",
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "acoustid",
		DisplayName:     "Repair fingerprint duration (re-runs fpcalc)",
		Description:     "Re-runs fpcalc on book_files that have a fingerprint but AcoustIDFingerprintDurationSec==0, to repopulate that field (STOREFID follow-up). This DECODES audio, so it is refused on the server when a reference tool pair is configured. Dry-run by default; pass live=true to write.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "acoustid.fingerprint",
		Isolate:         false,
		Timeout:         6 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
			sdk.CapFilesExecute,
			sdk.CapSubprocessSpawn,
		},
		Run: p.runDurationBackfill,
	}
}

// runDurationBackfill scopes to exactly the rows where AcoustIDFingerprint is
// set but AcoustIDFingerprintDurationSec==0 (via GetFilesWithZeroDurationFingerprint)
// and re-runs fpcalc (force=true) on each. Uses the same bounded worker-pool
// shape as fingerprint_rescan.go's runFingerprintRescan — the correctly-
// parallel sibling for this exact fpcalc-subprocess workload.
func (p *Plugin) runDurationBackfill(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("database store not available")
	}
	// No top-level fingerprint.Available() gate: dry-run mode never calls
	// fpcalc, and the live path already handles backend unavailability
	// per-file via doFingerprintFile's fpcalc→ffmpeg→fail fallback chain
	// (markFingerprintFailure), so a hard stop here would only abort the
	// whole batch unnecessarily.

	var req DurationBackfillParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			reporter.Logger().Error("failed to unmarshal params", "error", err)
			req = DurationBackfillParams{}
		}
	}

	// No decoding on the server (owner rule, 2026-09-19). This op calls fpcalc
	// directly on the host that runs it, and it had no gate at all — the same
	// hazard window-backfill refuses by hand. It does not yet speak the
	// worker-hub lease protocol, so there is no remote mode to fall back to;
	// until it has one, refuse rather than decode where we must not.
	if req.Live && strings.TrimSpace(config.AppConfig.FingerprintWindowReferenceTools) != "" {
		return errors.New("acoustid.fingerprint-duration-repair: fingerprint_window_reference_tools is set, so audio is decoded by remote workers only " +
			"(no decoding on the server). This op still decodes locally and has no remote mode yet; run it on a worker host, or clear the reference pair deliberately first")
	}

	_ = reporter.UpdateProgress(0, 1, "Scanning for zero-duration fingerprinted files...")

	files, _, err := p.store.GetFilesWithZeroDurationFingerprint(0, 0)
	if err != nil {
		return fmt.Errorf("scan zero-duration fingerprints: %w", err)
	}

	total := len(files)
	if total == 0 {
		_ = reporter.UpdateProgress(1, 1, "No zero-duration fingerprinted files found")
		return nil
	}

	if !req.Live {
		sample := make([]string, 0, 10)
		for i, f := range files {
			if i >= 10 {
				break
			}
			sample = append(sample, f.FilePath)
		}
		reporter.Logger().Info("duration-backfill dry run", "affected_count", total, "sample", sample)
		_ = reporter.UpdateProgress(1, 1, fmt.Sprintf("Dry run: %d files affected (pass live=true to fix)", total))
		return nil
	}

	workers := fpRescanWorkers()

	var (
		fixed      atomic.Int64
		failed     atomic.Int64
		ineligible atomic.Int64
	)
	startedAt := time.Now()

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for _, f := range files {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		default:
		}

		sem <- struct{}{}
		bf := f
		wg.Go(func() {
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}

			switch fingerprintBookFile(p.store, bf, true) {
			case fingerprintOutcomeFingerprinted:
				fixed.Add(1)
			case fingerprintOutcomeFailed:
				failed.Add(1)
			case fingerprintOutcomeIneligible:
				ineligible.Add(1)
			}
			done := fixed.Load() + failed.Load() + ineligible.Load()
			_ = reporter.UpdateProgress(int(done), total,
				fmt.Sprintf("fixed=%d failed=%d ineligible=%d / %d", fixed.Load(), failed.Load(), ineligible.Load(), total))
		})
	}
	wg.Wait()

	_ = reporter.UpdateProgress(total, total,
		fmt.Sprintf("Duration backfill complete in %s — fixed=%d failed=%d ineligible=%d (of %d)",
			time.Since(startedAt).Round(time.Second), fixed.Load(), failed.Load(), ineligible.Load(), total))
	return nil
}
