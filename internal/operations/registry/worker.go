// file: internal/operations/registry/worker.go
// version: 2.36.0
// guid: b8c9d0e1-f2a3-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-10-13

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
)

var operationTracer = otel.Tracer("audiobook-organizer/operations")

// ErrSubprocessNotImplemented is returned when a Run with Isolate=true is
// dispatched. Subprocess execution lands in UOS-03.
var ErrSubprocessNotImplemented = errors.New("subprocess runner not yet wired (UOS-03)")

// defaultAbandonGrace is the time a ctx-canceled goroutine has to return before
// it is classified as abandoned and the worker slot is freed. Overridable per
// Registry via Options.AbandonGrace (tests shorten it).
const defaultAbandonGrace = 5 * time.Second

// graceDuration returns the configured abandon grace, or the default.
func (r *Registry) graceDuration() time.Duration {
	if r.abandonGrace > 0 {
		return r.abandonGrace
	}
	return defaultAbandonGrace
}

// runHandle tracks a single in-flight operation.
type runHandle struct {
	id             string
	defID          string
	plugin         string
	concurrencyKey string
	resumePolicy   ResumePolicy
	// writes is the def's declared write-set, copied onto the handle so the
	// dispatcher's Gate 3b can check running ops without a def lookup. Because
	// it lives ON the handle, the slot is released wherever the handle is
	// released — releaseRunHandle covers completion, failure, cancel, and the
	// channel-full undo path all at once (same hygiene as concurrencyKey; the
	// C-2 leak class cannot recur here because there is no separate map to
	// forget to clear).
	writes []Resource
	cancel context.CancelFunc
	// cancelCause cancels the run with a cause (see cancelWithCause). Nil on
	// dispatcher stub handles, like cancel.
	cancelCause   context.CancelCauseFunc
	abandoned     bool
	currentItem   string
	currentItemMu sync.Mutex
	// lastProgressAt is stamped by the reporter on every UpdateProgress call.
	// Stored as Unix nanoseconds; zero means progress has never been reported.
	// The watchdog reads this first (lock-free) before falling back to the DB row.
	lastProgressAt atomic.Int64
	// attemptStartedAt is stamped when this worker begins an attempt. A resumed
	// operation deliberately retains its persisted progress for audit/history,
	// but that timestamp belongs to the previous process. The watchdog must use
	// this attempt-local baseline until the first new UpdateProgress arrives.
	attemptStartedAt atomic.Int64
	// lastUncheckpointedStrike (Unix nanoseconds) dedupes the watchdog's
	// uncheckpointed strikes: at most one strike per threshold interval per op
	// (C-4). Only the watchdog goroutine touches it; atomic for -race hygiene.
	lastUncheckpointedStrike atomic.Int64
	// userCanceled records that this run's context was canceled by a DELIBERATE
	// decision -- Registry.Cancel, or the watchdog killing a stuck op -- rather
	// than by shutdown.
	//
	// It exists because a canceled context alone cannot tell those apart, and the
	// terminal status must. Once shutdown starts writing a resumable status, a run
	// that was canceled just before shutdown began would otherwise be recorded as
	// "interrupted by shutdown" and resurrected on the next boot. The race is not
	// narrow: a cancel fires, the op is mid-item and takes seconds to return, and
	// by the time it reaches its terminal switch the shutdown flag is already set.
	// Recording the intent at the cancel site is the only ordering-independent
	// answer.
	userCanceled atomic.Bool
	// claimed is set once this run's queued->running compare-and-set applied.
	// The full handle is registered before that claim (and before the scan
	// stand-down pickup gate), so Registry.Cancel reads it to know whether it
	// stopped a run that had started or only a handle that might never run.
	claimed atomic.Bool
	// queuedCancel is set by Registry.Cancel on a dispatcher STUB handle (nil
	// cancel func) when the op is canceled while sitting in the buffered
	// nextRun channel. The worker checks it — under Registry.mu, the same lock
	// that guards the write — before registering the full handle, and drops
	// the run without ever invoking Run (C-1). Guarded by Registry.mu.
	queuedCancel bool
	// quiescing records that this run's context was canceled by a scan
	// stand-down (AcquireScanStandDown), NOT by a user cancel or shutdown. It is
	// checked in finalStatusForCanceledRun BEFORE the shutdown branch so the run
	// is recorded interrupted_quiesced (a RESUMABLE status) rather than
	// "canceled" (deliberately non-resumable). Distinct from userCanceled so a
	// genuine Registry.Cancel of the same op still wins and stays canceled.
	quiescing atomic.Bool
	// parked is closed at the TRUE exit of the run goroutine (deferred, so it
	// fires on panic too). AcquireScanStandDown waits on it to know a quiesced
	// scan has actually stopped — r.running membership cannot tell "parked" from
	// "abandoned but still writing". A full handle always has a non-nil parked;
	// dispatcher STUB handles do not (no goroutine to wait on).
	parked chan struct{}
	// fence is this attempt's write permit (fence.go). Every stop path revokes
	// it BEFORE canceling the context: cancelIfActive / cancelWithCause (user
	// cancel, watchdog kill, stand-down quiesce, shutdown), the timeout branch
	// of executeRun's select, and RenewScanStandDown on a lost lease. The zero
	// Fence on a dispatcher stub is a no-op to revoke.
	fence Fence
	// zombie is set when the run goroutine outlived the abandon grace (both the
	// genuine-abandonment and the shutdown branches of executeRun). The handle
	// then stays in r.running, holding its ConcurrencyKey and declared write
	// set, until the goroutine really returns (owner decision D6).
	zombie atomic.Bool
	// zombieSince is when the run became a zombie (liveness clock, Unix ns).
	zombieSince atomic.Int64
	// zombieWarnedAt is when zombieSweep last logged this zombie (liveness
	// clock, Unix ns); 0 means never. Only the watchdog goroutine writes it.
	zombieWarnedAt atomic.Int64
	// slotReleased records that releaseSlotKeepKeyLocked already gave back this
	// run's pluginRunning slot (genuine abandonment), so releaseRunHandleLocked
	// must not decrement it a second time. Guarded by Registry.mu.
	slotReleased bool
	// zombieFinal is the final status a genuinely abandoned run (v3
	// "stopping") was given at abandonment: written when its goroutine
	// returns, or by Shutdown if the process exits first (05-PR4).
	zombieFinal atomic.Pointer[zombieOutcome]
	// stoppingRecorded is true once markStopping stored the run as v3
	// stopping{reason}: from then on a crash before the goroutine returns
	// leaves the stop reason for the boot sweep, which keeps a user or
	// timeout stop's outcome. Cancel relies on it to leave the row to the
	// goroutine's exit (cancelStoppingRun); markStopping is best effort.
	stoppingRecorded atomic.Bool
}

// zombieOutcome is a stopping run's decided final status. It carries no
// completion time: the run ends when its goroutine returns (or the process
// exits first), so the writer stamps completed_at then.
type zombieOutcome struct {
	status string
	msg    *string
	// outcome is the ops.runs outcome of status (finalStatusForCanceledRun's
	// third result): status alone cannot tell a timeout from a cancel, both
	// are "canceled".
	outcome string
}

// cancelIfActive cancels the run's context if it has been wired up.
//
// The dispatcher (dispatcher.go) inserts a *stub* handle into r.running with a
// nil cancel func to block Gate-0 re-dispatch the instant an op is claimed; the
// worker overwrites it with the full handle (with cancel) on pickup
// (worker.go). Between those two events a handle is present in r.running with
// cancel == nil. Callers that walk r.running and cancel (Shutdown, Cancel, the
// watchdog) must go through this guard so they no-op on a stub instead of
// panicking with a nil-pointer dereference. For Shutdown a no-op on a stub is
// correct only because of the shutdown pickup gate in executeRun: shuttingDown
// is set before the walk, and the worker that eventually picks the stub up
// checks the flag under r.mu before registering its full handle, so a stubbed
// op is never executed. That gate did not exist until 2026-10-03 -- this
// comment claimed it anyway, and stubs ran to completion after Shutdown began
// (OPS-V2-DISPATCH-RACE).
//
// It revokes the run's fence first, so every cancel route (Cancel, the
// watchdog, a stand-down quiesce) withdraws the write permit before the run
// can observe ctx.Done().
func (h *runHandle) cancelIfActive() {
	if h == nil {
		return
	}
	h.fence.revoke()
	if h.cancel != nil {
		h.cancel()
	}
}

