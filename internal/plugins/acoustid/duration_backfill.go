// file: internal/plugins/acoustid/duration_backfill.go
// version: 1.4.0
// guid: e5f6a7b8-c9d0-4e1f-9a2b-3c4d5e6f7a8b
// last-edited: 2026-11-19

package acoustid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// durationRepairKind is rule R3's native kind for
// acoustid.fingerprint-duration-repair.
const durationRepairKind = tasks.Kind("books.acoustid.fingerprint-duration-repair")

// durationRepairHelp is the definition's description, HEAD's and the native
// kind's Help (rule R5: verbatim). It still says "pass live=true to write";
// natively the mode is the start request's.
const durationRepairHelp = "Re-runs fpcalc on book_files that have a fingerprint but AcoustIDFingerprintDurationSec==0, to repopulate that field (STOREFID follow-up). This DECODES audio, so it is refused on the server when a reference tool pair is configured. Dry-run by default; pass live=true to write."

// durationRepairEnv is what the duration-repair core takes from its shell
// (rules R2 and R10): reads through Store, the fingerprint writes through
// Writer (nil in a dry run). Live is HEAD's inverted flag already translated:
// true for HEAD's live=true or a native ModeLive run.
type durationRepairEnv struct {
	kindEnv
	Store  kindStore
	Writer kindWriter
	Host   hostHooks
}

// durationRepairResult is the native run's result. HEAD stored no result, so its
// shell drops it.
type durationRepairResult struct {
	Affected   int `json:"affected"`
	Fixed      int `json:"fixed"`
	Failed     int `json:"failed"`
	Ineligible int `json:"ineligible"`
}

// durationRepairParallelism is the fpcalc pool (rule R4): HEAD's hand-rolled
// semaphore of fpRescanWorkers() (the FP_PARALLEL_WORKERS knob, default 4), one
// fpcalc subprocess per item, so a fixed count and never the CPU budget. The
// hand-rolled pool stays HEAD's exact pool in HEAD's shell (env.Pool).
func durationRepairParallelism(workers int) tasks.Parallelism {
	return tasks.Fixed(workers, "fpcalc subprocess pool; sized from the shared FP_PARALLEL_WORKERS knob (fpRescanWorkers), default 4")
}

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
		Preview:         sdk.PreviewSpec{Params: DurationBackfillParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "acoustid",
		DisplayName:     "Repair fingerprint duration (re-runs fpcalc)",
		Description:     durationRepairHelp,
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

// runDurationBackfill is the v2 shell: HEAD's lenient params decode (a bad
// body is logged and means a dry run) over durationRepairCore.
func (p *Plugin) runDurationBackfill(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	env := p.headDurationRepairEnv(reporter)
	if env.Store != nil {
		var req DurationBackfillParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				reporter.Logger().Error("failed to unmarshal params", "error", err)
				req = DurationBackfillParams{}
			}
		}
		env.Live = req.Live
		if req.Live {
			env.Writer = headKindWriter{store: p.store}
		}
	}
	_, err := durationRepairCore(ctx, env)
	return err
}

// headDurationRepairEnv is HEAD's env before the params are read: the plugin's
// store for reads (a plugin with no store hands the core a nil Store, which it
// refuses with HEAD's message), a dry run, and no writer.
func (p *Plugin) headDurationRepairEnv(reporter sdk.Reporter) durationRepairEnv {
	env := durationRepairEnv{kindEnv: headEnv(reporter, false), Host: headHostHooks()}
	if p.store != nil {
		env.Store = p.store
	}
	return env
}

// durationRepairCore scopes to exactly the rows where AcoustIDFingerprint is
// set but AcoustIDFingerprintDurationSec==0 (via GetFilesWithZeroDurationFingerprint)
// and re-runs fpcalc (force=true) on each. Uses the same bounded worker-pool
// shape as fingerprint_rescan.go's runFingerprintRescan — the correctly-
// parallel sibling for this exact fpcalc-subprocess workload. It is the body of
// runDurationBackfill, shared by both shells.
func durationRepairCore(ctx context.Context, env durationRepairEnv) (durationRepairResult, error) {
	if env.Store == nil {
		return durationRepairResult{}, fmt.Errorf("database store not available")
	}
	// No top-level fingerprint.Available() gate: dry-run mode never calls
	// fpcalc, and the live path already handles backend unavailability
	// per-file via doFingerprintFile's fpcalc→ffmpeg→fail fallback chain
	// (markFingerprintFailure), so a hard stop here would only abort the
	// whole batch unnecessarily.

	// No decoding on the server (owner rule, 2026-09-19). This op calls fpcalc
	// directly on the host that runs it, and it had no gate at all — the same
	// hazard window-backfill refuses by hand. It does not yet speak the
	// worker-hub lease protocol, so there is no remote mode to fall back to;
	// until it has one, refuse rather than decode where we must not.
	if env.Live && env.Host.ReferenceToolsSet() {
		return durationRepairResult{}, errors.New("acoustid.fingerprint-duration-repair: fingerprint_window_reference_tools is set, so audio is decoded by remote workers only " +
			"(no decoding on the server). This op still decodes locally and has no remote mode yet; run it on a worker host, or clear the reference pair deliberately first")
	}

	env.Progress(0, 1, "Scanning for zero-duration fingerprinted files...")

	files, _, err := env.Store.GetFilesWithZeroDurationFingerprint(0, 0)
	if err != nil {
		return durationRepairResult{}, fmt.Errorf("scan zero-duration fingerprints: %w", err)
	}

	total := len(files)
	if total == 0 {
		env.Progress(1, 1, "No zero-duration fingerprinted files found")
		return durationRepairResult{}, nil
	}

	if !env.Live {
		sample := make([]string, 0, 10)
		for i, f := range files {
			if i >= 10 {
				break
			}
			sample = append(sample, f.FilePath)
		}
		env.Logger.Info("duration-backfill dry run", "affected_count", total, "sample", sample)
		env.Progress(1, 1, fmt.Sprintf("Dry run: %d files affected (pass live=true to fix)", total))
		return durationRepairResult{Affected: total}, nil
	}

	workers := env.Host.Workers()

	var (
		fixed      atomic.Int64
		failed     atomic.Int64
		ineligible atomic.Int64
	)
	startedAt := time.Now()
	view := kindView{env.Store, env.Writer}

	if err := env.Pool(ctx, durationRepairParallelism(workers), total, kindItemsOptions{}, func(ctx context.Context, i int) error {
		if ctx.Err() != nil {
			return nil
		}

		switch fingerprintBookFile(view, files[i], true) {
		case fingerprintOutcomeFingerprinted:
			fixed.Add(1)
		case fingerprintOutcomeFailed:
			failed.Add(1)
		case fingerprintOutcomeIneligible:
			ineligible.Add(1)
		}
		done := fixed.Load() + failed.Load() + ineligible.Load()
		env.ItemProgress(int(done), total,
			fmt.Sprintf("fixed=%d failed=%d ineligible=%d / %d", fixed.Load(), failed.Load(), ineligible.Load(), total))
		return nil
	}); err != nil {
		return durationRepairResult{}, err
	}

	env.Progress(total, total,
		fmt.Sprintf("Duration backfill complete in %s — fixed=%d failed=%d ineligible=%d (of %d)",
			time.Since(startedAt).Round(time.Second), fixed.Load(), failed.Load(), ineligible.Load(), total))
	return durationRepairResult{
		Affected: total, Fixed: int(fixed.Load()), Failed: int(failed.Load()), Ineligible: int(ineligible.Load()),
	}, nil
}
