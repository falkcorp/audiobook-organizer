// file: internal/plugins/deluge/protected_paths.go
// version: 1.3.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-11-19

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// protectedPathsSyncKind is rule R3's native kind for deluge.protected-paths-sync.
const protectedPathsSyncKind = tasks.Kind("books.deluge.protected-paths-sync")

// protectedPathsSyncHelp is HEAD's Description and the native Help (rule R5).
const protectedPathsSyncHelp = "Refreshes the protected-path cache from Deluge torrent save paths."

func (p *Plugin) protectedPathsSyncDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "deluge.protected-paths-sync",
		Liveness:        sdk.LivenessManual,
		Plugin:          "deluge",
		DisplayName:     "Sync protected paths from Deluge",
		Description:     protectedPathsSyncHelp,
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

// protectedRefresher is the one call the core makes on the protected-path cache:
// a fetch from Deluge now, whatever the cache's TTL says.
type protectedRefresher interface {
	Refresh() error
}

// protectedPathsSyncEnv is what the core takes from its shell. The sync writes
// no store: the cache is in memory, so there is no Store or Writer here.
type protectedPathsSyncEnv struct {
	kindEnv
	Cache protectedRefresher
}

// protectedPathsSyncResult is the native run's result. HEAD stored none.
type protectedPathsSyncResult struct{}

// protectedPathsSyncCore is the body of runProtectedPathsSync, shared by both
// shells.
func protectedPathsSyncCore(_ context.Context, env protectedPathsSyncEnv) (protectedPathsSyncResult, error) {
	if env.Cache == nil {
		return protectedPathsSyncResult{}, errors.New("deluge.protected-paths-sync: the protected-path cache is not configured")
	}
	prog := newProgress(env.kindEnv, 1)
	prog.Start("Refreshing protected paths from Deluge...")

	// Fetch now, whatever the cache TTL says. This used to be Invalidate()
	// followed by IsProtected(""), which fetched nothing: an empty path is
	// answered before the cache looks at its TTL.
	if err := env.Cache.Refresh(); err != nil {
		return protectedPathsSyncResult{}, err
	}
	prog.Step("Cache refreshed")

	prog.Finalize("Writing results...")
	prog.Done("Protected paths refreshed")
	return protectedPathsSyncResult{}, nil
}

// runProtectedPathsSync is the v2 shell: HEAD's Run over protectedPathsSyncCore.
func (p *Plugin) runProtectedPathsSync(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	env := protectedPathsSyncEnv{kindEnv: headEnv(reporter, true)}
	// An untyped nil when the plugin has no cache: a typed nil would pass the
	// core's check and fail inside Refresh.
	if p.cache != nil {
		env.Cache = p.cache
	}
	_, err := protectedPathsSyncCore(ctx, env)
	return err
}
