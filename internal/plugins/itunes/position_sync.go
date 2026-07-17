// file: internal/plugins/itunes/position_sync.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-fghi-456789012345
// last-edited: 2026-07-17

package itunes

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) positionSyncDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "itunes.position-sync",
		Plugin:      "itunes",
		DisplayName: "iTunes Position Sync",
		Description: "Sync reading positions between iTunes bookmarks and the app.",
		// Schedule removed 2026-07-17: Run is an unimplemented stub, so the
		// "*/10 * * * *" cron burned a green no-op op-history row every 10
		// minutes. Restore the schedule when position sync is implemented.
		Isolate:               false,
		ResumePolicy:          sdk.ResumeRequeue,
		DefaultPriority:       sdk.PriorityNormal,
		Cancellable:           false,
		Timeout:               30 * time.Minute,
		ConcurrencyKey:        "itunes.position-sync",
		MinCheckpointInterval: 0, // Use default
		Run:                   p.runPositionSync,
	}
}

func (p *Plugin) runPositionSync(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	// TODO: Implement iTunes position sync operation.
	// This should call p.svc.Positions.Sync().
	_ = reporter.Log(slog.LevelWarn, "op not implemented — no-op", slog.String("def_id", "itunes.position-sync"))
	return nil
}
