// file: core/internal/logjournal/prune.go
// version: 1.0.2
// guid: 5d0c7e32-8a41-4f6b-9b13-2e7a4c1d8f90
// last-edited: 2026-11-18

package logjournal

import (
	"errors"
	"fmt"
	"iter"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

const (
	// KindPrune is the journal retention kind (05-logging §5).
	KindPrune tasks.Kind = "core.log.journal.prune"

	// PruneScheduleKey is core.log.schedules.journal_prune: the field
	// Schedules.JournalPrune types, default every 24h, re-read every poll.
	// This file only passes it to tasks.ScheduleFromConfig.
	PruneScheduleKey = string(SchedulesPath) + ".journal_prune"

	// PruneTimeout bounds one pass. Each call is one bounded batch and stamps
	// liveness as it lands, so the watchdog's derived gap (a sixth of this) is
	// the longest one call may take.
	PruneTimeout = time.Hour
)

// MaxRetentionDays bounds core.log.journal.retention_days (100 years). It is
// the schema maximum of Settings.RetentionDays, and the prune task enforces it
// again at run time so that a value that got past the schema (a stored value
// older than the cap, a direct write) never reaches the date arithmetic: a day
// count near the int range wraps time.AddDate to a cutoff at or after now,
// which would delete the whole journal. A value above it fails the run and
// deletes nothing.
const MaxRetentionDays = 36500

// PruneParams is empty: each pass reads the settings afresh.
type PruneParams struct{}

// PruneResult is what a pass did. Kept is true when retention_days is 0
// (keep everything) and Disabled when the journal is off; neither deletes.
type PruneResult struct {
	Deleted  int64 `json:"deleted"`
	Kept     bool  `json:"kept,omitempty"`
	Disabled bool  `json:"disabled,omitempty"`
}

// PruneDefinition returns the definition of core.log.journal.prune over store.
// settings is read at the start of each run, so a changed retention_days
// applies to the next pass (the key is live, 05-logging §8); a nil store or
// settings yields a definition whose Run is nil, which tasks.Problems reports,
// so registration fails closed. now is the clock (nil = time.Now).
//
// A pass calls store.Prune with the cutoff now minus retention_days until a
// call reports no more records, each call one bounded batch of at most
// MaxPruneBatch. retention_days 0 keeps everything, and a journal that is off
// is neither written nor pruned (Settings.Enabled). A call that reports more
// records but deleted none cannot advance, so the pass fails instead of
// calling it again.
//
// Perms.Start is core:tasks.manage for the reason core.outbox.relay's is: the
// scheduler starts the kind as core's system actor, which holds no
// AuthRequired scope (SEC2-15).
//
//tasklint:constructor the kernel passes the result to the executor's definition list from the tasks flip F5 (F5-18); nothing registers it before
func PruneDefinition(store log.JournalStore, settings func() Settings, now func() time.Time) tasks.Definition {
	var run func(rc *tasks.RC, _ PruneParams) (PruneResult, error)
	if store != nil && settings != nil {
		if now == nil {
			now = time.Now
		}
		run = func(rc *tasks.RC, _ PruneParams) (PruneResult, error) {
			return pruneJournal(rc, store, settings(), now())
		}
	}
	return tasks.Task(KindPrune, tasks.TaskSpec[PruneParams, PruneResult]{
		Title:    "Prune the system log journal",
		Help:     "Deletes system log records older than the journal's retention in days. A retention of 0 keeps every record.",
		Effects:  tasks.Writes(tasks.Res("core", "log")),
		Perms:    tasks.Permissions{Start: rbac.Need("core:tasks.manage")},
		Resume:   tasks.ResumeFromZero,
		Priority: tasks.PriorityLow,
		Timeout:  PruneTimeout,
		Schedule: tasks.ScheduleFromConfig(PruneScheduleKey).Live(),
		Run:      run,
	})
}

var errNoPruneProgress = errors.New("logjournal: prune reported more records but deleted none")

// ErrRetentionOutOfRange: retention_days is above MaxRetentionDays, or the
// cutoff it yields is not strictly before now. The prune task refuses the run
// and deletes nothing: an unusable retention must never read as "everything is
// old".
var ErrRetentionOutOfRange = errors.New("logjournal: retention_days out of range")

// retentionCutoff is the instant records older than which are pruned: now minus
// days calendar days, as HEAD's PruneOldLogs computes it (AddDate, so no
// Duration arithmetic that could wrap). days must be in [1, MaxRetentionDays]
// and the result strictly before now, or it fails closed with
// ErrRetentionOutOfRange.
func retentionCutoff(now time.Time, days int) (time.Time, error) {
	if days < 1 || days > MaxRetentionDays {
		return time.Time{}, fmt.Errorf("%w: %d (want 1 to %d)", ErrRetentionOutOfRange, days, MaxRetentionDays)
	}
	cutoff := now.AddDate(0, 0, -days)
	if !cutoff.Before(now) {
		return time.Time{}, fmt.Errorf("%w: cutoff %s is not before %s", ErrRetentionOutOfRange, cutoff.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return cutoff, nil
}

func pruneJournal(rc *tasks.RC, store log.JournalStore, s Settings, now time.Time) (PruneResult, error) {
	switch {
	case !s.Enabled:
		rc.Progress().Note("the journal is off: nothing to prune")
		return PruneResult{Disabled: true}, nil
	case s.RetentionDays <= 0:
		rc.Progress().Note("retention_days is not positive: every record is kept")
		return PruneResult{Kept: true}, nil
	}
	cutoff, err := retentionCutoff(now, s.RetentionDays)
	if err != nil {
		return PruneResult{}, err
	}
	var more atomic.Bool
	more.Store(true)
	calls := iter.Seq[int](func(yield func(int) bool) {
		for i := 0; more.Load(); i++ {
			if !yield(i) {
				return
			}
		}
	})
	// One worker takes one bounded batch at a time. RunSeq, not a bare loop,
	// so the pass honours cancel and the pause gate between batches and
	// stamps liveness as each lands.
	res, err := tasks.RunSeq(rc, tasks.Sequential("one bounded delete batch at a time, oldest first"), calls, tasks.Unknown(),
		func(ic *tasks.ItemRC, _ int) error {
			n, m, err := store.Prune(ic.Context(), cutoff, MaxPruneBatch)
			ic.Count("deleted", int64(n))
			if err != nil {
				return err
			}
			if m && n == 0 {
				return errNoPruneProgress
			}
			more.Store(m)
			return nil
		})
	out := PruneResult{Deleted: res.Counters["deleted"]}
	// The executor stores a result only for a run that succeeded, so the
	// status line is where a failed or canceled pass's count survives.
	rc.Progress().Note(fmt.Sprintf("pruned %d records older than %s", out.Deleted, cutoff.UTC().Format(time.RFC3339)))
	if err != nil {
		return out, fmt.Errorf("logjournal: prune: %w", err)
	}
	return out, nil
}
