// file: internal/plugins/maintenance/backfill.go
// version: 1.7.0
// guid: f2a3b4c5-d6e7-8901-5678-123456789012
// last-edited: 2026-10-15

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// One-shot startup backfills / file repairs. Schedule is nil — they are
// enqueued once per boot by Server.startBackfills (enqueueBootOps) and not
// repeated, and an operator can run any of them by hand. ResumeDrop because
// they are idempotent (guarded by skip-keys) and short enough that re-running
// from zero on restart is safe; the transcode is the exception (ResumeAsk).
// Each waits for the store's startup warmup before it runs: the registry's
// dispatcher holds every def without NoWarmupWait (D44, warmup_gate.go).
//
// A run that found nothing to do logs its summary with opsregistry.NoOpAttr,
// so the six boot runs that are usually no-ops stay out of the default
// activity feed.

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
		ResumePolicy:    sdk.ResumeDrop,
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
	_ = reporter.Log(slog.LevelInfo, "Starting external ID backfill", slog.Bool("if_needed", params.IfNeeded))
	skipped, err := p.deps.BackfillExternalIDs(ctx, params.IfNeeded, func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	})
	if err != nil {
		_ = reporter.Log(slog.LevelError, "External ID backfill failed", slog.String("error", err.Error()))
		return err
	}
	if skipped {
		_ = reporter.Log(slog.LevelInfo, "External ID backfill already complete; nothing to do", opsregistry.NoOpAttr())
		return nil
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
		ResumePolicy:    sdk.ResumeDrop,
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

func (p *Plugin) runMovementAtomCleanup(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelInfo, "Starting movement atom cleanup")
	if p.deps.StripMovementAtoms(ctx) {
		_ = reporter.Log(slog.LevelInfo, "Movement atom cleanup already complete; nothing to do", opsregistry.NoOpAttr())
		return nil
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
		ResumePolicy:    sdk.ResumeDrop,
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

func (p *Plugin) runMalformedM4BRemux(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelInfo, "Starting malformed M4B remux")
	alreadyDone, err := p.deps.RemuxMalformedM4BFiles(ctx, func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	})
	if err != nil {
		_ = reporter.Log(slog.LevelError, "Malformed M4B remux failed", slog.String("error", err.Error()))
		return err
	}
	if alreadyDone {
		_ = reporter.Log(slog.LevelInfo, "Malformed M4B remux already complete; nothing to do", opsregistry.NoOpAttr())
		return nil
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
	_ = reporter.Log(slog.LevelInfo, "Starting malformed M4B transcode", slog.Bool("quarantine_after", params.QuarantineAfter))
	alreadyDone, transcodeErr := p.deps.TranscodeMalformedM4BFiles(ctx, func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	})
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
	_ = reporter.Log(slog.LevelInfo, "Malformed M4B transcode complete")
	return nil
}
