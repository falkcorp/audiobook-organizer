// file: internal/operations/registry/dispatcher.go
// version: 2.6.3
// guid: a7b8c9d0-e1f2-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-10-10

package registry

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// runDispatcher is the central dispatch loop. It ticks every 100ms or
// on a signal, walks queued ops in priority DESC / queued_at ASC order,
// and dispatches eligible ones to the worker pool.
func (r *Registry) runDispatcher(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("registry: dispatcher stopping")
			return
		case <-ticker.C:
			r.dispatchCycle(ctx)
		case <-r.dispatch:
			r.dispatchCycle(ctx)
		}
	}
}

// dispatchCycle walks all queued ops and sends eligible ones to nextRun.
func (r *Registry) dispatchCycle(ctx context.Context) {
	if r.shuttingDown.Load() {
		return
	}
	queued, err := r.store.ListQueuedOperationsV2()
	if err != nil {
		r.logger.Warn("registry: list queued ops failed", "error", err)
		return
	}

	// Prune write-set deferral log-dedupe entries for ops that left the queue
	// (dispatched, canceled, deleted) so the map cannot grow unbounded.
	r.mu.Lock()
	if len(r.writeSetDeferred) > 0 {
		queuedIDs := make(map[string]struct{}, len(queued))
		for _, row := range queued {
			queuedIDs[row.ID] = struct{}{}
		}
		for opID := range r.writeSetDeferred {
			if _, still := queuedIDs[opID]; !still {
				delete(r.writeSetDeferred, opID)
			}
		}
	}
	r.mu.Unlock()

	// Startup warmup gate (D44). While the store's in-memory read layer is still
	// warming after a restart, non-exempt ops are held HERE, before anything is
	// claimed: a held op keeps its queued row, takes no worker slot, no plugin
	// or ConcurrencyKey slot and no write-set claim, and has no run timeout or
	// watchdog clock started, so an exempt (interactive) op always finds a free
	// worker and nothing time-based is spent on the wait. Registry.Cancel and
	// Shutdown act on a held op exactly as on any queued op. The hold is bounded
	// by WarmupWaitTimeout.
	holdForWarmup := r.warmupHolding()
	if !holdForWarmup {
		r.restoreWarmupHeld()
	}

	for _, row := range queued {
		if ctx.Err() != nil {
			return
		}

		// Gate 0: already claimed / running? The worker marks the DB row
		// "running" only AFTER receiving from nextRun (worker.go:143), but
		// the dispatcher can fire again (via r.dispatch signal or the
		// 100ms ticker) in the gap between channel-send and worker-pickup.
		// Without this in-memory guard, ListQueuedOperationsV2() still
		// sees the row as "queued" and we re-dispatch the same opID —
		// observed in prod on dedup.book-merge running twice
		// (same op_id, two "dispatched op" + "starting run" log lines
		// 3ms apart, two book-merge complete events).
		r.mu.RLock()
		_, alreadyClaimed := r.running[row.ID]
		r.mu.RUnlock()
		if alreadyClaimed {
			continue
		}

		// Gate 1: def must be registered.
		// row.DefID may be a former ID (a row persisted before a rename); it
		// resolves to the canonical def, and everything built from this row
		// below carries def.ID, never the stored alias.
		def, ok := r.lookupDef(row.DefID)
		if ok {
			r.noteAliasUse(row.DefID, aliasEntryStoredRow)
		}
		if !ok {
			// Unknown def — skip; may appear during rolling restarts.
			continue
		}

		// Gate 1b: startup warmup (see holdForWarmup above). Exempt defs pass.
		if holdForWarmup && !def.NoWarmupWait {
			r.announceWarmupHeld(row)
			continue
		}

		r.mu.RLock()
		// Gate 2: plugin max_concurrent.
		maxC := r.pluginMax[def.Plugin]
		currentRunning := r.pluginRunning[def.Plugin]
		r.mu.RUnlock()
		if maxC > 0 && currentRunning >= maxC {
			continue
		}

		// Gate 2b: abandoned goroutine cap.
		if r.abandoned.isBlocked(def.Plugin) {
			r.logger.Warn("registry: plugin blocked due to abandoned goroutines; skipping dispatch",
				"plugin", def.Plugin, "abandoned", r.abandoned.countFor(def.Plugin))
			continue
		}

		// Gate 3: ConcurrencyKey already running?
		if def.ConcurrencyKey != "" {
			r.mu.RLock()
			holder, held := r.concurrencyKeys[def.ConcurrencyKey]
			r.mu.RUnlock()
			if held && holder != row.ID {
				continue
			}
		}

		// Gate 3.5: scan stand-down. While a maintenance op holds the scan gate
		// (AcquireScanStandDown), do NOT dispatch library.scan — leave it queued
		// so it resumes from its checkpoint once the gate clears. Only library.scan
		// is affected; all other ops pass. Mirrored in the claim block below.
		if def.ID == scanStandDownDefID && r.scanStandDownActive() {
			continue
		}

		// Gate 3b: declared write-set conflict? An op with a non-empty Writes
		// declaration must not start while any RUNNING op declares an
		// overlapping write-set — whole-row read-modify-write on both sides
		// means concurrent execution silently loses fields (the 2026-08-07
		// acoustid.backfill × maintenance.repair-transcribe-status incident).
		// The op stays QUEUED, exactly like the ConcurrencyKey gate, and
		// dispatches when the conflicting op releases its handle. Ops with
		// empty Writes bypass the gate entirely in both directions.
		if len(def.Writes) > 0 {
			r.mu.RLock()
			holder, overlap := r.writeSetConflictLocked(row.ID, def.Writes)
			r.mu.RUnlock()
			if holder != nil {
				r.logWriteSetDeferral(row.ID, row.DefID, holder, overlap)
				continue
			}
		}

		// Gate 4: DependsOn — all listed op defs must NOT be currently running.
		if blocked := r.checkDependsOn(def.DependsOn); blocked {
			continue
		}

		// All gates passed — claim and dispatch.
		r.mu.Lock()
		// Shutdown re-check (OPS-V2-DISPATCH-RACE). The check at the top of
		// this cycle is a whole store list plus a dispatch loop stale by now;
		// Shutdown can have begun anywhere in between. Stop claiming the moment
		// it has: nothing claimed after this point could run anyway (the
		// worker's pickup gate in executeRun drops it, and that gate -- not
		// this check -- is the guarantee, because the flag can still flip
		// between this unlock and the channel send). This is the early exit
		// that keeps a shutdown from logging "dispatched op" lines at all.
		if r.shuttingDown.Load() {
			r.mu.Unlock()
			return
		}
		// Re-check under write lock to avoid TOCTOU.
		if _, alreadyClaimed := r.running[row.ID]; alreadyClaimed {
			r.mu.Unlock()
			continue
		}
		maxC = r.pluginMax[def.Plugin]
		currentRunning = r.pluginRunning[def.Plugin]
		if maxC > 0 && currentRunning >= maxC {
			r.mu.Unlock()
			continue
		}
		if def.ConcurrencyKey != "" {
			if holder, held := r.concurrencyKeys[def.ConcurrencyKey]; held && holder != row.ID {
				r.mu.Unlock()
				continue
			}
		}
		// Gate 3.5 mirror (see read pass above): re-check the scan gate under the
		// write lock to close the TOCTOU window this file guards against.
		if def.ID == scanStandDownDefID && r.scanStandDownActive() {
			r.mu.Unlock()
			continue
		}
		if len(def.Writes) > 0 {
			if holder, _ := r.writeSetConflictLocked(row.ID, def.Writes); holder != nil {
				r.mu.Unlock()
				continue
			}
		}
		if def.ConcurrencyKey != "" {
			r.concurrencyKeys[def.ConcurrencyKey] = row.ID
		}
		r.pluginRunning[def.Plugin]++
		// Stub handle: blocks Gate 0 re-dispatch immediately. The worker
		// overwrites this with the full handle (with cancel func) on
		// pickup at worker.go:138.
		r.running[row.ID] = &runHandle{
			id:             row.ID,
			defID:          def.ID,
			plugin:         def.Plugin,
			concurrencyKey: def.ConcurrencyKey,
			resumePolicy:   def.ResumePolicy,
			writes:         def.Writes,
		}
		r.mu.Unlock()

		// Everything in `row` came from the ListQueuedOperationsV2 snapshot at
		// the top of this cycle, which is read WITHOUT holding r.mu. Two things
		// can have happened to the row since, and the claim just published is
		// what makes one re-read here enough to catch both.
		//
		// 1. The row is no longer queued. Gate 0 only knows about a run while
		//    its claim is in r.running, and the claim is released when the run
		//    ends. So a snapshot taken while the row still read "queued"
		//    (claimed by the previous cycle, not yet marked running by the
		//    worker) is stale by the time this loop reaches the row if that run
		//    has already ended: no claim, and the op is dispatched and RUN A
		//    SECOND TIME under the same id.
		//
		//      T0  this cycle lists X (status queued; X is claimed, in nextRun)
		//      T1  worker picks X up, marks it running, runs it, releases it
		//      T2  this loop reaches X: Gate 0 finds no claim
		//      T3  without the status check below, X is sent to a worker again
		//
		//    The window is as wide as the cycle, so it grows with the queue. It
		//    was caught in CI on 2026-10-03 (one op, two "starting run" lines in
		//    TestDispatcher_PriorityOrderingHighBeforeLow). The symptom is the
		//    same as the dedup.book-merge double run in Gate 0's comment, by a
		//    path Gate 0 cannot see. A row canceled before this re-read was
		//    dispatched the same way.
		//
		//    Nothing can start this row while the claim is held, and a run only
		//    starts through the worker's queued->running compare-and-set, so
		//    "queued" now means it has not run. This check is the early exit;
		//    the worker's compare-and-set is the guarantee (it also covers a
		//    cancel that lands after this read).
		//
		// 2. The params changed. For a def that can absorb a new request into
		//    an already-queued row, a merge can land between the snapshot and
		//    the claim:
		//
		//      T0  dispatchCycle reads row X, Params={BookIDs:[A]}
		//      T1  EnqueueOp merges B -> persists {BookIDs:[A,B]}, hands the
		//          caller X's op id, so the caller believes B is queued
		//      T2  this loop reaches X and claims it (X was not yet claimed at
		//          T1, so tryMergeQueuedParams was right to merge)
		//      T3  without the re-read, qr.params is the T0 snapshot [A]
		//
		//    The run then processes A only. B is never applied, progress
		//    reports "complete" against a total that never counted it, and the
		//    DB row shows [A,B] forever -- a silent drop with a success receipt.
		//    tryMergeQueuedParams takes r.mu and skips any row already in
		//    r.running, so once the claim is published no further merge can
		//    touch this row: merge-then-claim is caught by this re-read,
		//    claim-then-merge is refused by the merge.
		//
		// Until 2026-10-03 the re-read ran only for defs declaring
		// MergeQueuedParams, to save the read. It is one point read per
		// DISPATCHED op, not per queued row per cycle.
		fresh, freshErr := r.store.GetOperationV2(row.ID)
		if freshErr != nil {
			// Fail closed. Running a row we cannot confirm is the double run
			// and the silent drop this guard exists to prevent; if the op is
			// still queued, releasing the claim just retries next cycle.
			r.logger.Warn("registry: re-read of claimed op failed; releasing claim",
				"op_id", row.ID, "def_id", row.DefID, "error", freshErr)
			r.releaseClaim(row.ID, def.Plugin, def.ConcurrencyKey)
			continue
		}
		if fresh == nil {
			// Deleted between the snapshot and the claim.
			r.logger.Info("registry: queued op vanished before dispatch; releasing claim",
				"op_id", row.ID, "def_id", row.DefID)
			r.releaseClaim(row.ID, def.Plugin, def.ConcurrencyKey)
			continue
		}
		if fresh.Status != "queued" {
			r.logger.Info("registry: stale snapshot, op is no longer queued; releasing claim",
				"op_id", row.ID, "def_id", row.DefID, "status", fresh.Status)
			r.releaseClaim(row.ID, def.Plugin, def.ConcurrencyKey)
			continue
		}
		if fresh.Params != row.Params {
			r.logger.Info("registry: dispatching merged params captured after snapshot",
				"op_id", row.ID, "def_id", row.DefID)
		}
		params := json.RawMessage(fresh.Params)

		qr := &queuedRun{
			opID:         row.ID,
			defID:        def.ID,
			params:       params,
			priority:     Priority(row.Priority),
			concurrKey:   def.ConcurrencyKey,
			plugin:       def.Plugin,
			resumePolicy: def.ResumePolicy,
		}

		select {
		case r.nextRun <- qr:
			r.mu.Lock()
			delete(r.writeSetDeferred, row.ID)
			r.mu.Unlock()
			r.logger.Info("registry: dispatched op", "op_id", row.ID, "def_id", row.DefID)
		default:
			// Worker channel is full; undo accounting and try next cycle.
			r.releaseClaim(row.ID, def.Plugin, def.ConcurrencyKey)
		}
	}
}

