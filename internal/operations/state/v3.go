// file: internal/operations/state/v3.go
// version: 1.1.0
// guid: f4b44f29-abd2-405d-9167-2beac6c9d083
// last-edited: 2026-10-11

package state

import (
	"errors"
	"fmt"
	"strings"
)

// V3 is a run state in the v3 vocabulary (ten states), the one stored on
// database.RunRecord.State under the opv3: keyspace. The table, the closed
// transition set and the v2 mappings below are
// docs/proposals/2026-10-holistic/05-operations-v3/state-and-persistence.md
// sections 1, 3 and 4; the v2 vocabulary above (State) stays the one the v2
// registry writes until the port waves move it onto TransitionRunV3.
type V3 string

const (
	V3Queued           V3 = "queued"            // WaitReason: "" | deps | ready | pause
	V3Running          V3 = "running"           //
	V3Stopping         V3 = "stopping"          // StopReason: user | timeout | watchdog | quiesce | shutdown; Zombie bool
	V3Interrupted      V3 = "interrupted"       // InterruptedWhy: quiesced | crash | legacy
	V3AwaitingDecision V3 = "awaiting_decision" //
	V3Succeeded        V3 = "succeeded"         //
	V3Failed           V3 = "failed"            //
	V3Canceled         V3 = "canceled"          //
	V3TimedOut         V3 = "timed_out"         //
	V3Dropped          V3 = "dropped"           // SuccessorID optional
)

// Wait reasons (queued only), stop reasons (stopping only) and interrupted
// reasons. A queued run with a wait reason is not in the dispatch queue.
const (
	WaitDeps  = "deps"
	WaitReady = "ready"
	WaitPause = "pause"

	StopUser     = "user"
	StopTimeout  = "timeout"
	StopWatchdog = "watchdog"
	StopQuiesce  = "quiesce"
	StopShutdown = "shutdown"

	WhyQuiesced = "quiesced"
	WhyCrash    = "crash"
	WhyLegacy   = "legacy"
	// WhyRequeued is FromV2's why for a v2 requeue row (dropped with a
	// successor): the successor id itself is not on the v2 row.
	WhyRequeued = "requeued"
)

// RequeuedMessage is the error message v2 resumeRequeue writes on the row it
// replaces (resume.go), and the one ToV2 gives a dropped run with a successor.
const RequeuedMessage = "requeued: original op replaced"

// TimeoutPrefix starts the error message of a v2 "canceled" row that was a
// deadline expiry (worker.go finalStatusForCanceledRun).
const TimeoutPrefix = "timeout:"

// Event names a v3 transition. Next accepts exactly the events of the
// transition list; EvMigrate and EvV2Write are recorded on the audit trail but
// are never accepted by Next (see each).
type Event string

const (
	EvEnqueue           Event = "enqueue"
	EvWaitCleared       Event = "wait_cleared"
	EvDispatch          Event = "dispatch"
	EvReturnNil         Event = "return_nil"
	EvReturnErr         Event = "return_err"
	EvStopRequested     Event = "stop_requested"
	EvGoroutineReturned Event = "goroutine_returned"
	EvBootSweep         Event = "boot_sweep"
	EvResume            Event = "resume"
	EvAsk               Event = "ask"
	EvDecideRetry       Event = "decide_retry"
	EvDecideDrop        Event = "decide_drop"
	// EvMigrate is the synthetic event migration 065 records once per run it
	// builds from an opv2: row. No runtime transition produces it.
	EvMigrate Event = "migrate"
	// EvV2Write is recorded when a v2-vocabulary write (the opv2 store methods
	// the v2 registry still calls during the shim period) moves a run between
	// two v3 states that no single Next event connects: the v2 registry makes
	// moves the v3 table does not have (queued -> canceled for a cancel before
	// dispatch, running -> interrupted_* on shutdown with no stopping step,
	// waiting_deps -> failed when a dependency fails, canceled -> completed
	// when a run finished over a cancel). The v2 row is authoritative while
	// the v2 registry writes it, so the store mirrors the move instead of
	// refusing it, and the audit row says plainly it was not a v3 transition.
	EvV2Write Event = "v2_write"
)

// AllEvents is every event Next can accept, in a stable order (InferEvent
// tries them in this order).
var AllEvents = []Event{
	EvEnqueue, EvWaitCleared, EvDispatch, EvReturnNil, EvReturnErr,
	EvStopRequested, EvGoroutineReturned, EvBootSweep, EvResume, EvAsk,
	EvDecideRetry, EvDecideDrop,
}

// StopReasons are the reasons a run can enter stopping with.
var StopReasons = []string{StopUser, StopTimeout, StopWatchdog, StopQuiesce, StopShutdown}