// cancelWithCause is cancelIfActive with a cancellation cause the run can read
// through context.Cause. Shutdown passes ErrShutdown so a ResumeRestart op can
// tell a restart from an operator cancel.
func (h *runHandle) cancelWithCause(cause error) {
	if h == nil {
		return
	}
	// Revoke before canceling, as cancelIfActive does: this early-return
	// branch never reaches cancelIfActive.
	h.fence.revoke()
	if h.cancelCause != nil {
		h.cancelCause(cause)
		return
	}
	h.cancelIfActive()
}

func (h *runHandle) setCurrentItem(label string) {
	h.currentItemMu.Lock()
	h.currentItem = label
	h.currentItemMu.Unlock()
}

func (h *runHandle) getCurrentItem() string {
	h.currentItemMu.Lock()
	defer h.currentItemMu.Unlock()
	return h.currentItem
}

// queuedRun is the payload the dispatcher sends to a worker goroutine.
type queuedRun struct {
	opID         string
	defID        string
	params       json.RawMessage
	priority     Priority
	concurrKey   string
	plugin       string
	resumePolicy ResumePolicy
}

// startWorker is a long-running goroutine that reads from r.nextRun and
// executes each run in sequence. When the worker is notified its run was
// abandoned (ctx-canceled goroutine didn't return within abandonGrace), it
// exits so the replacement worker (spawned by executeRun) becomes the sole
// occupant of that conceptual slot.
func (r *Registry) startWorker(ctx context.Context, slot int) {
	r.logger.Info("registry: worker started", "slot", slot)
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("registry: worker stopping", "slot", slot)
			return
		case qr := <-r.nextRun:
			abandoned := r.executeRun(ctx, qr)
			if abandoned {
				// The run goroutine is still alive but classified as abandoned.
				// A replacement worker was already spawned by executeRun.
				// This worker exits so we don't grow the pool.
				r.logger.Info("registry: worker exiting after abandoning run", "slot", slot, "op_id", qr.opID)
				return
			}
		}
	}
}