// releaseClaim undoes the accounting published by the claim block in
// dispatchCycle when the op turns out not to be dispatchable after all (worker
// channel full, its row could not be re-read, or the re-read showed it is no
// longer queued). A row that is still queued is retried on a later cycle; one
// that is not simply stops appearing in the queue listing.
//
// Dropping the stub handle from r.running is the load-bearing part: it is what
// Gate 0 consults, so leaving it behind makes the op permanently
// un-dispatchable. Note this also re-opens the row to tryMergeQueuedParams,
// which is correct -- an op that is not going to run should still be able to
// absorb further requests.
func (r *Registry) releaseClaim(opID, plugin, concurrencyKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pluginRunning[plugin]--
	if concurrencyKey != "" {
		if holder := r.concurrencyKeys[concurrencyKey]; holder == opID {
			delete(r.concurrencyKeys, concurrencyKey)
		}
	}
	delete(r.running, opID)
}

// writeSetConflictLocked returns the first running op (other than candidateID
// itself) whose declared write-set overlaps candidateWrites, plus the
// overlapping resources. Returns (nil, nil) when there is no conflict.
// v1 semantics: Writes∩Writes only — a running op's Reads never block a
// writer, and ops with empty Writes are invisible to the gate.
// Caller must hold r.mu (read or write).
func (r *Registry) writeSetConflictLocked(candidateID string, candidateWrites []Resource) (*runHandle, []Resource) {
	if len(candidateWrites) == 0 {
		return nil, nil
	}
	for _, h := range r.running {
		if h.id == candidateID || len(h.writes) == 0 {
			continue
		}
		var overlap []Resource
		for _, w := range candidateWrites {
			if slices.Contains(h.writes, w) {
				overlap = append(overlap, w)
			}
		}
		if len(overlap) > 0 {
			return h, overlap
		}
	}
	return nil, nil
}

