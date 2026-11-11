// file: core/internal/taskhttp/ports.go
// version: 1.0.0
// guid: 3875c5a2-c9a6-4714-87cf-90f231a75f26
// last-edited: 2026-11-11

package taskhttp

import (
	"context"
	"iter"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

// Approver is the Fixer approval: tasks.Executor's Approve. It acts as the
// caller through the executor's Principal, and refuses a caller without the
// definition's Perms.Approve itself; this package adds the verified-method
// rule in front (SEC-24).
type Approver interface {
	Approve(ctx context.Context, id tasks.RunID, a tasks.ApproveRequest) error
}

// RunReader is the read side of the run store that Client does not expose.
// tasks.RunStore satisfies it. A route calls it only after Client.Get
// returned the run, so the definition's View rule has been applied.
type RunReader interface {
	Counts(ctx context.Context) (map[tasks.RunState]int64, error)
	Rows(ctx context.Context, id tasks.RunID) iter.Seq2[tasks.Row, error]
	Pages(ctx context.Context, id tasks.RunID, q tasks.PageQuery) iter.Seq2[tasks.PageEntry, error]
}

// ConfigPort is the part of *config.Store the schedule routes use: Lookup
// reads a schedule key's effective value and who locks it, Apply writes the
// override and records core.settings.update with the write.
type ConfigPort interface {
	Lookup(p config.Path) (config.KeyState, bool)
	Apply(ctx context.Context, p config.Patch, a config.Actor) (config.Result, error)
}