// executeRun runs a single queued operation. It returns true if the run was
// classified as abandoned (ctx-canceled but goroutine didn't drain within
// abandonGrace), in which case the caller (startWorker) should exit so the
// replacement worker owns that slot.
func (r *Registry) executeRun(parentCtx context.Context, qr *queuedRun) (wasAbandoned bool) {
	def, ok := r.lookupDef(qr.defID)
	if !ok {
		r.logger.Warn("registry: worker got run for unknown def; skipping", "def_id", qr.defID)
		// Release the dispatcher's stub handle so its plugin slot and
		// ConcurrencyKey don't leak (same leak shape as the force-drop path, C-2).
		r.releaseRunHandle(qr.opID)
		return false
	}

	// Early shutdown exit, before anything below can write a status. The
	// infinite-restart check can force-drop the row (interrupted_dropped), and
	// a run that reaches a worker after Shutdown began must leave its row
	// exactly as it found it: "queued". This read is unlocked, so it only
	// narrows the window; the locked check at handle registration below is
	// the guarantee that no run STARTS after Shutdown.
	if r.shuttingDown.Load() {
		r.releaseRunHandle(qr.opID)
		r.logger.Info("registry: run not started, shutdown in progress; left queued for the next start",
			"op_id", qr.opID, "def_id", qr.defID)
		return false
	}

	// Check infinite-restart strike: if resume_count >= 3 and high_water hasn't
	// advanced, force ResumeDrop behavior.
	if qr.resumePolicy == ResumeRestart {
		if forced := r.checkInfiniteRestart(qr, def); forced {
			return false
		}
	}

	// Build per-run context with cancel and optional timeout.
	timeout := def.Timeout
	if timeout == 0 {
		if def.Isolate {
			timeout = 6 * time.Hour
		} else {
			timeout = 120 * time.Minute
		}
	}
	// The cause layer sits under the timeout so a cancel with a cause
	// (Shutdown's ErrShutdown) is visible through context.Cause(runCtx).
	causeCtx, cancelCause := context.WithCancelCause(parentCtx)
	runCtx, cancelTimeout := context.WithTimeout(causeCtx, timeout)
	cancel := func() {
		cancelTimeout()
		cancelCause(nil)
	}
	// Decorate the run context (e.g. install a context-bound slog.Logger that
	// tags every line with the operation id) via the optional decorator hook.
	// Nil is the default when no decorator was wired via
	// SetRunContextDecorator — runs proceed undecorated.
	if r.runContextDecorator != nil {
		runCtx = r.runContextDecorator(runCtx, qr.opID)
	}
	// The attempt's write permit (fence.go). Fenced writers (repairs.Writer)
	// read it from the run context and refuse every write once it is revoked
	// or runCtx is done (the fence is bound to runCtx.Done(), so a timeout or
	// parent cancel fences the run at the instant the context ends, before
	// any revoke below runs).
	fence := newFence(1, runCtx.Done())
	runCtx = WithFence(runCtx, fence)
	// reporterForJoin is assigned once the reporter is built below. The deferred
	// join must be registered HERE, above every early return between this point
	// and the reporter's construction, so no exit path can skip it.
	var reporterForJoin *dbReporter
	defer func() {
		// Revoke before cancel on every exit of executeRun, including the
		// abandonment returns: the run is over from the registry's side, so a
		// goroutine still alive must not write.
		fence.revoke()
		cancel()
		// Cancelling runCtx only ASKS the flush loop to stop; it then runs a
		// terminal flush that writes to the store. Returning without joining it
		// drops this run from Registry.running while that write is still in
		// flight, so Shutdown sees zero running ops and closes Pebble underneath
		// it — a "pebble: closed" panic and the data race it drags in.
		if reporterForJoin != nil {
			reporterForJoin.awaitFlush(10 * time.Second)
		}
	}()

	// Register the handle. Before overwriting the dispatcher's stub, honor a
	// cancel that arrived while the run sat in the buffered nextRun channel
	// (C-1): the stub has a nil cancel func, so Registry.Cancel flags it with
	// queuedCancel instead. The flag is written and read under r.mu, so this
	// check races with nothing — if Cancel got the lock first, we see the flag
	// and drop the run without ever invoking Run.
	attemptStartedAt := r.livenessClock().UTC()
	h := &runHandle{
		id:             qr.opID,
		defID:          qr.defID,
		plugin:         qr.plugin,
		concurrencyKey: qr.concurrKey,
		resumePolicy:   qr.resumePolicy,
		writes:         def.Writes,
		cancel:         cancel,
		cancelCause:    cancelCause,
		parked:         make(chan struct{}),
		fence:          fence,
	}
	h.attemptStartedAt.Store(attemptStartedAt.UnixNano())
	r.mu.Lock()
	// prev is always this run's own dispatcher stub, never a zombie (05-PR3):
	// Gate 0 refuses to claim an id that is in r.running, so a zombie's
	// same-id re-queued row (possible only between the exit monitor's status
	// write and its handle release, 05-PR4) cannot reach a worker until the
	// zombie's handle is released.
	if prev, exists := r.running[qr.opID]; exists && prev.queuedCancel {
		r.mu.Unlock()
		// Status first, release second. The claim is what stops the dispatcher
		// from handing this row out again, and the row still reads "queued"
		// until this write lands; releasing first left a window in which a
		// canceled op was dispatched and run.
		now := time.Now().UTC()
		msg := "canceled before start (canceled while queued for a worker)"
		if err := r.store.UpdateOperationV2Status(qr.opID, "canceled", nil, &now, &msg); err != nil {
			r.logger.Warn("registry: failed to mark channel-canceled op canceled", "op_id", qr.opID, "error", err)
		}
		r.releaseRunHandle(qr.opID)
		// R-1: this is a terminal transition too — publish op.terminal so the UI
		// bell drops the op instead of leaving it phantom-"running".
		r.publishOpTerminal(qr.opID, qr.defID, "canceled")
		r.logger.Info("registry: dropped run canceled while queued in worker channel", "op_id", qr.opID, "def_id", qr.defID)
		return false
	}
	// Shutdown pickup gate (OPS-V2-DISPATCH-RACE). A run that reaches a worker
	// after Shutdown has begun must not start. Two paths deliver one here:
	//   - a dispatchCycle that passed its shuttingDown check before the flag
	//     flipped, then claimed and sent the row afterwards;
	//   - a claim already sitting in the buffered nextRun channel when
	//     Shutdown began.
	// Either way Shutdown cannot stop it: the handle it gathered (if any) was
	// the dispatcher's stub, whose cancel funcs are nil, so cancelWithCause was
	// a no-op and the run would execute to completion on a live context -- the
	// "dispatched op" after "registry: shutting down" then "status=completed"
	// sequence measured on CI run 32655184277.
	//
	// This check is the guarantee. Shutdown stores the flag BEFORE it gathers
	// r.running under r.mu, and this check runs under r.mu too, so exactly one
	// of two things happens: this worker took the lock first, registered the
	// full handle, and Shutdown's gather sees it and cancels it with
	// ErrShutdown (the normal interrupted path); or Shutdown flipped the flag
	// first and this worker sees it here and never starts the run.
	//
	// It sits after the queuedCancel check so a user cancel still wins (that
	// row ends canceled, not queued), and before the queued->running
	// compare-and-set, so no status is written: the row stays "queued", which
	// is exactly what it is -- it never ran -- and the next Start dispatches it
	// (TestResume_QueuedAtShutdownIsRestartedNotDropped).
	if r.shuttingDown.Load() {
		r.mu.Unlock()
		r.releaseRunHandle(qr.opID)
		r.logger.Info("registry: run not started, shutdown in progress; left queued for the next start",
			"op_id", qr.opID, "def_id", qr.defID)
		return false
	}
	r.running[qr.opID] = h
	r.mu.Unlock()

	// Scan stand-down pickup gate: a maintenance op may have raised the scan gate
	// after this scan was CLAIMED by the dispatcher but before this worker picked
	// it up. Dropping it here (as interrupted_quiesced, resumable) rather than
	// running it is what closes the stub-race window that Gate 3.5 alone cannot:
	// Gate 3.5 blocks dispatch, but a row already in the nextRun channel has
	// passed it. The scan resumes from its checkpoint when the gate clears.
	if qr.defID == scanStandDownDefID && r.scanStandDownActive() {
		close(h.parked)
		// Status first, release second (same reason as the canceled-while-queued
		// exit above): released first, the row still read "queued" with no
		// claim, so a stand-down ending in that gap let the scan be dispatched
		// and run, and this write then landed on top of the live run.
		//
		// A compare-and-set from "queued", like the claim below. The row is not
		// this worker's until one of the two applies: Registry.Cancel can have
		// written "canceled" since the dispatcher's re-read (it found no handle
		// yet, or found this one), and an unconditional write turned that back
		// into a resumable interrupted_quiesced, which the stand-down release
		// then re-queued -- the scan the user had canceled ran.
		quiescedAt := time.Now().UTC()
		status := interruptedStatus(qr.resumePolicy)
		msg := "quiesced before start: scan gate held by a maintenance op"
		quiesced, qErr := r.store.UpdateOperationV2StatusIf(qr.opID, "queued", status, nil, &quiescedAt, &msg)
		if qErr != nil || !quiesced {
			r.releaseRunHandle(qr.opID)
			if qErr != nil {
				// The row is as it was. Still queued, Gate 3.5 holds it while the
				// scan gate is up and it is dispatched once the gate clears.
				r.logger.Warn("registry: failed to mark scan quiesced-before-start; not running it",
					"op_id", qr.opID, "error", qErr)
			} else {
				// Some other transition (a cancel) owns the row: no status to
				// publish and nothing to resume.
				r.logger.Info("registry: scan not quiesced before start; the row is no longer queued",
					"op_id", qr.opID, "def_id", qr.defID)
			}
			return false
		}
		// A cancel that found this handle (it is registered, so Cancel takes it
		// for a live run) set userCanceled and may have read the row before
		// the write above. Honor it now, from the status just written.
		out := r.settleInterrupted(qr, h, status, pauseOutcome(status), &quiescedAt)
		status = out.status
		r.releaseRunHandle(qr.opID)
		if out.publish {
			r.publishOpTerminal(qr.opID, qr.defID, status)
		}
		// A ResumeDrop scan quiesced before it started is dropped: a run end
		// with no attempt to time. interrupted_quiesced records nothing.
		r.recordRunEnd(qr.defID, out.outcome)
		r.logger.Info("registry: scan quiesced before start (scan gate held)", "op_id", qr.opID, "status", status)
		// Without this the dropped scan was never recorded anywhere a release
		// reads, so it sat interrupted_quiesced until the next startup sweep.
		if status == "interrupted_quiesced" {
			r.resumeDroppedScanOnRelease(qr.opID)
		}
		return false
	}

	// Take the row from "queued" to "running" as a compare-and-set, and do not
	// run unless it applied. This is the one place that decides a run starts.
	//
	// It used to be an unconditional write whose failure was only logged, and
	// Run was invoked regardless. That let an op run more than once under one
	// id, three ways:
	//   - the write failed: the row read "queued" for the whole run, so the
	//     moment this worker released its claim the dispatcher handed the row
	//     out again (and forever, if the terminal write failed too);
	//   - the row was no longer queued at all (already finished, picked from a
	//     stale dispatcher snapshot): the write flipped a completed row back to
	//     "running" and ran it again;
	//   - Registry.Cancel flipped a queued row to "canceled" just as the
	//     dispatcher claimed it: the write overwrote "canceled" and the op ran
	//     after the caller had been told it was canceled.
	// The dispatcher re-reads the row after claiming it and catches most of
	// these earlier, but it reads and then sends; only a conditional write here
	// leaves no gap.
	//
	// Not applied means some other transition owns the row now, so its status is
	// left alone. A failed write leaves the row as it was: still queued, it is
	// dispatched again next cycle; anything else, it was not ours to run.
	claimedAt := attemptStartedAt
	started, casErr := r.store.UpdateOperationV2StatusIf(qr.opID, "queued", "running", &claimedAt, nil, nil)
	if casErr == nil && started {
		// Registry.Cancel reads this to tell a run it stopped from a handle
		// whose run never started (cancelIntent.stopped).
		h.claimed.Store(true)
	}
	if casErr != nil || !started {
		close(h.parked)
		r.releaseRunHandle(qr.opID)
		if casErr != nil {
			r.logger.Warn("registry: could not mark op running; not running it (retried while it stays queued)",
				"op_id", qr.opID, "def_id", qr.defID, "error", casErr)
		} else {
			r.logger.Info("registry: dropped run; the row is no longer queued",
				"op_id", qr.opID, "def_id", qr.defID)
		}
		return false
	}
	// The claim above also stamped the attempt's start time (started_at).

	r.logger.Info("registry: starting run", "op_id", qr.opID, "def_id", qr.defID)

	// Build reporter (DB-backed). Pass setItemFn so SetCurrentItem updates
	// the runHandle's in-memory currentItem without a DB write. Pass touchFn
	// so UpdateProgress stamps lastProgressAt atomically — the watchdog reads
	// this instead of the DB row, preventing false-positive cancellations when
	// UpdateOpProgressV2 is blocked behind PebbleDB compaction.
	setItemFn := func(label string) { h.setCurrentItem(label) }
	touchFn := func() { h.lastProgressAt.Store(r.livenessClock().UnixNano()) }
	flushInterval := def.ProgressFlushInterval
	if flushInterval <= 0 || flushInterval > 5*time.Minute {
		flushInterval = 30 * time.Second
	}
	reporter := newDBReporter(runCtx, qr.opID, qr.defID, def.DisplayName, qr.plugin,
		"", "", // traceID / spanID loaded from DB row in future; empty for now
		r.store, r.bus, r.activityRecorder, r.logger, setItemFn, touchFn, flushInterval, def.Synchronous,
		r.newAttemptItems(qr.opID, qr.defID))
	// Same assertion the markTerminal call sites below use: newDBReporter
	// returns the Reporter interface, and only the DB-backed one has a flush
	// loop to join.
	if dbr, ok := reporter.(*dbReporter); ok {
		reporterForJoin = dbr
	}

	// Canonical "operation started" log line, with all the tags downstream
	// readers (op_log feed, activity-log enricher, digest aggregator) need
	// to group, filter, and search without parsing the message. Every op
	// gets this even if its Run forgets to emit one.
	runStartedAt := time.Now().UTC()
	metrics.IncOperationStarted(qr.defID)
	r.ops.Started(qr.defID)
	reporter.Logger().LogAttrs(runCtx, slog.LevelInfo, "operation started",
		slog.String("phase", "start"),
		slog.String("op_display", def.DisplayName),
		slog.Int("params_bytes", len(qr.params)),
		slog.Bool("isolated", def.Isolate),
		slog.Int("priority", int(def.DefaultPriority)),
		slog.String("concurrency_key", def.ConcurrencyKey),
	)

	// Subprocess path (Isolate=true): re-exec self.
	if def.Isolate {
		runErr := runSubprocess(runCtx, def, qr.opID, qr.params, reporter)
		r.releaseRunHandle(qr.opID)
		var finalStatus, outcome string
		var errMsg *string
		completedAt := time.Now().UTC()
		if runCtx.Err() != nil {
			finalStatus, errMsg, outcome = r.finalStatusForCanceledRun(runCtx, h, qr, timeout)
		} else if runErr != nil {
			finalStatus, outcome = "failed", opsmetrics.OutcomeFailed
			msg := runErr.Error()
			errMsg = &msg
		} else {
			finalStatus, outcome = "completed", opsmetrics.OutcomeCompleted
		}
		if outcome == opsmetrics.OutcomeTimedOut {
			r.markStopping(qr, h, state.StopTimeout, false)
		}
		out := r.writeRunTerminalStatus(qr, h, finalStatus, outcome, &completedAt, errMsg)
		finalStatus = out.status
		// R-1: fan out op.terminal so the UI bell stops showing this op as running.
		if out.publish {
			r.publishOpTerminal(qr.opID, qr.defID, finalStatus)
		}
		r.recordRunMetrics(qr.defID, out.outcome, runStartedAt)
		// C-5: notify the dep scheduler on ALL terminal transitions (the
		// subprocess path previously notified on none of them).
		r.notifyDepTerminal(finalStatus, qr)
		emitPendingProgress(reporter)
		emitOpFinishedLog(runCtx, reporter, runStartedAt, finalStatus, runErr, true, h.lastProgressAt.Load() != 0)
		r.logger.Info("registry: subprocess run finished", "op_id", qr.opID, "status", finalStatus)
		return false
	}

	// In-process path: run in a separate goroutine so we can detect abandonment.
	// The goroutine is enrolled in goroutineWG for its full lifetime so
	// Registry.Shutdown's final goroutineWG.Wait() genuinely covers plugin code
	// (BUG-2): if the shutdown ctx expires during the drain poll and the op is
	// marked interrupted, Shutdown must still wait for this goroutine before
	// returning — otherwise the caller closes the store while Run is mid-write.
	// No notifyStopped-style gate is needed here (unlike notifyDepCompletion/
	// notifyDepFailed): WaitGroup.Go increments the counter synchronously,
	// before it spawns, and it does so inside executeRun — which runs on the
	// already-enrolled startWorker goroutine, whose own Done() has not run yet.
	// So the counter is provably non-zero across this enrollment and can never
	// be observed at zero by a concurrent Wait().
	done := make(chan error, 1)
	r.goroutineWG.Go(func() {
		// Close parked at the TRUE goroutine exit so a scan stand-down waiting on
		// it unblocks whether the run returns normally, errors, is canceled, or
		// panics (safeRun recovers, but the deferred close covers every path and
		// the abandonment case where the handle is released before this returns).
		defer close(h.parked)
		done <- r.safeRun(runCtx, def, qr.params, reporter)
	})

	// Wait for the run to finish or the context to be canceled.
	var runErr error
	var ctxCanceled bool

	select {
	case runErr = <-done:
		// Normal completion or run-level error; check if ctx was already done.
		if runCtx.Err() != nil {
			ctxCanceled = true
			fence.revoke()
		}
	case <-runCtx.Done():
		ctxCanceled = true
		// Revoke here for the stop paths no code of ours initiates: the def
		// Timeout firing inside context.WithTimeout, or the parent context
		// ending. Every other path (Cancel, watchdog, quiesce, shutdown) has
		// already revoked inside cancelIfActive / cancelWithCause before
		// canceling. On these two paths the revoke necessarily follows the
		// cancel, but there is no window: Fence.Check also refuses once
		// runCtx is done (newFence above binds it), so a run that sees
		// ctx.Done() already fails Check. The revoke makes Revoked() report
		// the stop as well.
		fence.revoke()
		// Give the goroutine abandonGrace to return cleanly.
		select {
		case runErr = <-done:
			// Goroutine returned within grace — not abandoned, but ctx was canceled.
		case <-time.After(r.graceDuration()):
			// Goroutine didn't return within the grace period.
			r.abandoned.increment(qr.plugin)
			if r.shuttingDown.Load() {
				// During Shutdown, do NOT spawn a replacement worker — the pool
				// is on its way out and a new worker would race against store
				// close (pebble: closed panic).
				//
				// Critically, keep the run handle REGISTERED until the goroutine
				// actually exits. Releasing it now would let Shutdown's drain
				// poll report "all workers drained" while this goroutine is still
				// alive and touching shared state (the global config it reads,
				// the store it writes). That premature release was the root cause
				// of the test-suite data race AND the "pebble: closed" panic: the
				// abandoned goroutine outlived the caller and collided with the
				// next test's config write / the store being closed. Release the
				// handle in the monitor below, after the goroutine truly returns,
				// so Shutdown (bounded by its own context) genuinely drains.
				r.logger.Info("registry: op goroutine abandoned during shutdown; waiting for it to exit before freeing slot",
					"op_id", qr.opID, "plugin", qr.plugin)
				// Reported by Zombies(). The slot is NOT released here (the pool
				// is going away) and no terminal status is written, so Shutdown's
				// timeout loop still stamps this row interrupted_*.
				// v3: the run is stopping, a zombie (05-PR4), recorded before
				// it is listed as one. Its row keeps reading "running";
				// Shutdown's timeout loop stamps it.
				r.markStopping(qr, h, r.stopReasonFor(runCtx, h), true)
				h.zombieSince.Store(r.livenessClock().UnixNano())
				h.zombie.Store(true)
				// R-3: same rationale as the genuine-abandonment path — the
				// wedged goroutine's flushLoop is gone, so no-op its Log calls.
				if dbr, ok := reporter.(*dbReporter); ok {
					dbr.markTerminal()
				}
				go func() {
					<-done
					r.releaseRunHandle(qr.opID)
					r.abandoned.decrement(qr.plugin)
				}()
				return true
			}
			// Not shutting down: the op is genuinely abandoned, and becomes a
			// zombie (owner decision D6). Free its WORKER SLOT now and spawn a
			// replacement so the pool doesn't shrink, but keep its handle in
			// r.running: the handle holds the ConcurrencyKey (Gate 3) and the
			// declared write set (Gate 3b) until the goroutine truly returns,
			// so a second run of the same def cannot overlap it. Releasing the
			// whole handle here (as this branch did until 05-PR3) freed the key
			// at the 5s grace while the runaway Run was still writing.
			//
			// The run is now v3 "stopping" (05-PR4, owner decision D6): it was
			// stopped, its fence is revoked, and it keeps its key until the
			// goroutine returns. Its final status is decided NOW, by
			// finalStatusForCanceledRun -- the same single decision point the
			// non-abandoned path uses -- and written when the goroutine
			// returns (the monitor below), or by Shutdown if the process exits
			// first. Until then its row reads "running" (v3 stopping mirrors
			// as running) and EnqueueOp's dedupe skips it (isZombie), so the
			// C-3 trap (dedupe handing out a dead op's id forever) stays shut.
			//
			// Until 05-PR4 this branch wrote the terminal status here, at
			// abandonment, which recorded the run as finished while its
			// goroutine still ran. The status still comes from
			// finalStatusForCanceledRun and not interruptedStatus(ResumePolicy):
			// a run the WATCHDOG killed, or a user canceled, that merely took
			// longer than the grace to unwind must not become resumable (hit on
			// 2026-09-22: a watchdog-killed library.scan returned 87s later and
			// was left resumable). Whether a run is abandoned says how slowly
			// it unwound, not why it was stopped.
			//
			// Order: the decided status is stored before the slot is released
			// (Shutdown reads zombieFinal for a handle whose slot is released,
			// under r.mu), and v3 records stopping before Zombies() lists the
			// run, so the two never disagree. The zombie flag is set BEFORE the
			// slot is released, and the handle never leaves r.running until the
			// goroutine exits, so RetryInterrupted and Cancel always see either
			// a live handle or a finished run. The fence was already revoked
			// (select above).
			abandonStatus, reason, abandonOutcome := r.finalStatusForCanceledRun(runCtx, h, qr, timeout)
			abandonMsg := "abandoned: op goroutine did not exit within grace after context cancellation"
			if reason != nil {
				// The decided reason leads: a timeout's message must start
				// with state.TimeoutPrefix, which is how a v2 "canceled" row
				// says it timed out, and what the v3 projection reads to
				// record timed_out (state.FromV2) rather than canceled -- the
				// same outcome the boot sweep gives this run after a crash.
				abandonMsg = *reason + "; " + abandonMsg
			}
			h.zombieFinal.Store(&zombieOutcome{status: abandonStatus, msg: &abandonMsg, outcome: abandonOutcome})
			r.markStopping(qr, h, r.stopReasonFor(runCtx, h), true)
			h.zombieSince.Store(r.livenessClock().UnixNano())
			h.zombie.Store(true)
			r.mu.Lock()
			r.releaseSlotKeepKeyLocked(qr.opID)
			r.mu.Unlock()
			r.pingDispatch()
			// R-3: the runaway goroutine keeps calling reporter.Log even though
			// its flushLoop has exited (runCtx canceled) — the buffered lines are
			// unrecoverable by design, so flip the reporter's terminal flag to make
			// those Log calls cheap no-ops and stop logBuf from growing unbounded.
			if dbr, ok := reporter.(*dbReporter); ok {
				dbr.markTerminal()
			}
			r.logger.Warn("registry: op goroutine abandoned; spawning replacement worker",
				"op_id", qr.opID, "plugin", qr.plugin, "final_status", abandonStatus)
			r.goroutineWG.Go(func() { r.startWorker(parentCtx, -1) })
			// Enrolled in goroutineWG: since 05-PR4 this monitor writes the
			// final status, publishes and may re-queue a scan, so Shutdown's
			// drain must cover it like any other store writer. Go from this
			// already-enrolled worker goroutine, so the counter is non-zero.
			r.goroutineWG.Go(func() {
				<-done
				age := r.livenessClock().Sub(time.Unix(0, h.zombieSince.Load())).Round(time.Millisecond)
				// Status first, release second: the row leaves "running" while
				// the handle still holds the key, so nothing can start another
				// run of this id or key in between. op.terminal and the
				// dependency scheduler are told now, when the run is really
				// over, not at abandonment (05-PR4 moved both here from the
				// abandonment branch: a dependent must not start, and the UI
				// must not drop the run, while its goroutine still writes).
				// The final status was decided at abandonment (a user cancel
				// since may have replaced it; Cancel), but the run ends now.
				final := h.zombieFinal.Load()
				endedAt := time.Now().UTC()
				out := r.writeRunTerminalStatus(qr, h, final.status, final.outcome, &endedAt, final.msg)
				if out.publish {
					r.publishOpTerminal(qr.opID, qr.defID, out.status)
				}
				// The run ends now, so it is counted now, with the duration
				// up to its goroutine's return. Until 05-PR2 this path counted
				// nothing in either family, so a timed-out run that ignored its
				// context was never a failure or a timed_out run.
				r.recordRunMetrics(qr.defID, out.outcome, runStartedAt)
				r.notifyDepTerminal(out.status, qr)
				// Release the key and write set now that the goroutine is gone.
				// Handle first, abandoned counter second -- the shutdown branch's
				// order -- so a caller that waits for AbandonedCount to drain
				// never finds the handle still registered. The identity check
				// is belt and braces: Gate 0 refuses to claim an op id that is
				// in r.running, so no other handle can sit under this id.
				r.mu.Lock()
				if cur, ok := r.running[qr.opID]; ok && cur == h {
					r.releaseRunHandleLocked(qr.opID)
				}
				r.mu.Unlock()
				r.pingDispatch()
				r.abandoned.decrement(qr.plugin)
				// A scan quiesced by a stand-down is re-queued by the release
				// path only once its row reads interrupted_quiesced, which is
				// now; hand it to the stand-down so a release that already
				// happened is not missed (the pickup gate does the same).
				//
				// Not while shutting down: the rows stay interrupted_quiesced
				// and the next boot's resume picks them up (the convention
				// endScanStandDownGrace keeps). Re-queueing now would publish
				// a run nobody will start, and count a second resume against
				// the restart-strike guard for one restart.
				if qr.defID == scanStandDownDefID && out.status == "interrupted_quiesced" && !r.shuttingDown.Load() {
					r.resumeDroppedScanOnRelease(qr.opID)
				}
				r.logger.Info("registry: abandoned goroutine returned",
					"op_id", qr.opID, "plugin", qr.plugin, "zombie_age", age, "status", out.status)
			})
			return true // signal caller to exit
		}
	}

	// We have a result. Release the handle and write terminal status.
	r.releaseRunHandle(qr.opID)

	var finalStatus, outcome string
	var errMsg *string
	completedAt := time.Now().UTC()

	switch {
	case ctxCanceled:
		finalStatus, errMsg, outcome = r.finalStatusForCanceledRun(runCtx, h, qr, timeout)
	case runErr != nil:
		finalStatus, outcome = "failed", opsmetrics.OutcomeFailed
		msg := runErr.Error()
		errMsg = &msg
	default:
		finalStatus, outcome = "completed", opsmetrics.OutcomeCompleted
	}

	// A deadline hit is recorded natively in v3 first (stopping{timeout}),
	// so the v2 write below projects stopping -> timed_out as the
	// goroutine_returned event rather than an off-table v2_write. The row
	// keeps reading "running" (stopping mirrors as running) until that write.
	if outcome == opsmetrics.OutcomeTimedOut {
		r.markStopping(qr, h, state.StopTimeout, false)
	}
	out := r.writeRunTerminalStatus(qr, h, finalStatus, outcome, &completedAt, errMsg)
	finalStatus = out.status

	// R-1: fan out op.terminal so the UI bell stops showing this op as running.
	if out.publish {
		r.publishOpTerminal(qr.opID, qr.defID, finalStatus)
	}
	r.recordRunMetrics(qr.defID, out.outcome, runStartedAt)

	// Notify the dependency scheduler (async; non-blocking) so waiting_deps ops
	// for the same subject can be re-evaluated or failed as appropriate.
	r.notifyDepTerminal(finalStatus, qr)

	emitPendingProgress(reporter)
	emitOpFinishedLog(runCtx, reporter, runStartedAt, finalStatus, runErr, false, h.lastProgressAt.Load() != 0)
	r.logger.Info("registry: run finished", "op_id", qr.opID, "status", finalStatus)
	return false
}

