// file: internal/operations/registry/queued_merge.go
// version: 1.0.0
// guid: 342326a8-40f7-440f-b6a6-0f7f8255b6b6
// last-edited: 2026-08-28

package registry

import (
	"encoding/json"
	"fmt"
)

// tryMergeQueuedParams merges only an unclaimed queued row. Holding r.mu over
// the read/merge/write prevents the dispatcher from claiming the row between
// the merge decision and persistence; a running operation therefore keeps its
// immutable parameter snapshot.
func (r *Registry) tryMergeQueuedParams(
	defID string,
	merge func(existing, incoming json.RawMessage) (json.RawMessage, bool, error),
	incoming json.RawMessage,
) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	active, err := r.store.ListActiveOperationsV2()
	if err != nil {
		return "", false, fmt.Errorf("registry: list active operations for queued merge: %w", err)
	}
	for _, op := range active {
		if op.DefID != defID || op.Status != "queued" {
			continue
		}
		if _, claimed := r.running[op.ID]; claimed {
			continue
		}
		params, ok, mergeErr := merge(json.RawMessage(op.Params), incoming)
		if mergeErr != nil {
			return "", false, fmt.Errorf("registry: merge queued params for %s: %w", defID, mergeErr)
		}
		if !ok {
			continue
		}
		if err := r.store.UpdateOperationV2Params(op.ID, params); err != nil {
			return "", false, fmt.Errorf("registry: persist queued param merge for %s: %w", op.ID, err)
		}
		r.logger.Info("registry: merged request into queued operation", "op_id", op.ID, "def_id", defID)
		return op.ID, true, nil
	}
	return "", false, nil
}
