// file: core/internal/restoreswap/restore.go
// version: 1.0.1
// guid: 729b02e6-c9d2-4e68-993b-d4ac02281f97
// last-edited: 2026-11-07

package restoreswap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The swap-back's durable marks in restore/<id>/ (decision D5 of R1-21,
// refined): machine 1 has no "swapping back" step, so step x records its
// progress as files beside the journal. backStarted exists from x's first
// operation: a later boot continues x instead of resuming forward (which
// would journal applied over the original store). backMoved exists once
// every non-original live item is in failed/: from then on a live path that
// exists can only be an original moved back.
const (
	backStarted = "swapback.started"
	backMoved   = "swapback.moved"
)

func (s *Swapper) markPath(id, name string) string { return filepath.Join(s.journalDir(id), name) }

// mark creates one of step x's mark files, durably.
func (s *Swapper) mark(ctx context.Context, id, name string) error {
	return bootseam.Labelled(bootseam.From(ctx), "swap.back.mark", func() error {
		ok, err := exists(s.fs, s.markPath(id, name))
		if err != nil || ok {
			return err
		}
		return writeFileAtomic(s.fs, s.markPath(id, name), nil, privateFile)
	})
}

// startRestore is steps b and the in-flight rename of a restore kind whose
// pending.json exists and passed step a (05-backup §5B.3.4).
func (s *Swapper) startRestore(ctx context.Context, in restorejournal.Intent, last string) (Outcome, error) {
	recheck := s.o.Recheck
	if recheck == nil {
		recheck = s.defaultRecheck
	}
	if err := recheck(ctx, in); err != nil {
		return s.rejectPending(ctx, in, "step b: "+err.Error(), false, DegradedRestoreRejected)
	}
	if last != StepRechecked {
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepRechecked}); err != nil {
			return Outcome{}, err
		}
	}
	// R9-7: the in-flight file exists before the first rename of a live item.
	if err := bootseam.Labelled(bootseam.From(ctx), "swap.slot.inflight", func() error {
		return slotMove(s.fs, s.pendingPath(), s.inflightPath(in.ID))
	}); err != nil {
		return s.inflightFailed(ctx, in, err)
	}
	return s.forward(ctx, in, StepRechecked)
}

// inflightFailed is the failure exit of the in-flight rename itself (R10-6,
// DATA10-4): once the in-flight file exists the swap takes its failure edge
// (step x for a restore, which finds nothing moved; the reset failure rule
// for a reset), and while it does not, nothing left the slot and the intent
// is rejected. Either way the untouched store boots, degraded, instead of
// every later boot halting on the same error.
func (s *Swapper) inflightFailed(ctx context.Context, in restorejournal.Intent, cause error) (Outcome, error) {
	ok, err := exists(s.fs, s.inflightPath(in.ID))
	switch {
	case err != nil:
		return Outcome{}, errors.Join(cause, err)
	case !ok:
		degraded := DegradedRestoreFailed
		if in.Kind == restorejournal.KindReset {
			degraded = DegradedFactoryResetFailed
		}
		return s.rejectPending(ctx, in, "the in-flight rename failed: "+cause.Error(), false, degraded)
	case in.Kind == restorejournal.KindReset:
		return s.rejectReset(ctx, in, cause)
	}
	return s.swappedBack(in, s.SwapBack(ctx, in.ID, cause))
}

// swappedBack is P0a's outcome of a step x it ran itself: a completed
// swap-back boots the untouched store, degraded restore_failed; a swap-back
// that failed halts boot, and the next boot continues it.
func (s *Swapper) swappedBack(in restorejournal.Intent, err error) (Outcome, error) {
	if !errors.Is(err, ErrSwappedBack) {
		return Outcome{}, err
	}
	return Outcome{ID: in.ID, Intent: in, Last: StepSwappedBack, Degraded: DegradedRestoreFailed, Reason: err.Error()}, nil
}

// defaultRecheck is step b at R1 (decision D4): every placement's source
// still exists, and the rename pre-check passes again. F8 installs the
// archive manifest's re-hash in its place.
func (s *Swapper) defaultRecheck(_ context.Context, in restorejournal.Intent) error {
	for _, t := range in.Targets {
		ok, err := exists(s.fs, t.From)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("the staged item %s is gone", t.From)
		}
	}
	return restorejournal.Validate(s.o.Probe, s.restoreMoves(in))
}

// restoreMoves is every rename a restore makes: step c's set-asides, then
// step d's placements.
func (s *Swapper) restoreMoves(in restorejournal.Intent) []restorejournal.Target {
	return append(s.setAsides(in), in.Targets...)
}

