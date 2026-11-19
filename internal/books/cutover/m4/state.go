// file: internal/books/cutover/m4/state.go
// version: 1.1.0
// guid: 6f20a8d3-91c5-4be7-a3d0-2e5b7c19f486
// last-edited: 2026-11-18

package m4

import (
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/mediacore/tasks"
)

// MapState is the imported state of a HEAD v3 run, its reason token, and
// whether HEAD's state is one the table knows (a state it does not know is
// quarantined, never guessed). An interrupted run with no recorded why is an
// "interrupted" one whose why HEAD never wrote: it imports as interrupted with
// reason "legacy", the why HEAD's own v2 conversion gives that case
// (state.FromV2: status "interrupted").
func MapState(s state.V3, waitReason, stopReason, interruptedWhy string) (tasks.RunState, string, bool) {
	switch s {
	case state.V3Queued:
		if waitReason == state.WaitDeps {
			return tasks.StateWaitingDeps, "", true
		}
		return tasks.StateQueued, "", true
	case state.V3Running:
		// HEAD stopped mid-run: the resume policy decides (5T.4 step 2).
		return tasks.StateInterrupted, state.WhyCrash, true
	case state.V3Stopping:
		switch stopReason {
		case state.StopShutdown:
			return tasks.StateInterrupted, stopReason, true
		case state.StopUser:
			return tasks.StateCanceled, stopReason, true
		case state.StopQuiesce:
			return tasks.StateInterruptedQuiesced, stopReason, true
		case state.StopTimeout:
			return tasks.StateTimedOut, stopReason, true
		case state.StopWatchdog:
			return tasks.StateFailed, stopReason, true
		}
		return "", "", false
	case state.V3Interrupted:
		if interruptedWhy == state.WhyQuiesced {
			return tasks.StateInterruptedQuiesced, interruptedWhy, true
		}
		if interruptedWhy == "" {
			interruptedWhy = state.WhyLegacy
		}
		return tasks.StateInterrupted, interruptedWhy, true
	case state.V3AwaitingDecision:
		return tasks.StateAwaitingDecision, "", true
	case state.V3Succeeded:
		return tasks.StateSucceeded, "", true
	case state.V3Failed:
		return tasks.StateFailed, "", true
	case state.V3TimedOut:
		return tasks.StateTimedOut, "", true
	case state.V3Canceled:
		return tasks.StateCanceled, "", true
	case state.V3Dropped:
		return tasks.StateInterruptedDropped, "", true
	}
	return "", "", false
}

// eventState is a trail entry's state in the native vocabulary: the same word,
// except HEAD's "dropped".
func eventState(s state.V3) tasks.RunState {
	if s == state.V3Dropped {
		return tasks.StateInterruptedDropped
	}
	return tasks.RunState(s)
}
