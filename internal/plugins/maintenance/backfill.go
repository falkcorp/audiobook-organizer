// file: internal/plugins/maintenance/backfill.go
// version: 1.9.0
// guid: f2a3b4c5-d6e7-8901-5678-123456789012
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// One-shot startup backfills / file repairs. Schedule is nil — they are
// enqueued once per boot by Server.startBackfills (enqueueBootOps) and not
// repeated, and an operator can run any of them by hand.
//
// RESUMABLE (04-P6, docs/decisions/2026-10-30-boot-ops-resumable-checkpoint.md).
// Each is a full-library walk under a fixed Timeout that a large first run can
// exceed, and a timeout is terminal: the done flag stays unset and the next
// boot starts again from zero. So the three ResumeRestart ops (external-id,
// movement-atom, remux) checkpoint a cursor as they go (bootBackfillParams
// .Resume, merged back into params on a restart) AND yield before their
// deadline (opsregistry.ErrContinueInPlace), which re-queues the SAME operation
// with a fresh Timeout and the checkpoint merged in. The Timeouts are
// unchanged. The transcode stays ResumeAsk (it re-encodes audio in place; the
// owner confirms an interrupted run): it checkpoints too, so the operator's
// Retry continues from the cursor, but it never yields by itself.
//
// Each waits for the store's startup warmup before it runs: the registry's
// dispatcher holds every def without NoWarmupWait (D44, warmup_gate.go).
//
// A run that found nothing to do logs its summary with opsregistry.NoOpAttr,
// and every op here logs its "Starting" line at Debug (the activity mirror
// files it in the debug tier), so the boot runs that are usually no-ops add
// no line to the default activity feed. A cancelled run returns ctx's error
// instead of logging "complete".

// bootBackfillParams is the params shape of the boot-enqueued ops in this
// file. Absent params (an operator's run) decode to the zero value, which is
// each op's behaviour from before boot enqueued them.
type bootBackfillParams struct {
	// IfNeeded (external-id only) takes the sentinel-gated path boot used to
	// run: skip when an earlier pass recorded completion.
	IfNeeded bool `json:"if_needed,omitempty"`
	// QuarantineAfter (transcode only) runs the known-bad-file quarantine
	// after the transcode, the second step of the old boot goroutine.
	QuarantineAfter bool `json:"quarantine_after,omitempty"`
	// Resume is the checkpoint (it is not a param an operator sets): the
	// registry merges the saved checkpoint over params on a restart, and a
	// checkpoint is these same params with Resume updated.
	Resume walkresume.State `json:"resume,omitempty"`
}

// bootYieldMargin is the least time left before the run's deadline at which a
// resumable boot op stops at its next checkpoint and hands itself back to the
// registry. The margin used is the larger of this and twice the longest gap
// between checkpoints the attempt has seen, so the items in flight when the
// line is crossed finish well inside the deadline. A variable so a test can
// move it.
var bootYieldMargin = 5 * time.Minute

// Pool sizes for the per-file passes (CLAUDE.md concurrency rule). The work is
// a taglib read per file, and for a malformed one an ffmpeg process, so a fixed
// small pool rather than NumCPU: ffmpeg is itself multi-threaded and these ops
// run at PriorityLow beside user work. The transcode is a full AAC re-encode,
// so it gets the smallest.
const (
	bootWalkWorkers      = 4
	bootTranscodeWorkers = 2
)

// quietReporter drops RunItems' own progress and current-item calls. The walk
// reports its own absolute (processed, total, message) through the progress
// callback; letting RunItems report the position within the pool's index list
// as well would make the bar jump between two numberings.
type quietReporter struct{ sdk.Reporter }

func (quietReporter) UpdateProgress(int, int, string) error { return nil }
func (quietReporter) SetCurrentItem(string)                 {}

// bootWalkRun builds the resume plumbing of one attempt: the saved position
// from params, a Checkpoint that persists params with an updated position
// (reporter.Checkpoint, the registry's one checkpoint format), the worker pool
// (opsregistry.RunItems; Concurrency is set, it defaults to one), and, when
// canYield, a YieldDue that fires near the deadline.
func bootWalkRun(ctx context.Context, reporter sdk.Reporter, params bootBackfillParams, workers int, canYield bool) walkresume.Run {
	run := walkresume.Run{
		Resume: params.Resume,
		Checkpoint: func(st walkresume.State) {
			next := params
			next.Resume = st
			if err := reporter.Checkpoint(next); err != nil {
				_ = reporter.Log(slog.LevelWarn, "Could not save the resume point; a restart would redo more", slog.String("error", err.Error()))
			}
		},
		Exec: func(ctx context.Context, n, resumeFrom int, fn func(context.Context, int) error, onWatermark func(context.Context, int)) error {
			idx := make([]int, n)
			for i := range idx {
				idx[i] = i
			}
			return opsregistry.RunItems(ctx, quietReporter{reporter}, idx, fn, opsregistry.RunItemsOptions{
				Concurrency: workers,
				// Collect, not Fail: a yield makes every not-yet-started item
				// return at once, and the first of those must not cancel the
				// items already in flight (an ffmpeg killed mid-file).
				ErrMode:         opsregistry.ErrModeCollect,
				ResumeFrom:      resumeFrom,
				CheckpointEvery: walkresume.CheckpointEvery,
				CheckpointStateFn: func(ctx context.Context, watermark int) error {
					onWatermark(ctx, watermark)
					return nil
				},
			})
		},
		Progress: func(processed, total int, msg string) { _ = reporter.UpdateProgress(processed, total, msg) },
	}
	if canYield {
		run.YieldDue = newYieldDue(ctx, bootYieldMargin)
	}
	return run
}

