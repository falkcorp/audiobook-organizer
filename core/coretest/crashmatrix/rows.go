// file: core/coretest/crashmatrix/rows.go
// version: 1.0.10
// guid: d18639c8-9b23-4ce6-a83c-4b77e83754c1
// last-edited: 2026-11-19

package crashmatrix

import (
	backupjournal "github.com/falkcorp/mediacore/backup/journal"
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
//
// Unbound, when non-empty, is why no boot of this build reaches the row's
// writer yet: the writer's code has landed (or its call site is known) but
// nothing wires it into a boot. Unobserved does not report such a row's
// labels as missing, and reports one a fixture does reach as stale, so the
// field cannot outlive the wiring that makes it untrue: the PR that binds
// the writer drops it, and until then the matrix knows the writer exists.
type Row struct {
	Phase   string
	Writer  string
	Labels  []string
	Retried bool
	Unbound string
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
		{Phase: "P7 (tasks start rule held: a module registered a kind)", Writer: "the kernel: a definition snapshot c/tasks/def/<kind> for each registered definition, one batch per registration, written only when the stored snapshot differs (05-tasks 5T.4 step 1, 5T.3.11)", Labels: []string{
			"tasks.def.upsert",
		}},
		{Phase: "P4d 1 (reset boot; R1: after P4b)", Writer: "the kernel: the delete entries, hook_done, inflight.json removed (COR8-4)", Labels: []string{
			"swap.delete.remove", "swap.journal.hook_done", "swap.slot.remove",
		}},
		{Phase: "P4d 2", Writer: "journal: the revocation journal file's creation, its header line and the directory fsync that makes the name durable, on the first open of a new journal (OpenFile; 05-backup §5B.3.6 row \"Format and integrity\")", Labels: []string{
			backupjournal.LabelCreate, backupjournal.LabelHeader, backupjournal.LabelDirSync,
		}, Unbound: unboundJournal},
		{Phase: "P4d 2", Writer: "journal: a line or several written and fsynced (Append, AppendLine), the cut back of one whose append failed, and the cut of an incomplete last line at open (§5B.3.6 \"Append failure\")", Labels: []string{
			backupjournal.LabelAppend, backupjournal.LabelRollback, backupjournal.LabelCut,
		}, Unbound: unboundJournal},
		{Phase: "P4d 2", Writer: "journal: CreateBased, a new journal that continues after the store's head, exclusively created, written, fsynced and its directory fsynced (the set-aside of another install's journal, §5B.3.6 \"Boot re-apply\" (0))", Labels: []string{
			backupjournal.LabelBaseCreate, backupjournal.LabelBaseDirSync,
		}, Unbound: unboundJournal},
		{Phase: "P4d 2", Writer: "journal: backup.location.json, a temporary file written and fsynced, renamed over the file, the state directory fsynced, a failed temporary file removed (WriteLocation; §5B.3.6 row \"Where\")", Labels: []string{
			backupjournal.LabelLocationTmp, backupjournal.LabelLocationRename, backupjournal.LabelLocationDirSync, backupjournal.LabelLocationCleanup,
		}, Unbound: unboundJournal},
		{Phase: "P4d 2", Writer: "journal: MoveFile, the destination directory, a leftover temporary copy cleared, the copy written and fsynced, renamed into place, the directory fsynced, a failed copy removed, and the old file removed once the record points at the new one (RemoveMoved; §5B.3.6 row \"Where\")", Labels: []string{
			backupjournal.LabelMoveMkdir, backupjournal.LabelMoveClear, backupjournal.LabelMoveCopy, backupjournal.LabelMoveRename,
			backupjournal.LabelMoveDirSync, backupjournal.LabelMoveCleanup, backupjournal.LabelMoveRemoveSource,
		}, Unbound: unboundJournal + "; core/kernel/restore_reapply.go still removes the moved source with a raw os.Remove (writeStoreMarkers, around :1274), which F4-08 moves onto journal.RemoveMoved"},
		{Phase: "P4d 2", Writer: "the kernel: another install's journal renamed aside and the directory fsynced before CreateBased starts the new one (restore_reapply.go setAside, around :491, and syncDirOf, around :504), raw os calls the kit cannot see until F4-08 routes them through a labelled journal filesystem", Labels: []string{
			LabelKernelJournalAside, LabelKernelJournalAsideSync,
		}, Unbound: "the kernel's set-aside still calls os.Rename and syncDirOf directly and nothing binds the journal at boot until F4-08 (P4d step 2); F4-08 wires both through journalfs under these labels"},
		{Phase: "P4d 3 (R1)", Writer: "the kernel, on a restore boot with no restore hooks: marker_written, applied, inflight.json removed (DATA9-5)", Labels: []string{
			"swap.journal.marker_written", "swap.journal.applied", "swap.slot.remove",
		}},
		{Phase: "Run 0 (before start step 1)", Writer: "storage: ready_at and status served on the live store's open checkpoint records ((*DB).MarkServed, 05-storage §5S.3.12)", Labels: []string{
			"storage.checkpoint.ready",
		}},
		{Phase: "After readiness (05-storage §5S.4 step 15)", Writer: "the checkpoint sweeper's first sweep: delete_after on a checkpoint that stopped being needed, deletion of every checkpoint past it (record, copy, pointer), garbage (record-less directories, dangling records, a dangling pointer) and expired scratch entries ((*DB).SweepCheckpoints, 05-storage §5S.3.12)", Labels: storage.SweepLabels(), Retried: true},
	}
}

// unboundJournal is the Unbound reason of the journal's file-writer rows.
const unboundJournal = "no boot binds the revocation journal until F4-08 wires P4d step 2 (the kernel opens the journal component with core/internal/journalfs's filesystem)"

// The kernel-side labels of the journal's set-aside, which F4-08 gives the two
// raw os calls of core/kernel/restore_reapply.go's setAside. No code carries
// them yet; they are listed so the matrix already knows the writers exist.
const (
	LabelKernelJournalAside     = "kernel.journal.aside.rename"
	LabelKernelJournalAsideSync = "kernel.journal.aside.dirsync"
)

// Unobserved checks every label of rows against observed, the labels the
// fixtures' fault-free boots reach (06 §6.5.1;
// TestBootCrashMatrixCoversEveryWriter; R1-21 review M4). Run makes every
// operation of a fault-free boot a kill point and an error point, so a
// label no fault-free boot reaches is a writer the matrix never breaks,
// whatever its row's other labels. missing is every such label not in
// allow; allow maps a label to the reason no fixture can reach it. stale is
// every allow entry that a fixture reaches after all, or that no row lists,
// and every label of a row marked Unbound that a fixture reaches after all
// (the row's Unbound reason is then untrue and is dropped). A label of an
// Unbound row is never missing. Both are sorted.
func Unobserved(rows []Row, observed map[string]bool, allow map[string]string) (missing, stale []string) {
	listed, bound := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		for _, l := range r.Labels {
			if !listed[l] {
				bound[l] = r.Unbound != ""
			} else {
				bound[l] = bound[l] && r.Unbound != ""
			}
			listed[l] = true
		}
	}
	for l := range listed {
		switch {
		case observed[l] && bound[l]:
			stale = append(stale, l+" (its row is marked Unbound)")
		case !observed[l] && !bound[l] && allow[l] == "":
			missing = append(missing, l)
		}
	}
	for l := range allow {
		if observed[l] || !listed[l] {
			stale = append(stale, l)
		}
	}
	return uniqStrings(missing), uniqStrings(stale)
}
