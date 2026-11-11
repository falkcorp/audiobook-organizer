// file: core/internal/restoreswap/rewrite.go
// version: 1.0.3
// guid: e4287385-23de-4df7-b1bb-31ac3240dac4
// last-edited: 2026-11-11

package restoreswap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// M8's phase engine (05-storage §5S.3.13 "M8: the rewrite"; 05-backup
// §5B.3.4 "M8's store rewrite is KindRewrite of this swap", rules (1)-(5)).
// storage/rewrite.go holds storage's half (the record, the compaction, the
// copy, the in-process reopen); this file derives M8's phase and runs the
// KindRewrite swap in process with the one journaled swap's own steps.
//
// The phase is derived from the KindRewrite journals whose StepID is this
// M8's own (rejected ones skipped), newest first, not from the copy (whose
// own record reads copied, §5S.3.13):
//
//   - record done: nothing;
//   - no journal: phases 1 (compact), 2 (copy) and 3 (the swap), then 4,
//     all in this boot, so the copy that is swapped in is always one taken
//     from the store as this boot found it;
//   - newest journal swapped, opened or applied: phase 4 without a second
//     swap;
//   - newest journal swapped_back: phases 1-4 once more;
//   - the two newest journals both swapped_back: storage.ErrRewriteSwappedBack
//     (exit 78), nothing staged. M8 never loops.
//
// A swap-back moves no checkpoint (the checkpoint area is not a set-aside)
// and starts no clock (no record of the checkpoint area is touched).

// The labels of the in-process swap's own writes beyond the shared swap
// steps (06 §6.5.1's boot-writer table, M8's phases).
const (
	// LabelRewriteStage: the KindRewrite intent's create in the slot and its
	// journal directory.
	LabelRewriteStage = "swap.rewrite.stage"
	// LabelRewriteDelete: phase 4's deletion of the set-aside directory and
	// its rewriteDeleteStarted mark.
	LabelRewriteDelete = "swap.rewrite.delete"
)

// rewriteExpiry bounds a KindRewrite intent: it is staged and taken in one
// process within moments, so an older one is a crash's leftover.
const rewriteExpiry = time.Hour

// rewriteJournal is one of M8's own journals: its id and last step.
type rewriteJournal struct {
	id   string
	at   time.Time
	last string
}

// rewriteJournals lists the KindRewrite journals under restore/ whose staged
// record names stepID, newest first (by the staged record's time, then by
// id), skipping rejected ones: a rejected intent moved nothing.
func (s *Swapper) rewriteJournals(stepID string) ([]rewriteJournal, error) {
	dir := restorejournal.Dir(s.o.StateDir)
	names, err := s.fs.List(dir)
	if err != nil {
		ok, xerr := exists(s.fs, dir)
		if xerr == nil && !ok {
			return nil, nil // no swap has ever journaled
		}
		return nil, errors.Join(fmt.Errorf("restoreswap: list %s: %w", dir, err), xerr)
	}
	var out []rewriteJournal
	for _, n := range names {
		if !restorejournal.ValidID(n) {
			continue
		}
		fi, err := s.fs.Stat(filepath.Join(dir, n))
		if err != nil || !fi.IsDir() {
			continue
		}
		recs, err := ReadJournal(s.fs, s.o.StateDir, n)
		if err != nil {
			return nil, err
		}
		if len(recs) == 0 || recs[0].Step != StepStaged || recs[0].Note != string(restorejournal.KindRewrite) || recs[0].StepID != stepID {
			continue
		}
		last := lastStep(recs)
		if last == StepRejected {
			continue
		}
		out = append(out, rewriteJournal{id: n, at: recs[0].At, last: last})
	}
	slices.SortFunc(out, func(a, b rewriteJournal) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return strings.Compare(b.id, a.id)
	})
	return out, nil
}

