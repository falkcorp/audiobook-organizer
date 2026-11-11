// file: core/internal/restoreswap/offline.go
// version: 1.0.1
// guid: 8d41e6b3-0a27-4c95-b13f-6e2a9c7d5f08
// last-edited: 2026-11-11

package restoreswap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// StepAppliedOffline is the offline restore's terminal step when no
// post-restore hook is due (05-storage §5S.3.12 "In place", §5S.3.16
// machine 1: swapped --> applied_offline, and the escape's failed -->
// applied_offline), with HooksOwed when the source owes them.
const StepAppliedOffline = "applied_offline"

// supersededNote is the rejected record's note of a pending intent the
// offline restore rejects itself (§5S.3.12 "In place").
const supersededNote = "superseded by offline restore"

// The escape's durable marks in the failed journal's directory (§5S.3.12
// "In place", the one recipe admitted while inflight.json exists): machine
// 1 has no step between failed and applied_offline, so, as step x does with
// backStarted and backMoved, the escape records its progress as files
// beside the journal. escapeStarted exists from the escape's first
// rename: a boot that finds it refuses the store (resumeRestore), which
// may be half swapped, and names the recipe to finish it. escapeMoved
// exists once every live item is in failed/: from then on a live path
// that exists can only be the source placed.
const (
	escapeStarted = "escape.started"
	escapeMoved   = "escape.moved"
)

// ErrEscapeInterrupted: an offline escape from a failed checkpoint restore
// was interrupted; its store may be half swapped. Boot halts; rerunning
// the same storage restore-offline --checkpoint finishes it.
var ErrEscapeInterrupted = errors.New("restoreswap: an offline restore of a failed checkpoint restore was interrupted; rerun storage restore-offline --checkpoint with the same name")

// OfflineSource is what storage restore-offline swaps in place of the live
// store (§5S.3.12): the source store directory (a checkpoint's db/, or an
// archive extracted into the swap's staging directory) and each state file
// the source holds, staged and checked against its recorded SHA-256
// ({From: the staged copy, To: the recorded path}).
type OfflineSource struct {
	// ID is the restore's intent ID, "" = NewID: the caller that extracts
	// an archive into StagingDir(dbPath, ID) before the swap names it.
	ID         string
	Kind       restorejournal.Kind // KindCheckpoint or KindArchive
	Store      string
	StateFiles []restorejournal.Target
	HooksOwed  bool   // the checkpoint record's hooks_owed (DATA-7)
	InstallID  string // the live store's, for a marker-less source
	ArchiveID  string // KindArchive
}

// OfflineResult is what RestoreOffline did.
type OfflineResult struct {
	ID         string // the journal: this restore's, or the escaped failed journal
	Escaped    bool   // a failed KindCheckpoint journal was finished by this restore
	Superseded string // the pending intent rejected as superseded; "" when none
	// Linked: Stage linked this restore's intent, so the swap owns the
	// source from then on (step x, a rejection, or applied_offline settles
	// it). A caller that staged the source (an archive extracted into
	// StagingDir, state-file copies) removes it when RestoreOffline fails
	// with Linked false (R1-25 review SF3). An escape journals no staging
	// path, so it never links: what it staged is copies.
	Linked bool
	// Warnings are what the restore could not finish beyond the swap,
	// which stands: the replaced store's KindRewrite journals left
	// unretired (retireRewriteJournals).
	Warnings []string
}

// CheckOffline is RestoreOffline's slot check, read without writing
// anything, for the caller to run before it stages a source (R1-25 review
// SF3): a staged intent passes (RestoreOffline supersedes it); a swap in
// flight refuses, naming it (restore_inflight), unless it is the one
// escape RestoreOffline admits for a source of kind, whose journal id is
// returned. RestoreOffline checks again, under the same rules, before it
// moves anything.
func (s *Swapper) CheckOffline(dbPath string, kind restorejournal.Kind) (escape string, err error) {
	id, k, last, inflight, err := InFlightJournal(s.fs, s.o.StateDir)
	if err != nil || !inflight {
		return "", err
	}
	recs, err := ReadJournal(s.fs, s.o.StateDir, id)
	if err != nil {
		return "", err
	}
	if _, err := s.escapable(id, k, last, dbPath, kind, recs); err != nil {
		return "", err
	}
	return id, nil
}