// resumeRestore continues a restore journal whose in-flight file exists.
func (s *Swapper) resumeRestore(ctx context.Context, in restorejournal.Intent, recs []restorejournal.Record) (Outcome, error) {
	if ok, err := exists(s.fs, s.markPath(in.ID, backStarted)); err != nil {
		return Outcome{}, err
	} else if ok {
		return s.swappedBack(in, s.continueSwapBack(ctx, in, "a swap-back interrupted by a crash"))
	}
	last := lastStep(recs)
	switch last {
	case StepStaged, StepRechecked, StepPreviousMoved, StepStagedMoved, StepSidecars:
		// A journal at staged with its in-flight file cannot be written by
		// this swapper (rechecked precedes the rename); c onward is
		// idempotent, so it resumes as rechecked would.
		return s.forward(ctx, in, last)
	case StepSwapped, StepOpened:
		return s.restoreBoot(in, last), nil
	case StepFailed:
		if in.Kind == restorejournal.KindCheckpoint && recs[len(recs)-1].Note == s.o.BuildID {
			return Outcome{}, fmt.Errorf("%w (journal %s)", ErrCheckpointRestoreFailedHere, in.ID)
		}
		return s.restoreBoot(in, last), nil // failed → opened: a fixed build resumes it
	case StepMarkerWritten:
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepApplied}); err != nil {
			return Outcome{}, err
		}
		if err := s.removeInflight(ctx, in.ID); err != nil {
			return Outcome{}, err
		}
		return Outcome{ID: in.ID, Intent: in, Last: StepApplied}, nil
	case StepApplied, StepSwappedBack:
		// Terminal; the kill fell before the in-flight file was deleted.
		if err := s.removeInflight(ctx, in.ID); err != nil {
			return Outcome{}, err
		}
		return Outcome{ID: in.ID, Intent: in, Last: last}, nil
	}
	return Outcome{}, fmt.Errorf("%w: restore journal %s at %q with its in-flight file", ErrJournalCorrupt, in.ID, last)
}

func (s *Swapper) restoreBoot(in restorejournal.Intent, last string) Outcome {
	return Outcome{Boot: BootRestore, ID: in.ID, Intent: in, Last: last}
}

// forward runs a restore from the step after last to swapped (05-backup
// steps c, d, e, f, g). Any failure goes to step x, which moves back
// whatever moved and boots the untouched store degraded; a swap-back that
// fails too halts boot and the next boot continues it.
func (s *Swapper) forward(ctx context.Context, in restorejournal.Intent, last string) (Outcome, error) {
	if err := s.forwardSteps(ctx, in, last); err != nil {
		return s.swappedBack(in, s.SwapBack(ctx, in.ID, err))
	}
	return s.restoreBoot(in, StepSwapped), nil
}

func (s *Swapper) forwardSteps(ctx context.Context, in restorejournal.Intent, last string) error {
	seams := bootseam.From(ctx)
	asides := s.setAsides(in)
	switch last {
	case StepStaged, StepRechecked:
		// c: the location file's copy (copy-if-exists, R10-6), the set-aside
		// directories, then each live item to its destination.
		if err := bootseam.Labelled(seams, "swap.location.copy", func() error {
			return copyIfExists(s.fs, filepath.Join(s.o.StateDir, locationFile), filepath.Join(s.journalDir(in.ID), locationFile))
		}); err != nil {
			return err
		}
		if err := bootseam.Labelled(seams, "swap.setaside.mkdir", func() error {
			for _, m := range asides {
				if err := mkdirAllSynced(s.fs, filepath.Dir(m.To)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		for _, m := range asides {
			// Before previous_moved nothing but this rename writes the
			// destination, so a second name is its twin.
			if err := bootseam.Labelled(seams, "swap.setaside.mv", func() error { return mv(s.fs, m.From, m.To, always) }); err != nil {
				return err
			}
		}
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepPreviousMoved}); err != nil {
			return err
		}
		fallthrough
	case StepPreviousMoved:
		// d: each staged item into place. A live path present beside its
		// staged source is the source's twin only when step c moved that
		// live item away; otherwise boot halts naming both.
		for _, t := range in.Targets {
			aside := restorejournal.SetAsideDest(s.o.StateDir, in.DBPath, in.ID, t.To)
			if err := bootseam.Labelled(seams, "swap.place.mv", func() error {
				return mv(s.fs, t.From, t.To, existsProof(s.fs, aside))
			}); err != nil {
				return err
			}
		}
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepStagedMoved}); err != nil {
			return err
		}
		fallthrough
	case StepStagedMoved:
		// e: the sidecars from the staged store, so no reader of a sidecar
		// sees the pre-restore store's levels (DATA-23d, OPS4-15).
		if err := bootseam.Labelled(seams, "swap.sidecars", func() error {
			return storage.RewriteSidecars(ctx, s.fs, in.DBPath, s.o.Owners)
		}); err != nil {
			return err
		}
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepSidecars}); err != nil {
			return err
		}
		fallthrough
	case StepSidecars:
		// f: no derived index or cache exists at R1. g: the journal step.
		return s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepSwapped})
	}
	return fmt.Errorf("restoreswap: no forward step after %q", last)
}

