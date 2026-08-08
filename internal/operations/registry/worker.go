// file: internal/operations/registry/worker.go
// version: 2.13.0
// guid: b8c9d0e1-f2a3-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-08-07

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
	writes        []Resource
	cancel        context.CancelFunc
	abandoned     bool
	currentItem   string
	currentItemMu sync.Mutex
	// lastProgressAt is stamped by the reporter on every UpdateProgress call.
	// Stored as Unix nanoseconds; zero means progress has never been reported.
	// The watchdog reads this first (lock-free) before falling back to the DB row.
	lastProgressAt atomic.Int64
	// lastUncheckpointedStrike (Unix nanoseconds) dedupes the watchdog's
	// uncheckpointed strikes: at most one strike per threshold interval per op
	// (C-4). Only the watchdog goroutine touches it; atomic for -race hygiene.
	lastUncheckpointedStrike atomic.Int64
	// queuedCancel is set by Registry.Cancel on a dispatcher STUB handle (nil
	// cancel func) when the op is canceled while sitting in the buffered
	// nextRun channel. The worker checks it — under Registry.mu, the same lock
	// that guards the write — before registering the full handle, and drops
	// the run without ever invoking Run (C-1). Guarded by Registry.mu.
	queuedCancel bool
}

// cancelIfActive cancels the run's context if it has been wired up.
//
// The dispatcher (dispatcher.go) inserts a *stub* handle into r.running with a
// nil cancel func to block Gate-0 re-dispatch the instant an op is claimed; the
// worker overwrites it with the full handle (with cancel) on pickup
// (worker.go). Between those two events a handle is present in r.running with
// cancel == nil. Callers that walk r.running and cancel (Shutdown, Cancel, the
// watchdog) must go through this guard so they no-op on a stub instead of
// panicking with a nil-pointer dereference. For Shutdown this is fully correct:
// shuttingDown is set before the walk, so a stubbed op is never executed by the
// worker that eventually picks it up.
func (h *runHandle) cancelIfActive() {
	if h != nil && h.cancel != nil {
		h.cancel()
	}
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
	r.mu.RLock()
	def, ok := r.defs[qr.defID]
	r.mu.RUnlock()
	if !ok {
		r.logger.Warn("registry: worker got run for unknown def; skipping", "def_id", qr.defID)
		// Release the dispatcher's stub handle so its plugin slot and
		// ConcurrencyKey don't leak (same leak shape as the force-drop path, C-2).
		r.releaseRunHandle(qr.opID)
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
	runCtx, cancel := context.WithTimeout(parentCtx, timeout)
	// Decorate the run context (e.g. install a context-bound slog.Logger that
	// tags every line with the operation id) via the optional decorator hook.
	// Nil is the default when no decorator was wired via
	// SetRunContextDecorator — runs proceed undecorated.
	if r.runContextDecorator != nil {
		runCtx = r.runContextDecorator(runCtx, qr.opID)
	}
	defer cancel()

	// Register the handle. Before overwriting the dispatcher's stub, honor a
	// cancel that arrived while the run sat in the buffered nextRun channel
	// (C-1): the stub has a nil cancel func, so Registry.Cancel flags it with
	// queuedCancel instead. The flag is written and read under r.mu, so this
	// check races with nothing — if Cancel got the lock first, we see the flag
	// and drop the run without ever invoking Run.
	h := &runHandle{
		id:             qr.opID,
		defID:          qr.defID,
		plugin:         qr.plugin,
		concurrencyKey: qr.concurrKey,
		resumePolicy:   qr.resumePolicy,
		writes:         def.Writes,
		cancel:         cancel,
	}
	r.mu.Lock()
	if prev, exists := r.running[qr.opID]; exists && prev.queuedCancel {
		r.mu.Unlock()
		r.releaseRunHandle(qr.opID)
		now := time.Now().UTC()
		msg := "canceled before start (canceled while queued for a worker)"
		if err := r.store.UpdateOperationV2Status(qr.opID, "canceled", nil, &now, &msg); err != nil {
			r.logger.Warn("registry: failed to mark channel-canceled op canceled", "op_id", qr.opID, "error", err)
		}
		// R-1: this is a terminal transition too — publish op.terminal so the UI
		// bell drops the op instead of leaving it phantom-"running".
		r.publishOpTerminal(qr.opID, qr.defID, "canceled")
		r.logger.Info("registry: dropped run canceled while queued in worker channel", "op_id", qr.opID, "def_id", qr.defID)
		return false
	}
	r.running[qr.opID] = h
	r.mu.Unlock()

	// Mark running in DB.
	now := time.Now().UTC()
	if err := r.store.UpdateOperationV2Status(qr.opID, "running", &now, nil, nil); err != nil {
		r.logger.Warn("registry: failed to mark op running", "op_id", qr.opID, "error", err)
	}

	r.logger.Info("registry: starting run", "op_id", qr.opID, "def_id", qr.defID)

	// Build reporter (DB-backed). Pass setItemFn so SetCurrentItem updates
	// the runHandle's in-memory currentItem without a DB write. Pass touchFn
	// so UpdateProgress stamps lastProgressAt atomically — the watchdog reads
	// this instead of the DB row, preventing false-positive cancellations when
	// UpdateOpProgressV2 is blocked behind PebbleDB compaction.
	setItemFn := func(label string) { h.setCurrentItem(label) }
	touchFn := func() { h.lastProgressAt.Store(time.Now().UnixNano()) }
	flushInterval := def.ProgressFlushInterval
	if flushInterval <= 0 || flushInterval > 5*time.Minute {
		flushInterval = 30 * time.Second
	}
	reporter := newDBReporter(runCtx, qr.opID, qr.defID, def.DisplayName, qr.plugin,
		"", "", // traceID / spanID loaded from DB row in future; empty for now
		r.store, r.bus, r.activityRecorder, r.logger, setItemFn, touchFn, flushInterval, def.Synchronous)

	// Canonical "operation started" log line, with all the tags downstream
	// readers (op_log feed, activity-log enricher, digest aggregator) need
	// to group, filter, and search without parsing the message. Every op
	// gets this even if its Run forgets to emit one.
	runStartedAt := time.Now().UTC()
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
		var finalStatus string
		var errMsg *string
		completedAt := time.Now().UTC()
		if runCtx.Err() != nil {
			finalStatus = "canceled"
			// C-5: distinguish def.Timeout expiry from a user/watchdog cancel.
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				msg := fmt.Sprintf("timeout: run exceeded %s", timeout)
				errMsg = &msg
				r.logger.Warn("registry: subprocess run timed out", "op_id", qr.opID, "def_id", qr.defID, "timeout", timeout)
			}
		} else if runErr != nil {
			finalStatus = "failed"
			msg := runErr.Error()
			errMsg = &msg
		} else {
			finalStatus = "completed"
		}
		if err := r.store.UpdateOperationV2Status(qr.opID, finalStatus, nil, &completedAt, errMsg); err != nil {
			r.logger.Warn("registry: failed to update subprocess op terminal status", "op_id", qr.opID, "error", err)
		}
		// R-1: fan out op.terminal so the UI bell stops showing this op as running.
		r.publishOpTerminal(qr.opID, qr.defID, finalStatus)
		// C-5: notify the dep scheduler on ALL terminal transitions (the
		// subprocess path previously notified on none of them).
		r.notifyDepTerminal(finalStatus, qr)
		emitOpFinishedLog(runCtx, reporter, runStartedAt, finalStatus, runErr, true)
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
	// notifyDepFailed): this Add happens synchronously inside executeRun, which
	// is called from the already-enrolled startWorker goroutine, whose own
	// Done() has not run yet — so the counter is provably non-zero across this
	// Add and can never be observed at zero by a concurrent Wait().
	done := make(chan error, 1)
	r.goroutineWG.Add(1)
	go func() {
		defer r.goroutineWG.Done()
		done <- r.safeRun(runCtx, def, qr.params, reporter)
	}()

	// Wait for the run to finish or the context to be canceled.
	var runErr error
	var ctxCanceled bool

	select {
	case runErr = <-done:
		// Normal completion or run-level error; check if ctx was already done.
		if runCtx.Err() != nil {
			ctxCanceled = true
		}
	case <-runCtx.Done():
		ctxCanceled = true
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
			// Not shutting down: the op is genuinely abandoned. Free the slot now
			// and spawn a replacement so the pool doesn't shrink; the runaway
			// goroutine is monitored so the abandoned counter drains when it
			// eventually returns.
			r.releaseRunHandle(qr.opID)
			// C-3: write a terminal status so the row leaves the opv2 active
			// index. Without this the row stayed "running" forever, and the
			// ConcurrencyKey enqueue-dedupe in EnqueueOp kept returning this
			// zombie op's ID for every future enqueue of the def — silently
			// disabling the op type until restart. Mirror Shutdown's semantics:
			// interrupted_* per the def's ResumePolicy. Nothing else writes this
			// row's status afterwards: the runaway goroutine's eventual return
			// only decrements the abandoned counter (below).
			abandonedAt := time.Now().UTC()
			abandonStatus := interruptedStatus(qr.resumePolicy)
			abandonMsg := "abandoned: op goroutine did not exit within grace after context cancellation"
			if err := r.store.UpdateOperationV2Status(qr.opID, abandonStatus, nil, &abandonedAt, &abandonMsg); err != nil {
				r.logger.Warn("registry: failed to mark abandoned op interrupted", "op_id", qr.opID, "error", err)
			}
			// R-3: the runaway goroutine keeps calling reporter.Log even though
			// its flushLoop has exited (runCtx canceled) — the buffered lines are
			// unrecoverable by design, so flip the reporter's terminal flag to make
			// those Log calls cheap no-ops and stop logBuf from growing unbounded.
			if dbr, ok := reporter.(*dbReporter); ok {
				dbr.markTerminal()
			}
			// R-1: fan out op.terminal for the abandonment transition.
			r.publishOpTerminal(qr.opID, qr.defID, abandonStatus)
			// Terminal transition: resolve dep-waiting subjects now rather than
			// leaving them to the 5m sweep (C-5).
			for _, sub := range subjectsFromParams(qr.params) {
				r.notifyDepFailed(sub, qr.defID)
			}
			r.logger.Warn("registry: op goroutine abandoned; spawning replacement worker",
				"op_id", qr.opID, "plugin", qr.plugin)
			r.goroutineWG.Add(1)
			go func() { defer r.goroutineWG.Done(); r.startWorker(parentCtx, -1) }()
			go func() {
				<-done
				r.abandoned.decrement(qr.plugin)
				r.logger.Info("registry: abandoned goroutine returned",
					"op_id", qr.opID, "plugin", qr.plugin)
			}()
			return true // signal caller to exit
		}
	}

	// We have a result. Release the handle and write terminal status.
	r.releaseRunHandle(qr.opID)

	var finalStatus string
	var errMsg *string
	completedAt := time.Now().UTC()

	switch {
	case ctxCanceled:
		finalStatus = "canceled"
		// C-5: def.Timeout expiry used to be recorded as a bare "canceled" —
		// indistinguishable from a user cancel. The run context carries the
		// timeout deadline, so DeadlineExceeded means the run timed out (user/
		// watchdog cancels yield context.Canceled). Status stays "canceled"
		// (no new schema/UI status value) but the reason is persisted in
		// error_message and logged distinctly.
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			msg := fmt.Sprintf("timeout: run exceeded %s", timeout)
			errMsg = &msg
			r.logger.Warn("registry: run timed out", "op_id", qr.opID, "def_id", qr.defID, "timeout", timeout)
		}
	case runErr != nil:
		finalStatus = "failed"
		msg := runErr.Error()
		errMsg = &msg
	default:
		finalStatus = "completed"
	}

	if err := r.store.UpdateOperationV2Status(qr.opID, finalStatus, nil, &completedAt, errMsg); err != nil {
		r.logger.Warn("registry: failed to update op terminal status", "op_id", qr.opID, "error", err)
	}

	// R-1: fan out op.terminal so the UI bell stops showing this op as running.
	r.publishOpTerminal(qr.opID, qr.defID, finalStatus)

	// Notify the dependency scheduler (async; non-blocking) so waiting_deps ops
	// for the same subject can be re-evaluated or failed as appropriate.
	r.notifyDepTerminal(finalStatus, qr)

	emitOpFinishedLog(runCtx, reporter, runStartedAt, finalStatus, runErr, false)
	r.logger.Info("registry: run finished", "op_id", qr.opID, "status", finalStatus)
	return false
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

// emitOpFinishedLog emits the canonical "operation finished" line through
// the reporter's tagged logger. Every op gets this line regardless of
// whether its Run emitted one. Downstream readers can rely on a
// phase=end tag + structured outcome instead of parsing the message.
func emitOpFinishedLog(ctx context.Context, rep Reporter, startedAt time.Time, outcome string, runErr error, subprocess bool) {
	durMs := time.Since(startedAt).Milliseconds()
	attrs := []slog.Attr{
		slog.String("phase", "end"),
		slog.String("outcome", outcome),
		slog.Int64("duration_ms", durMs),
		slog.Bool("subprocess", subprocess),
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
	if row.ResumeCount < 3 {
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
	_ = r.store.UpdateOperationV2Status(qr.opID, "interrupted_dropped", nil, &completed, &msg)
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