// RunRewrite is M8's Run (the storage.DataStep the kernel builds for
// step): the phase engine above over db, inside a writable boot's Migrate.
// It returns nil once M8's record is done. A swap that fails and swaps back
// returns an error wrapping ErrSwappedBack (the boot fails; the next boot
// restarts M8 once); the second consecutive swap-back is
// storage.ErrRewriteSwappedBack.
func (s *Swapper) RunRewrite(ctx context.Context, db *storage.DB, step storage.RewriteStep) error {
	state, err := db.RewriteRecord(ctx, step.ID)
	if err != nil || state == storage.DataMigrationDone {
		return err
	}
	js, err := s.rewriteJournals(step.ID)
	if err != nil {
		return err
	}
	switch {
	case len(js) >= 2 && js[0].last == StepSwappedBack && js[1].last == StepSwappedBack:
		return fmt.Errorf("%w (journals %s and %s)", storage.ErrRewriteSwappedBack, js[0].id, js[1].id)
	case len(js) > 0 && (js[0].last == StepSwapped || js[0].last == StepOpened || js[0].last == StepApplied):
		return s.rewriteFinish(ctx, db, step, js[0].id)
	case len(js) > 0 && js[0].last == StepSwappedBack:
		s.o.Logger.Warn("restoreswap: the store rewrite swapped back once; restarting it from the compaction",
			"step", step.ID, "journal", js[0].id)
	case len(js) > 0:
		return fmt.Errorf("%w: KindRewrite journal %s of %s at %q with no swap in flight", ErrJournalCorrupt, js[0].id, step.ID, js[0].last)
	}
	if err := s.rewriteCopy(ctx, db, step); err != nil {
		return err
	}
	id, err := s.rewriteSwap(ctx, db, step)
	if err != nil {
		return err
	}
	return s.rewriteFinish(ctx, db, step, id)
}

// rewriteCopy is phases 1 and 2: compact, then copy into RewriteDir (the
// copy carries M8's record at copied; the live store's stays pending).
func (s *Swapper) rewriteCopy(ctx context.Context, db *storage.DB, step storage.RewriteStep) error {
	if err := db.RewriteCompact(ctx); err != nil {
		return err
	}
	stats, err := db.RewriteCopy(ctx, storage.RewriteDir(db.Path()), step.ID)
	if err != nil {
		return err
	}
	s.o.Logger.Info("restoreswap: the store rewrite copied the store", "step", step.ID,
		"keys", stats.Keys, "bytes", stats.Bytes, "tables", stats.Tables)
	return nil
}

