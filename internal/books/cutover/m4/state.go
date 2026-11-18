// file: internal/books/cutover/m4/state.go
// version: 1.0.0
// guid: 6f20a8d3-91c5-4be7-a3d0-2e5b7c19f486
// last-edited: 2026-11-18

package m4

import (
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/mediacore/tasks"
)

// StateCase is one row of 05-tasks 5T.8.3's state table: a HEAD v3 state with
// the qualifier the row depends on, and what the run is imported as.
type StateCase struct {
	State          state.V3
	WaitReason     string
	StopReason     string
	InterruptedWhy string
	Want           tasks.RunState
	// Reason is the fixed token the imported run carries ("" for none).
	Reason string
}

// StateTable is the table of 5T.8.3, copied here as data so the test walks
// every row the spec lists. MapState is the code that must agree with it.
//
// "queued (WaitReason "", ready, pause)" imports as queued and "queued with
// WaitReason deps" as waiting_deps; a v1 status goes through its own
// conversion (v1.go), not this table.
func StateTable() []StateCase {
	return []StateCase{
		{State: state.V3Queued, WaitReason: "", Want: tasks.StateQueued},
		{State: state.V3Queued, WaitReason: state.WaitReady, Want: tasks.StateQueued},
		{State: state.V3Queued, WaitReason: state.WaitPause, Want: tasks.StateQueued},
		{State: state.V3Queued, WaitReason: state.WaitDeps, Want: tasks.StateWaitingDeps},
		{State: state.V3Running, Want: tasks.StateInterrupted, Reason: "crash"},
		{State: state.V3Stopping, StopReason: state.StopShutdown, Want: tasks.StateInterrupted, Reason: state.StopShutdown},
		{State: state.V3Stopping, StopReason: state.StopUser, Want: tasks.StateCanceled, Reason: state.StopUser},
		{State: state.V3Stopping, StopReason: state.StopQuiesce, Want: tasks.StateInterruptedQuiesced, Reason: state.StopQuiesce},
		{State: state.V3Stopping, StopReason: state.StopTimeout, Want: tasks.StateTimedOut, Reason: state.StopTimeout},
		{State: state.V3Stopping, StopReason: state.StopWatchdog, Want: tasks.StateFailed, Reason: state.StopWatchdog},
		{State: state.V3Interrupted, InterruptedWhy: state.WhyCrash, Want: tasks.StateInterrupted, Reason: state.WhyCrash},
		{State: state.V3Interrupted, InterruptedWhy: state.WhyLegacy, Want: tasks.StateInterrupted, Reason: state.WhyLegacy},
		{State: state.V3Interrupted, InterruptedWhy: state.WhyQuiesced, Want: tasks.StateInterruptedQuiesced, Reason: state.WhyQuiesced},
		{State: state.V3AwaitingDecision, Want: tasks.StateAwaitingDecision},
		{State: state.V3Succeeded, Want: tasks.StateSucceeded},
		{State: state.V3Failed, Want: tasks.StateFailed},
		{State: state.V3TimedOut, Want: tasks.StateTimedOut},
		{State: state.V3Canceled, Want: tasks.StateCanceled},
		{State: state.V3Dropped, Want: tasks.StateInterruptedDropped},
	}
}

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