type v3Entry struct {
	state V3
	props Props
}

// v3Table is the ten-state table of state-and-persistence.md section 1, as
// Props. It is a second table on purpose: v2 Classify answers for v2 status
// strings and its fallbacks (unknown is live, interrupted_* prefix) are v2's.
var v3Table = []v3Entry{
	{V3Queued, Props{Resumable: true}},
	{V3Running, Props{HoldsSlot: true, Resumable: true}},
	// stopping holds the slot and the exclusive key until the goroutine
	// returns; the boot sweep maps it to interrupted{crash}.
	{V3Stopping, Props{HoldsSlot: true, Resumable: true}},
	{V3Interrupted, Props{Resumable: true, Retryable: true, Discardable: true, Settled: true}},
	{V3AwaitingDecision, Props{Retryable: true, Discardable: true, AwaitingDecision: true, Settled: true}},
	{V3Succeeded, Props{Terminal: true, Discardable: true, Settled: true}},
	{V3Failed, Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}},
	{V3Canceled, Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}},
	{V3TimedOut, Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}},
	// Retryable is "no" when successor_id is set; that is a property of the
	// run, not the state, so the caller checks it.
	{V3Dropped, Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}},
}

var v3ByName = func() map[V3]Props {
	m := make(map[V3]Props, len(v3Table))
	for _, e := range v3Table {
		m[e.state] = e.props
	}
	return m
}()

// ClassifyV3 returns the properties of a v3 state. An unknown state is live
// (zero Props), the same safe direction as Classify.
func ClassifyV3(s V3) Props { return v3ByName[s] }

// StopTerminal is the state a stopping run reaches when its goroutine
// returns: user -> canceled, timeout and watchdog -> timed_out, quiesce and
// shutdown -> interrupted.
func StopTerminal(stopReason string) (V3, error) {
	switch stopReason {
	case StopUser:
		return V3Canceled, nil
	case StopTimeout, StopWatchdog:
		return V3TimedOut, nil
	case StopQuiesce, StopShutdown:
		return V3Interrupted, nil
	}
	return "", fmt.Errorf("state: unknown stop reason %q", stopReason)
}

// ErrTransition is wrapped by every error Next returns.
var ErrTransition = errors.New("state: transition not allowed")

// Next is the closed transition table of state-and-persistence.md section 1.
// Anything not listed returns an error wrapping ErrTransition; the store never
// writes a native transition Next rejects. from is "" for a run that does not
// exist yet (enqueue). stopReason is read by stop_requested (it must be a
// known reason) and goroutine_returned (it decides the terminal state).
func Next(from V3, ev Event, stopReason string) (V3, error) {
	bad := func() (V3, error) {
		return "", fmt.Errorf("%w: %q --%s-->", ErrTransition, from, ev)
	}
	switch ev {
	case EvEnqueue:
		if from == "" {
			return V3Queued, nil
		}
	case EvWaitCleared:
		if from == V3Queued {
			return V3Queued, nil
		}
	case EvDispatch:
		if from == V3Queued {
			return V3Running, nil
		}
	case EvReturnNil:
		if from == V3Running {
			return V3Succeeded, nil
		}
	case EvReturnErr:
		if from == V3Running {
			return V3Failed, nil
		}
	case EvStopRequested:
		if from == V3Running {
			if _, err := StopTerminal(stopReason); err != nil {
				return "", fmt.Errorf("%w: %v", ErrTransition, err)
			}
			return V3Stopping, nil
		}
	case EvGoroutineReturned:
		if from == V3Stopping {
			to, err := StopTerminal(stopReason)
			if err != nil {
				return "", fmt.Errorf("%w: %v", ErrTransition, err)
			}
			return to, nil
		}
	case EvBootSweep:
		if from == V3Running || from == V3Stopping {
			return V3Interrupted, nil
		}
	case EvResume:
		if from == V3Interrupted {
			return V3Queued, nil
		}
	case EvAsk:
		if from == V3Interrupted {
			return V3AwaitingDecision, nil
		}
	case EvDecideRetry:
		switch from {
		case V3AwaitingDecision, V3Failed, V3Canceled, V3TimedOut, V3Dropped:
			return V3Queued, nil
		}
	case EvDecideDrop:
		if from == V3AwaitingDecision || from == V3Interrupted {
			return V3Dropped, nil
		}
	}
	return bad()
}

