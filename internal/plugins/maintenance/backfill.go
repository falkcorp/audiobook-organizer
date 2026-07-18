// file: internal/plugins/maintenance/backfill.go
// version: 1.3.0
// guid: f2a3b4c5-d6e7-8901-5678-123456789012
// last-edited: 2026-07-18

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// One-shot startup backfills / file repairs. Schedule is nil — they are
// enqueued once at startup by server.Start() and not repeated. ResumeDrop
// because they are idempotent (guarded by skip-keys) and short enough that
// re-running from zero on restart is safe.

func (p *Plugin) externalIDBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.external-id-backfill",
		Plugin:          "maintenance",
		DisplayName:     "External ID backfill",
		Description:     "One-shot backfill of external IDs (iTunes PIDs, etc.) from the existing database.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.external-id-backfill",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runExternalIDBackfill,
	}
}

func (p *Plugin) runExternalIDBackfill(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelInfo, "Starting external ID backfill")
	err := p.deps.BackfillExternalIDs(func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	})
	if err != nil {
		_ = reporter.Log(slog.LevelError, "External ID backfill failed", slog.String("error", err.Error()))
		return err
	}
	_ = reporter.Log(slog.LevelInfo, "External ID backfill complete")
	return nil
}

func (p *Plugin) movementAtomCleanupDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.movement-atom-cleanup",
		Plugin:          "maintenance",
		DisplayName:     "Strip movement atoms",
		Description:     "Strips unwanted movement atoms from M4B files that cause chapter parsing issues.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.movement-atom-cleanup",
		Cancellable:     false,
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         60 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite, sdk.CapSubprocessSpawn},
		Run:             p.runMovementAtomCleanup,
	}
}

func (p *Plugin) runMovementAtomCleanup(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelInfo, "Starting movement atom cleanup")
	p.deps.StripMovementAtoms(ctx)
	_ = reporter.Log(slog.LevelInfo, "Movement atom cleanup complete")
	return nil
}

func (p *Plugin) malformedM4BRemuxDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.malformed-m4b-remux",
		Plugin:          "maintenance",
		DisplayName:     "Remux malformed M4B files",
		Description:     "Remuxes M4B files with broken container structure without re-encoding audio.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.malformed-m4b-remux",
		Cancellable:     false,
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         120 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite, sdk.CapSubprocessSpawn},
		Run:             p.runMalformedM4BRemux,
	}
}

func (p *Plugin) runMalformedM4BRemux(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelInfo, "Starting malformed M4B remux")
	err := p.deps.RemuxMalformedM4BFiles(ctx, func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	})
	if err != nil {
		_ = reporter.Log(slog.LevelError, "Malformed M4B remux failed", slog.String("error", err.Error()))
		return err
	}
	_ = reporter.Log(slog.LevelInfo, "Malformed M4B remux complete")
	return nil
}

// Hard rule: transcode = ResumeAsk (destructive; operator must confirm).

func (p *Plugin) malformedM4BTranscodeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.malformed-m4b-transcode",
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

func (p *Plugin) runMalformedM4BTranscode(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	_ = reporter.Log(slog.LevelInfo, "Starting malformed M4B transcode")
	err := p.deps.TranscodeMalformedM4BFiles(ctx, func(processed, total int, msg string) {
		_ = reporter.UpdateProgress(processed, total, msg)
	})
	if err != nil {
		_ = reporter.Log(slog.LevelError, "Malformed M4B transcode failed", slog.String("error", err.Error()))
		return err
	}
	_ = reporter.Log(slog.LevelInfo, "Malformed M4B transcode complete")
	return nil
}