// newRewriteID is a fresh intent id: rewrite-<UTC time>[-n], one no journal
// holds.
func (s *Swapper) newRewriteID() (string, error) {
	base := "rewrite-" + s.now().UTC().Format("20060102T150405.000000000Z")
	for n := range 1000 {
		id := base
		if n > 0 {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		if ok, err := exists(s.fs, s.journalDir(id)); err != nil {
			return "", err
		} else if !ok {
			return id, nil
		}
	}
	return "", fmt.Errorf("restoreswap: no free KindRewrite id for %s", base)
}

// rewriteSwap is phase 3: the KindRewrite swap in process, inside
// storage's RewriteSwap (engine closed around it). The one intent slot: a
// staged or in-flight swap refuses (restorejournal.ErrSwapPending). The
// in-flight file exists before the first rename; then the steps of every
// swap (the store set aside, the copy moved into place, the sidecars from
// the copy, swapped), and on any failure after the in-flight file step x
// moves the original back. A copy in place that storage cannot reopen
// writable is swapped back the same way (RewriteSwap's swapBack). Then
// opened once the copy is open.
func (s *Swapper) rewriteSwap(ctx context.Context, db *storage.DB, step storage.RewriteStep) (string, error) {
	if s.o.RewriteStep != step.ID {
		return "", fmt.Errorf("restoreswap: the swapper serves M8 %q, not %q", s.o.RewriteStep, step.ID)
	}
	id, err := s.newRewriteID()
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	dbPath := filepath.Clean(db.Path())
	in := restorejournal.Intent{
		ID: id, Kind: restorejournal.KindRewrite, DBPath: dbPath,
		Targets:       []restorejournal.Target{{From: storage.RewriteDir(dbPath), To: dbPath}},
		StagedByBuild: s.o.BuildID, ExpiresAt: now.Add(rewriteExpiry),
		RequestedBy: "core/storage " + step.ID, RequestedAt: now, StepID: step.ID,
	}
	if err := restorejournal.ValidateTargets(s.o.StateDir, in, s.legacySuffixes()); err != nil {
		return "", err
	}
	err = db.RewriteSwap(ctx, func(ctx context.Context) error {
		if err := s.stageRewrite(ctx, in); err != nil {
			return err
		}
		if err := bootseam.Labelled(bootseam.From(ctx), "swap.slot.inflight", func() error {
			return slotMove(s.fs, s.pendingPath(), s.inflightPath(id))
		}); err != nil {
			if ok, xerr := exists(s.fs, s.inflightPath(id)); xerr != nil || ok {
				return errors.Join(err, xerr, s.SwapBack(ctx, id, err))
			}
			_, rerr := s.rejectPending(ctx, in, "the in-flight rename failed: "+err.Error(), false, "")
			return errors.Join(err, rerr)
		}
		if err := s.forwardSteps(ctx, in, StepStaged); err != nil {
			return s.SwapBack(ctx, id, err)
		}
		return nil
	}, func(ctx context.Context, cause error) error { return s.SwapBack(ctx, id, cause) })
	if err != nil {
		return "", fmt.Errorf("restoreswap: the store rewrite's swap (%s): %w", id, err)
	}
	if err := s.Opened(ctx, id); err != nil {
		return "", err
	}
	return id, nil
}

// stageRewrite stages in into the one slot: refused while any swap is
// staged or in flight; the journal directory, the intent's create
// (restorejournal.CreateIntentFS, whose link is the exclusion) and the
// staged record naming M8's step.
func (s *Swapper) stageRewrite(ctx context.Context, in restorejournal.Intent) error {
	if _, ok, err := InFlight(s.fs, s.o.StateDir); err != nil {
		return err
	} else if ok {
		return restorejournal.ErrSwapPending
	}
	if err := bootseam.Labelled(bootseam.From(ctx), LabelRewriteStage, func() error {
		if err := s.mkdirDurable(s.journalDir(in.ID)); err != nil {
			return err
		}
		return restorejournal.CreateIntentFS(s.fs, s.o.StateDir, in)
	}); err != nil {
		return err
	}
	return s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepStaged, Note: string(in.Kind), StepID: in.StepID})
}

// rewriteFinish is phase 4, idempotent: the swap's set-aside deleted at once
// (KindRewrite keeps no previous/; finishRewriteSwap), the journal applied,
// the in-flight file removed, then M8's record done. A kill anywhere repeats
// it.
func (s *Swapper) rewriteFinish(ctx context.Context, db *storage.DB, step storage.RewriteStep, id string) error {
	if err := s.finishRewriteSwap(ctx, id, step.ID, db.Path()); err != nil {
		return err
	}
	s.o.Logger.Info("restoreswap: the store rewrite is done; the old directory is deleted", "step", step.ID, "journal", id)
	return db.SetRewriteRecord(ctx, step.ID, storage.DataMigrationDone)
}

// rewriteDeleteStarted is phase 4's durable mark in restore/<id>/: it exists
// from before the set-aside's deletion, so a later run that finds the
// set-aside gone knows the deletion did it.
const rewriteDeleteStarted = "rewrite.delete.started"

