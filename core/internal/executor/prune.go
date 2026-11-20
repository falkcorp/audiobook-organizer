// file: core/internal/executor/prune.go
// version: 1.0.0
// guid: c3df2324-1f30-4bbd-9988-21fd398d67ff
// last-edited: 2026-11-18

package executor

import (
	"context"
	"fmt"
	"iter"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

const (
	// KindPrune is the run-history retention kind (05-tasks 5T.8.2, 5T.10):
	// the operation-record half of HEAD's maintenance.retention-and-hygiene.
	KindPrune tasks.Kind = "core.tasks.prune"

	// PruneScheduleKey is the schedule key of the kind: a tasks.ScheduleConfig
	// declared by core/tasks as core.tasks.schedules.prune (5T.10), default
	// every 24h, re-read every poll. This file only passes it to
	// tasks.ScheduleFromConfig.
	PruneScheduleKey = "core.tasks.schedules.prune"

	// PruneTimeout bounds one pass. A pass is chunked and stamps liveness after
	// every chunk, so the watchdog's derived gap (a sixth of this) is the
	// longest one chunk may take.
	PruneTimeout = time.Hour

	// pruneChunk is how many runs one RunStore.Prune call may delete. The
	// store commits its own batches inside a call (64 runs each), so a chunk
	// bounds how long the run goes without reporting, not the size of a
	// transaction.
	pruneChunk = 1000
)

// RunPruner is the part of tasks.RunStore the kind uses.
type RunPruner interface {
	Prune(ctx context.Context, before time.Time, limit int) (deleted int, err error)
}

// PruneParams is empty: each pass reads the retention afresh.
type PruneParams struct{}

// PruneResult is what a pass did. Kept is true when the retention was zero or
// negative, which keeps every run.
type PruneResult struct {
	Deleted int64 `json:"deleted"`
	Kept    bool  `json:"kept,omitempty"`
}

// PruneDefinition returns the definition of core.tasks.prune. retention reads
// core.tasks.history_retention at the start of each run, so a changed value
// applies to the next pass; a value of zero or less keeps every run. now is
// the clock (nil = time.Now). A nil runs or retention yields a definition
// whose Run is nil, which tasks.Problems reports, so registration fails closed.
//
// The pass calls RunStore.Prune until a call deletes fewer runs than it was
// allowed, which is how that store reports having reached the cutoff. Prune
// deletes terminal runs only, so the pass never touches an active run or its
// own. A run is the unit of retention: every record a run owns goes with it.
//
// Perms.Start is core:tasks.manage for the reason core.outbox.relay's is: the
// scheduler starts the kind as core's system actor, which holds no
// AuthRequired scope (SEC2-15).
//
//tasklint:constructor the kernel passes the result to the executor's definition list from the tasks flip F5 (F5-18); nothing registers it before
func PruneDefinition(runs RunPruner, retention func() time.Duration, now func() time.Time) tasks.Definition {
	var run func(rc *tasks.RC, _ PruneParams) (PruneResult, error)
	if runs != nil && retention != nil {
		if now == nil {
			now = time.Now
		}
		run = func(rc *tasks.RC, _ PruneParams) (PruneResult, error) {
			return pruneRuns(rc, runs, retention(), now())
		}
	}
	return tasks.Task(KindPrune, tasks.TaskSpec[PruneParams, PruneResult]{
		Title:    "Prune old task history",
		Help:     "Deletes finished task runs, and everything each one recorded, once they are older than the history retention. Running and waiting runs are never touched.",
		Effects:  tasks.Writes(tasks.Res("core", "tasks")),
		Perms:    tasks.Permissions{Start: rbac.Need("core:tasks.manage")},
		Resume:   tasks.ResumeFromZero,
		Priority: tasks.PriorityLow,
		Timeout:  PruneTimeout,
		Schedule: tasks.ScheduleFromConfig(PruneScheduleKey).Live(),
		Run:      run,
	})
}

func pruneRuns(rc *tasks.RC, runs RunPruner, keep time.Duration, now time.Time) (PruneResult, error) {
	if keep <= 0 {
		rc.Progress().Note("history retention is not positive: every run is kept")
		return PruneResult{Kept: true}, nil
	}
	cutoff := now.Add(-keep)
	var more atomic.Bool
	more.Store(true)
	// One worker takes one chunk at a time. RunSeq, not a bare loop, so the
	// pass honours cancel and the pause gate between chunks and stamps
	// liveness as each chunk lands.
	chunks := iter.Seq[int](func(yield func(int) bool) {
		for i := 0; more.Load(); i++ {
			if !yield(i) {
				return
			}
		}
	})
	res, err := tasks.RunSeq(rc, tasks.Sequential("one chunk of deletes at a time: each commits its own batches"), chunks, tasks.Unknown(),
		func(ic *tasks.ItemRC, _ int) error {
			n, err := runs.Prune(ic.Context(), cutoff, pruneChunk)
			ic.Count("deleted", int64(n))
			if err != nil {
				return err
			}
			if n < pruneChunk {
				more.Store(false)
			}
			return nil
		}, tasks.ItemsOptions[int]{Label: func(i int) string { return fmt.Sprintf("chunk %d", i+1) }})
	out := PruneResult{Deleted: res.Counters["deleted"]}
	// The executor keeps this result on a failed or canceled run as well as
	// on a succeeded one (F5-18d, O5); the status line carries the count too,
	// for the run's live view.
	rc.Progress().Note(fmt.Sprintf("pruned %d runs older than %s", out.Deleted, cutoff.UTC().Format(time.RFC3339)))
	if err != nil {
		return out, fmt.Errorf("executor: prune task history: %w", err)
	}
	return out, nil
}