// newYieldDue returns the YieldDue of one attempt. It is called once per
// checkpoint, serialized. The first call only starts the clock: the time to
// collect the candidates is not a checkpoint gap.
func newYieldDue(ctx context.Context, margin time.Duration) func() bool {
	var last time.Time
	var slowest time.Duration
	return func() bool {
		now := time.Now()
		if !last.IsZero() {
			slowest = max(slowest, now.Sub(last))
		}
		last = now
		dl, ok := ctx.Deadline()
		return ok && time.Until(dl) < max(margin, 2*slowest)
	}
}

// yielded turns a walk's ErrYield into the registry's continue-in-place
// sentinel; any other error is returned as is.
func yielded(reporter sdk.Reporter, what string, err error) error {
	if !errors.Is(err, walkresume.ErrYield) {
		return err
	}
	_ = reporter.Log(slog.LevelInfo, what+": pausing near this run's time limit; the operation restarts under the same id and continues from its saved position")
	return fmt.Errorf("%s: %w", what, opsregistry.ErrContinueInPlace)
}

func decodeBootBackfillParams(opID string, raw json.RawMessage) (bootBackfillParams, error) {
	var params bootBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return params, fmt.Errorf("%s: params: %w", opID, err)
		}
	}
	return params, nil
}

func (p *Plugin) externalIDBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.external-id-backfill",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "External ID backfill",
		Description:     "Backfill of external IDs (iTunes PIDs, etc.) from the existing database. Runs in full whenever triggered, even after an earlier pass has recorded completion, unless enqueued with {\"if_needed\": true} (as boot does).",
		ResumePolicy:    sdk.ResumeRestart, // 04-P6: checkpoints a book-ID cursor / XML track index and yields near its deadline
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.external-id-backfill",
		Cancellable:     true, // honours ctx at every book/file; a cancelled run leaves its done flag unset
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runExternalIDBackfill,
	}
}

func (p *Plugin) runExternalIDBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeBootBackfillParams("external-id-backfill", raw)
	if err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelDebug, "Starting external ID backfill", slog.Bool("if_needed", params.IfNeeded))
	skipped, err := p.deps.BackfillExternalIDs(ctx, params.IfNeeded, func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	}, bootWalkRun(ctx, reporter, params, 1, true))
	if errors.Is(err, walkresume.ErrYield) {
		return yielded(reporter, "External ID backfill", err)
	}
	if err != nil {
		_ = reporter.Log(slog.LevelError, "External ID backfill failed", slog.String("error", err.Error()))
		return err
	}
	if skipped {
		_ = reporter.Log(slog.LevelInfo, "External ID backfill already complete; nothing to do", opsregistry.NoOpAttr())
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelInfo, "External ID backfill complete")
	return nil
}

func (p *Plugin) movementAtomCleanupDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.movement-atom-cleanup",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Strip movement atoms",
		Description:     "Strips unwanted movement atoms from M4B files that cause chapter parsing issues.",
		ResumePolicy:    sdk.ResumeRestart, // 04-P6: checkpoints a file-path cursor and yields near its deadline
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.movement-atom-cleanup",
		Cancellable:     true,  // honours ctx at every book/file; a cancelled run leaves its done flag unset
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         60 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite, sdk.CapSubprocessSpawn},
		Run:             p.runMovementAtomCleanup,
	}
}

func (p *Plugin) runMovementAtomCleanup(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeBootBackfillParams("movement-atom-cleanup", raw)
	if err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelDebug, "Starting movement atom cleanup")
	alreadyDone, err := p.deps.StripMovementAtoms(ctx, bootWalkRun(ctx, reporter, params, bootWalkWorkers, true))
	if err != nil {
		return yielded(reporter, "Movement atom cleanup", err)
	}
	if alreadyDone {
		_ = reporter.Log(slog.LevelInfo, "Movement atom cleanup already complete; nothing to do", opsregistry.NoOpAttr())
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelInfo, "Movement atom cleanup complete")
	return nil
}

