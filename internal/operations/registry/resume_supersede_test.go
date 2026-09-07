// file: internal/operations/registry/resume_supersede_test.go
// version: 1.1.0
// guid: 7e1a4c92-6b3d-4f58-9a07-2d5e8b1c4f60
// last-edited: 2026-09-07

package registry

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// supersedeStaleQuiesced is the guard that makes including interrupted_quiesced
// rows in the resume sweep safe. Without it the sweep would hand every
// historical interrupted run to ResumePolicy at once: prod held 21 quiesced
// library.scan rows accumulated over a month of deploys, and ResumeRestart on all
// 21 means 21 concurrent full library scans on one boot. That is a worse failure
// than the stall the fix cures, so these tests are the load-bearing half of it.
//
// ULIDs sort lexicographically by creation time, so the fixtures use ids whose
// string order IS their age order.

func quiesced(id, defID string) database.OperationV2Row {
	return database.OperationV2Row{ID: id, DefID: defID, Status: "interrupted_quiesced"}
}

func ids(rows []database.OperationV2Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		seen[w]--
		if seen[w] < 0 {
			return false
		}
	}
	return true
}

// THE CORE RULE: many quiesced runs of one def collapse to the newest.
func TestSupersedeStaleQuiesced_KeepsOnlyTheNewestPerDef(t *testing.T) {
	rows := []database.OperationV2Row{
		quiesced("op-01", "library.scan"),
		quiesced("op-03", "library.scan"),
		quiesced("op-02", "library.scan"),
	}

	keep, superseded := supersedeStaleQuiesced(rows, nil)

	if !sameSet(ids(keep), "op-03") {
		t.Errorf("keep = %v, want only the newest (op-03). Keeping more than one "+
			"quiesced row per def is what launches N concurrent full scans on boot.",
			ids(keep))
	}
	if !sameSet(ids(superseded), "op-01", "op-02") {
		t.Errorf("superseded = %v, want op-01 and op-02", ids(superseded))
	}
}

// Different defs are independent — collapsing across defs would silently drop
// unrelated work.
func TestSupersedeStaleQuiesced_IsPerDefNotGlobal(t *testing.T) {
	rows := []database.OperationV2Row{
		quiesced("op-01", "library.scan"),
		quiesced("op-02", "maintenance.dedupe-book-file-rows"),
	}

	keep, superseded := supersedeStaleQuiesced(rows, nil)

	if !sameSet(ids(keep), "op-01", "op-02") {
		t.Errorf("keep = %v, want both: they are different defs and neither "+
			"supersedes the other", ids(keep))
	}
	if len(superseded) != 0 {
		t.Errorf("superseded = %v, want none", ids(superseded))
	}
}

// A live row wins outright. Resuming a quiesced run alongside a queued one would
// double-run the same def.
func TestSupersedeStaleQuiesced_ALiveRowBeatsEveryQuiescedRow(t *testing.T) {
	for _, liveStatus := range []string{"queued", "running"} {
		t.Run(liveStatus, func(t *testing.T) {
			rows := []database.OperationV2Row{
				quiesced("op-09", "library.scan"), // newest by ULID order...
				{ID: "op-01", DefID: "library.scan", Status: liveStatus},
			}

			keep, superseded := supersedeStaleQuiesced(rows, nil)

			// ...and still loses, because "newest" only decides between quiesced
			// rows. A live request outranks any interrupted history.
			if !sameSet(ids(keep), "op-01") {
				t.Errorf("keep = %v, want only the %s row op-01; a quiesced row "+
					"resumed next to a live one double-runs the def",
					ids(keep), liveStatus)
			}
			if !sameSet(ids(superseded), "op-09") {
				t.Errorf("superseded = %v, want op-09", ids(superseded))
			}
		})
	}
}

