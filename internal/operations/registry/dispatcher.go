// file: internal/operations/registry/dispatcher.go
// version: 2.1.0
// guid: a7b8c9d0-e1f2-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-08-07

package registry

import (
	"context"
	"encoding/json"
	"time"
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
		r.mu.RLock()
		def, ok := r.defs[row.DefID]
		r.mu.RUnlock()
		if !ok {
			// Unknown def — skip; may appear during rolling restarts.
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
			defID:          row.DefID,
			plugin:         def.Plugin,
			concurrencyKey: def.ConcurrencyKey,
			resumePolicy:   def.ResumePolicy,
			writes:         def.Writes,
		}
		r.mu.Unlock()

		qr := &queuedRun{
			opID:         row.ID,
			defID:        row.DefID,
			params:       json.RawMessage(row.Params),
			priority:     Priority(row.Priority),
			concurrKey:   def.ConcurrencyKey,
			plugin:       def.Plugin,
			resumePolicy: def.ResumePolicy,
		}

		select {
		case r.nextRun <- qr:
			delete(r.writeSetDeferred, row.ID)
			r.logger.Info("registry: dispatched op", "op_id", row.ID, "def_id", row.DefID)
		default:
			// Worker channel is full; undo accounting and try next cycle.
			r.mu.Lock()
			r.pluginRunning[def.Plugin]--
			if def.ConcurrencyKey != "" {
				if holder := r.concurrencyKeys[def.ConcurrencyKey]; holder == row.ID {
					delete(r.concurrencyKeys, def.ConcurrencyKey)
				}
			}
			// Undo the stub handle we added for Gate 0 — without this,
			// the op would be permanently un-dispatchable.
			delete(r.running, row.ID)
			r.mu.Unlock()
		}
	}
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
			for _, hw := range h.writes {
				if w == hw {
					overlap = append(overlap, w)
					break
				}
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
// times a second for as long as the conflict lasts. writeSetDeferred is only
// ever touched from the dispatcher goroutine (dispatchCycle is single-caller),
// so it needs no locking; entries are dropped once the blocking op changes or
// the deferred op leaves the queue (see the prune in dispatchCycle).
func (r *Registry) logWriteSetDeferral(opID, defID string, holder *runHandle, overlap []Resource) {
	if r.writeSetDeferred[opID] == holder.id {
		return
	}
	r.writeSetDeferred[opID] = holder.id
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
		for _, depID := range depDefIDs {
			if h.defID == depID {
				return true
			}
		}
	}
	return false
}
