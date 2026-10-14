// file: internal/plugins/maintenance/reconcile.go
// version: 1.5.0
// guid: b8c9d0e1-f2a3-4567-1234-789012345678
// last-edited: 2026-10-14

package maintenance

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// itunesHealDef registers the iTunes XML-driven path healing operation.
// It parses the iTunes Library.xml, finds tracks whose expected path is
// missing on disk, locates the file via filename+author+track-number
// matching, and refllinks it back — 16 workers, completes in minutes.
func (p *Plugin) itunesHealDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID: "itunes.heal",
		// Renamed 2026-09-25 (naming audit class 8): every iTunes op lives in itunes.*, whichever Go package implements it.
		FormerIDs:       []string{"maintenance.itunes-heal"},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "iTunes path heal",
		Description:     "Heals iTunes tracks moved by the organize operation: parses iTunes XML, finds each missing file in the library by filename/author/track, and reflinks it back to the expected path.",
		ResumePolicy:    sdk.ResumeRestart, // reflink is idempotent
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "itunes.heal",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runITunesHeal,
	}
}

func (p *Plugin) runITunesHeal(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	return reconcile.RunITunesHeal(ctx, p.deps.ReconcileStore(), reporter, params)
}