// runTerminalWriteAttempts bounds writeRunTerminalStatus's compare-and-set
// retries when the row is not "running" (see there).
const runTerminalWriteAttempts = 4

// terminalOutcome is what a worker's final status write left behind.
type terminalOutcome struct {
	// status is the row's final status as far as this worker knows: what the
	// caller counts, logs and hands to the deps scheduler.
	status string
	// publish is false only when the worker would otherwise publish a STALE
	// interrupted_* status after a cancel moved its row to "canceled" (Cancel
	// published that). A "canceled" the worker found and kept is published
	// again: not every writer of "canceled" publishes (Shutdown's timeout
	// stamp does not), and a duplicate op.terminal is harmless where a missing
	// one leaves the legacy twin and the UI bell showing the op as running.
	publish bool
	// outcome is the ops.runs outcome of the run's end ("" for an interrupted
	// pause): the decided outcome, or canceled when a user's cancel was kept
	// over it. It is carried, not derived from status, because a timeout and
	// a cancel share the status "canceled".
	outcome string
}

// keepsUserCancel reports whether a run whose own outcome is status should
// leave a user's "canceled" on its row. An interrupted_* status would make a
// canceled run resumable (the stand-down release or the boot sweep re-queues
// it), and "canceled" agrees with the cancel. A run that completed or failed
// did finish: its outcome stands over the cancel, exactly as when a cancel
// reaches the live handle too late to stop it.
func keepsUserCancel(status string) bool {
	return status == "canceled" || strings.HasPrefix(status, "interrupted_")
}

