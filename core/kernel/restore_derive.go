// file: core/kernel/restore_derive.go
// version: 0.1.0
// guid: 0e5b7c24-9a31-4d68-b6f2-81c4d0a73e95
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// This file is the identity boot's derivation of the restore boot P4d steps 2
// and 3 take (reapplyInput.Restore): what the kernel knows of the restore this
// boot applies, read from P0a's outcome, the restore journal and the staged
// source (05-backup §5B.3.6 "Coverage at restore").
//
// P4b's swapAfter journals a restore boot's marker_written{HooksOwed} and
// applied (machine 1's hook-less edge) before P4d runs, whether the app declared
// the identity boot or not, so the journal already reads applied and no edge of
// it is left to walk: the derivation is always the Owed one (restoreBoot.Owed,
// Steps nil). The restore line is appended and the hooks run now, with the
// source's boundary, and a crash before the hooks commit leaves the line
// without its c/backup/restore_hooked/<seq> record, which the next boot resumes
// as an external one (no coverage, fail-closed). Walking opened, restore_line,
// hooks_done, marker_written and applied from P4d, the spec's order, needs
// swapAfter's P4b edge to stand down for an identity boot (swap.go): that is
// F4-08's carry.

// errRestoreSourceUnreadable refuses an identity boot whose restore source
// cannot be read: the boundary, the provenance and the audit high-water mark all
// come from it, and a guess at any of them lets a hook overwrite audit records or
// re-apply the wrong journal lines. The restored store stays swapped in and
// journaled applied (P4b did that before P4d), and the intent is gone, so the
// next boot does not derive again: it is an ordinary boot of that store, which
// re-applies any journaled revocation the store lacks and runs the hooks
// fail-closed on a replaced store (TestIdentityBoot_ARefusedRestoreIsNotServedByTheNextBoot).
var errRestoreSourceUnreadable = errors.New("kernel: the restore source cannot be read")

// deriveRestoreBoot builds the restoreBoot of this boot's restore.
func (k *Kernel) deriveRestoreBoot(ctx context.Context) (*restoreBoot, error) {
	if k.swap == nil || k.swap.out.Boot != restoreswap.BootRestore {
		return nil, errors.New("kernel: no restore boot to derive")
	}
	out := k.swap.out
	in := out.Intent
	recs, err := k.swap.s.Journal(out.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: restore journal %s: %w", errRestoreSourceUnreadable, out.ID, err)
	}
	rb := &restoreBoot{ID: out.ID, Owed: true, Records: recs}

	// The pre-swap store: the DB directory's set-aside, absent on a host loss
	// (nothing was live to set aside).
	prev := restorejournal.SetAsideDest(k.cfg.StateDir, k.cfg.StoragePath, out.ID, in.DBPath)
	if _, err := os.Stat(prev); err == nil {
		rb.PreSwapDir = prev
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: the pre-swap store %s: %w", errRestoreSourceUnreadable, prev, err)
	}

	switch in.Kind {
	case restorejournal.KindArchive:
		rb.Kind, rb.ArchiveID = journal.KindArchive, in.ArchiveID
		m, _, legacy, err := backup.ReadStagedManifest(in)
		if err != nil {
			return nil, fmt.Errorf("%w: archive %s: %w", errRestoreSourceUnreadable, in.ArchiveID, err)
		}
		if legacy || m == nil {
			// HEAD's archive: no provenance, no journal sequence, and a store with
			// no c/audit/ for the audit copy-forward to collide with.
			rb.Foreign = true
			break
		}
		rb.Foreign = backup.Classify(m, k.sec.store, in.InstallID) != backup.Own
		rb.SourceSeq, rb.AuditHighWater = m.JournalSeq, m.AuditHighWater
	case restorejournal.KindRevert:
		rb.Kind = journal.KindRevert
		// The staged store was this install's live store once, set aside by an
		// earlier restore: what it had seen is its own journal_head, which
		// restoreLine takes by min(SourceSeq, head). The `staged` record's Seq
		// (the outgoing store's head, DATA2-25) is the bound when a stager wrote
		// it; none does yet (stagedRecord sets no Seq), and the outgoing store's
		// head is at least the set-aside store's, so no bound is the same
		// boundary.
		rb.SourceSeq = math.MaxUint64
		for _, r := range recs {
			if r.Step == restoreswap.StepStaged && r.Seq != 0 {
				rb.SourceSeq = r.Seq
			}
		}
	case restorejournal.KindCheckpoint:
		rb.Kind = journal.KindCheckpoint
		seq, err := k.checkpointSeq(ctx, in)
		if err != nil {
			return nil, err
		}
		rb.SourceSeq = seq
	default:
		return nil, fmt.Errorf("kernel: a restore boot of intent kind %q has no restore hooks", in.Kind)
	}
	return rb, nil
}

// checkpointSeq is the journal seq the checkpoint a KindCheckpoint intent
// restores recorded: its store copy is <checkpoint dir>/<name>/db, which the
// intent's DB target names.
func (k *Kernel) checkpointSeq(ctx context.Context, in restorejournal.Intent) (uint64, error) {
	var from string
	for _, t := range in.Targets {
		if filepath.Clean(t.To) == filepath.Clean(in.DBPath) {
			from = t.From
		}
	}
	if from == "" {
		return 0, fmt.Errorf("%w: the checkpoint intent has no store target", errRestoreSourceUnreadable)
	}
	cps, err := storage.CheckpointsOf(storage.FSOf(bootseam.FS(ctx)), k.cfg.StoragePath, "", k.coreLog("storage"))
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errRestoreSourceUnreadable, err)
	}
	name := filepath.Base(filepath.Dir(from))
	cp, ok, err := cps.Named(name)
	if err != nil {
		return 0, fmt.Errorf("%w: checkpoint %s: %w", errRestoreSourceUnreadable, name, err)
	}
	if !ok {
		return 0, fmt.Errorf("%w: checkpoint %s has no record", errRestoreSourceUnreadable, name)
	}
	return cp.JournalSeq, nil
}