// RestoreOffline is storage restore-offline's in-place swap (05-storage
// §5S.3.12 "In place"), in this process, with the service stopped (the
// caller holds that the store opened read-only first, so no service holds
// its LOCK):
//
//   - a swap in flight refuses (restore_inflight), except a KindCheckpoint
//     journal at failed staged by another binary, which a KindCheckpoint
//     source escapes: the failed store goes to that journal's failed/, the
//     source is placed, the sidecars rewritten, and the journal finishes at
//     applied_offline with HooksOwed (DATA-7);
//   - a pending intent is rejected (note "superseded by offline restore");
//   - otherwise the restore stages its own intent in the one slot (Stage:
//     every rename pre-validated, ErrRenameRefused), renames it in flight,
//     runs steps c to g, journals applied_offline (HooksOwed from the
//     source) and deletes the in-flight file. A failure after the in-flight
//     rename runs step x and returns ErrSwappedBack; a failure of step x
//     itself returns its error, and the next boot continues it.
//
// No post-restore hook is due in release R1, which has no revocation
// journal, so the restore always finishes at applied_offline itself.
//
// A restore that placed its source (an escape too) then retires the
// KindRewrite journals of the store it replaced (retireRewriteJournals), so
// the restored store's M8 counts none of that store's swap-backs; a retire
// that fails is a warning, since the restored store's next boot is a new
// upgrade attempt and M8 counts only its attempt's swap-backs anyway.
func (s *Swapper) RestoreOffline(ctx context.Context, dbPath, requestedBy string, src OfflineSource) (OfflineResult, error) {
	res, err := s.restoreOffline(ctx, dbPath, requestedBy, src)
	if err != nil {
		return res, err
	}
	if err := s.retireRewriteJournals(ctx, dbPath, res.ID); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("the replaced store's M8 journals were not retired: %v; "+
			"the restored store's next boot is a new upgrade attempt, so M8 counts none of their swap-backs", err))
	}
	return res, nil
}

// retireRewriteJournals journals retired after every KindRewrite journal
// of the store at dbPath that ended (applied or swapped_back): restore id
// replaced that store (§5S.3.13 "M8 never loops"; RunRewrite). A swap in
// flight refuses the restore before it stages anything, so no such journal
// is in flight here.
func (s *Swapper) retireRewriteJournals(ctx context.Context, dbPath, id string) error {
	js, err := s.kindRewriteJournals("", dbPath)
	if err != nil {
		return err
	}
	var errs []error
	for _, j := range js {
		if j.last != StepApplied && j.last != StepSwappedBack {
			continue
		}
		if err := s.appendRec(ctx, j.id, restorejournal.Record{Step: StepRetired, Note: "replaced by storage restore-offline " + id}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Swapper) restoreOffline(ctx context.Context, dbPath, requestedBy string, src OfflineSource) (OfflineResult, error) {
	if src.Kind != restorejournal.KindCheckpoint && src.Kind != restorejournal.KindArchive {
		return OfflineResult{}, fmt.Errorf("restoreswap: an offline restore swaps in a checkpoint or an archive, not %q", src.Kind)
	}
	if !filepath.IsAbs(dbPath) || !filepath.IsAbs(src.Store) {
		return OfflineResult{}, fmt.Errorf("restoreswap: offline restore paths %q and %q must be absolute", dbPath, src.Store)
	}
	id, kind, last, inflight, err := InFlightJournal(s.fs, s.o.StateDir)
	if err != nil {
		return OfflineResult{}, err
	}
	if inflight {
		return s.escape(ctx, id, kind, last, dbPath, src)
	}
	var res OfflineResult
	if pending, err := exists(s.fs, s.pendingPath()); err != nil {
		return res, err
	} else if pending {
		if res.Superseded, err = s.supersede(ctx); err != nil {
			return res, err
		}
	}
	now := s.now()
	id = src.ID
	if id == "" {
		id = NewID(now)
	}
	in := restorejournal.Intent{
		ID: id, Kind: src.Kind, DBPath: filepath.Clean(dbPath),
		Targets:       append([]restorejournal.Target{{From: src.Store, To: filepath.Clean(dbPath)}}, src.StateFiles...),
		StagedByBuild: s.o.BuildID, ExpiresAt: now.Add(intentTTL), RequestedBy: requestedBy, RequestedAt: now,
		InstallID: src.InstallID, ArchiveID: src.ArchiveID,
	}
	res.ID = in.ID
	if err := s.Stage(ctx, in); err != nil {
		return res, err
	}
	res.Linked = true
	if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepRechecked}); err != nil {
		return res, err
	}
	if err := labelled(ctx, "swap.slot.inflight", func() error {
		return slotMove(s.fs, s.pendingPath(), s.inflightPath(in.ID))
	}); err != nil {
		// Nothing moved: the intent leaves the slot rejected, or step x
		// finds nothing to put back.
		_, ferr := s.inflightFailed(ctx, in, err)
		return res, errors.Join(err, ferr)
	}
	if err := s.forwardSteps(ctx, in, StepRechecked); err != nil {
		return res, s.SwapBack(ctx, in.ID, err)
	}
	return res, s.appliedOffline(ctx, in.ID, src.HooksOwed)
}