// writeRunTerminalStatus writes the final status of a run that reached Run.
//
// The write is a compare-and-set from "running". Every exit path releases the
// run's handle BEFORE it decides and writes the final status, so in that gap
// the row reads "running" with no handle, and Registry.Cancel cancels such a
// row itself (cancelByRow). So when the row is not "running":
//   - "canceled", and the run's outcome is interrupted_* or canceled
//     (keepsUserCancel): the cancel stands. An unconditional write here wrote
//     interrupted_quiesced over it for a scan quiesced by a stand-down, and
//     the stand-down release re-queued the scan the user had canceled.
//   - "canceled", and the run completed or failed: the run's outcome is
//     written over "canceled" (compare-and-set from "canceled"). Keeping the
//     cancel recorded completed work as canceled, and the deps scheduler then
//     failed every dependent waiting on it.
//   - anything else (Shutdown stamps an interrupted status on a run that did
//     not drain in time): the run's own outcome is written over it, as the
//     unconditional write always did, with a compare-and-set from what was
//     read.
//
// A store error, or a row that keeps changing, is logged and the decided
// status returned, as before: it is the run's own outcome and always a final
// status for the run, never a live one the row might momentarily hold.
// settleInterrupted then covers the other order: a cancel that set
// userCanceled after the status was decided.
//
// outcome is the decided ops.runs outcome of status (finalStatusForCanceledRun
// for a stopped run). The result carries it, except where the row's existing
// cancel is kept: that run ends canceled, even when it had decided timed_out.
func (r *Registry) writeRunTerminalStatus(qr *queuedRun, h *runHandle, status, outcome string, completedAt *time.Time, errMsg *string) terminalOutcome {
	from := "running"
	for attempt := 1; ; attempt++ {
		applied, err := r.store.UpdateOperationV2StatusIf(qr.opID, from, status, nil, completedAt, errMsg)
		if err != nil {
			r.logger.Warn("registry: failed to write op terminal status",
				"op_id", qr.opID, "def_id", qr.defID, "status", status, "error", err)
			return terminalOutcome{status: status, publish: true, outcome: outcome}
		}
		if applied {
			break
		}
		row, gerr := r.store.GetOperationV2(qr.opID)
		if gerr != nil || row == nil || row.ID == "" {
			r.logger.Warn("registry: op terminal status not written; the row is no longer running and could not be re-read",
				"op_id", qr.opID, "def_id", qr.defID, "status", status, "error", gerr)
			return terminalOutcome{status: status, publish: true, outcome: outcome}
		}
		if row.Status == "canceled" && keepsUserCancel(status) {
			r.logger.Info("registry: run stopped after its row was canceled; keeping canceled",
				"op_id", qr.opID, "def_id", qr.defID, "run_status", status)
			// Published even though Cancel may already have: Shutdown's
			// timeout stamp also writes "canceled" and publishes nothing.
			// The row's cancel is what the run ended as, so it is counted
			// as canceled even when this run had decided timed_out.
			return terminalOutcome{status: "canceled", publish: true, outcome: opsmetrics.OutcomeCanceled}
		}
		if row.Status == "canceled" && errMsg == nil {
			// The run completed: its row must not keep the cancel's message.
			// An empty message clears it (a nil one leaves it unchanged).
			cleared := ""
			errMsg = &cleared
		}
		if row.Status == "canceled" {
			r.logger.Info("registry: run finished before the cancel could stop it; recording its outcome over canceled",
				"op_id", qr.opID, "def_id", qr.defID, "run_status", status)
		}
		if attempt >= runTerminalWriteAttempts {
			r.logger.Warn("registry: op terminal status not written; the row's status kept changing",
				"op_id", qr.opID, "def_id", qr.defID, "status", status, "row_status", row.Status)
			return terminalOutcome{status: status, publish: true, outcome: outcome}
		}
		from = row.Status
	}
	return r.settleInterrupted(qr, h, status, outcome, completedAt)
}

