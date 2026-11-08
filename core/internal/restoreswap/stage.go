// file: core/internal/restoreswap/stage.go
// version: 1.0.3
// guid: f8808c55-579d-4ed5-81e1-2c5276a578b9
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// Stage puts in into the one slot (05-backup §5B.3.4 steps 1, 6, 8;
// 05-storage §5S.3.15 "Stage"): the early refusal while an intent is staged
// or a swap is in flight (ErrSwapPending), the rename pre-check of every
// rename the swap will make (ErrRenameRefused, nothing staged), the
// journal's directory, the one atomic create (restorejournal.CreateIntentFS:
// link(2) decides between two stagers, in one process or two, with no
// mutex), then the journal's staged record. A restore intent's Targets are
// its placements ({From: staged item, To: live path}, decision D1) and every
// From must exist; a KindReset intent's Targets are its set-aside pairs,
// from the stager (R1-25's StageReset), and its Delete entries pass
// restorejournal.ValidateDelete (absolute, no kept path, off the DB's
// filesystem); a restore deletes nothing. Both pass
// restorejournal.ValidateTargets (ErrTargetsRefused: a placement at, or a
// set-aside of, DBPath; no live path in or holding restore/, the state
// directory or a swap's private directory; R1-21 review m4, m5).
//
// Once the create linked pending.json, Stage's result says what P0a will
// do (R1-21 review m1): a create that could not sync the slot but left the
// intent in it (restorejournal.ErrIntentUnsynced), and a staged record that
// could not be written (P0a writes it, ensureStaged), are logged as
// warnings and Stage succeeds. Only a create that left nothing in the slot
// is reported as failed.
//
// Stage runs outside a boot (an offline command, or a request after P0a),
// so its operations are not crash-matrix rows: a crash leaves the slot
// taken or free, and an empty restore/<id>/ that P0a ignores.
func (s *Swapper) Stage(ctx context.Context, in restorejournal.Intent) error {
	if !restorejournal.ValidID(in.ID) {
		return fmt.Errorf("restoreswap: intent id %q is not one path segment", in.ID)
	}
	if !filepath.IsAbs(in.DBPath) {
		return fmt.Errorf("restoreswap: intent db_path %q is not absolute", in.DBPath)
	}
	var moves []restorejournal.Target
	switch in.Kind {
	case restorejournal.KindArchive, restorejournal.KindRevert, restorejournal.KindCheckpoint:
		if len(in.Targets) == 0 {
			return errors.New("restoreswap: a restore intent places nothing")
		}
		for _, t := range in.Targets {
			if ok, err := exists(s.fs, t.From); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("restoreswap: staged item %s does not exist", t.From)
			}
		}
		moves = s.restoreMoves(in)
	case restorejournal.KindReset:
		if in.Reset != restorejournal.ResetStore && in.Reset != restorejournal.ResetFactory {
			return fmt.Errorf("restoreswap: reset kind %q is neither store nor factory", in.Reset)
		}
		moves = in.Targets
	case restorejournal.KindRewrite:
		return fmt.Errorf("%w: M8 stages its own swap in process", ErrRewriteNotInThisBuild)
	default:
		return fmt.Errorf("restoreswap: unknown swap kind %q", in.Kind)
	}
	if err := restorejournal.ValidateTargets(s.o.StateDir, in, s.legacySuffixes()); err != nil {
		return err
	}
	// S1: the early refusal; S4's create is the one that decides.
	if ok, err := exists(s.fs, s.pendingPath()); err != nil {
		return err
	} else if ok {
		return restorejournal.ErrSwapPending
	}
	if _, ok, err := InFlight(s.fs, s.o.StateDir); err != nil {
		return err
	} else if ok {
		return restorejournal.ErrSwapPending
	}
	// S2: DATA-23, before anything is written; and a reset's delete entries
	// (rule (4)'s kept paths, the DB's filesystem; R1-21 review M1).
	if err := errors.Join(restorejournal.Validate(s.o.Probe, moves),
		restorejournal.ValidateDelete(s.o.Probe, s.o.StateDir, in, s.legacySuffixes())); err != nil {
		return err
	}
	return bootseam.Labelled(bootseam.From(ctx), "swap.stage", func() error {
		// S3: the journal's directory. S4: the one create.
		dir := s.journalDir(in.ID)
		if ok, err := exists(s.fs, dir); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("restoreswap: restore/%s already exists; an intent id is never reused", in.ID)
		}
		if err := mkdirAllSynced(s.fs, dir); err != nil {
			return err
		}
		if err := restorejournal.CreateIntentFS(s.fs, s.o.StateDir, in); errors.Is(err, restorejournal.ErrIntentUnsynced) {
			s.o.Logger.Warn("restoreswap: intent staged, but the slot directory could not be synced; a crash may lose it",
				"id", in.ID, "kind", string(in.Kind), "err", err)
		} else if err != nil {
			if rmErr := s.fs.RemoveAll(dir); rmErr != nil {
				err = errors.Join(err, fmt.Errorf("restoreswap: remove %s: %w", dir, rmErr))
			}
			return err
		}
		// S5: a create without this record is completed by P0a
		// (ensureStaged), so its failure does not make the staged intent a
		// failed Stage.
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepStaged, Note: string(in.Kind), StepID: in.StepID}); err != nil {
			s.o.Logger.Warn("restoreswap: intent staged; its staged record was not written and P0a writes it",
				"id", in.ID, "kind", string(in.Kind), "err", err)
		}
		return nil
	})
}
