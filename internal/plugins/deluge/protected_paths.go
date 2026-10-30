// file: internal/plugins/deluge/protected_paths.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-30

package deluge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) protectedPathsSyncDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "deluge.protected-paths-sync",
		Liveness:        sdk.LivenessManual,
		Plugin:          "deluge",
		DisplayName:     "Sync protected paths from Deluge",
		Description:     "Refreshes the protected-path cache from Deluge torrent save paths.",
		ResumePolicy:    sdk.ResumeRequeue,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "deluge.protected-paths-sync",
		Cancellable:     false,
		Isolate:         false,
		Timeout:         5 * time.Minute,
		Run:             p.runProtectedPathsSync,
		// Scheduled to run every 30 minutes (matches the TTL of the cache).
		Schedule: new("*/30 * * * *"),
	}
}

func (p *Plugin) runProtectedPathsSync(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	prog := sdk.NewProgress(reporter, 1)
	prog.Start("Refreshing protected paths from Deluge...")

	// Fetch now, whatever the cache TTL says. This used to be Invalidate()
	// followed by IsProtected(""), which fetched nothing: an empty path is
	// answered before the cache looks at its TTL.
	if err := p.cache.Refresh(); err != nil {
		return err
	}
	prog.Step("Cache refreshed")

	prog.Finalize("Writing results...")
	prog.Done("Protected paths refreshed")
	return nil
}