// settleInterrupted runs after this worker wrote status to its row. For an
// interrupted_* status it settles a racing user cancel; any other status is
// returned as written.
//
// finalStatusForCanceledRun reads userCanceled once, when it decides. A cancel
// that sets the flag after that decision (Cancel found the handle, then the
// run decided interrupted_quiesced on a stand-down quiesce or an interrupted
// status on shutdown) relies on its own read of the row to cancel it, which
// it makes only after setting the flag; if that read came before this
// worker's write it saw "running" and stopped there. Checking the flag after
// the write closes that: either this check sees the flag and moves the row to
// "canceled" (a compare-and-set from the status just written, so it never
// undoes anyone else's write), or the cancel's read comes after the write and
// cancels the interrupted row itself. In that second case the row is
// "canceled" and Cancel published it, so the stale interrupted status is not
// published; the re-read narrows that window, it cannot close it (Cancel may
// still land after it), and the row is right either way.
//
// outcome is status's ops.runs outcome; it is returned unless the run ends
// canceled here.
func (r *Registry) settleInterrupted(qr *queuedRun, h *runHandle, status, outcome string, completedAt *time.Time) terminalOutcome {
	if !strings.HasPrefix(status, "interrupted_") {
		return terminalOutcome{status: status, publish: true, outcome: outcome}
	}
	if h != nil && h.userCanceled.Load() {
		msg := "canceled by user while the run was being interrupted"
		applied, err := r.store.UpdateOperationV2StatusIf(qr.opID, status, "canceled", nil, completedAt, &msg)
		if err != nil {
			r.logger.Warn("registry: failed to record a user cancel over an interrupted status",
				"op_id", qr.opID, "def_id", qr.defID, "status", status, "error", err)
		} else if applied {
			r.logger.Info("registry: user canceled the run while it was being interrupted; recorded canceled",
				"op_id", qr.opID, "def_id", qr.defID, "interrupted_status", status)
			return terminalOutcome{status: "canceled", publish: true, outcome: opsmetrics.OutcomeCanceled}
		}
	}
	if row, err := r.store.GetOperationV2(qr.opID); err == nil && row != nil && row.Status == "canceled" {
		return terminalOutcome{status: "canceled", publish: false, outcome: opsmetrics.OutcomeCanceled}
	}
	return terminalOutcome{status: status, publish: true, outcome: outcome}
}

// recordRunMetrics publishes one finished run attempt, keyed by def_id, to the
// legacy lifecycle counters and operation_duration_seconds histogram and to
// their OTel twins ops.runs and ops.run.duration (both families are emitted
// during the soak). The in-process path, the subprocess (Isolate=true) path
// and the zombie monitor call it right after the terminal write, so every
// attempt that executed is observed once, when it really ends.
//
// outcome is the run's ops.runs outcome (terminalOutcome.outcome), not its v2
// status: a timeout and a cancel are both "canceled" in v2. The legacy
// counters map it as completed -> operations_completed_total; failed and
// timed_out -> operations_failed_total (a deadline hit is a failure of the
// op, and until 05-PR2 it was counted as canceled); canceled ->
// operations_canceled_total; dropped has no legacy counter. "" is an
// interrupted pause (interrupted_quiesced, interrupted_ask): no outcome, the
// resumed attempt reports its own. The legacy duration is observed for every
// attempt, so a run canceled after three hours still shows up as three hours;
// ops.run.duration is observed with each run-end outcome.
//
// Before 2026-10-03 the four Inc* helpers and ObserveOperationDuration were
// defined in internal/metrics and called from nowhere: the deploy/prometheus
// alert rules on operations_failed_total had never had a sample to fire on.
func (r *Registry) recordRunMetrics(defID, outcome string, startedAt time.Time) {
	incLegacyOutcome(defID, outcome)
	took := time.Since(startedAt)
	metrics.ObserveOperationDuration(defID, took)
	r.ops.Finished(defID, outcome, took)
}

