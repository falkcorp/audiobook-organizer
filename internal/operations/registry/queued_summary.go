// file: internal/operations/registry/queued_summary.go
// version: 1.1.0
// guid: 5b1e9a24-73cf-4d08-9e6a-1c4f2d80b7a3
// last-edited: 2026-09-09

package registry

import (
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// stampQueuedSummary sets the progress columns on a row that has been built but
// not yet inserted, reading the params already on that row.
//
// Every path that CREATES a queued row calls this rather than writing the row
// and updating it back: it costs no second store round-trip, the op.created
// event each of those paths publishes carries the count with it, and a row is
// never briefly visible claiming to hold nothing. There are three such paths and
// they are easy to miss because only one of them is the obvious one —
// EnqueueOp, the requeue that replaces an interrupted run with a fresh row
// (ResumeRequeue), and the batch flush that turns a bucket of subjects into a
// single op. A def wired for one and not the others reports its size on some
// runs and not others, which is worse than never reporting it.
//
// Pure apart from the write to row. Callers may hold r.mu.
func stampQueuedSummary(def OperationDef, row *database.OperationV2Row) {
	done, total, message, ok := queuedSummary(def, json.RawMessage(row.Params))
	if !ok {
		return
	}
	row.ProgressCurrent = done
	row.ProgressTotal = total
	row.ProgressMessage = message
}

// persistQueuedSummary writes a def's queued-work summary onto an existing row
// and reports what it wrote, so a caller still holding an in-memory copy of the
// row can keep that copy in step. resumeRestart is exactly that caller: it goes
// on to publish an op.created event built from its local struct, and an
// unpatched copy would announce a run holding no work while the stored row says
// otherwise.
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
func (r *Registry) persistQueuedSummary(
	def OperationDef,
	opID string,
	params json.RawMessage,
) (done, total int, message string, written bool) {
	done, total, message, ok := queuedSummary(def, params)
	if !ok {
		return 0, 0, "", false
	}
	written, err := r.store.SetOpQueuedProgressV2(opID, done, total, message)
	if err != nil {
		r.logger.Warn("registry: failed to write queued-work summary",
			"op_id", opID, "def_id", def.ID, "error", err)
		return 0, 0, "", false
	}
	if !written {
		r.logger.Debug("registry: queued-work summary skipped (row no longer queued)",
			"op_id", opID, "def_id", def.ID)
		return 0, 0, "", false
	}
	return done, total, message, true
}
