// file: core/coretest/crashmatrix/rows.go
// version: 1.0.12
// guid: d18639c8-9b23-4ce6-a83c-4b77e83754c1
// last-edited: 2026-11-21

package crashmatrix

import (
	"slices"

	"github.com/falkcorp/mediacore/auth"
	backupjournal "github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/rbac"
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
// writer yet: the writer's code has landed but nothing wires it into a boot.
// Unobserved does not report such a row's labels as missing, and reports one
// a fixture does reach as stale. That catches a row a fixture reaches, and
// nothing else: a row no fixture ever reaches (a writer on a non-default
// branch of its caller) would keep the mark after the wiring lands and the
// matrix would go on never breaking it. So a row that no fault-free boot
// reaches once the writer is wired is not Unbound: its labels go in each
// fixture's allow map, one reason per label, the reviewed mechanism for a
// label no fixture can reach, and TestJournalRowsAreUnboundUntilF408 fails
// while any row is Unbound once core/kernel imports core/internal/journalfs
// (the wiring), so the mark cannot be left behind by a PR that wires it.
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
		{Phase: "P0", Writer: "the kernel: step 0 creates the state directory candidate it resolves (05-config §1.7; config.ResolveStateDir's mkdir, through statefs)", Labels: []string{
			"config.statedir.mkdir",
		}},
		{Phase: "P0", Writer: "config.Store.Boot: an unparsable config.boot.json renamed config.boot.json.bad and the state directory synced (05-config §1.5, fail-open quarantine)", Labels: []string{
			"config.bootrecord.quarantine",
		}},
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
		{Phase: "P4b, first", Writer: "secrets.Open: stale keyring temporary files removed, the keyring created (exclusive temp, fsync, link, directory fsync) or its mode fixed and its header's install_id written or rewritten (a reset boot), then c/secrets/meta's batch (05-config §2.4 step 1); a present keyring's directory is synced before it is trusted (secrets.keyring.load-sync: the boot that created it may have failed at its own directory fsync)", Labels: []string{
			"secrets.keyring.stale", "secrets.keyring.load-sync", "secrets.keyring", "secrets.meta",
		}},
		{Phase: "P4b, after secrets.Open (reset boot)", Writer: "the kernel: the reset's fresh rows, every FailClosed key stored at its default in one batch, before applied (05-storage §5S.3.15 \"Next boot\"; SEC8-5)", Labels: []string{
			"config.reset.fresh_rows",
		}},
		{Phase: "P4b, after secrets.Open (reset boot)", Writer: "the kernel: journal applied once the keyring header carries the fresh store's install ID and the fresh rows are committed (06 §6.5.1 P4b first; DATA7-17, SEC8-5)", Labels: []string{
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
		}},
		{Phase: "P4d 2", Writer: "journal: a line or several written and fsynced (Append, AppendLine), the cut back of one whose append failed, and the cut of an incomplete last line at open (§5B.3.6 \"Append failure\")", Labels: []string{
			backupjournal.LabelAppend, backupjournal.LabelRollback, backupjournal.LabelCut,
		}},
		{Phase: "P4d 2", Writer: "journal: CreateBased, a new journal that continues after the store's head, exclusively created, written, fsynced and its directory fsynced (the set-aside of another install's journal, §5B.3.6 \"Boot re-apply\" (0))", Labels: []string{
			backupjournal.LabelBaseCreate, backupjournal.LabelBaseDirSync,
		}},
		{Phase: "P4d 2", Writer: "journal: backup.location.json, a temporary file written and fsynced, renamed over the file, the state directory fsynced, a failed temporary file removed (WriteLocation; §5B.3.6 row \"Where\")", Labels: []string{
			backupjournal.LabelLocationTmp, backupjournal.LabelLocationRename, backupjournal.LabelLocationDirSync, backupjournal.LabelLocationCleanup,
		}},
		{Phase: "P4d 2", Writer: "journal: MoveFile, the source's open, the destination directory, a leftover temporary copy cleared, the copy written and fsynced, renamed into place, the directory fsynced, a failed copy removed, and the old file removed once the record points at the new one (RemoveMoved; §5B.3.6 row \"Where\")", Labels: []string{
			backupjournal.LabelMoveSource, backupjournal.LabelMoveMkdir, backupjournal.LabelMoveClear, backupjournal.LabelMoveCopy, backupjournal.LabelMoveRename,
			backupjournal.LabelMoveDirSync, backupjournal.LabelMoveCleanup, backupjournal.LabelMoveRemoveSource,
		}},
		{Phase: "Retention (after a backup, not a boot step)", Writer: "journal: File.Trim, a leftover temporary file cleared, the kept lines and a new header written and fsynced to a temporary file that is locked, the temporary file renamed over the journal, the directory fsynced, a failed temporary file removed (retention's PruneJournal; §5B.3.6 row \"Retention\")", Labels: []string{
			backupjournal.LabelTrimClear, backupjournal.LabelTrimCopy, backupjournal.LabelTrimRename, backupjournal.LabelTrimDirSync, backupjournal.LabelTrimCleanup,
		}},
		{Phase: "P4d 2", Writer: "the kernel: another install's journal renamed aside and the directory fsynced before CreateBased starts the new one (restore_reapply.go setAside, around :491, and syncDirOf, around :504), raw os calls the kit cannot see until F4-08 routes them through a labelled journal filesystem; a fixture that reaches them (another install's journal on disk) is the F4-08 scenario", Labels: []string{
			LabelKernelJournalAside, LabelKernelJournalAsideSync,
		}},
		{Phase: "P4d 3 (R1)", Writer: "the kernel, on a restore boot with no restore hooks: marker_written, applied, inflight.json removed (DATA9-5)", Labels: []string{
			"swap.journal.marker_written", "swap.journal.applied", "swap.slot.remove",
		}},
		{Phase: "P4d 2 and 3 (identity restore boot walk)", Writer: "the kernel, on an identity restore boot that walks the journal's edges: restore_line once the restore line is committed and hooks_done once the hooks completed, between opened and marker_written (identity_p4d.go and restore_derive.go, through swap.go's restoreWalks; 05-backup §5B.3.4)", Labels: []string{
			"swap.journal.restore_line", "swap.journal.hooks_done",
		}},
		{Phase: "Run 0 (before start step 1)", Writer: "storage: ready_at and status served on the live store's open checkpoint records ((*DB).MarkServed, 05-storage §5S.3.12)", Labels: []string{
			"storage.checkpoint.ready",
		}},
		{Phase: "After readiness (05-storage §5S.4 step 15)", Writer: "the checkpoint sweeper's first sweep: delete_after on a checkpoint that stopped being needed, deletion of every checkpoint past it (record, copy, pointer), garbage (record-less directories, dangling records, a dangling pointer) and expired scratch entries ((*DB).SweepCheckpoints, 05-storage §5S.3.12)", Labels: storage.SweepLabels(), Retried: true},

		// F4-08 A5: the identity boot's store commits (P4d steps 2-4, P7's
		// seeds), one block so a re-cut of another PR's edits to this table
		// conflicts trivially. The boot wires them behind app.Options.IdentityBoot
		// (F4-08 B3); IdentityUnreachedLabels is what no fixture reaches yet.
		{Phase: "P4d 2", Writer: "the kernel: the re-apply's store commits: each tail chunk's two batches (auth's revocations, then rbac's with the head), each drained chunk's two (the second with the dequeue of its entries), the head of the boot's own line committed alone, a restore boot's restore line with its head, the unapplied queue's batches, the dequeue of the entries a kept admin held, the kept admins' restriction, the withheld head, MarkUnappliedClosed, and served_boot (on a restore boot after the hooks) (restore_reapply.go; 05-backup §5B.3.6 \"Boot re-apply\")", Labels: kernel.ReapplyLabels()},
		{Phase: "P4d 3", Writer: "the kernel: the restore hooks' run ID, written before a run with no restore line and cleared after it (journal.EnsureHookRun, journal.ClearHookRun), the hooks' report that the restore health tokens read, recorded before restore_hooked/<seq> (journal.PutRestoreReport), restore_hooked/<seq> once the hooks completed (journal.MarkHooked), the head a replaced store's restore line held back (journal.CommitHead), and the commits of auth's, rbac's and audit's restore hooks, each under its own region (restore_hooks.go)", Labels: kernel.HookRunLabels()},
		{Phase: "P4d 4", Writer: "auth: BootWrites' commits: the _system user's seed, the bootstrap token's mint or the deletion of the stored ones, and the start-up key's revoke and mint (05-authn §3.2, §4.6)", Labels: auth.BootWritesLabels()},
		{Phase: "P4d 2 (before the journal opens)", Writer: "the kernel: the effective backup directory created in the boot's journal filesystem, so the journal file can be created inside it (identity_p4d.go ensureBackupDir; a no-operation when the directory exists)", Labels: []string{kernel.LabelBackupDirMkdir}},
		{Phase: "P4d 4", Writer: "auth: BootWrites' state files, each a temporary file written and fsynced, renamed into place and the state directory fsynced: the bootstrap token (written, or removed when bootstrap is disabled) and the start-up key's file (05-authn §4.6; auth.StateFiles over the labelled state directory)", Labels: []string{auth.BootstrapTokenLabel, auth.ReadOnlyKeyLabel}},
		{Phase: "P7", Writer: "rbac: the seed roles' batch (CommitSeeds; 05-rbac §5)", Labels: []string{rbac.LabelCommitSeeds}},
	}
}

