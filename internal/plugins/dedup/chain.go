// file: internal/plugins/dedup/chain.go
// version: 1.1.1
// guid: 1f33e68f-5822-4267-bfe7-0106d08d6de6
// last-edited: 2026-11-19

package dedup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// F5-04: the one child-following driver behind dedup.run-all and
// dedup.label-refinement, shared by HEAD's v2 shell and the native kind. Both
// start each step as a child operation and follow it to the end; they differ only
// in how a child is started and awaited, which is the childDriver seam below.
// Everything else (when a recorded child is re-attached, restarted or failed, the
// progress mirrored onto the parent, the checkpoint throttle, the wording of the
// errors) is runChainChild, so the parity suites test the code production runs
// (05-tasks 5T.8.2 rule 2).

// childMode is what a chain starts a child as.
type childMode int

const (
	// childLive starts the child to do its work (run-all: HEAD's dry_run=false).
	childLive childMode = iota
	// childReportOnly starts the child with HEAD's empty params, which is each
	// child's own report-only default (label-refinement). Natively it is Preview.
	childReportOnly
)

// childEvent is what a driver reports while it waits.
type childEvent int

const (
	// childWaiting: the driver is waiting on a run it cannot read progress from
	// (the native await); reported once, when the wait starts.
	childWaiting childEvent = iota
	// childQueued: the child has not started (HEAD's operation row says queued).
	childQueued
	// childPaused: an operator pause is parking the child.
	childPaused
	// childRunning: the child's row changed (HEAD only).
	childRunning
)

// childObservation is one thing a driver saw while it waited.
type childObservation struct {
	Event          childEvent
	Current, Total int
	Message        string
}

// childEnd is how a child ended, in the driver's own words.
type childEnd struct {
	// Status names how it ended in an error message: HEAD's operation status or
	// the native run state ("failed", "canceled", "interrupted_dropped", ...).
	Status string
	// Why is the child's own error text, "" when it has none.
	Why string
	// Completed is a successful end.
	Completed bool
	// Interrupted is set when the child had been interrupted by a restart or
	// dropped by the kernel before the wait began; the step then starts again.
	// HEAD sets it from the row read on re-attach, so a child that is alive then
	// ends interrupted is a failure, as at HEAD. Natively the await cannot tell the
	// two apart, so any interrupted end of a re-attached child counts.
	Interrupted bool
}

// childGoneError says a recorded child could not be read back: it is gone or
// unreadable, so the step starts again.
type childGoneError struct{ cause error }

func (e childGoneError) Error() string {
	if e.cause == nil {
		return "recorded child not found"
	}
	return "recorded child unreadable: " + e.cause.Error()
}

func (e childGoneError) Unwrap() error { return e.cause }

// childDriver is how a shell starts and waits for a child.
type childDriver struct {
	// NoID names who returned no id for a started child, worded as the shell
	// words it ("the registry returned no operation id", "the kernel returned no
	// run id").
	NoID string
	// Start starts defID as a child in mode and returns its id.
	Start func(ctx context.Context, defID string, mode childMode) (string, error)
	// Wait follows the child to its end, reporting what it sees to observe.
	// reattach is set when the id was recorded by an earlier attempt: a child that
	// cannot be read back ends the wait with a childGoneError, and one already over
	// is returned as it ended. Otherwise any failure to wait is the error.
	Wait func(ctx context.Context, id string, reattach bool, observe func(childObservation)) (childEnd, error)
}

// phaseFunc runs fn as one named phase of the run: HEAD's Reporter.RunPhase (the
// Operations page shows which step is running), and fn gets the env over that
// phase's reporter. Natively there is no phase, so it runs fn on the run's env.
type phaseFunc func(ctx context.Context, id string, fn func(ctx context.Context, env kindEnv) error) error

// chainEnv is what runChainChild takes from its shell.
type chainEnv struct {
	kindEnv
	Children childDriver
}

// chainChild describes one child step.
type chainChild struct {
	// Label is the step's name in messages; DefID the child's definition id.
	Label, DefID string
	// StepID is the chain's own id for the step (run-all's "find", not the child's
	// DefID): the `step` attribute of the re-attach warning, as HEAD logged it.
	StepID string
	// Head prefixes every progress note; Base and Total place the step on the
	// parent's bar, which is Units wide per step.
	Head        string
	Base, Total int
	Units       int
	Mode        childMode
	// StopNote is appended to the error of a child that did not complete.
	StopNote string
	// Chain names the chain in a log line ("dedup.run-all").
	Chain string
}