// appliedOffline journals applied_offline (once) and deletes the in-flight
// file.
func (s *Swapper) appliedOffline(ctx context.Context, id string, hooksOwed bool) error {
	last, err := s.last(id)
	if err != nil {
		return err
	}
	if last != StepAppliedOffline {
		if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepAppliedOffline, HooksOwed: hooksOwed}); err != nil {
			return err
		}
	}
	return s.removeInflight(ctx, id)
}

// supersede rejects the pending intent itself (§5S.3.12 "In place"): the
// restore replaces everything it would have changed. A pending.json that
// is no usable intent is moved out of the slot as P0a does.
func (s *Swapper) supersede(ctx context.Context) (string, error) {
	p, err := readIntent(s.fs, s.pendingPath())
	if errors.Is(err, errIntentInvalid) {
		_, err := s.rejectInvalid(ctx, err)
		return "unusable intent", err
	}
	if err != nil {
		return "", err
	}
	recs, err := s.ensureStaged(ctx, p)
	if err != nil {
		return "", err
	}
	if _, err := s.rejectPending(ctx, p, supersededNote, lastStep(recs) == StepRejected, ""); err != nil {
		return "", err
	}
	return p.ID, nil
}

// escape is the one recipe admitted while inflight.json exists (§5S.3.12
// "In place"; machine 1 failed --> applied_offline (HooksOwed)): a
// KindCheckpoint journal at failed, staged and failed by another binary,
// and a KindCheckpoint source. Every step is idempotent and marked, so a
// rerun after a crash resumes it.
func (s *Swapper) escape(ctx context.Context, id string, kind restorejournal.Kind, last, dbPath string, src OfflineSource) (OfflineResult, error) {
	res := OfflineResult{ID: id, Escaped: true}
	var recs []restorejournal.Record
	if kind == restorejournal.KindCheckpoint && last == StepFailed {
		var err error
		if recs, err = s.durableJournal(ctx, id); err != nil {
			return res, err
		}
	}
	in, err := s.escapable(id, kind, last, dbPath, src.Kind, recs)
	if err != nil {
		return res, err
	}
	// The escape's own intent: the failed journal's set-aside list over
	// this source. Its live paths go to failed/ (the store that failed,
	// and the sidecars and state files that boot wrote), never previous/,
	// which keeps the original store from before the failed restore.
	esc := restorejournal.Intent{ID: id, Kind: restorejournal.KindCheckpoint, DBPath: in.DBPath,
		Targets: append([]restorejournal.Target{{From: src.Store, To: in.DBPath}}, src.StateFiles...)}
	if err := restorejournal.ValidateTargets(s.o.StateDir, esc, s.legacySuffixes()); err != nil {
		return res, err
	}
	asides := s.setAsides(esc)
	var moves []restorejournal.Target
	for _, m := range asides {
		moves = append(moves, restorejournal.Target{From: m.From, To: restorejournal.FailedDest(s.o.StateDir, in.DBPath, id, m.From)})
	}
	moved, err := exists(s.fs, s.markPath(id, escapeMoved))
	if err != nil {
		return res, err
	}
	if !moved {
		if ok, err := exists(s.fs, src.Store); err != nil {
			return res, err
		} else if !ok {
			return res, fmt.Errorf("restoreswap: the checkpoint store %s is gone", src.Store)
		}
		if err := restorejournal.Validate(s.o.Probe, append(moves, esc.Targets...)); err != nil {
			return res, err
		}
		if err := s.mark(ctx, id, escapeStarted); err != nil {
			return res, err
		}
		for _, m := range moves {
			if err := labelled(ctx, "swap.escape.mv", func() error {
				if err := s.mkdirDurable(filepath.Dir(m.To)); err != nil {
					return err
				}
				return mv(s.fs, m.From, m.To, memGhost)
			}); err != nil {
				return res, fmt.Errorf("restoreswap: escape of %s: move %s to %s: %w", id, m.From, m.To, err)
			}
		}
		if err := s.mark(ctx, id, escapeMoved); err != nil {
			return res, err
		}
	}
	// After escapeMoved a live path that exists is the source placed, by
	// this run or by an earlier one a kill interrupted. That run's staged
	// state files were other copies than this run's (each run copies them
	// from the checkpoint again), so mv would find two names it cannot prove
	// one file (ErrSwapAmbiguous) and the rerun could never finish the
	// escape (TestEscapeKillMatrix). A placed target is left as it is.
	for _, t := range esc.Targets {
		if placed, err := exists(s.fs, t.To); err != nil {
			return res, err
		} else if placed {
			continue
		}
		if err := labelled(ctx, "swap.escape.place", func() error { return mv(s.fs, t.From, t.To, nil) }); err != nil {
			return res, fmt.Errorf("restoreswap: escape of %s: place %s at %s: %w", id, t.From, t.To, err)
		}
	}
	if err := labelled(ctx, "swap.sidecars", func() error {
		return storage.RewriteSidecars(ctx, s.fs, in.DBPath, s.o.Owners)
	}); err != nil {
		return res, fmt.Errorf("restoreswap: escape of %s: %w", id, err)
	}
	// The source the failed boot took carries the failed store's owed hooks
	// (DATA-7): the escape always owes them.
	return res, s.appliedOffline(ctx, id, true)
}