// logWriteSetDeferral logs one clear line per (deferred op → blocking op)
// pair. Without dedupe the 100ms dispatch ticker would repeat the line ten
// times a second for as long as the conflict lasts. Entries are dropped once
// the blocking op changes or the deferred op leaves the queue (see the prune
// in dispatchCycle).
//
// writeSetDeferred is guarded by r.mu. An earlier version of this comment
// claimed the map needed no locking because "dispatchCycle is single-caller".
// That holds only within one Start(): Start() is explicitly restartable after
// Shutdown() and does not wait for or exclude a prior dispatcher goroutine, so
// during a restart two dispatchCycle calls can overlap on the same Registry.
// That is a real race -- caught by -race as a concurrent map read/delete
// between the prune in dispatchCycle and this dedupe write.
//
// The check and the set must happen under one acquisition: splitting them
// would let two goroutines both miss the entry and log the same line twice,
// which is the exact spam this dedupe exists to prevent.
func (r *Registry) logWriteSetDeferral(opID, defID string, holder *runHandle, overlap []Resource) {
	r.mu.Lock()
	if r.writeSetDeferred[opID] == holder.id {
		r.mu.Unlock()
		return
	}
	r.writeSetDeferred[opID] = holder.id
	r.mu.Unlock()
	resources := make([]string, len(overlap))
	for i, res := range overlap {
		resources[i] = string(res)
	}
	r.logger.Info("registry: op deferred: write-set conflict with running op",
		"op_id", opID, "def_id", defID,
		"running_op_id", holder.id, "running_def_id", holder.defID,
		"resources", resources)
}