// childEndedError is the error for a child that did not complete: label, id, how
// it ended and why.
func childEndedError(label, id string, end childEnd) error {
	why := ""
	if end.Why != "" {
		why = ": " + end.Why
	}
	return fmt.Errorf("%s (%s) ended as %q%s", label, id, end.Status, why)
}

// runChainChild runs one child step. A recorded child (*childID != "") is
// re-attached first: still alive, it is waited for; over and completed, the step
// is done; over and failed or canceled, the step fails; interrupted by a restart
// (or dropped by the kernel), or gone, the step starts again. Otherwise the child
// is started, its id recorded through record, and waited for. A child that is
// interrupted DURING a live wait is not a restart (this run would have been
// stopped with it): it fails the step.
//
// record is called with force after the child's id is set (the plan must be
// frozen before the wait) and, throttled by its owner, while the wait goes on. It
// may be nil. "Force" is the caller's own unthrottle only: natively record ends in
// rc.Checkpoint, which the kernel holds in memory until MinCheckpointInterval (30
// s) has passed since the last flush (core/internal/executor/attempt.go), and the
// native wait calls record only through this forced call (its driver reports one
// childWaiting frame, no row changes). So natively the child id is NOT durable
// for the wait: a hard kill or a fenced attempt resumes at this step with no
// child id and starts a second child. F5-18 owes a flush-now checkpoint from
// core, or a route that keeps the id durable (todo.d/2026-11-19-f5-04-native-run-all-child-id-flush.md).
func runChainChild(ctx context.Context, env chainEnv, c chainChild, childID *string, record func(force bool)) error {
	if record == nil {
		record = func(bool) {}
	}
	share := 1
	observe := func(o childObservation) {
		switch o.Event {
		case childWaiting:
			env.Progress(c.Base+share, c.Total, c.Head+" — waiting for "+c.DefID)
		case childQueued:
			env.Progress(c.Base+share, c.Total, c.Head+" — waiting to start (queued behind other work)")
			record(false)
		case childPaused:
			// The operator pause parks a RunItems child between items and keeps
			// only ITS in-memory liveness clock stamped; its row does not change.
			// Mirror the gate so a long pause does not get this run reaped.
			env.Progress(c.Base+share, c.Total, c.Head+" — paused by an operator")
		default: // the running child's row changed
			if o.Total > 0 {
				// Monotonic within the step: full-scan runs two 0..N phases, and a
				// bar that jumps back reads as a restart. The note carries the
				// child's real phase and counts.
				if s := min(o.Current*c.Units/o.Total, c.Units-1); s > share {
					share = s
				}
			}
			msg := c.Head
			if o.Message != "" {
				msg += " — " + o.Message
			}
			env.Progress(c.Base+share, c.Total, msg)
			record(false)
		}
	}

	if *childID != "" {
		end, err := env.Children.Wait(ctx, *childID, true, observe)
		var gone childGoneError
		switch {
		case errors.As(err, &gone):
			// Unreadable or gone: start the step again. HEAD's enqueue collapses onto
			// a live run of the same definition. Natively the start folds only into a
			// QUEUED run of the same kind, mode and params (stageFold,
			// core/internal/executor/start.go), never a running one: a child that is
			// already running is not joined, and a second one queues behind it on the
			// shared ConcurrencyKey.
			env.Logger.Warn(c.Chain+": recorded child unreadable; re-enqueueing step",
				"step", c.StepID, "child_op_id", *childID, "error", gone.cause)
			*childID = ""
		case err != nil:
			return err
		case end.Completed:
			return nil
		case end.Interrupted:
			env.Log(slog.LevelInfo, fmt.Sprintf("%s was interrupted by a restart (%s); starting it again", c.Label, end.Status))
			*childID = ""
		default:
			return fmt.Errorf("%w%s", childEndedError(c.Label, *childID, end), c.StopNote)
		}
	}

	id, err := env.Children.Start(ctx, c.DefID, c.Mode)
	if err != nil {
		return fmt.Errorf("%s: could not start %s: %w", c.Label, c.DefID, err)
	}
	if id == "" {
		return fmt.Errorf("%s: %s for %s", c.Label, env.Children.NoID, c.DefID)
	}
	*childID = id
	record(true)
	env.Log(slog.LevelInfo, fmt.Sprintf("%s: started %s (%s)", c.Label, c.DefID, id))

	end, err := env.Children.Wait(ctx, id, false, observe)
	if err != nil {
		return err
	}
	if !end.Completed {
		return fmt.Errorf("%w%s", childEndedError(c.Label, id, end), c.StopNote)
	}
	return nil
}
