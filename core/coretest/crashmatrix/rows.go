// file: core/coretest/crashmatrix/rows.go
// version: 1.0.8
// guid: d18639c8-9b23-4ce6-a83c-4b77e83754c1
// last-edited: 2026-11-11

package crashmatrix

import (
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage"
)

// Row is one row of 06 §6.5.1's boot-writer table as this build has it:
// the phase, the writer, and the crash-point labels its durable operations
// carry (internal/bootseam's regions, set at the writer's call sites).
//
// Retried marks a writer that runs after readiness and whose failure the
// boot logs and its next run repairs (the checkpoint sweeper: a sweep that
// fails is tried again at the next period, and the next boot's first sweep
// is one). An error injected at one of its labels leaves a boot that reached
// readiness with the failed sweep's leftovers, which only the retry clears:
// the kit checks I2 and I3 there, then boots once more and checks I1-I6
// after that boot's retry.
type Row struct {
	Phase   string
	Writer  string
	Labels  []string
	Retried bool
}

// Rows is core's part of the writer table that exists in this build (06
// §6.5.1 "Releases": a row whose package has not landed is absent, not
// stubbed). A PR that adds a boot writer adds its row here, or to its app's
// Scenario.Rows, in the same PR; Run fails on a label no row lists.
func Rows() []Row {
	return []Row{
		{Phase: "P0a", Writer: "restoreswap: the swap and the set-aside (05-backup §5B.3.4; 05-storage §5S.3.15)", Labels: []string{
			"swap.journal.staged", "swap.journal.resync", "swap.journal.rechecked", "swap.slot.inflight", "swap.location.copy",
			"swap.setaside.mkdir", "swap.setaside.mv", "swap.journal.previous_moved", "swap.place.mv",
			"swap.journal.staged_moved", "swap.sidecars", "swap.journal.sidecars", "swap.journal.swapped",
			"swap.journal.set_aside_started", "swap.journal.moved", "swap.journal.set_aside",
		}},
		{Phase: "P0a", Writer: "restoreswap: a rejected intent (staged_by_build, expires_at, target shapes, the in-flight rename refused; an unusable pending.json moved aside)", Labels: []string{
			"swap.journal.rejected", "swap.slot.rejected", "swap.slot.invalid", "swap.slot.cleanup",
		}},
		{Phase: "P0a, and after P2/P4 on a restore boot", Writer: "restoreswap: step x, the swap-back of a failed restore and the move-back of a reset (05-backup §5B.3.4 step x)", Labels: []string{
			"swap.back.mark", "swap.back.location", "swap.back.mv", "swap.back.sidecars", "swap.journal.swapped_back",
		}},
		{Phase: "P2", Writer: "storage open: Pebble's own files", Labels: []string{bootseamtest.LabelPebble}},
		{Phase: "P2 to Run step 8 (a boot that fails after P2)", Writer: "storage close: Pebble's last WAL sync as the failed boot closes the store (COR-24); after readiness the close is the shutdown's, outside the table's range", Labels: []string{
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
		{Phase: "P4b", Writer: "storagemigrate: a data migration's plan with its running state, each chunk's batch with its shard cursor (and its held and quarantine records), the quarantine sequence's lease (under the chunk's label), and its verifying, done and failed states with Verify's report (05-storage §5S.3.6, §5S.3.13 \"Quarantine\"; 09 §9.2.3 one batch per chunk)", Labels: DataMigrationLabels()},
		{Phase: "P4b, last (M8)", Writer: "storage and restoreswap: the store rewrite's record, compaction and copy, the engine closed and reopened around the in-process KindRewrite swap (its intent staged, then the shared swap steps), and phase 4's deletion of the set-aside (05-storage §5S.3.13 \"M8: the rewrite\"; 05-backup §5B.3.4)", Labels: append(storage.RewriteLabels(), restoreswap.LabelRewriteStage, restoreswap.LabelRewriteDelete)},
		{Phase: "P4b (KindCheckpoint restore boot)", Writer: "the kernel: journal failed when the restored checkpoint's migrations fail (DATA7-12)", Labels: []string{
			"swap.journal.failed",
		}},
		{Phase: "P4d 1 (reset boot; R1: after P4b)", Writer: "the kernel: the delete entries, hook_done, inflight.json removed (COR8-4)", Labels: []string{
			"swap.delete.remove", "swap.journal.hook_done", "swap.slot.remove",
		}},
		{Phase: "P4d 3 (R1)", Writer: "the kernel, on a restore boot with no restore hooks: marker_written, applied, inflight.json removed (DATA9-5)", Labels: []string{
			"swap.journal.marker_written", "swap.journal.applied", "swap.slot.remove",
		}},
		{Phase: "Run 0 (before start step 1)", Writer: "storage: ready_at and status served on the live store's open checkpoint records ((*DB).MarkServed, 05-storage §5S.3.12)", Labels: []string{
			"storage.checkpoint.ready",
		}},
		{Phase: "After readiness (05-storage §5S.4 step 15)", Writer: "the checkpoint sweeper's first sweep: delete_after on a checkpoint that stopped being needed, deletion of every checkpoint past it (record, copy, pointer), garbage (record-less directories, dangling records, a dangling pointer) and expired scratch entries ((*DB).SweepCheckpoints, 05-storage §5S.3.12)", Labels: storage.SweepLabels(), Retried: true},
	}
}

// Unobserved checks every label of rows against observed, the labels the
// fixtures' fault-free boots reach (06 §6.5.1;
// TestBootCrashMatrixCoversEveryWriter; R1-21 review M4). Run makes every
// operation of a fault-free boot a kill point and an error point, so a
// label no fault-free boot reaches is a writer the matrix never breaks,
// whatever its row's other labels. missing is every such label not in
// allow; allow maps a label to the reason no fixture can reach it. stale is
// every allow entry that a fixture reaches after all, or that no row lists.
// Both are sorted.
func Unobserved(rows []Row, observed map[string]bool, allow map[string]string) (missing, stale []string) {
	listed := map[string]bool{}
	for _, r := range rows {
		for _, l := range r.Labels {
			listed[l] = true
			if !observed[l] && allow[l] == "" {
				missing = append(missing, l)
			}
		}
	}
	for l := range allow {
		if observed[l] || !listed[l] {
			stale = append(stale, l)
		}
	}
	return uniqStrings(missing), uniqStrings(stale)
}
