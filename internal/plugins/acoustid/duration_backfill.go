// file: internal/plugins/acoustid/duration_backfill.go
// version: 1.0.0
// guid: e5f6a7b8-c9d0-4e1f-9a2b-3c4d5e6f7a8b
// last-edited: 2026-07-07

package acoustid

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// DurationBackfillParams controls the acoustid.duration-backfill operation.
// Live defaults to false (the Go zero value), so triggering the op with no
// params — or any params JSON that omits the field — is always a safe,
// read-only dry run. Callers must explicitly pass {"live": true} to write.
type DurationBackfillParams struct {
	Live bool `json:"live,omitempty"`
}

func (p *Plugin) durationBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.duration-backfill",
		Plugin:          "acoustid",
		DisplayName:     "AcoustID DurationSec repair",
		Description:     "Re-derives AcoustIDFingerprintDurationSec for book_files that have a fingerprint but DurationSec==0 (STOREFID follow-up). Dry-run by default; pass live=true to write.",
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
	if !fingerprint.Available() {
		return fmt.Errorf("no fingerprint backend (fpcalc / ffmpeg) found")
	}

	var req DurationBackfillParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			reporter.Logger().Error("failed to unmarshal params", "error", err)
			req = DurationBackfillParams{}
		}
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
		wg.Add(1)
		go func(bf database.BookFile) {
			defer func() { <-sem; wg.Done() }()

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
		}(f)
	}
	wg.Wait()

	_ = reporter.UpdateProgress(total, total,
		fmt.Sprintf("Duration backfill complete in %s — fixed=%d failed=%d ineligible=%d (of %d)",
			time.Since(startedAt).Round(time.Second), fixed.Load(), failed.Load(), ineligible.Load(), total))
	return nil
}