// recordRunEnd counts a run end that no attempt in this process timed: the
// boot sweep finishing a stopped run or dropping one, a run force-dropped or
// quiesced-and-dropped before it started. Same counters as recordRunMetrics,
// no durations. "" records nothing.
func (r *Registry) recordRunEnd(defID, outcome string) {
	incLegacyOutcome(defID, outcome)
	r.ops.Ended(defID, outcome)
}

// incLegacyOutcome is recordRunMetrics' legacy counter mapping.
func incLegacyOutcome(defID, outcome string) {
	switch outcome {
	case opsmetrics.OutcomeCompleted:
		metrics.IncOperationCompleted(defID)
	case opsmetrics.OutcomeFailed, opsmetrics.OutcomeTimedOut:
		metrics.IncOperationFailed(defID)
	case opsmetrics.OutcomeCanceled:
		metrics.IncOperationCanceled(defID)
	}
}

// pauseOutcome is the ops.runs outcome of an interruptedStatus result:
// dropped for interrupted_dropped (a ResumeDrop run does not come back), ""
// for interrupted_quiesced (a pause; the resumed attempt reports its own).
func pauseOutcome(status string) string {
	outcome, _ := opsmetrics.OutcomeFor(status, "")
	return outcome
}

// newAttemptItems builds the ops.items counter for this attempt, seeded with
// the row's persisted progress. A fresh row carries 0/0 (or its queued
// summary), so nothing changes for it; a resumed or retried row still carries
// the previous attempt's progress, and seeding from it keeps a resumed op whose
// first report is its checkpoint position from counting those items again. An
// attempt that restarts below the seed (a retry from zero, a checkpoint that
// trails the flushed progress) starts a segment at its first report instead
// of losing every report up to the old position (processedItems). A failed
// read seeds 0/0, the fresh-run behaviour.
func (r *Registry) newAttemptItems(opID, defID string) *processedItems {
	var cur, total int
	if row, err := r.store.GetOperationV2(opID); err == nil && row != nil {
		cur, total = row.ProgressCurrent, row.ProgressTotal
	}
	return newProcessedItems(r.ops, defID, cur, total)
}

// notifyDepTerminal notifies the dependency scheduler for every subject of a
// run that reached a terminal status. C-5: ALL terminal transitions notify —
// "completed" as a completion, everything else (failed, canceled, timeout,
// interrupted_*) as a failure — so dep-waiting subjects are resolved
// immediately instead of falling back to the 5m sweep. subjectsFromParams
// handles both single-subject {"book_id":"..."} params and batched
// {"subjects":[...]} params so every subject in a batch op is notified.
func (r *Registry) notifyDepTerminal(finalStatus string, qr *queuedRun) {
	for _, sub := range subjectsFromParams(qr.params) {
		if finalStatus == "completed" {
			r.notifyDepCompletion(sub, qr.defID)
		} else {
			r.notifyDepFailed(sub, qr.defID)
		}
	}
}

// neverReportedGrace is how long an op may run without reporting before its
// silence is treated as a wiring defect rather than ordinary brevity. Most ops
// are legitimately sub-second and have nothing to report; the threshold sits
// well below the 5m ProgressTimeout so the warning arrives while the op is
// still succeeding, not after it starts being killed.
const neverReportedGrace = 60 * time.Second

// shouldWarnNeverReported is split out as a pure function so the policy can be
// tested without standing up a registry, a store and a worker pool.
func shouldWarnNeverReported(runDuration time.Duration, everReported bool) bool {
	return !everReported && runDuration >= neverReportedGrace
}

// emitPendingProgress writes the progress line the log throttle was holding
// back, so a run's final progress line lands before its "operation finished"
// line. A reporter that is not DB-backed has nothing pending.
func emitPendingProgress(rep Reporter) {
	if dbr, ok := rep.(*dbReporter); ok {
		dbr.emitPendingProgressLine()
	}
}

// emitOpFinishedLog emits the canonical "operation finished" line through
// the reporter's tagged logger. Every op gets this line regardless of
// whether its Run emitted one. Downstream readers can rely on a
// phase=end tag + structured outcome instead of parsing the message.
func emitOpFinishedLog(ctx context.Context, rep Reporter, startedAt time.Time, outcome string, runErr error, subprocess bool, everReported bool) {
	runDuration := time.Since(startedAt)
	durMs := runDuration.Milliseconds()
	attrs := []slog.Attr{
		slog.String("phase", "end"),
		slog.String("outcome", outcome),
		slog.Int64("duration_ms", durMs),
		slog.Bool("subprocess", subprocess),
	}

	// An op that did real work without ever reporting is one slow run away from
	// being killed by the watchdog, and says nothing about itself until then.
	// Flagging it on a SUCCESSFUL run is what turns a latent outage into a bug
	// report: library.scan was in this state from 2026-05-11, completing
	// quietly on a small library, and only became visible once the library grew
	// past five minutes of scanning. The watchdog cannot catch this case --
	// by definition the op finished in time.
	if shouldWarnNeverReported(runDuration, everReported) {
		rep.Logger().LogAttrs(ctx, slog.LevelError, "operation never reported progress",
			slog.String("phase", "end"),
			slog.String("outcome", outcome),
			slog.Int64("duration_ms", durMs),
			slog.String("hint", "this op ran for "+runDuration.Round(time.Second).String()+
				" without a single reporter.UpdateProgress call; it will be canceled as stuck once it "+
				"exceeds ProgressTimeout. Run the work through registry.RunItems, or report directly."),
		)
	}
	if runErr != nil {
		attrs = append(attrs, slog.String("error", runErr.Error()))
	}
	level := slog.LevelInfo
	switch outcome {
	case "failed":
		level = slog.LevelError
	case "canceled", "interrupted_dropped", "interrupted_restart":
		level = slog.LevelWarn
	}
	rep.Logger().LogAttrs(ctx, level, "operation finished", attrs...)
}

// checkInfiniteRestart checks whether an op should be force-dropped due to
// repeated restarts without progress. Returns true if the op was force-dropped
// (terminal status written, handle released).
func (r *Registry) checkInfiniteRestart(qr *queuedRun, def OperationDef) bool {
	row, err := r.store.GetOperationV2(qr.opID)
	if err != nil || row == nil {
		return false
	}
	// Count only AUTOMATIC restarts since the operator last retried this row.
	// A manual retry (RetryInterrupted) never increments resume_count and
	// records resume_count_at_manual_retry = resume_count, so the operator's
	// explicit decision to run it again is not mistaken for a boot loop. That
	// matters most for the row this guard itself force-dropped: it carries
	// resume_count >= 3 with no progress by construction, so without the
	// baseline every manual retry of it would be re-dropped on first dispatch.
	// After three further automatic restarts without progress the guard
	// re-arms on its own; there is no flag to clear.
	if row.ResumeCount-row.ResumeCountAtManualRetry < 3 {
		return false
	}
	// Check if high_water_progress advanced. We use 0 as the baseline when
	// no prior state is tracked; if it's still 0 after 3 restarts, force drop.
	// (The reporter writes high_water_progress in UOS-03; for now we use
	// whatever value is in the DB.)
	if row.HighWaterProgress > 0 {
		return false
	}

	// Write infinite_restart strike.
	r.writeStrike(qr.opID, def.ID, def.Plugin, "infinite_restart",
		fmt.Sprintf("resume_count=%d high_water_progress=%d; forcing drop", row.ResumeCount, row.HighWaterProgress))

	// Mark interrupted_dropped.
	completed := time.Now().UTC()
	msg := "force-dropped: infinite restart without progress"
	if err := r.store.UpdateOperationV2Status(qr.opID, "interrupted_dropped", nil, &completed, &msg); err != nil {
		// The row stays queued, so this drop repeats every dispatch cycle until
		// the write lands. Say so rather than loop silently.
		r.logger.Warn("registry: failed to mark force-dropped op interrupted_dropped; it will be dropped again",
			"op_id", qr.opID, "def_id", def.ID, "error", err)
	} else {
		// Counted only once the write lands, so a retried drop counts once.
		r.recordRunEnd(def.ID, opsmetrics.OutcomeDropped)
	}
	// R-1: fan out op.terminal for the force-drop transition.
	r.publishOpTerminal(qr.opID, def.ID, "interrupted_dropped")
	// C-2: release the dispatcher's stub handle. The force-drop path returned
	// before executeRun ever registered/released the run, leaking the
	// ConcurrencyKey and the pluginRunning slot until restart — which blocked
	// every def sharing that key (e.g. "acoustid.fingerprint").
	r.releaseRunHandle(qr.opID)
	// Terminal transition: notify dep-waiting subjects (C-5).
	for _, sub := range subjectsFromParams(qr.params) {
		r.notifyDepFailed(sub, qr.defID)
	}
	r.logger.Warn("registry: force-dropping op due to infinite restart",
		"op_id", qr.opID, "def_id", qr.defID, "resume_count", row.ResumeCount)
	return true
}

