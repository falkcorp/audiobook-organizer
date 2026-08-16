// file: internal/plugins/dedup/purge_stale.go
// version: 1.0.0
// guid: 7f3e1b2c-4a8d-4e1f-9c2a-3e5b8d0a1c47

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// purgeStaleDef defines the dedup.purge-stale operation. It's a fast,
// synchronous-feeling sweep over pending candidates that removes the ones
// that are no longer real duplicates: chapter files in the same parent
// directory, books already in the same version group, distinct numbered
// series volumes, etc. Implementation lives in
// dedup.Engine.PurgeStaleCandidates; this op exists so the user gets bell
// progress + start/end log lines + a queryable history of cleanup runs.
//
// Why an op and not a plain HTTP handler: the user explicitly asked for
// every user-triggered backend action to show up in the bell. The previous
// version (PR #1200) was a sync HTTP handler that silently returned a
// count, so the bell never knew anything happened.
func (p *Plugin) purgeStaleDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.purge-stale",
		Liveness: sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Cleanup stale dedup candidates",
		Description:     "Deletes pending dedup candidates that are no longer valid (chapter files in same folder, same version-group, distinct series volumes).",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh, // user-triggered, finishes in seconds
		ConcurrencyKey:  "dedup.purge-stale",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         10 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runPurgeStale,
	}
}

func (p *Plugin) runPurgeStale(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}

	_ = reporter.UpdateProgress(0, 1, "Scanning pending candidates for stale rows…")
	deleted, err := p.engine.PurgeStaleCandidates(ctx)
	if err != nil {
		reporter.Logger().Error("purge stale candidates error", "error", err)
		return fmt.Errorf("purge stale candidates: %w", err)
	}
	_ = reporter.UpdateProgress(1, 1,
		fmt.Sprintf("Cleanup complete — %d stale candidate(s) removed", deleted))
	reporter.Logger().Info("purged stale candidates", "deleted", deleted)
	return nil
}
