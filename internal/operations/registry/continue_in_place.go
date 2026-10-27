// file: internal/operations/registry/continue_in_place.go
// version: 1.0.0
// guid: 79eb165b-27ea-402c-9107-49374f5ad3eb
// last-edited: 2026-10-26

package registry

import "errors"

// ErrContinueInPlace is what a Run returns, BEFORE its deadline, to say "I have
// checkpointed durable progress and have more to do: start me again under this
// same operation id". It is how a resumable run too big for one Timeout finishes
// anyway.
//
// WHY A SENTINEL AND NOT A LONGER TIMEOUT. Timeout is a per-attempt wall-clock
// cap whose job is to kill a run that is stuck. A run's size grows with the
// library, so no fixed cap finishes every library (and the cap is 24h). A run
// that hits the cap ends timed_out, which v3 makes terminal and RetryInterrupted
// refuses (not in the interrupted family), and a retry or a fresh enqueue gets a
// new operation id: a job whose progress record is keyed by the operation id
// (backfill-file-hashes names its per-run report after it) would start from row
// zero every time. The run itself knows when it is close to its deadline and
// where a clean stopping point is; the registry does not.
//
// WHAT HAPPENS. In-process, ResumePolicy ResumeRestart, run context still live:
// the run is recorded as stopping{quiesce} then interrupted_quiesced (a pause,
// no outcome counted), and the SAME row is re-queued through the path the boot
// sweep and the scan stand-down release use (resumeRestart: resume_count + 1,
// checkpoint merged into params, status queued). The resumed attempt gets a
// fresh Timeout. Anything else that returns this error (a def that does not
// resume in place, a subprocess run, a run whose context is already done) is an
// ordinary failure carrying the error text, so the sentinel can never turn into
// a silent drop.
//
// WHAT IS UNCHANGED. A deadline that fires, a Cancel, and a watchdog kill all
// cancel the run's context, and finalStatusForCanceledRun decides those first
// (deadline, then user intent) exactly as before: a run that does not yield in
// time is still timed_out, and a watchdog-killed run is still canceled. The
// watchdog's no-progress check is untouched, so a run that stops reporting is
// still killed. The returner must guarantee it made progress this attempt:
// checkInfiniteRestart only force-drops a row whose high-water progress is zero,
// so a run that yielded without progress would loop forever.
var ErrContinueInPlace = errors.New("registry: run yielded before its deadline; continue in place")

// continuesInPlace reports whether runErr is a yield the registry should honour
// for def: the sentinel, from an in-process def that resumes in place.
func continuesInPlace(def OperationDef, runErr error) bool {
	return runErr != nil && !def.Isolate && def.ResumePolicy == ResumeRestart && errors.Is(runErr, ErrContinueInPlace)
}

// continueInPlaceMessage is the interrupted row's message for a yield.
const continueInPlaceMessage = "continuing in place: the run reached its time budget and resumes from its saved progress"
