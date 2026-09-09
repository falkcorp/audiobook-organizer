// file: internal/operations/registry/queued_summary.go
// version: 1.0.0
// guid: 5b1e9a24-73cf-4d08-9e6a-1c4f2d80b7a3
// last-edited: 2026-09-09

package registry

import (
	"encoding/json"
)

// queuedSummary runs a def's SummarizeQueued hook and reports whether it said
// anything worth persisting. Split out from the two persistence paths so the
// "is there anything to write" rule is stated once: a hook that returns a zero
// total, no completed work and no message has declined, and its silence must
// not blank a row that already carries a summary.
func queuedSummary(def OperationDef, params json.RawMessage) (done, total int, message string, ok bool) {
	if def.SummarizeQueued == nil {
		return 0, 0, "", false
	}
	done, total, message = def.SummarizeQueued(params)
	if done <= 0 && total <= 0 && message == "" {
		return 0, 0, "", false
	}
	return done, total, message, true
}

// persistQueuedSummary writes a def's queued-work summary onto an existing row.
//
// Failure is logged, never returned: this is display metadata hanging off a
// path whose real job (merging params, re-queuing a resumed run) has already
// succeeded and must not be undone because a progress column would not write.
// The write is a no-op when the row has since started — SetOpQueuedProgressV2
// refuses anything but a queued row — which is reported at Debug rather than
// dropped, because "the op started first" and "the store rejected the write"
// look identical from here and only one of them is normal.
//
// Callers may hold r.mu. Nothing here takes it.
func (r *Registry) persistQueuedSummary(def OperationDef, opID string, params json.RawMessage) {
	done, total, message, ok := queuedSummary(def, params)
	if !ok {
		return
	}
	written, err := r.store.SetOpQueuedProgressV2(opID, done, total, message)
	if err != nil {
		r.logger.Warn("registry: failed to write queued-work summary",
			"op_id", opID, "def_id", def.ID, "error", err)
		return
	}
	if !written {
		r.logger.Debug("registry: queued-work summary skipped (row no longer queued)",
			"op_id", opID, "def_id", def.ID)
	}
}
