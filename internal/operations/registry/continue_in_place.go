// file: internal/operations/registry/continue_in_place.go
// version: 1.1.0
// guid: 79eb165b-27ea-402c-9107-49374f5ad3eb
// last-edited: 2026-10-26

package registry

import (
	"context"
	"errors"
)

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
// PROGRESS IS CHECKED, NOT TRUSTED. A yield is honoured only if THIS attempt
// reported progress past where it started (AttemptProgress: the same signal
// ops.items counts, seeded with the position the attempt inherited). The
// infinite-restart check cannot catch a run that yields without progress once
// the row's high-water progress is above zero, so without this check a def that
// returned the sentinel with no progress would loop dispatch, yield, requeue for
// ever. A yield with no progress is recorded as failed, with a message that says
// why. TouchLiveness does not count: it proves the run is alive, not that it
// advanced.
//
// WHAT THE REST OF THE SYSTEM SEES. The pause is a pause, not an ending: no
// op.terminal event, no "operation finished" line, no failure told to the
// dependency scheduler, no legacy-row mirror. op.created (the re-queue's own
// announcement) is what the UI sees next. The row itself does read
// interrupted_quiesced (a settled status) for the few store writes between the
// pause and the re-queue; the reporter is joined BEFORE the pause is written so
// that window holds no waiting, and a poller that samples inside it sees a
// pause (see the client pollers' note about resumable statuses). If the
// re-queue cannot happen (shutdown, a failed write) the pause is announced as
// the terminal event it then is.
//
// WHAT IS UNCHANGED. A deadline that fires, a Cancel, and a watchdog kill all
// cancel the run's context, and finalStatusForCanceledRun decides those first
// (deadline, then user intent) exactly as before: a run that does not yield in
// time is still timed_out, and a watchdog-killed run is still canceled. The
// watchdog's no-progress check is untouched, so a run that stops reporting is
// still killed. The registry checks the attempt made progress (above), because
// checkInfiniteRestart only force-drops a row whose high-water progress is zero.
var ErrContinueInPlace = errors.New("registry: run yielded before its deadline; continue in place")

// continuesInPlace reports whether runErr is a yield the registry should honour
// for def: the sentinel, from an in-process def that resumes in place.
func continuesInPlace(def OperationDef, runErr error) bool {
	return runErr != nil && !def.Isolate && def.ResumePolicy == ResumeRestart && errors.Is(runErr, ErrContinueInPlace)
}

// AttemptProgress is implemented by the reporter the registry hands a run. It
// reports whether the attempt advanced past the position it started from (the
// seeded progress of a resumed row), which is the proof a yield needs.
type AttemptProgress interface {
	AttemptMadeProgress() bool
}

// attemptProgressed reports whether rep's attempt made progress. A reporter that
// cannot say has not proven it.
func attemptProgressed(rep Reporter) bool {
	p, ok := rep.(AttemptProgress)
	return ok && p.AttemptMadeProgress()
}

// yieldHonoured is the one decision whether a Run's return is a yield the
// registry honours: the run's context still live (a deadline, a cancel and a
// watchdog kill keep the status they always had), the def resumes in place, the
// error is the sentinel, and the attempt made progress. The worker decides with
// it after Run returns; WillContinueInPlace asks it from inside Run's wrapper.
func yieldHonoured(runCtx context.Context, def OperationDef, rep Reporter, runErr error) bool {
	return runCtx.Err() == nil && continuesInPlace(def, runErr) && attemptProgressed(rep)
}

// WillContinueInPlace tells a def's Run wrapper, as it returns runErr, whether
// the registry will honour it as a yield and restart the operation, so the
// wrapper can leave out an end-of-run summary that would otherwise say "done"
// mid-flight. It is the worker's own decision (yieldHonoured), asked early: a
// cancel or deadline that lands after the wrapper returns still wins in the
// worker, and the summary was then skipped for a run that ended canceled, which
// the terminal status and its own error already say.
func (r *Registry) WillContinueInPlace(ctx context.Context, defID string, rep Reporter, runErr error) bool {
	def, ok := r.lookupDef(defID)
	return ok && yieldHonoured(ctx, def, rep, runErr)
}

// continueInPlaceMessage is the interrupted row's message for a yield.
const continueInPlaceMessage = "continuing in place: the run reached its time budget and resumes from its saved progress"

// continueInPlaceNoProgressMessage is the failed row's message for a yield the
// attempt made no progress for.
func continueInPlaceNoProgressMessage(runErr error) string {
	return "run returned " + ErrContinueInPlace.Error() + " but made no progress this attempt, so it was not restarted (a restart would repeat it for ever): " + runErr.Error()
}
