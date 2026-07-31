// file: internal/maintenance/jobs/abs_backfill_reporter.go
// version: 1.0.0
// guid: c17b031b-c063-4d55-bdf7-7e01c4fe0969
// last-edited: 2026-07-30

package jobs

import (
	"context"
	"log/slog"
	"runtime"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// BackfillConcurrency is the worker-pool size every ABS-sync backfill job in
// this package passes to registry.RunItems.
//
// It is a function, not a bare const, for two reasons: the value depends on the
// host (runtime.NumCPU()), and a test needs to read it to assert it is > 1.
// RunItemsOptions.Concurrency treats 0 AND 1 as "sequential", and a sequential
// loop over tens of thousands of books doing per-item Pebble writes is the
// exact shape that stalled a dedup.full-scan run for 3+ hours at 100% CPU on a
// single core on 2026-07-05 (see CLAUDE.md's concurrency rule and
// docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md). The work
// here is local Pebble I/O — CPU/disk-bound, not network-bound — so NumCPU
// sizing applies rather than a small fixed network-politeness limit.
func BackfillConcurrency() int {
	if n := runtime.NumCPU(); n > 1 {
		return n
	}
	// A single-CPU host still gets 2 workers: the per-item cost is dominated by
	// Pebble commit latency, not CPU, so one in-flight write per core would
	// leave the store idle between batches — and returning 1 here would make
	// RunItems silently sequential.
	return 2
}

// absBackfillReporter bridges maintenance.ProgressReporter (the 3-method
// interface a MaintenanceJob's Run receives) to registry.Reporter (the
// 7-method interface registry.RunItems needs to drive its worker pool).
//
// This mirrors internal/server/embedding_backfill.go's embeddingBackfillReporter
// and exists for the same reason its comment gives: a plain maintenance job,
// like that background goroutine, has no registry.Reporter already in scope.
// Most methods are deliberately thin or no-op — the maintenance side has no
// equivalent concept for checkpoints, phases, or event triggers.
//
// The name is task-unique on purpose: several agents add helpers to this
// package in parallel, and a generically-named shared helper is a known
// post-merge collision source in this repo.
type absBackfillReporter struct {
	ctx   context.Context
	inner maintenance.ProgressReporter
}

var _ registry.Reporter = (*absBackfillReporter)(nil)

// UpdateProgress ignores current/total: maintenance.ProgressReporter counts
// completions itself via SetTotal + Increment, and RunItems already calls this
// exactly once per completed item (monotonically, even in parallel mode).
func (r *absBackfillReporter) UpdateProgress(_, _ int, _ string) error {
	r.inner.Increment()
	return nil
}

func (r *absBackfillReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	slog.Default().LogAttrs(context.Background(), level, message, attrs...)
	return nil
}

func (r *absBackfillReporter) Logger() *slog.Logger { return slog.Default() }

// Checkpoint is a no-op: these backfills are idempotent per item, so
// re-running from the start after an interruption is the resume story and
// there is no index worth persisting.
func (r *absBackfillReporter) Checkpoint(_ any) error { return nil }

func (r *absBackfillReporter) IsCanceled() bool { return r.ctx != nil && r.ctx.Err() != nil }

func (r *absBackfillReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}

func (r *absBackfillReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }

// SetCurrentItem is dropped: the ephemeral "currently working on" label fans
// out via the operations SSE stream, which a maintenance.ProgressReporter has
// no channel for.
func (r *absBackfillReporter) SetCurrentItem(_ string) {}