// identityFlipOnly is the reason of an identity label that this build's own
// identity scenario (identity_matrix_test.go, TestIdentityBootCrashMatrix)
// reaches, and that no other fixture does because none declares the flip.
const identityFlipOnly = "reached by the kit's identity scenario (TestIdentityBootCrashMatrix); no other fixture declares the identity flip (app.Options.IdentityBoot) until F4-08 B9b (hello) and B10 (ABO), and the first one that does reaches this writer and drops the entry"

// IdentityUnreachedLabels is the allow-map entries, one reason per label, for
// every writer the identity boot wires (F4-08 B3: P4d steps 2-4, the journal,
// P7's seed commit) that a fixture's fault-free boot does not reach. The kit's
// own identity scenario declares the flip and reaches most of them; hello's
// fixtures do not (hello declares the flip at B9b, ABO at B10). A fixture's
// allow map takes the whole set (maps.Copy) and drops a label only when one of
// its own scenarios reaches it; Unobserved reports an entry a fixture reaches
// as stale, so a boot that starts reaching a writer fails until its entry goes.
// The labels that no fault-free boot reaches even with identity on are given
// their own reasons: they need a journal state, a replaced store or a restore
// that no fixture sets up, and TestIdentityScenario_ReachesWhatIdentityUnreachedLabelsSaysItDoes
// fails when a reason says more or less than the scenario does.
func IdentityUnreachedLabels() map[string]string {
	const (
		trim     = "nothing calls File.Trim until F8-04a G2 wires backup.Options.PruneJournal to Component.Trim; core/internal/journalfs's TestKill_Trim breaks its steps meanwhile"
		roll     = "a failed append's cut back runs only after a journal write failed; core/backup/journal's own crash cells break it, and no fault-free boot appends a line that fails"
		cut      = "the cut of an incomplete last line runs only when a journal ends mid-line, which a crash leaves; no fault-free boot starts from one"
		tail     = "the re-apply's tail, drain, queue, restriction and head writers run only when the journal holds lines past the store's head or an unapplied queue, a journal state no fixture builds: the kernel's restore_* tests break each of them with a crash between its batches (TailBatch 1, a wrapped NewBatch); F4-08 B10 or a journal-tail fixture reaches them in the matrix"
		hooks    = "the restore hooks' run ID is written only when the hooks run with no restore line (a replaced store whose line could not be journaled), a state no fixture builds: restore_runkey_test.go crashes between its ensure and its clear"
		startup  = "the start-up key's revoke and mint run only with core.auth.bootstrap.readonly_key set and an active admin, state no fixture builds (the kit's identity scenario has no user)"
		disabled = "the deletion of stored bootstrap tokens runs only with core.auth.bootstrap.enabled false (the default mints one), a setting the kit's identity scenario leaves at its default"
		appendOK = "a revocation line is appended only when something is revoked, which no fault-free boot does; core/backup/journal's own crash cells and the kernel's restore_* tests break the append"
		held     = "the held head is committed only when a replaced store's restore line could not be journaled (a keyring that cannot sign it), a state no fixture builds: the kernel's TestReapplyRegions_EveryCommitSiteCarriesItsLabel reaches it"
		rbacHook = "rbac's restore hook commits only when it changes a role assignment (a re-applied demotion, a restriction); the scenario's revert restore has none to re-apply, and the kernel's restore_* tests break the hook's batch"
		cleanup  = "the failed location temporary file's removal runs only after the temporary file's write failed; the error mode of journal.location.tmp reaches it from the next boot's cleanup, and no fault-free boot does"
	)
	out := map[string]string{}
	for _, l := range slices.Concat(
		[]string{rbac.LabelCommitSeeds, kernel.LabelBackupDirMkdir, auth.BootstrapTokenLabel},
		auth.BootWritesLabels(),
		[]string{backupjournal.LabelCreate, backupjournal.LabelHeader, backupjournal.LabelDirSync},
		[]string{backupjournal.LabelLocationTmp, backupjournal.LabelLocationRename, backupjournal.LabelLocationDirSync},
		// A restore boot's own writers: the restore line, restore_hooked, and the
		// auth and audit hooks' commits (the scenario's revert fixture stages a restore).
		[]string{"kernel.reapply.served_boot", "kernel.reapply.restore_line", "kernel.hooks.marked", "kernel.hooks.report", "kernel.hooks.auth", "kernel.hooks.audit"},
		// The walk's own journal edges between them.
		[]string{"swap.journal.restore_line", "swap.journal.hooks_done"},
	) {
		out[l] = identityFlipOnly
	}
	for _, l := range kernel.ReapplyLabels() {
		if _, set := out[l]; !set {
			out[l] = tail
		}
	}
	for _, l := range kernel.HookRunLabels() {
		if _, set := out[l]; !set {
			out[l] = hooks
		}
	}
	out["kernel.hooks.held_head"] = held
	out["kernel.hooks.rbac"] = rbacHook
	out[auth.LabelBootBootstrapDelete] = disabled
	out[auth.LabelBootStartupKey] = startup
	out[auth.ReadOnlyKeyLabel] = startup
	out[backupjournal.LabelAppend] = appendOK
	out[backupjournal.LabelLocationCleanup] = cleanup
	for _, l := range []string{backupjournal.LabelTrimClear, backupjournal.LabelTrimCopy, backupjournal.LabelTrimRename, backupjournal.LabelTrimDirSync, backupjournal.LabelTrimCleanup} {
		out[l] = trim
	}
	out[backupjournal.LabelRollback] = roll
	out[backupjournal.LabelCut] = cut
	return out
}