// safeRun calls def.Run with panic recovery, returning any panic as an error.
func (r *Registry) safeRun(ctx context.Context, def OperationDef, params json.RawMessage, rep Reporter) (runErr error) {
	defer func() {
		if rec := recover(); rec != nil {
			runErr = fmt.Errorf("operation panicked: %v", rec)
			r.logger.Error("registry: op panicked", "def_id", def.ID, "panic", rec)
		}
	}()

	// Create a root span for this operation execution.
	_, span := operationTracer.Start(ctx, "operation.run",
		trace.WithAttributes(
			attribute.String("operation_id", def.ID),
			attribute.String("operation_name", def.DisplayName),
			attribute.String("plugin", def.Plugin),
		))
	defer span.End()

	runErr = def.Run(ctx, params, rep)
	if runErr != nil {
		span.RecordError(runErr)
		span.SetAttributes(attribute.Bool("error", true))
	}
	return runErr
}

// finalStatusForCanceledRun decides the terminal status for a run whose context
// was canceled, and is the single place that decision is made. Both terminal
// switches call it -- the in-process one and the subprocess (Isolate) one -- so
// the two cannot drift. They already had drifted once: each carried its own copy
// of the C-5 timeout logic.
//
// The precedence is load-bearing and the order is not arbitrary:
//
//  1. DEADLINE FIRST. A def.Timeout expiry is a failure of the op, not an
//     interruption of the server, and C-5 exists to make it distinguishable from
//     a user cancel. Checking shutdown before this would silently undo C-5 for
//     any op whose timeout happens to expire during a shutdown.
//  2. USER INTENT NEXT. A run canceled by Registry.Cancel or by the watchdog
//     killing a stuck op must stay canceled. Deciding this from the shutdown flag
//     alone would resurrect deliberately-killed work on the next boot -- and for
//     the watchdog case would produce a stuck -> cancel -> resume -> stuck loop.
//  3. SCAN STAND-DOWN. A run quiesced by AcquireScanStandDown must be RESUMABLE:
//     it was making progress and a maintenance op deliberately stood it down to
//     do filesystem work, intending it to continue afterward. Placed after user
//     intent so a genuine Registry.Cancel of a scan still wins and stays canceled.
//  4. SHUTDOWN. Also a resumable status: the run was making progress and the
//     server stopped it.
//  5. Otherwise a plain cancel, which is the pre-existing behaviour.
//
// A resumable status (interrupted_quiesced) DOES come back: resumeAfterStartup
// reads ListResumableOperationsV2, which includes interrupted_quiesced rows (see
// resume.go), and for a scan stand-down releaseScanStandDown also re-queues the
// row immediately via resumeQuiescedOp. (This comment previously claimed the
// status recorded here never resumes; that predated the ListResumableOperationsV2
// change and was stale.)
//
// The third result is the run's ops.runs outcome (opsmetrics), which the v2
// status cannot carry: timed_out for a deadline (stored as "canceled" with a
// state.TimeoutPrefix message, which the previous binary reads as canceled
// and the v3 projection reads as timed_out), canceled for a cancel, dropped
// for a stop that left a ResumeDrop run interrupted_dropped, and "" for an
// interrupted pause, which is not an outcome.
func (r *Registry) finalStatusForCanceledRun(runCtx context.Context, h *runHandle, qr *queuedRun, timeout time.Duration) (status string, msg *string, outcome string) {
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		m := fmt.Sprintf("%s run exceeded %s", state.TimeoutPrefix, timeout)
		r.logger.Warn("registry: run timed out",
			"op_id", qr.opID, "def_id", qr.defID, "timeout", timeout)
		return "canceled", &m, opsmetrics.OutcomeTimedOut
	}
	if h != nil && h.userCanceled.Load() {
		return "canceled", nil, opsmetrics.OutcomeCanceled
	}
	if h != nil && h.quiescing.Load() {
		status := interruptedStatus(qr.resumePolicy)
		m := "quiesced: stood down for a maintenance op holding the scan gate"
		r.logger.Info("registry: run quiesced for scan stand-down",
			"op_id", qr.opID, "def_id", qr.defID, "status", status)
		return status, &m, pauseOutcome(status)
	}
	if r.shuttingDown.Load() {
		status := interruptedStatus(qr.resumePolicy)
		m := "interrupted: server shutdown during run"
		r.logger.Info("registry: run interrupted by shutdown",
			"op_id", qr.opID, "def_id", qr.defID, "status", status)
		return status, &m, pauseOutcome(status)
	}
	return "canceled", nil, opsmetrics.OutcomeCanceled
}

// stopReasonFor names why a canceled run was stopped, in the v3 vocabulary,
// with finalStatusForCanceledRun's precedence: a deadline is a timeout; a
// user cancel or a watchdog kill (both set userCanceled, so they cannot be
// told apart here and are recorded as "user", whose terminal state is
// canceled -- what v2 records for both); a stand-down quiesce; a shutdown;
// anything else a plain cancel.
func (r *Registry) stopReasonFor(runCtx context.Context, h *runHandle) string {
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return state.StopTimeout
	case h != nil && h.userCanceled.Load():
		return state.StopUser
	case h != nil && h.quiescing.Load():
		return state.StopQuiesce
	case r.shuttingDown.Load():
		return state.StopShutdown
	}
	return state.StopUser
}

// markStopping records a stopped run as v3 stopping{reason, zombie}
// (TransitionRunV3 stop_requested, a compare-and-set on the run's stored
// state, attempt and fence epoch). The v2 row is untouched in substance:
// stopping mirrors as "running". Best effort: a store without the run's v3
// record (a fake, or a row the v2 registry wrote before migration 065) or a
// run that is no longer running is logged and left alone -- the in-process
// zombie handle is what holds the key either way. h.stoppingRecorded says
// whether it stored the stop.
//
// zombie is true for a run whose goroutine outlived the abandon grace. The
// other caller is a run that timed out and returned (05-PR2): it is stopping
// only between this call and its terminal write, which then projects
// stopping{timeout} -> timed_out as goroutine_returned, the event v3 names
// for that end; written straight from running, the audit trail could only
// call it an off-table v2_write.
func (r *Registry) markStopping(qr *queuedRun, h *runHandle, reason string, zombie bool) {
	rec, err := r.getRunV3(qr.opID)
	if err != nil || rec == nil {
		r.logger.Debug("registry: no v3 run record to mark stopping", "op_id", qr.opID, "error", err)
		return
	}
	if rec.State != state.V3Running {
		r.logger.Info("registry: stopped run is no longer running in v3; not marking it stopping",
			"op_id", qr.opID, "v3_state", rec.State, "zombie", zombie)
		return
	}
	if _, err := r.transitionRunV3(database.TransitionRequest{
		ID:               qr.opID,
		ExpectState:      rec.State,
		ExpectAttempt:    rec.Attempt,
		ExpectFenceEpoch: rec.FenceEpoch,
		Event:            state.EvStopRequested,
		StopReason:       reason,
		Zombie:           zombie,
		Actor:            &database.Actor{AuthMethod: "system"},
	}); err != nil {
		r.logger.Warn("registry: could not record the stopped run as stopping", "op_id", qr.opID, "zombie", zombie, "error", err)
		return
	}
	h.stoppingRecorded.Store(true)
}