// InferEvent names the Next event that takes from to to, for the v2 façade:
// a v2-vocabulary write says where a run ended up, not why. stopReason is the
// reason that event needs (goroutine_returned decides by it). ok is false
// when no single event connects the two states; the caller then records
// EvV2Write (see there). from == to is not a transition and reports false.
func InferEvent(from, to V3) (ev Event, stopReason string, ok bool) {
	if from == to {
		return "", "", false
	}
	for _, e := range AllEvents {
		reasons := []string{""}
		if e == EvStopRequested || e == EvGoroutineReturned {
			reasons = StopReasons
		}
		for _, r := range reasons {
			if got, err := Next(from, e, r); err == nil && got == to {
				return e, r, true
			}
		}
	}
	return "", "", false
}

// FromV2 is the forward map of state-and-persistence.md section 3, used by
// migration 065: the v2 status (and its error message, which tells a timeout
// apart from a cancel and a requeue apart from a drop) gives the v3 state and
// its reason (a wait reason for queued, an interrupted reason for interrupted,
// WhyRequeued for a dropped run that has a successor). ok is false for a
// status the table does not know; the run is then "failed" and the caller
// keeps the original string (RunRecord.FromV2.Status).
//
// A "running" row maps to interrupted{crash}: at migration time nothing in
// this process owns it. Live v2 writes use FromV2Live instead.
func FromV2(status, errMsg string) (s V3, why string, ok bool) {
	switch State(status) {
	case Queued:
		return V3Queued, "", true
	case WaitingDeps:
		return V3Queued, WaitDeps, true
	case Running:
		return V3Interrupted, WhyCrash, true
	case Completed:
		return V3Succeeded, "", true
	case Failed:
		return V3Failed, "", true
	case Canceled:
		if strings.HasPrefix(errMsg, TimeoutPrefix) {
			return V3TimedOut, "", true
		}
		return V3Canceled, "", true
	case InterruptedQuiesced:
		return V3Interrupted, WhyQuiesced, true
	case InterruptedAsk:
		return V3AwaitingDecision, "", true
	case InterruptedDropped:
		if errMsg == RequeuedMessage {
			return V3Dropped, WhyRequeued, true
		}
		return V3Dropped, "", true
	case InterruptedRestart:
		return V3Dropped, WhyRequeued, true
	case Interrupted:
		return V3Interrupted, WhyLegacy, true
	}
	return V3Failed, "", false
}

// FromV2Live is FromV2 for a write made by the running v2 registry: the
// same table, except that "running" is a run this process is executing.
func FromV2Live(status, errMsg string) (s V3, why string, ok bool) {
	if State(status) == Running {
		return V3Running, "", true
	}
	return FromV2(status, errMsg)
}

// ToV2 is the mirror back-map used while opv2: is dual-written
// (state-and-persistence.md section 4). hasLedger is true when a chunk ledger
// exists: a v2 binary cannot resume one, so an interrupted ledger run mirrors
// as interrupted_dropped. It returns the v2 status and the v2 error message
// (errMsg, prefixed for a timeout and defaulted for a requeue).
//
// One deliberate difference from the spec's back-map: a dropped run with a
// successor mirrors as interrupted_dropped + RequeuedMessage, which is what
// the v2 registry itself writes for that case (resume.go resumeRequeue), and
// not as interrupted_restart. interrupted_restart is a legacy spelling whose
// v2 Props are not Terminal and are Retryable, so mirroring to it would make
// a superseded run read as unfinished and show a Retry button in v2.
func ToV2(s V3, waitReason, interruptedWhy string, hasSuccessor, hasLedger bool, errMsg string) (v2Status, v2ErrMsg string) {
	_ = interruptedWhy // every interrupted reason mirrors the same way today
	switch s {
	case V3Queued:
		if waitReason == WaitDeps {
			return string(WaitingDeps), errMsg
		}
		return string(Queued), errMsg
	case V3Running, V3Stopping:
		return string(Running), errMsg
	case V3Interrupted:
		if hasLedger {
			return string(InterruptedDropped), errMsg
		}
		return string(InterruptedQuiesced), errMsg
	case V3AwaitingDecision:
		return string(InterruptedAsk), errMsg
	case V3Succeeded:
		return string(Completed), errMsg
	case V3Failed:
		return string(Failed), errMsg
	case V3Canceled:
		return string(Canceled), errMsg
	case V3TimedOut:
		if !strings.HasPrefix(errMsg, TimeoutPrefix) {
			if errMsg == "" {
				errMsg = TimeoutPrefix + " run exceeded its timeout"
			} else {
				errMsg = TimeoutPrefix + " " + errMsg
			}
		}
		return string(Canceled), errMsg
	case V3Dropped:
		if hasSuccessor && errMsg == "" {
			errMsg = RequeuedMessage
		}
		return string(InterruptedDropped), errMsg
	}
	return string(Failed), errMsg
}