// The kernel-side labels of the journal's set-aside: the rename of another
// install's journal and the fsync of its directory (core/kernel/restore_reapply.go
// setAside, through the boot's journal filesystem). They run on a non-default
// branch of the re-apply, so every fixture allows them with a reason.
const (
	LabelKernelJournalAside     = kernel.LabelJournalAside
	LabelKernelJournalAsideSync = kernel.LabelJournalAsideSync
)

// JournalUnreachedLabels is the allow-map entries, one reason per label, for
// the journal rows whose writers run on a non-default branch of the kernel's
// revocation re-apply (reapplyRevocations), which no fixture sets up before
// F4-08 binds the journal: CreateBased and the kernel's set-aside (another
// install's journal on disk), and MoveFile and RemoveMoved (a changed backup
// directory). A fixture's allow map takes the whole set (maps.Copy) and drops
// a label only when one of its own scenarios reaches it; Unobserved reports
// an entry a fixture reaches as stale, so the entry cannot hide a writer the
// matrix breaks, and a label no fixture reaches keeps its reason in review.
func JournalUnreachedLabels() map[string]string {
	const (
		create = "CreateBased runs only when the re-apply finds another install's journal on disk, a scenario no fixture sets up before F4-08 binds the journal"
		move   = "MoveFile and RemoveMoved run only when the backup directory changed since the recorded location, a scenario no fixture sets up before F4-08 binds the journal"
		aside  = "the kernel's set-aside of another install's journal (restore_reapply.go setAside and syncDirOf) runs on the same non-default branch as CreateBased, and its labels land with F4-08"
	)
	out := map[string]string{}
	for _, l := range []string{backupjournal.LabelBaseCreate, backupjournal.LabelBaseDirSync} {
		out[l] = create
	}
	for _, l := range []string{backupjournal.LabelMoveSource, backupjournal.LabelMoveMkdir, backupjournal.LabelMoveClear, backupjournal.LabelMoveCopy, backupjournal.LabelMoveRename,
		backupjournal.LabelMoveDirSync, backupjournal.LabelMoveCleanup, backupjournal.LabelMoveRemoveSource} {
		out[l] = move
	}
	for _, l := range []string{LabelKernelJournalAside, LabelKernelJournalAsideSync} {
		out[l] = aside
	}
	return out
}

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
	listed, unbound := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		for _, l := range r.Labels {
			if !listed[l] {
				unbound[l] = r.Unbound != ""
			} else {
				unbound[l] = unbound[l] && r.Unbound != ""
			}
			listed[l] = true
		}
	}
	for l := range listed {
		switch {
		case observed[l] && unbound[l]:
			stale = append(stale, l+" (its row is marked Unbound)")
		case !observed[l] && !unbound[l] && allow[l] == "":
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
