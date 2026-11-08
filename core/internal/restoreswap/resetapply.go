// file: core/internal/restoreswap/resetapply.go
// version: 1.0.4
// guid: dca1f7d5-4bfe-4d11-a0dc-0941c34a8ebd
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// startReset is P0a's set-aside of a KindReset intent whose pending.json
// exists and passed step a (05-storage §5S.3.15 "Set aside"): the in-flight
// rename before anything moves (R9-7), then the set-aside.
func (s *Swapper) startReset(ctx context.Context, in restorejournal.Intent) (Outcome, error) {
	if err := bootseam.Labelled(bootseam.From(ctx), "swap.slot.inflight", func() error {
		return slotMove(s.fs, s.pendingPath(), s.inflightPath(in.ID))
	}); err != nil {
		return s.inflightFailed(ctx, in, err)
	}
	return s.resetFrom(ctx, in, StepStaged, nil)
}

// resumeReset continues a KindReset journal whose in-flight file exists.
// It is never rejected by step a (DATA9-4, R9-6): whichever core build
// boots, however late, the set-aside resumes forward to set_aside.
func (s *Swapper) resumeReset(ctx context.Context, in restorejournal.Intent, recs []restorejournal.Record) (Outcome, error) {
	last := lastStep(recs)
	switch last {
	case StepStaged, StepSetAsideStarted, StepMoved:
		moved := map[string]bool{}
		for _, r := range recs {
			if r.Step == StepMoved {
				moved[filepath.Clean(r.From)] = true
			}
		}
		return s.resetFrom(ctx, in, last, moved)
	case StepSetAside:
		return Outcome{Boot: BootReset, ID: in.ID, Intent: in, Last: last}, nil
	case StepApplied:
		return Outcome{Boot: BootResetApplied, ID: in.ID, Intent: in, Last: last}, nil
	case StepHookDone:
		if err := s.removeInflight(ctx, in.ID); err != nil {
			return Outcome{}, err
		}
		return Outcome{ID: in.ID, Intent: in, Last: last}, nil
	case StepRejected:
		// The failure rule journaled rejected; the kill fell inside the
		// rename of the in-flight file to rejected.json.
		if err := bootseam.Labelled(bootseam.From(ctx), "swap.slot.rejected", func() error {
			return slotMove(s.fs, s.inflightPath(in.ID), s.rejectedPath(in.ID))
		}); err != nil {
			return Outcome{}, err
		}
		return Outcome{ID: in.ID, Intent: in, Last: last, Degraded: DegradedFactoryResetFailed,
			Reason: "a reset whose set-aside failed was rejected"}, nil
	}
	return Outcome{}, fmt.Errorf("%w: reset journal %s at %q with its in-flight file", ErrJournalCorrupt, in.ID, last)
}

// resetFrom runs the set-aside from the step after last: the location
// file's copy and set_aside_started (a failure there moved nothing, so the
// reset is rejected at once, DATA10-4), then each target's idempotent
// rename journaled as moved, then set_aside. A rename failure from
// set_aside_started on moves every target back first (R9-6). Every target's
// rename runs on every resume: a moved record says the item was set aside
// once, not where it is now, since a move-back killed before its rejected
// record puts items back under their moved records (R1-21 review M6).
// moved holds the targets an earlier boot journaled, whose record is not
// written again, and a target that never existed (nothing at either name
// after its rename) is not journaled at all (review n1): one moved record
// per item set aside.
func (s *Swapper) resetFrom(ctx context.Context, in restorejournal.Intent, last string, moved map[string]bool) (Outcome, error) {
	seams := bootseam.From(ctx)
	if last == StepStaged {
		err := bootseam.Labelled(seams, "swap.location.copy", func() error {
			return copyIfExists(s.fs, filepath.Join(s.o.StateDir, locationFile), filepath.Join(s.journalDir(in.ID), locationFile))
		})
		if err == nil {
			err = s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepSetAsideStarted, InstallID: in.InstallID})
		}
		if err != nil {
			return s.rejectReset(ctx, in, err)
		}
	}
	for _, t := range in.Targets {
		// Nothing but this set-aside writes a target's destination in the
		// swap's private namespace, so in the crash model a second name
		// there is a twin (memGhost); on the real filesystem mv proves it
		// by identity.
		// The destination's chain may be an earlier boot's, created with a
		// failed fsync (R1-21 review M6): synced before the rename into it.
		if err := bootseam.Labelled(seams, "swap.setaside.mv", func() error {
			if err := s.mkdirDurable(filepath.Dir(t.To)); err != nil {
				return err
			}
			return mv(s.fs, t.From, t.To, memGhost)
		}); err != nil {
			return s.moveBack(ctx, in, err)
		}
		if moved[filepath.Clean(t.From)] {
			continue
		}
		if ok, err := exists(s.fs, t.To); err != nil {
			return s.moveBack(ctx, in, err)
		} else if !ok {
			continue // never existed: nothing was set aside
		}
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepMoved, From: t.From, To: t.To}); err != nil {
			return s.moveBack(ctx, in, err)
		}
	}
	if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepSetAside}); err != nil {
		return s.moveBack(ctx, in, err)
	}
	return Outcome{Boot: BootReset, ID: in.ID, Intent: in, Last: StepSetAside}, nil
}

// moveBack is the reset failure rule from set_aside_started (05-storage
// §5S.3.15 "Failure rule"): every intent target in reverse order, a superset
// of the moved records (a target renamed but not yet journaled included),
// back to its source, then rejected. A move-back that fails halts boot
// naming every path; the next boot resumes the set-aside forward, which
// machine 1 allows.
func (s *Swapper) moveBack(ctx context.Context, in restorejournal.Intent, cause error) (Outcome, error) {
	seams := bootseam.From(ctx)
	var errs []error
	for _, t := range slices.Backward(in.Targets) {
		if err := bootseam.Labelled(seams, "swap.back.mv", func() error { return mv(s.fs, t.To, t.From, memGhost) }); err != nil {
			errs = append(errs, fmt.Errorf("move %s back to %s: %w", t.To, t.From, err))
		}
	}
	if len(errs) > 0 {
		return Outcome{}, fmt.Errorf("restoreswap: reset %s failed (%w) and its move-back failed: %w", in.ID, cause, errors.Join(errs...))
	}
	return s.rejectReset(ctx, in, cause)
}

// rejectReset journals rejected, renames the in-flight file to
// rejected.json and boots the untouched store, degraded
// factory_reset_failed.
func (s *Swapper) rejectReset(ctx context.Context, in restorejournal.Intent, cause error) (Outcome, error) {
	if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepRejected, Note: cause.Error()}); err != nil {
		return Outcome{}, errors.Join(cause, err)
	}
	if err := bootseam.Labelled(bootseam.From(ctx), "swap.slot.rejected", func() error {
		return slotMove(s.fs, s.inflightPath(in.ID), s.rejectedPath(in.ID))
	}); err != nil {
		return Outcome{}, errors.Join(cause, err)
	}
	s.o.Logger.Error("restoreswap: factory reset failed and was rejected; booting the untouched store",
		"id", in.ID, "err", cause)
	return Outcome{ID: in.ID, Intent: in, Last: StepRejected, Degraded: DegradedFactoryResetFailed,
		Reason: fmt.Sprintf("reset %s: %v", in.ID, cause)}, nil
}