// SwapBack is step x (05-backup §5B.3.4): any failure of a restore after its
// in-flight rename, up to marker_written (a failed rename inside c, d or e,
// the kernel's DB path check, P2 refusing the restored store, P4a, P4b of
// every kind but KindCheckpoint). The kernel closes the store first. It
// returns ErrSwappedBack wrapping cause once the journal reads
// swapped_back, or the joined error when x itself fails, which halts boot
// with every path named; the next boot continues x.
func (s *Swapper) SwapBack(ctx context.Context, id string, cause error) error {
	in, err := readIntent(s.fs, s.inflightPath(id))
	if err != nil {
		return errors.Join(cause, err)
	}
	if err := s.mark(ctx, id, backStarted); err != nil {
		return errors.Join(cause, fmt.Errorf("restoreswap: step x of %s could not start: %w", id, err))
	}
	if cause == nil {
		cause = errors.New("the restore failed")
	}
	reason := cause.Error()
	if err := s.continueSwapBack(ctx, in, reason); !errors.Is(err, ErrSwappedBack) {
		return errors.Join(cause, err)
	}
	return fmt.Errorf("%w: %w", ErrSwappedBack, cause)
}

// continueSwapBack runs step x from its first operation; every operation is
// idempotent, so a crash anywhere in it resumes here.
func (s *Swapper) continueSwapBack(ctx context.Context, in restorejournal.Intent, reason string) error {
	seams := bootseam.From(ctx)
	id := in.ID
	asides := s.setAsides(in)
	// x1: the location file's saved copy back over the live one (only when
	// step c saved one; an absent one stays absent), so the original store
	// reads as the same store (DATA6-3, R7-4).
	if err := bootseam.Labelled(seams, "swap.back.location", func() error {
		saved := filepath.Join(s.journalDir(id), locationFile)
		ok, err := exists(s.fs, saved)
		if err != nil || !ok {
			return err
		}
		live := filepath.Join(s.o.StateDir, locationFile)
		if err := s.fs.Rename(saved, live); err != nil {
			return fmt.Errorf("restoreswap: restore %s: %w", live, err)
		}
		if err := syncDir(s.fs, s.o.StateDir); err != nil {
			return err
		}
		return syncDir(s.fs, s.journalDir(id))
	}); err != nil {
		return fmt.Errorf("restoreswap: step x of %s: %w", id, err)
	}
	moved, err := exists(s.fs, s.markPath(id, backMoved))
	if err != nil {
		return err
	}
	recs, err := s.Journal(id)
	if err != nil {
		return err
	}
	cDone := slices.ContainsFunc(recs, func(r restorejournal.Record) bool { return r.Step == StepPreviousMoved })
	if !moved && cDone {
		// x2: once step c is journaled every live path of step c's list
		// that exists is not an original: a staged item step d placed, or
		// a file the restore boot wrote (a sidecar, a checkpoint pointer
		// the original store never had). It goes to failed/. Before
		// previous_moved nothing but c's own renames touched these paths,
		// so a live path is the original or its second name, and x4 alone
		// puts it back.
		for _, m := range asides {
			if ok, err := exists(s.fs, m.From); err != nil {
				return err
			} else if !ok {
				continue
			}
			failed := restorejournal.FailedDest(s.o.StateDir, in.DBPath, id, m.From)
			if err := bootseam.Labelled(seams, "swap.back.mv", func() error { return mv(s.fs, m.From, failed, always) }); err != nil {
				return fmt.Errorf("restoreswap: step x of %s: move %s to %s: %w", id, m.From, failed, err)
			}
		}
	}
	if !moved {
		if err := s.mark(ctx, id, backMoved); err != nil {
			return err
		}
	}
	// x4: every original back. After backMoved a live path that exists can
	// only be the original's second name: x2 emptied every live path, or
	// (before previous_moved) c's interrupted rename left both names.
	for _, m := range asides {
		if err := bootseam.Labelled(seams, "swap.back.mv", func() error { return mv(s.fs, m.To, m.From, always) }); err != nil {
			return fmt.Errorf("restoreswap: step x of %s: move %s back to %s: %w", id, m.To, m.From, err)
		}
	}
	// x5: the sidecars from the original stamps. A host with no previous
	// store has no original to describe.
	if ok, err := exists(s.fs, in.DBPath); err != nil {
		return err
	} else if ok {
		if err := bootseam.Labelled(seams, "swap.back.sidecars", func() error {
			return storage.RewriteSidecars(ctx, s.fs, in.DBPath, s.o.Owners)
		}); err != nil {
			return fmt.Errorf("restoreswap: step x of %s: sidecars of the original store: %w", id, err)
		}
	}
	// x6: the journal, then the in-flight file.
	if last, err := s.last(id); err != nil {
		return err
	} else if last != StepSwappedBack {
		if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepSwappedBack, Note: reason}); err != nil {
			return err
		}
	}
	if err := s.removeInflight(ctx, id); err != nil {
		return err
	}
	s.o.Logger.Error("restoreswap: restore failed and was swapped back; the original store boots after the restart",
		"id", id, "kind", string(in.Kind), "reason", reason)
	return fmt.Errorf("%w: %s", ErrSwappedBack, reason)
}