// The converse, so the guard cannot pass by superseding everything: rows that are
// not quiesced are never touched, and the pre-existing sweep behaviour for the
// in-flight set is therefore unchanged.
func TestSupersedeStaleQuiesced_NeverSupersedesALiveRow(t *testing.T) {
	rows := []database.OperationV2Row{
		{ID: "op-01", DefID: "library.scan", Status: "queued"},
		{ID: "op-02", DefID: "library.scan", Status: "running"},
		{ID: "op-03", DefID: "other.op", Status: "queued"},
	}

	keep, superseded := supersedeStaleQuiesced(rows, nil)

	if !sameSet(ids(keep), "op-01", "op-02", "op-03") {
		t.Errorf("keep = %v, want all three untouched: this function must only "+
			"ever remove interrupted_quiesced rows", ids(keep))
	}
	if len(superseded) != 0 {
		t.Fatalf("superseded = %v, want none — superseding a live row would drop "+
			"work the old sweep ran", ids(superseded))
	}
}

// A single quiesced row is the common case and must survive; a guard that
// dropped it would reinstate the original defect.
func TestSupersedeStaleQuiesced_KeepsALoneQuiescedRow(t *testing.T) {
	rows := []database.OperationV2Row{quiesced("op-01", "library.scan")}

	keep, superseded := supersedeStaleQuiesced(rows, nil)

	if !sameSet(ids(keep), "op-01") {
		t.Errorf("keep = %v, want op-01: dropping the only interrupted run is "+
			"exactly the stall OPS-V2-RESUME-BLIND described", ids(keep))
	}
	if len(superseded) != 0 {
		t.Errorf("superseded = %v, want none", ids(superseded))
	}
}

// A SET-PARAMETERIZED def breaks the "newest run is a superset" assumption the
// live-row rule rests on. metadata.batch-apply-cached carries its own list of
// books per run, so a queued row is a DIFFERENT batch, not a newer take on the
// interrupted one. Superseding here throws away the interrupted run's checkpoint
// and abandons every book it still owed — on an ordinary deploy that happens to
// have a second batch queued, which is not a rare shape.
func TestSupersedeStaleQuiesced_SetParameterizedDefSurvivesALiveRow(t *testing.T) {
	const setDef = "metadata.batch-apply-cached"
	rows := []database.OperationV2Row{
		quiesced("op-01", setDef),
		{ID: "op-02", DefID: setDef, Status: "queued"},
	}
	merges := func(defID string) bool { return defID == setDef }

	keep, superseded := supersedeStaleQuiesced(rows, merges)

	if !sameSet(ids(keep), "op-01", "op-02") {
		t.Errorf("keep = %v, want both: dropping op-01 abandons the books its "+
			"checkpoint still owed, which is the resume this def exists to provide", ids(keep))
	}
	if len(superseded) != 0 {
		t.Fatalf("superseded = %v, want none", ids(superseded))
	}

	// The SAME rows, for a def that does NOT declare MergeQueuedParams, must
	// still collapse. This is what proves the exception is scoped to the
	// declaration rather than quietly disabling the guard for everyone.
	keep, superseded = supersedeStaleQuiesced(rows, func(string) bool { return false })
	if !sameSet(ids(keep), "op-02") || !sameSet(ids(superseded), "op-01") {
		t.Errorf("one-run-per-def behaviour changed: keep = %v, superseded = %v; "+
			"want keep op-02, supersede op-01", ids(keep), ids(superseded))
	}
}

// The exception must not reopen the pile-up. Even for a set-parameterized def,
// several interrupted runs still collapse to the newest — restarting all of them
// at once is the 21-concurrent-scans failure in a different costume.
func TestSupersedeStaleQuiesced_SetParameterizedDefStillCollapsesItsOwnBacklog(t *testing.T) {
	const setDef = "metadata.batch-apply-cached"
	rows := []database.OperationV2Row{
		quiesced("op-01", setDef),
		quiesced("op-03", setDef),
		quiesced("op-02", setDef),
	}

	keep, superseded := supersedeStaleQuiesced(rows, func(string) bool { return true })

	if !sameSet(ids(keep), "op-03") {
		t.Errorf("keep = %v, want only the newest op-03", ids(keep))
	}
	if !sameSet(ids(superseded), "op-01", "op-02") {
		t.Errorf("superseded = %v, want op-01 and op-02", ids(superseded))
	}
}