// checkDependsOn returns true if any op in depDefIDs is currently running.
func (r *Registry) checkDependsOn(depDefIDs []string) bool {
	if len(depDefIDs) == 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, h := range r.running {
		if slices.Contains(depDefIDs, h.defID) {
			return true
		}
	}
	return false
}

// warmupHolding reports whether non-exempt ops should be held back this cycle.
// The store is resolved through the decorator chain each cycle (cheap); a store
// with no warmup to report on never holds.
func (r *Registry) warmupHolding() bool {
	src, _ := database.AsCapability[WarmupStatuser](r.store)
	if src == nil {
		return false
	}
	return r.warmupGate.Holding(r.livenessClock(), src, r.logger)
}

// announceWarmupHeld posts WarmupStatusMessage on a held queued row,
// once per row, keeping the row's progress numbers and remembering what it said
// before so restoreWarmupHeld can put it back.
func (r *Registry) announceWarmupHeld(row database.OperationV2Row) {
	r.warmupMu.Lock()
	if _, seen := r.warmupHeld[row.ID]; seen {
		r.warmupMu.Unlock()
		return
	}
	if r.warmupHeld == nil {
		r.warmupHeld = make(map[string]database.OperationV2Row)
	}
	r.warmupHeld[row.ID] = row
	r.warmupMu.Unlock()

	// SetOpQueuedProgressV2, not UpdateOpProgressV2: it writes only a row that is
	// still queued (a cancel that landed between this cycle's snapshot and now
	// leaves a terminal row untouched), and it stamps neither last_progress_at
	// nor high_water_progress, which a hold must never move.
	written, err := r.store.SetOpQueuedProgressV2(row.ID, row.ProgressCurrent, row.ProgressTotal, WarmupStatusMessage)
	if err != nil {
		r.logger.Warn("registry: could not post the startup-warmup wait message", "op_id", row.ID, "error", err)
		return
	}
	if !written {
		return
	}
	r.publishOpUpdated(row.ID, row.ProgressCurrent, row.ProgressTotal)
}

