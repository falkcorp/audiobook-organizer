// file: internal/operations/registry/legacy_op_status.go
// version: 1.3.0
// guid: 4a8c2f61-b703-49de-95e7-1c0d8b5a3e27
// last-edited: 2026-08-22

package registry

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// legacyOpStore is the slice of the v1 operations surface needed to keep a
// legacy operation row in step with the v2 run that superseded it.
//
// It is an OPTIONAL capability, discovered by type assertion on r.store rather
// than added to database.OpsV2Store. The registry's store is an OpsV2Store, and
// several test fakes implement exactly that interface and nothing more; widening
// it would break them for a concern none of them models. PebbleStore — the only
// production implementation — satisfies this already.
type legacyOpStore interface {
	GetOperationByID(id string) (*database.Operation, error)
	UpdateOperationStatus(id, status string, progress, total int, message string) error
}

// legacyOpParams is the shape every enqueue site uses to carry the legacy row's
// id into the v2 op's params. The field name is fixed by the JSON tag those
// param structs already declare (`json:"legacy_op_id"`), so this reads all of
// them — MaintenanceWindowOpParams, schedulerExtraOpParams, seriesPruneOpParams
// and the rest — without importing any of them.
type legacyOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
}

// legacyOpIDKey is the JSON key legacyOpParams above reads. Named once so the
// dedupe comparison and the status bridge cannot drift apart.
const legacyOpIDKey = "legacy_op_id"

// sameParamsIgnoringLegacyID reports whether two marshalled param blobs describe
// the same WORK, disregarding legacy_op_id.
//
// WHY THIS EXISTS. EnqueueOp dedupes an incoming request against active ops for
// the same def by comparing params. Every enqueue site that bridges to a v1
// operations row stamps that row's freshly-minted id into the params, so two
// requests for identical work NEVER compared equal and the merge never fired for
// any of them — maintenance, itunes, dedup, ai, metadata, entities, reconcile,
// diagnostics and filesystem alike. Measured in #2717: hold legacy_op_id constant
// and the merge fires; vary only it and the merge stops. It was the sole
// discriminator.
//
// The id is per-request bookkeeping, not work identity: two requests for the same
// job with the same parameters are the same work regardless of which v1 row each
// is twinned with. So it is excluded from the comparison rather than removed from
// the params — the stamp itself is load-bearing (propagateLegacyOpStatus below
// needs it to move the v1 row off "pending", and maintenance jobs key their
// activity-log entries off it).
//
// THIS DOES NOT WEAKEN THE BYTE-EQUALITY RULE at the call site. Params take the
// key-wise path only when BOTH sides carry legacy_op_id; anything else falls
// straight through to the exact comparison, unchanged. On that path every
// REMAINING value is still compared byte-for-byte: a present-but-zero field
// still differs from an absent one, and any difference in real parameters still
// queues a second run. What stops mattering is key ORDER and inter-token
// whitespace, which no caller varies — both sides are marshalled from the same
// Go struct type.
//
// REQUIRING THE KEY ON BOTH SIDES IS DELIBERATE, not incidental. Merging a
// request that has a v1 twin into one that does not would leave the loser's row
// twinned to nothing with no way to find out: runMaintenanceJob identifies its
// orphan by reading the WINNER's legacy_op_id, so a winner without one is
// indistinguishable from "we started our own run" and the orphan survives at
// "pending" — the exact stuck row this whole change exists to prevent. Demanding
// the key on both sides makes that cleanup total. The cost is a redundant queued
// run in a case no production caller currently produces, which is the safe
// direction and the same one the byte-equality rule above already chose.
func sameParamsIgnoringLegacyID(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	am, aHas := paramsWithoutLegacyID(a)
	bm, bHas := paramsWithoutLegacyID(b)
	if !aHas || !bHas {
		// Not a bridged-vs-bridged comparison — the exact rule stands untouched.
		return false
	}
	if am == nil || bm == nil || len(am) != len(bm) {
		return false
	}
	for k, av := range am {
		bv, ok := bm[k]
		if !ok || !bytes.Equal(av, bv) {
			return false
		}
	}
	return true
}

// paramsWithoutLegacyID decodes raw as a JSON object and returns it minus the
// legacy_op_id key, along with whether that key was present. A blob that is not
// a JSON object yields (nil, false).
func paramsWithoutLegacyID(raw []byte) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, false
	}
	_, had := m[legacyOpIDKey]
	delete(m, legacyOpIDKey)
	return m, had
}

