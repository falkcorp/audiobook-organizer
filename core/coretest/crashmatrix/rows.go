// file: core/coretest/crashmatrix/rows.go
// version: 1.0.0
// guid: d18639c8-9b23-4ce6-a83c-4b77e83754c1
// last-edited: 2026-11-08

package crashmatrix

import (
	"slices"

	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
)

// Row is one row of 06 §6.5.1's boot-writer table as this build has it:
// the phase, the writer, and the crash-point labels its durable operations
// carry (internal/bootseam's regions, set at the writer's call sites).
type Row struct {
	Phase  string
	Writer string
	Labels []string
}

// Rows is core's part of the writer table that exists in this build (06
// §6.5.1 "Releases": a row whose package has not landed is absent, not
// stubbed). A PR that adds a boot writer adds its row here, or to its app's
// Scenario.Rows, in the same PR; Run fails on a label no row lists.
func Rows() []Row {
	return []Row{
		{Phase: "P0a", Writer: "restoreswap: the swap and the set-aside (05-backup §5B.3.4; 05-storage §5S.3.15)", Labels: []string{
			"swap.journal.staged", "swap.journal.rechecked", "swap.slot.inflight", "swap.location.copy",
			"swap.setaside.mkdir", "swap.setaside.mv", "swap.journal.previous_moved", "swap.place.mv",
			"swap.journal.staged_moved", "swap.sidecars", "swap.journal.sidecars", "swap.journal.swapped",
			"swap.journal.set_aside_started", "swap.journal.moved", "swap.journal.set_aside",
		}},
		{Phase: "P0a", Writer: "restoreswap: a rejected intent (staged_by_build, expires_at, the in-flight rename refused)", Labels: []string{
			"swap.journal.rejected", "swap.slot.rejected", "swap.slot.cleanup",
		}},
		{Phase: "P0a, and after P2/P4 on a restore boot", Writer: "restoreswap: step x, the swap-back of a failed restore and the move-back of a reset (05-backup §5B.3.4 step x)", Labels: []string{
			"swap.back.mark", "swap.back.location", "swap.back.mv", "swap.back.sidecars", "swap.journal.swapped_back",
		}},
		{Phase: "P2", Writer: "storage open: Pebble's own files", Labels: []string{bootseamtest.LabelPebble}},
		{Phase: "P2 to P8 (a boot that fails after P2)", Writer: "storage close: Pebble's last WAL sync as the failed boot closes the store (COR-24); after readiness the close is the shutdown's, outside the table's range", Labels: []string{
			"storage.close",
		}},
		{Phase: "P2 (restore boot)", Writer: "the kernel: journal opened once the restored store opens", Labels: []string{
			"swap.journal.opened",
		}},
		{Phase: "P4a, first", Writer: "storage: the garbage sweep of record-less checkpoint directories (05-storage §5S.3.12)", Labels: []string{
			"storage.checkpoint.sweep",
		}},
		{Phase: "P4a", Writer: "storage: the checkpoint and its record, or the fresh-install batch (05-storage §5S.3.12, §5S.4 steps 5-7)", Labels: []string{
			"storage.fresh.batch", "storage.fresh.sidecars", "storage.checkpoint.reuse", "storage.checkpoint.stranded",
			"storage.checkpoint.mkdir", "storage.checkpoint.pebble", "storage.checkpoint.statefiles",
			"storage.checkpoint.rename", "storage.checkpoint.record", "storage.checkpoint.pointer",
			"storage.marker.batch", "storage.ratchet",
		}},
		{Phase: "P4a (reset boot; R1 has no keyring, 06 §6.5.1 P4b first)", Writer: "the kernel: journal applied after the fresh install", Labels: []string{
			"swap.journal.applied",
		}},
		{Phase: "P4b", Writer: "storage: the migration list (each step's batch, the level step's sidecar, completion)", Labels: []string{
			"storage.step.batch", "storage.levelstep.sidecars", "storage.levelstep.rollback", "storage.finish.batch",
			"storage.finish.sidecars", "storage.checkpoint.complete", "storage.attempt.clear",
		}},
		{Phase: "P4b (KindCheckpoint restore boot)", Writer: "the kernel: journal failed when the restored checkpoint's migrations fail (DATA7-12)", Labels: []string{
			"swap.journal.failed",
		}},
		{Phase: "P4d 1 (reset boot)", Writer: "the kernel: the delete entries, hook_done, inflight.json removed (COR8-4)", Labels: []string{
			"swap.delete.remove", "swap.journal.hook_done", "swap.slot.remove",
		}},
		{Phase: "P4d 3 (R1)", Writer: "the kernel, on a restore boot with no restore hooks: marker_written, applied, inflight.json removed (DATA9-5)", Labels: []string{
			"swap.journal.marker_written", "swap.journal.applied", "swap.slot.remove",
		}},
		{Phase: "Run 0 (before start step 1)", Writer: "storage: ready_at and status served on the live store's open checkpoint records ((*DB).MarkServed, 05-storage §5S.3.12)", Labels: []string{
			"storage.checkpoint.ready",
		}},
	}
}

// Uncovered is every row none of whose labels observed holds: a writer the
// fixtures never reach (06 §6.5.1; TestBootCrashMatrixCoversEveryWriter).
// A row counts as exercised when one of its labels is observed, since some
// labels are reachable only by faults the kit does not make (a level step's
// rollback runs only when its commit returns an error, and Pebble fails a
// WAL commit fatally instead).
func Uncovered(rows []Row, observed map[string]bool) []Row {
	var out []Row
	for _, r := range rows {
		if !slices.ContainsFunc(r.Labels, func(l string) bool { return observed[l] }) {
			out = append(out, r)
		}
	}
	return out
}