// finishRewriteSwap is phase 4's swap half for journal id of M8 stepID over
// the store at dbPath. Until the journal is applied, the in-flight file's
// intent is the one the move used: it must be stepID's KindRewrite intent
// for dbPath, and the set-aside its step c moved the old store to (the same
// SetAsideDest rule, s.setAsides) is deleted (deleteRewriteAside). Then
// applied is journaled and the in-flight file removed.
func (s *Swapper) finishRewriteSwap(ctx context.Context, id, stepID, dbPath string) error {
	last, err := s.last(id)
	if err != nil {
		return err
	}
	if last != StepApplied {
		in, err := readIntent(s.fs, s.inflightPath(id))
		if err != nil {
			return fmt.Errorf("%w: the store rewrite %s is at %q and its intent does not read: %w", ErrJournalCorrupt, id, last, err)
		}
		if in.ID != id || in.Kind != restorejournal.KindRewrite || in.StepID != stepID {
			return fmt.Errorf("%w: restore/%s/inflight.json holds %s intent %s of step %q, not M8 %q", ErrJournalCorrupt, id, in.Kind, in.ID, in.StepID, stepID)
		}
		if filepath.Clean(in.DBPath) != filepath.Clean(dbPath) {
			return fmt.Errorf("%w: the store rewrite %s moved the store at %s; this boot opened %s", ErrJournalCorrupt, id, in.DBPath, dbPath)
		}
		if err := s.deleteRewriteAside(ctx, in); err != nil {
			return err
		}
		if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepApplied}); err != nil {
			return err
		}
	}
	if ok, err := exists(s.fs, s.inflightPath(id)); err != nil {
		return err
	} else if ok {
		return s.removeInflight(ctx, id)
	}
	return nil
}

// rewriteAside is where KindRewrite intent in's step c moved the old store:
// dest, the DB directory's set-aside (s.setAsides, restorejournal's
// SetAsideDest rule), and leaf, the directory of the swap's aside root that
// holds it (previous/: <state dir>/restore/<id>/previous for a DB under the
// state directory, the compiled default, else <db parent>/.mediacore-restore/
// <id>/previous), with root, that aside root.
func (s *Swapper) rewriteAside(in restorejournal.Intent) (dest, leaf, root string, err error) {
	sa := s.setAsides(in)
	if len(sa) != 1 || filepath.Clean(sa[0].From) != filepath.Clean(in.DBPath) {
		return "", "", "", fmt.Errorf("restoreswap: KindRewrite %s sets aside %d items, want its DB directory alone", in.ID, len(sa))
	}
	dest = filepath.Clean(sa[0].To)
	sep := string(filepath.Separator)
	for _, r := range restorejournal.AsideRoots(s.o.StateDir, in.DBPath, in.ID, []string{in.DBPath}) {
		rel, err := filepath.Rel(filepath.Clean(r), dest)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+sep) {
			continue
		}
		if first, rest, ok := strings.Cut(rel, sep); ok && rest != "" {
			return dest, filepath.Join(filepath.Clean(r), first), filepath.Clean(r), nil
		}
	}
	return "", "", "", fmt.Errorf("restoreswap: KindRewrite %s's set-aside %s lies in none of its aside roots", in.ID, dest)
}

// deleteRewriteAside deletes the old store in's step c set aside (its
// previous/ leaf; rewriteAside), fail-closed: with no deletion begun
// (rewriteDeleteStarted absent) the set-aside must exist, or the journal
// says the move happened and the old store is not where the move put it, an
// error (ErrJournalCorrupt) that deletes nothing. The mark is written before
// the first removal; an aside root of the swap's own outside the journal
// directory is removed once empty. The journal directory itself stays.
func (s *Swapper) deleteRewriteAside(ctx context.Context, in restorejournal.Intent) error {
	dest, leaf, root, err := s.rewriteAside(in)
	if err != nil {
		return err
	}
	mark := s.markPath(in.ID, rewriteDeleteStarted)
	return bootseam.Labelled(bootseam.From(ctx), LabelRewriteDelete, func() error {
		started, err := exists(s.fs, mark)
		if err != nil {
			return err
		}
		present, err := exists(s.fs, dest)
		if err != nil {
			return err
		}
		if !present && !started {
			return fmt.Errorf("%w: the store rewrite %s moved the old store to %s and nothing is there", ErrJournalCorrupt, in.ID, dest)
		}
		if !started {
			if err := writeFileAtomic(s.fs, mark, nil, privateFile); err != nil {
				return err
			}
		}
		if err := s.fs.RemoveAll(leaf); err != nil {
			return fmt.Errorf("restoreswap: delete the rewrite's set-aside %s: %w", leaf, err)
		}
		if err := syncDirIfExists(s.fs, filepath.Dir(leaf)); err != nil {
			return err
		}
		if root == filepath.Clean(s.journalDir(in.ID)) {
			return nil
		}
		if names, err := s.fs.List(root); err == nil && len(names) == 0 {
			if err := s.fs.Remove(root); err != nil {
				return fmt.Errorf("restoreswap: remove the rewrite's aside root %s: %w", root, err)
			}
			return syncDirIfExists(s.fs, filepath.Dir(root))
		}
		return nil
	})
}

