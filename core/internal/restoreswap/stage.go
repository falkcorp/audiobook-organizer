// file: core/internal/restoreswap/stage.go
// version: 1.0.0
// guid: f8808c55-579d-4ed5-81e1-2c5276a578b9
// last-edited: 2026-11-07

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
// from the stager (R1-25's StageReset), each To its SetAsideDest.
//
// Stage runs outside a boot (an offline command, or a request after P0a),
// so its operations are not crash-matrix rows: a crash leaves the slot
// taken or free, and an empty restore/<id>/ that P0a ignores.
func (s *Swapper) Stage(ctx context.Context, in restorejournal.Intent) error {
	if in.ID == "" || filepath.Base(in.ID) != in.ID {
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
		for _, t := range in.Targets {
			if want := restorejournal.SetAsideDest(s.o.StateDir, in.DBPath, in.ID, t.From); filepath.Clean(t.To) != want {
				return fmt.Errorf("restoreswap: reset target %s is set aside to %s, want %s (05-storage §5S.3.15's one rule)", t.From, t.To, want)
			}
		}
		moves = in.Targets
	case restorejournal.KindRewrite:
		return fmt.Errorf("%w: M8 stages its own swap in process", ErrRewriteNotInThisBuild)
	default:
		return fmt.Errorf("restoreswap: unknown swap kind %q", in.Kind)
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
	// S2: DATA-23, before anything is written.
	if err := restorejournal.Validate(s.o.Probe, moves); err != nil {
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
		if err := restorejournal.CreateIntentFS(s.fs, s.o.StateDir, in); err != nil {
			if rmErr := s.fs.RemoveAll(dir); rmErr != nil {
				err = errors.Join(err, fmt.Errorf("restoreswap: remove %s: %w", dir, rmErr))
			}
			return err
		}
		// S5: a create without this record is completed by P0a.
		return s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepStaged, Note: string(in.Kind), StepID: in.StepID})
	})
}
