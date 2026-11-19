// file: internal/plugins/itunes/position_sync.go
// version: 1.3.0
// guid: f6a7b8c9-d0e1-2345-fghi-456789012345
// last-edited: 2026-11-19

package itunes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// positionSyncDefID is HEAD's def ID for the op below; the native kind is
// "books." + this (kinds.go).
const positionSyncDefID = "itunes.position-sync"

// positionSyncTitle and positionSyncHelp are the def's DisplayName and
// Description, which the native kind carries as its Title and Help.
const (
	positionSyncTitle = "iTunes Position Sync"
	positionSyncHelp  = "Sync reading positions between iTunes bookmarks and the app."
)

func (p *Plugin) positionSyncDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          positionSyncDefID,
		Liveness:    sdk.LivenessManual,
		Plugin:      "itunes",
		DisplayName: positionSyncTitle,
		Description: positionSyncHelp,
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
	return positionSyncCore()
}

// positionSyncCore is the body of runPositionSync, shared with the native kind
// (kinds.go). It reads and writes nothing, so it takes no env: the stub only
// refuses.
func positionSyncCore() error {
	// No implementation: iTunes is a one-time manual import (owner
	// decision), and the never-wired service-side PositionSync was deleted
	// on 2026-10-18. Whether to drop this stub op is the owner's call.
	return errNotImplemented(positionSyncDefID)
}