// RewriteDry is the dry run's M8 (`storage migrate --dry-run`): phases 1
// and 2 over the scratch store into dir, a second scratch directory, then
// M8's record done in the scratch store; nothing is staged or swapped.
func RewriteDry(ctx context.Context, db *storage.DB, step storage.RewriteStep, dir string) (storage.RewriteCopyStats, error) {
	if err := db.RewriteCompact(ctx); err != nil {
		return storage.RewriteCopyStats{}, err
	}
	stats, err := db.RewriteCopy(ctx, dir, step.ID)
	if err != nil {
		return stats, err
	}
	return stats, db.SetRewriteRecord(ctx, step.ID, storage.DataMigrationDone)
}

// resumeRewrite is P0a over an in-flight KindRewrite journal of this build's
// M8 (a kill inside phase 3): a swap-back that started is continued; a swap
// short of swapped is taken forward (step x on a failure); at swapped or
// opened the boot opens the copy and M8's phase 4 finishes (BootRewrite); a
// terminal journal only loses its in-flight file.
func (s *Swapper) resumeRewrite(ctx context.Context, in restorejournal.Intent, recs []restorejournal.Record) (Outcome, error) {
	if ok, err := exists(s.fs, s.markPath(in.ID, backStarted)); err != nil {
		return Outcome{}, err
	} else if ok {
		return s.rewriteSwappedBack(in, s.continueSwapBack(ctx, in, "a swap-back interrupted by a crash"))
	}
	last := lastStep(recs)
	switch last {
	case StepStaged, StepPreviousMoved, StepStagedMoved, StepSidecars:
		if err := s.forwardSteps(ctx, in, last); err != nil {
			return s.rewriteSwappedBack(in, s.SwapBack(ctx, in.ID, err))
		}
		return Outcome{Boot: BootRewrite, ID: in.ID, Intent: in, Last: StepSwapped}, nil
	case StepSwapped, StepOpened:
		return Outcome{Boot: BootRewrite, ID: in.ID, Intent: in, Last: last}, nil
	case StepApplied, StepSwappedBack, StepRejected:
		if err := s.removeInflight(ctx, in.ID); err != nil {
			return Outcome{}, err
		}
		return Outcome{ID: in.ID, Intent: in, Last: last}, nil
	}
	return Outcome{}, fmt.Errorf("%w: KindRewrite journal %s at %q with its in-flight file", ErrJournalCorrupt, in.ID, last)
}

// rewriteSwappedBack is P0a's outcome of a KindRewrite swap-back it ran: the
// original store boots plain (M8 restarts once, or refuses after a second
// swap-back); a swap-back that failed halts boot.
func (s *Swapper) rewriteSwappedBack(in restorejournal.Intent, err error) (Outcome, error) {
	if !errors.Is(err, ErrSwappedBack) {
		return Outcome{}, err
	}
	return Outcome{ID: in.ID, Intent: in, Last: StepSwappedBack}, nil
}

// rejectRewritePending rejects a KindRewrite intent left in the slot by a
// kill between its create and its in-flight rename: nothing moved, so it is
// journaled rejected and moved to rejected.json, and the boot goes on plain
// with no degraded token; M8 stages a new one.
func (s *Swapper) rejectRewritePending(ctx context.Context, in restorejournal.Intent) (Outcome, error) {
	recs, err := s.ensureStaged(ctx, in)
	if err != nil {
		return Outcome{}, err
	}
	return s.rejectPending(ctx, in, "a KindRewrite intent left staged by a crash before its in-flight rename",
		lastStep(recs) == StepRejected, "")
}
