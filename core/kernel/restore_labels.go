// file: core/kernel/restore_labels.go
// version: 0.1.0
// guid: 2494f969-89ba-4b53-853e-11b195808649
// last-edited: 2026-11-20

package kernel

// The crash-matrix labels of P4d steps 2 and 3's own store commits
// (restore_reapply.go, restore_hooks.go): a bootseam.Region around each, as
// tasks.go does around tasks.def.upsert. A region is a no-op in production (no
// boot carries a Labeler: outside test files, bootseam.With is called only by
// core/coretest and its sub-packages, archtest.BootseamHookCallers), so these
// change nothing but what the kit records. No boot reaches them until F4-08 wires P4d; their rows in
// core/coretest/crashmatrix are Unbound until then.
//
// The recorder's outer label wins, so a region that wraps a call which also
// writes the journal file (AppendLine, auth.RestrictLastAdmins' journaled
// append, AdvanceServedBoot's write of backup.location.json when the file
// describes the store) records that file write under the kernel's label too.
// The journal's own file labels (journal.FSLabels) stay on every journal write
// outside these regions: WriteLine's lines, Bind's open, the explicit
// WriteLocation after served_boot.
const (
	// labelReapplyTailAuth and labelReapplyTailRBAC are a tail chunk's two batches
	// (commitTail): auth's revocations, then rbac's with the chunk's head.
	labelReapplyTailAuth = "kernel.reapply.tail.auth"
	labelReapplyTailRBAC = "kernel.reapply.tail.rbac"
	// labelReapplyDrainAuth and labelReapplyDrainRBAC are a drained chunk's two
	// batches: auth's revocations, then rbac's with the dequeue of its entries.
	labelReapplyDrainAuth = "kernel.reapply.drain.auth"
	labelReapplyDrainRBAC = "kernel.reapply.drain.rbac"
	// labelReapplyPendingHead is the head of the line the boot wrote before the
	// re-apply (the gap line of a new journal, a replaced store's restore line),
	// committed alone when there is no tail to carry it.
	labelReapplyPendingHead = "kernel.reapply.pending_head"
	// labelReapplyRestoreLine is a restore boot's restore line and its head
	// (Component.AppendLine).
	labelReapplyRestoreLine = "kernel.reapply.restore_line"
	// labelReapplyQueue is a batch of the unapplied queue's entries
	// (queueUnapplied).
	labelReapplyQueue = "kernel.reapply.queue"
	// labelReapplyDequeue is the removal of the entries of drained lines that
	// kept an admin, once the restriction ran (restrictKept).
	labelReapplyDequeue = "kernel.reapply.dequeue"
	// labelReapplyRestrictKept is the restriction of the kept last admins
	// (auth.RestrictLastAdmins, journaled, one batch).
	labelReapplyRestrictKept = "kernel.reapply.restrict_kept"
	// labelReapplyWithheldHead is the head a chunk that kept an admin withheld,
	// committed after the restriction (restrictKept).
	labelReapplyWithheldHead = "kernel.reapply.withheld_head"
	// labelReapplyUnappliedClosed is MarkUnappliedClosed after the queue's
	// fail-closed pass (settleUnapplied).
	labelReapplyUnappliedClosed = "kernel.reapply.unapplied_closed"
	// labelReapplyServedBoot is c/backup/served_boot (AdvanceServedBoot) at the
	// end of step 2, or of step 3 on a restore boot.
	labelReapplyServedBoot = "kernel.reapply.served_boot"
	// labelHooksRunEnsure and labelHooksRunClear are the restore hooks' run ID,
	// written before a run with no restore line and cleared after it
	// (journal.EnsureHookRun, journal.ClearHookRun).
	labelHooksRunEnsure = "kernel.hooks.run.ensure"
	labelHooksRunClear  = "kernel.hooks.run.clear"
)

// ReapplyLabels is every label of P4d step 2's own store commits (the
// re-apply, restore_reapply.go), for the crash matrix's writer table
// (core/coretest/crashmatrix.Rows). served_boot is step 3's on a restore boot.
func ReapplyLabels() []string {
	return []string{
		labelReapplyTailAuth, labelReapplyTailRBAC, labelReapplyDrainAuth, labelReapplyDrainRBAC,
		labelReapplyPendingHead, labelReapplyRestoreLine, labelReapplyQueue, labelReapplyDequeue,
		labelReapplyRestrictKept, labelReapplyWithheldHead, labelReapplyUnappliedClosed, labelReapplyServedBoot,
	}
}

// HookRunLabels is every label of P4d step 3's own store commits that this
// file labels (the restore hooks' run ID, restore_hooks.go), for the writer
// table.
func HookRunLabels() []string { return []string{labelHooksRunEnsure, labelHooksRunClear} }

// The crash-matrix labels of the kernel's own journal-file operations: the
// set-aside of another install's journal (restore_reapply.go setAside) renames
// it and fsyncs its directory, each in a region of the boot's journal
// filesystem (journalfs.From). They run only on that non-default branch of the
// re-apply, so every fixture allows them with a reason
// (crashmatrix.JournalUnreachedLabels).
const (
	LabelJournalAside     = "kernel.journal.aside.rename"
	LabelJournalAsideSync = "kernel.journal.aside.dirsync"
)
