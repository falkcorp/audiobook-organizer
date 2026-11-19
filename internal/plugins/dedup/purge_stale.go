// file: internal/plugins/dedup/purge_stale.go
// version: 1.2.0
// guid: 7f3e1b2c-4a8d-4e1f-9c2a-3e5b8d0a1c47
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

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
		Liveness:        sdk.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Cleanup stale dedup candidates",
		Description:     "Deletes pending dedup candidates that are no longer valid (chapter files in same folder, same version-group, distinct series volumes).",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh, // user-triggered, finishes in seconds
		NoWarmupWait:    true,             // so it does not wait out the post-restart warmup
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

// purgeStaleKind is rule R3's native kind for dedup.purge-stale.
const purgeStaleKind = tasks.Kind("books.dedup.purge-stale")

// purgeStaleEnv is what the purge-stale core takes from its shell (rules R2 and
// R10): the run's hooks, and the engine it reaches through the shell, never
// through a receiver.
type purgeStaleEnv struct {
	kindEnv
	Engine scoringEngine
}

// purgeStaleResult is the native run's result: how many stale candidates went.
// HEAD stored no result for this definition, so its shell drops it.
type purgeStaleResult struct {
	Deleted int `json:"deleted"`
}

// purgeStaleCore is the body of runPurgeStale, shared by both shells.
func purgeStaleCore(ctx context.Context, env purgeStaleEnv) (purgeStaleResult, error) {
	env.Progress(0, 1, "Scanning pending candidates for stale rows…")
	deleted, err := env.Engine.PurgeStaleCandidates(ctx)
	if err != nil {
		env.Logger.Error("purge stale candidates error", "error", err)
		return purgeStaleResult{}, fmt.Errorf("purge stale candidates: %w", err)
	}
	env.Progress(1, 1, fmt.Sprintf("Cleanup complete — %d stale candidate(s) removed", deleted))
	env.Logger.Info("purged stale candidates", "deleted", deleted)
	return purgeStaleResult{Deleted: deleted}, nil
}

// runPurgeStale is the v2 shell: HEAD's Run over purgeStaleCore.
func (p *Plugin) runPurgeStale(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	_, err := purgeStaleCore(ctx, purgeStaleEnv{kindEnv: headEnv(reporter, true), Engine: p.engine})
	return err
}