// escapable is the escape's admission (§5S.3.12 "In place"): journal id,
// in flight, is a KindCheckpoint restore at failed of the store at dbPath,
// staged and failed by another binary (recs, the journal, read by the
// caller), and the source is a checkpoint. It returns the failed restore's
// intent; anything else refuses with restore_inflight, naming the swap.
func (s *Swapper) escapable(id string, kind restorejournal.Kind, last, dbPath string, src restorejournal.Kind,
	recs []restorejournal.Record) (restorejournal.Intent, error) {
	slot := restorejournal.SlotState{InFlight: []restorejournal.SlotEntry{{ID: id, Kind: kind}}}
	if kind != restorejournal.KindCheckpoint || last != StepFailed || src != restorejournal.KindCheckpoint || len(recs) == 0 {
		return restorejournal.Intent{}, fmt.Errorf("restoreswap: %w (journal at %q); only a failed checkpoint restore is escaped, by storage restore-offline --checkpoint", slot.Err(), last)
	}
	in, err := readIntent(s.fs, s.inflightPath(id))
	if err != nil {
		return in, err
	}
	if filepath.Clean(in.DBPath) != filepath.Clean(dbPath) {
		return in, fmt.Errorf("restoreswap: the failed restore %s swapped %s, not %s", id, in.DBPath, dbPath)
	}
	if note := recs[len(recs)-1].Note; note == s.o.BuildID {
		return in, fmt.Errorf("%w (journal %s): the escape is the previous binary's", ErrCheckpointRestoreFailedHere, id)
	}
	return in, nil
}

// escapeOf reports an escape whose marks exist in journal id's directory.
func (s *Swapper) escapeOf(id string) (bool, error) {
	return exists(s.fs, s.markPath(id, escapeStarted))
}

// labelled runs fn under the crash-matrix label name.
func labelled(ctx context.Context, name string, fn func() error) error {
	return bootseam.Labelled(bootseam.From(ctx), name, fn)
}