func (p *Plugin) malformedM4BRemuxDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.malformed-m4b-remux",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Remux malformed M4B files",
		Description:     "Remuxes M4B files with broken container structure without re-encoding audio.",
		ResumePolicy:    sdk.ResumeRestart, // 04-P6: checkpoints a file-path cursor and yields near its deadline
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.malformed-m4b-remux",
		Cancellable:     true,  // honours ctx at every book/file; a cancelled run leaves its done flag unset
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite, sdk.CapSubprocessSpawn},
		Run:             p.runMalformedM4BRemux,
	}
}

func (p *Plugin) runMalformedM4BRemux(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeBootBackfillParams("malformed-m4b-remux", raw)
	if err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelDebug, "Starting malformed M4B remux")
	alreadyDone, err := p.deps.RemuxMalformedM4BFiles(ctx, func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	}, bootWalkRun(ctx, reporter, params, bootWalkWorkers, true))
	if errors.Is(err, walkresume.ErrYield) {
		return yielded(reporter, "Malformed M4B remux", err)
	}
	if err != nil {
		_ = reporter.Log(slog.LevelError, "Malformed M4B remux failed", slog.String("error", err.Error()))
		return err
	}
	if alreadyDone {
		_ = reporter.Log(slog.LevelInfo, "Malformed M4B remux already complete; nothing to do", opsregistry.NoOpAttr())
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelInfo, "Malformed M4B remux complete")
	return nil
}

// Hard rule: transcode = ResumeAsk (destructive; operator must confirm).

func (p *Plugin) malformedM4BTranscodeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.malformed-m4b-transcode",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "Transcode malformed M4B files",
		Description:     "Full re-encode of M4B files that cannot be remuxed. Interrupted runs surface in UI for operator confirmation.",
		ResumePolicy:    sdk.ResumeAsk,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.malformed-m4b-transcode",
		Cancellable:     true,
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         6 * time.Hour,
		// C2: a full AAC re-encode can take minutes per file, so the every-25
		// -files progress cadence can leave a long gap between UpdateProgress
		// stamps. The registry watchdog's default ProgressTimeout is 5 minutes
		// (see internal/operations/registry/watchdog.go) — without an explicit
		// override here a slow but healthy transcode run risks being killed as
		// "stuck". 30m matches the precedent in intro_transcribe.go for a
		// similarly slow per-item op.
		ProgressTimeout: 30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite, sdk.CapSubprocessSpawn},
		Run:             p.runMalformedM4BTranscode,
	}
}

// runMalformedM4BTranscode transcodes, then, with {"quarantine_after": true}
// (boot passes it), quarantines the files the transcode could not fix: the two
// steps the old boot goroutine ran in sequence. As there, the quarantine runs
// even when the transcode failed, since it acts on files earlier transcode
// runs marked; both errors are returned.
func (p *Plugin) runMalformedM4BTranscode(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, err := decodeBootBackfillParams("malformed-m4b-transcode", raw)
	if err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelDebug, "Starting malformed M4B transcode", slog.Bool("quarantine_after", params.QuarantineAfter))
	// canYield is false: the transcode is ResumeAsk, and the registry honours a
	// continue-in-place yield only for ResumeRestart, so it would be a failure.
	// The checkpoint is what an operator's Retry of an interrupted run uses.
	alreadyDone, transcodeErr := p.deps.TranscodeMalformedM4BFiles(ctx, func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	}, bootWalkRun(ctx, reporter, params, bootTranscodeWorkers, false))
	if transcodeErr != nil {
		_ = reporter.Log(slog.LevelError, "Malformed M4B transcode failed", slog.String("error", transcodeErr.Error()))
	}
	quarantined := 0
	var quarantineErr error
	if params.QuarantineAfter {
		var qDone bool
		quarantined, qDone, quarantineErr = p.deps.QuarantineKnownBadFiles(ctx)
		switch {
		case quarantineErr != nil:
			_ = reporter.Log(slog.LevelError, "Quarantine of known-bad files failed",
				slog.Int("quarantined", quarantined), slog.String("error", quarantineErr.Error()))
		case !qDone:
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Quarantined %d known-bad file(s)", quarantined))
		}
	}
	if err := errors.Join(transcodeErr, quarantineErr); err != nil {
		return err
	}
	if alreadyDone && quarantined == 0 {
		_ = reporter.Log(slog.LevelInfo, "Malformed M4B transcode already complete; nothing to do", opsregistry.NoOpAttr())
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = reporter.Log(slog.LevelInfo, "Malformed M4B transcode complete")
	return nil
}
