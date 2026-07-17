// file: internal/plugins/itunes/path_repair.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1234-efgh-345678901234
// last-edited: 2026-07-17

package itunes

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) pathRepairDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:                    "itunes.path-repair",
		Plugin:                "itunes",
		DisplayName:           "iTunes Path Repair",
		Description:           "Repair iTunes track paths that reference stale file locations.",
		Isolate:               false,
		ResumePolicy:          sdk.ResumeRestart,
		DefaultPriority:       sdk.PriorityNormal,
		Cancellable:           true,
		Timeout:               120 * time.Minute,
		ConcurrencyKey:        "itunes.path-repair",
		MinCheckpointInterval: 30 * time.Second,
		Run:                   p.runPathRepair,
	}
}

func (p *Plugin) runPathRepair(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	// TODO: Implement iTunes path repair operation.
	// This should call p.svc.Repair.Repair(ctx, opID, dryRun, progress).
	_ = reporter.Log(slog.LevelWarn, "op not implemented — no-op", slog.String("def_id", "itunes.path-repair"))
	return nil
}