// legacyStatusFor maps a v2 terminal status onto the v1 vocabulary.
//
// The two vocabularies overlap but are not identical: v2 splits "interrupted"
// into a family of variants where v1 has a single "interrupted", which is the
// value server_lifecycle.go already writes when it sweeps rows on restart.
//
// MATCH THE PREFIX, NOT A LIST. This function used to enumerate the variants —
// interrupted_ask, interrupted_dropped, interrupted — and it had already drifted
// behind the side that mints them. The registry also publishes
// interrupted_quiesced (registry.go interruptedStatus, returned for EVERY
// ResumePolicy except ResumeDrop) and interrupted_restart (worker.go). Neither
// matched, so both fell to the default, returned "", and propagateLegacyOpStatus
// returned early — leaving the legacy row at "pending" forever with nothing
// logged, which is indistinguishable from an op that has no legacy twin.
//
// That mattered more the moment it was written: #2500 moved library.scan to
// ResumeRestart, which makes interrupted_quiesced its NORMAL outcome across a
// restart. The two shipped together.
//
// The variants are minted in one place and read here, and only the leading
// "interrupted" is load-bearing for v1 — so match on that and a sixth variant
// maps correctly without anyone remembering this file exists.
func legacyStatusFor(v2Status string) string {
	switch v2Status {
	case "completed", "failed", "canceled":
		return v2Status
	}
	if isInterruptedStatus(v2Status) {
		return "interrupted"
	}
	return "" // not a terminal status; nothing to mirror
}

// isInterruptedStatus is the prefix match the comment above argues for, in one
// place so IsTerminalStatus and legacyStatusFor cannot disagree about what the
// interrupted family contains.
func isInterruptedStatus(v2Status string) bool {
	return v2Status == "interrupted" || strings.HasPrefix(v2Status, "interrupted_")
}

// IsTerminalStatus reports whether a v2 operation status is terminal — the op
// has stopped and will not progress further.
//
// Exported so callers outside this package can ask the question without
// restating the answer. A caller that enumerates terminal statuses itself gets
// the bug documented on legacyStatusFor above: the interrupted family grew twice
// (interrupted_quiesced, interrupted_restart) and the enumerated copy silently
// classified both as "still running". Anything polling on that mistake waits
// forever.
func IsTerminalStatus(v2Status string) bool {
	switch v2Status {
	case "completed", "failed", "canceled":
		return true
	}
	return isInterruptedStatus(v2Status)
}

// propagateLegacyOpStatus mirrors a v2 op's terminal status onto the legacy
// operations row it was created alongside.
//
// WHY THIS EXISTS. Jobs dispatched through maintenance.job and the scheduler
// create a v1 operations row with store.CreateOperation(...) and then enqueue a
// v2 op carrying that row's id in its params. Nothing wrote the v1 row again.
// Its status was effectively write-only after creation, so the ops UI — which
// reads v1 — showed finished jobs as still running: on 2026-08-14 EVERY
// maintenance-job row of the day sat at "pending", including fix-file-modes and
// normalize-primary-flags, both of which had completed with journalled
// summaries. That misled the operator twice in one day.
//
// A handful of ops already did this by hand at their own call sites
// (internal/server/itunes_ops.go, diagnostics_ops.go, folder_autoscan_op.go).
// Everything dispatched by the scheduler did not, and never would have — which
// is the argument for doing it here instead of at a twenty-first call site.
// This runs from publishOpTerminal, which every terminal path already funnels
// through, so an op cannot reach a terminal state without passing this.
//
// It is deliberately best-effort and silent about absence: the vast majority of
// v2 ops have no legacy twin at all, and that is not a problem to report.
func (r *Registry) propagateLegacyOpStatus(opID, v2Status string) {
	legacyStatus := legacyStatusFor(v2Status)
	if legacyStatus == "" {
		return // not a terminal status; nothing to mirror
	}
	store, ok := r.store.(legacyOpStore)
	if !ok {
		return
	}

	row, err := r.store.GetOperationV2(opID)
	if err != nil || row == nil || row.Params == "" {
		return
	}
	var p legacyOpParams
	if err := json.Unmarshal([]byte(row.Params), &p); err != nil || p.LegacyOpID == "" {
		return // no legacy twin — the common case
	}

	// Preserve the counters. UpdateOperationStatus takes progress and total
	// positionally and overwrites both, so passing 0,0 would leave a completed
	// job rendering as 0% — trading "stuck at pending" for "finished at zero",
	// which is no more honest. A completed run with no counters is reported as
	// fully done, since that is what completing means.
	progress, total, message := 0, 0, legacyStatus
	if legacy, lerr := store.GetOperationByID(p.LegacyOpID); lerr == nil && legacy != nil {
		if legacy.Status == legacyStatus {
			return // already terminal at this status; nothing to write
		}
		progress, total, message = legacy.Progress, legacy.Total, legacy.Message
	}
	if legacyStatus == "completed" && total == 0 {
		progress, total = 1, 1
	}
	if message == "" {
		message = legacyStatus
	}

	if err := store.UpdateOperationStatus(p.LegacyOpID, legacyStatus, progress, total, message); err != nil {
		r.logger.Warn("registry: failed to propagate terminal status to legacy op row",
			"op_id", opID, "legacy_op_id", p.LegacyOpID, "status", legacyStatus, "error", err)
		return
	}
	r.logger.Debug("registry: mirrored terminal status onto legacy op row",
		"op_id", opID, "legacy_op_id", p.LegacyOpID, "status", legacyStatus)
}