// restoreWarmupHeld puts back the message every announced row had before the
// hold, once the hold ends, and publishes op.updated so a UI does not keep
// showing "waiting for startup warmup". A row that has left the queue, or whose
// message something else has since rewritten (a queued-summary merge), is left
// alone, and op.updated is published only for a row actually written.
func (r *Registry) restoreWarmupHeld() {
	r.warmupMu.Lock()
	held := r.warmupHeld
	r.warmupHeld = nil
	r.warmupMu.Unlock()
	for id, prev := range held {
		// The read is only for the message: a queued-summary merge may have
		// rewritten it since, and that newer text must not be overwritten with
		// the pre-hold one. Whether the row is still queued is decided by
		// SetOpQueuedProgressV2 itself, atomically with the write.
		cur, err := r.store.GetOperationV2(id)
		if err != nil || cur == nil || cur.ProgressMessage != WarmupStatusMessage {
			continue
		}
		written, err := r.store.SetOpQueuedProgressV2(id, prev.ProgressCurrent, prev.ProgressTotal, prev.ProgressMessage)
		if err != nil {
			r.logger.Warn("registry: could not restore the row message after the startup-warmup hold", "op_id", id, "error", err)
			continue
		}
		if written {
			r.publishOpUpdated(id, prev.ProgressCurrent, prev.ProgressTotal)
		}
	}
}

func (r *Registry) publishOpUpdated(opID string, current, total int) {
	if r.bus == nil {
		return
	}
	_ = r.bus.Publish(context.Background(), "op.updated", map[string]any{
		"op_id":            opID,
		"progress_current": current,
		"progress_total":   total,
	})
}
