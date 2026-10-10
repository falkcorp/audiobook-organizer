// file: internal/operations/childop/follow.go
// version: 1.1.0
// guid: 7c2e9a41-5d3b-4f86-a0e7-1b9c6d4f2a58
// last-edited: 2026-10-10

// Package childop follows a child operation to a terminal status and tells the
// parent when the child has shown signs of life, so the parent can feed its own
// watchdog from what it observes of the child.
//
// WHY THIS EXISTS. A parent op that enqueues a child and then waits reports no
// progress of its own while it waits. The registry watchdog kills an op that has
// not called UpdateProgress within its ProgressTimeout (5 minutes by default),
// so a parent waiting on a child that legitimately runs for an hour is reaped
// while the child is healthy. maintenance.library-optimize died this way; it is
// the same bug class as dedup.drain-stale (#3600).
//
// WHY OBSERVED PROGRESS, NOT A TIMER. A parent that heartbeats on every poll
// tick stays alive forever behind a wedged child, and the Operations page shows
// a supervisor that looks healthy. Follow only reports a running child when its
// row CHANGED (status, counts, message or last_progress_at), so a child that
// stops moving makes its parent go quiet too. Two states are reported on every
// poll because an unchanged row is expected there, not a sign of a hang:
//
//   - queued: the child is waiting on a ConcurrencyKey (acoustid.scan can sit
//     behind a fingerprint rescan for hours) and has no watchdog of its own yet;
//   - operator pause: the registry's pause gate parks a RunItems child between
//     items and stamps only ITS in-memory liveness clock, so its row does not
//     change while the parent must not be reaped for it either.
//
// A parent must still declare a ProgressTimeout at least as long as the longest
// silence any of its children is allowed (a LivenessNone child's budget, or a
// child with its own longer ProgressTimeout), because Follow can only relay the
// progress a child actually writes.
package childop

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// Reader reads one operation row. *database.PebbleStore satisfies it.
type Reader interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
}

// Event says why Follow is reporting an observation.
type Event int

const (
	// EventProgress: the running child's row changed since the last report.
	EventProgress Event = iota
	// EventQueued: the child has not started; reported on every poll.
	EventQueued
	// EventPaused: operations are paused by an operator; reported on every poll.
	EventPaused
)

// Observation is one report to the parent.
type Observation struct {
	Row   *database.OperationV2Row
	Event Event
}

// Options configures Follow.
type Options struct {
	// Interval between reads of the child row. Zero means DefaultInterval.
	Interval time.Duration
	// Paused reports whether an operator has paused operations
	// (registry.OperationsPaused in production). Nil means never paused.
	Paused func() bool
	// OnObserve is called with every observation worth relaying as progress.
	// Nil is allowed (Follow then only waits).
	OnObserve func(Observation)
}

// DefaultInterval is the poll interval when Options.Interval is zero.
const DefaultInterval = 5 * time.Second

// IsTerminal reports whether a follower has nothing more to wait for:
// state.Props.Settled (completed, failed, canceled, or any interrupted*
// status). An interrupted row does not move again in this process.
func IsTerminal(status string) bool {
	return state.IsSettled(status)
}

// Follow polls opID until its status is terminal and returns that row, or
// returns ctx.Err() when ctx ends first. The first read happens immediately.
//
// A read error or a missing row is not reported as progress and does not end
// the wait: the child may not be visible yet, and if the store stays unreadable
// the parent's watchdog should see a silent op.
func Follow(ctx context.Context, r Reader, opID string, opts Options) (*database.OperationV2Row, error) {
	if r == nil {
		return nil, fmt.Errorf("childop: no operation store to follow %s", opID)
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	observe := opts.OnObserve
	if observe == nil {
		observe = func(Observation) {}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastSig := ""
	for {
		row, err := r.GetOperationV2(opID)
		if err == nil && row != nil {
			switch {
			case IsTerminal(row.Status):
				return row, nil
			case row.Status == "queued":
				observe(Observation{Row: row, Event: EventQueued})
			case opts.Paused != nil && opts.Paused():
				observe(Observation{Row: row, Event: EventPaused})
			default: // running (or any other live status)
				if sig := Signature(row); sig != lastSig {
					lastSig = sig
					observe(Observation{Row: row, Event: EventProgress})
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Signature is what "the child's row changed" means for liveness.
func Signature(row *database.OperationV2Row) string {
	lp := ""
	if row.LastProgressAt != nil {
		lp = row.LastProgressAt.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("%s|%d|%d|%s|%s", row.Status, row.ProgressCurrent, row.ProgressTotal, row.ProgressMessage, lp)
}

// Percent is the child's progress as a whole percentage, 0 when it has no total.
func Percent(row *database.OperationV2Row) int {
	if row == nil || row.ProgressTotal <= 0 {
		return 0
	}
	p := row.ProgressCurrent * 100 / row.ProgressTotal
	return max(0, min(p, 100))
}
