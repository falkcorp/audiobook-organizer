// file: internal/plugins/itunes/sync.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-07-17

package itunes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) syncDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "itunes.sync",
		Liveness: sdk.LivenessNone,
		ProgressTimeout: 120 * time.Minute, // LivenessNone requires an explicit budget
		Plugin:      "itunes",
		DisplayName: "iTunes Library Sync",
		Description: "Sync audiobook metadata with the iTunes/Music library.",
		// Schedule removed 2026-07-17: Run is an unimplemented stub, so the
		// "*/30 * * * *" cron burned a green no-op op-history row every 30
		// minutes. Restore the schedule when the sync is actually implemented.
		Isolate:               false,
		ResumePolicy:          sdk.ResumeRestart,
		DefaultPriority:       sdk.PriorityNormal,
		Cancellable:           false,
		Timeout:               120 * time.Minute,
		ConcurrencyKey:        "itunes.sync",
		MinCheckpointInterval: 30 * time.Second,
		Run:                   p.runSync,
	}
}

func (p *Plugin) runSync(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	// TODO: Implement iTunes sync operation.
	// This should call p.svc.Importer.Sync with appropriate path mappings.
	return errNotImplemented("itunes.sync")
}
