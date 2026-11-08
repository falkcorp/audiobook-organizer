// file: core/internal/restoreswap/swapper.go
// version: 1.0.5
// guid: 1bb086f7-5f58-41ff-80f4-aecc32bca186
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// Errors of the swap. Each halts boot unless the kernel's table says
// otherwise (kernel/swap.go).
var (
	// ErrSwappedBack: a restore failed after its in-flight rename and step x
	// moved the original store back (05-backup §5B.3.4 step x). Boot exits
	// non-zero; the restart boots the original store, degraded
	// (restore_failed). It wraps the cause.
	ErrSwappedBack = errors.New("restoreswap: the restore failed; the original store was swapped back")
	// ErrCheckpointRestoreFailedHere: a KindCheckpoint journal at "failed"
	// names this binary: the binary whose migrations failed on the restored
	// checkpoint refuses to boot while the journal is in flight, and the
	// operator runbook replaces the binary (DATA7-12, DATA8-6).
	ErrCheckpointRestoreFailedHere = errors.New("restoreswap: this binary failed the checkpoint restore's migrations; install a fixed build or run the previous binary's storage restore-offline --checkpoint")
	// ErrRewriteNotInThisBuild: a KindRewrite intent or journal (M8, F3-04)
	// is staged or in flight and this build has no M8 to finish it.
	ErrRewriteNotInThisBuild = errors.New("restoreswap: a KindRewrite swap (M8) is staged or in flight; this build cannot finish it")
	// ErrSlotConflict: the slot's files name two swaps, which the one-slot
	// rule makes impossible; boot halts naming them.
	ErrSlotConflict = errors.New("restoreswap: the swap slot names two swaps")
)

// The degraded reasons P0a reports (05-health tokens).
const (
	DegradedRestoreRejected    = "restore_rejected"
	DegradedRestoreFailed      = "restore_failed"
	DegradedFactoryResetFailed = "factory_reset_failed"
)

// locationFile is <state_dir>/backup.location.json (05-backup §5B.3.6),
// saved into restore/<id>/ before the first live rename and restored by
// step x. R1 never writes it, so its absence is the R1 case.
const locationFile = "backup.location.json"

// Options configures a Swapper.
type Options struct {
	// FS is the filesystem of every swap operation; nil = vfs.Default.
	FS vfs.FS
	// StateDir is core.paths.state_dir, absolute (06 §6.4.5 StateDir).
	StateDir string
	// BuildID is this binary's ID (05-storage §5S.3.7 "Binary ID"), the
	// staged_by_build an intent must name.
	BuildID string
	// Clock is the expires_at check's clock; nil = time.Now.
	Clock func() time.Time
	// Owners are the build's storage owners: their LegacyGuard sidecars join
	// step c's list, and steps e and x rewrite the sidecars for them.
	Owners []storage.OwnerSpec
	// Recheck is step b (decision D4): nil checks that every placement's
	// source exists and re-runs Validate; F8 installs the manifest re-hash.
	Recheck func(ctx context.Context, in restorejournal.Intent) error
	// Probe is the rename pre-check's device probe; nil skips the device
	// and writability checks (an in-memory filesystem has neither).
	Probe restorejournal.Probe
	// Logger receives the swap's log lines; nil discards them.
	Logger *slog.Logger
	// Actor is every record's Actor; "" = "core/kernel".
	Actor string
}

// Swapper is the one journaled swap of 05-backup §5B.3.4 and 05-storage
// §5S.3.15: it stages intents into the one slot and applies them at boot
// phase P0a. The kernel holds it; storage.Admin has no staging or swap
// method (COR8-5).
type Swapper struct {
	o  Options
	fs vfs.FS
}

// New returns a Swapper over o.
func New(o Options) (*Swapper, error) {
	if o.StateDir == "" || !filepath.IsAbs(o.StateDir) {
		return nil, fmt.Errorf("restoreswap: state directory %q is not absolute", o.StateDir)
	}
	if o.BuildID == "" {
		return nil, errors.New("restoreswap: no binary ID")
	}
	if o.FS == nil {
		o.FS = vfs.Default
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Actor == "" {
		o.Actor = "core/kernel"
	}
	return &Swapper{o: o, fs: o.FS}, nil
}

func (s *Swapper) now() time.Time { return s.o.Clock() }

// BootKind is what P0a leaves the kernel's boot to be.
type BootKind int

const (
	// BootPlain: no swap is owed by this boot (none staged, one rejected,
	// or one completed or swapped back by P0a itself).
	BootPlain BootKind = iota
	// BootRestore: a restore journal at swapped, opened or failed (another
	// build's): storage opens at Intent.DBPath, the kernel journals opened,
	// then marker_written and applied after P4b, or steps x or failed on a
	// failure (05-backup §5B.3.4 steps g, h, x).
	BootRestore
	// BootReset: a KindReset journal at set_aside: the store path is empty,
	// P4a makes a fresh install, and the kernel journals applied after it
	// and, with no reset hook in R1, hook_done after P4b (05-storage
	// §5S.3.15; 06 §6.5.1 P4d step 1).
	BootReset
	// BootResetApplied: a KindReset journal at applied: the fresh install
	// is done and only hook_done is owed, after P4b.
	BootResetApplied
)

// Outcome is what P0a did and what it leaves the kernel.
type Outcome struct {
	Boot   BootKind
	ID     string
	Intent restorejournal.Intent // zero for BootPlain with no swap seen
	Last   string                // the journal's last step after P0a
	// Degraded is the 05-health token P0a sets ("" = none):
	// restore_rejected (step a), restore_failed (a step x P0a ran itself)
	// or factory_reset_failed (the reset failure rule); Reason says why.
	Degraded, Reason string
}

// legacySuffixes are the build's LegacyGuard sidecar suffixes.
func (s *Swapper) legacySuffixes() []string {
	var out []string
	for _, o := range s.o.Owners {
		if g := o.Spec.LegacyGuard; g != nil && g.SidecarSuffix != "" {
			out = append(out, g.SidecarSuffix)
		}
	}
	return out
}

// setAsides is step c's list for a restore intent.
func (s *Swapper) setAsides(in restorejournal.Intent) []restorejournal.Target {
	return restorejournal.SetAsides(s.o.StateDir, in, s.legacySuffixes())
}

func (s *Swapper) journalDir(id string) string { return restorejournal.JournalDir(s.o.StateDir, id) }
func (s *Swapper) inflightPath(id string) string {
	return filepath.Join(s.journalDir(id), restorejournal.InflightName)
}
func (s *Swapper) rejectedPath(id string) string {
	return filepath.Join(s.journalDir(id), restorejournal.RejectedName)
}
func (s *Swapper) pendingPath() string { return restorejournal.PendingPath(s.o.StateDir) }

// readIntent decodes an intent file (pending.json or inflight.json).
func readIntent(fs vfs.FS, path string) (restorejournal.Intent, error) {
	var in restorejournal.Intent
	f, err := fs.Open(path)
	if err != nil {
		return in, fmt.Errorf("restoreswap: open intent %s: %w", path, err)
	}
	b, err := io.ReadAll(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return in, fmt.Errorf("restoreswap: read intent %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, fmt.Errorf("%w: %s does not decode: %w", errIntentInvalid, path, err)
	}
	if !restorejournal.ValidID(in.ID) || !filepath.IsAbs(in.DBPath) {
		return in, fmt.Errorf("%w: %s has no usable id or db_path", errIntentInvalid, path)
	}
	return in, nil
}

// errIntentInvalid marks an intent file that was read and cannot be a swap:
// it does not decode, or its id or db_path is unusable. It is a verdict on
// the content; an error reading the file is not one.
var errIntentInvalid = errors.New("restoreswap: the intent is not a usable swap")

// InFlight reports the one swap whose in-flight file exists: the probe of
// 05-storage §5S.3.7's reader check and of the rollback guard. More than
// one is ErrSlotConflict.
func InFlight(fs vfs.FS, stateDir string) (id string, ok bool, err error) {
	ids, err := restorejournal.InFlightIDs(fs, stateDir)
	switch {
	case err != nil:
		return "", false, err
	case len(ids) > 1:
		return "", false, fmt.Errorf("%w: in flight: %v", ErrSlotConflict, ids)
	case len(ids) == 1:
		return ids[0], true, nil
	}
	return "", false, nil
}

// InFlightJournal is the in-flight swap's kind and last journal step: what
// the reader check's one admitted recipe reads (09 §9.2.1: the previous
// binary's storage restore-offline --checkpoint passes a KindCheckpoint
// journal at failed). ok is false when nothing is in flight.
func InFlightJournal(fs vfs.FS, stateDir string) (id string, kind restorejournal.Kind, last string, ok bool, err error) {
	if id, ok, err = InFlight(fs, stateDir); err != nil || !ok {
		return "", "", "", false, err
	}
	in, err := readIntent(fs, filepath.Join(restorejournal.JournalDir(stateDir, id), restorejournal.InflightName))
	if err != nil {
		return id, "", "", true, err
	}
	recs, err := ReadJournal(fs, stateDir, id)
	return id, in.Kind, lastStep(recs), true, err
}

// RunP0a is boot phase P0a (06 §6.5.1; 05-backup §5B.3.4): it resumes the
// swap whose in-flight file exists, else applies the staged intent (step a's
// rejection rule first), else returns BootPlain. A pending.json that is no
// usable intent is moved out of the slot (rejectInvalid) and the boot goes
// on plain, degraded restore_rejected; beside an in-flight file the swap
// resumes after it. Run once per boot, before storage opens; never from
// Describe. An error halts boot, an error reading pending.json among them;
// every step is idempotent, so the next boot resumes from the journal.
func (s *Swapper) RunP0a(ctx context.Context) (Outcome, error) {
	id, inflight, err := InFlight(s.fs, s.o.StateDir)
	if err != nil {
		return Outcome{}, err
	}
	pending, err := exists(s.fs, s.pendingPath())
	if err != nil {
		return Outcome{}, err
	}
	if inflight {
		if pending {
			// The slot artifact of a kill inside the in-flight rename: the
			// later file wins. Two different ids cannot both hold the slot.
			// A pending.json that is no usable intent cannot be this swap's
			// own slot file (the in-flight file is its copy): it is moved
			// aside and the swap resumes.
			p, err := readIntent(s.fs, s.pendingPath())
			switch {
			case errors.Is(err, errIntentInvalid):
				if _, err := s.rejectInvalid(ctx, err); err != nil {
					return Outcome{}, err
				}
			case err != nil:
				return Outcome{}, err
			case p.ID != id:
				return Outcome{}, fmt.Errorf("%w: pending %s, in flight %s", ErrSlotConflict, p.ID, id)
			default:
				if err := s.cleanupSlot(ctx, s.pendingPath()); err != nil {
					return Outcome{}, err
				}
			}
		}
		return s.resume(ctx, id)
	}
	if !pending {
		return Outcome{}, nil
	}
	in, err := readIntent(s.fs, s.pendingPath())
	if errors.Is(err, errIntentInvalid) {
		return s.rejectInvalid(ctx, err)
	}
	if err != nil {
		return Outcome{}, err
	}
	if in.Kind == restorejournal.KindRewrite {
		return Outcome{}, fmt.Errorf("%w (intent %s)", ErrRewriteNotInThisBuild, in.ID)
	}
	if ok, err := exists(s.fs, s.rejectedPath(in.ID)); err != nil {
		return Outcome{}, err
	} else if ok {
		// Rejected, and the kill fell inside the rejection's rename.
		if err := s.cleanupSlot(ctx, s.pendingPath()); err != nil {
			return Outcome{}, err
		}
		return Outcome{ID: in.ID, Intent: in, Last: StepRejected}, nil
	}
	recs, err := s.ensureStaged(ctx, in)
	if err != nil {
		return Outcome{}, err
	}
	if lastStep(recs) == StepRejected {
		return s.rejectPending(ctx, in, "", true, DegradedRestoreRejected)
	}
	if reason := s.rejection(in); reason != "" {
		return s.rejectPending(ctx, in, reason, false, DegradedRestoreRejected)
	}
	switch in.Kind {
	case restorejournal.KindArchive, restorejournal.KindRevert, restorejournal.KindCheckpoint:
		return s.startRestore(ctx, in, lastStep(recs))
	case restorejournal.KindReset:
		return s.startReset(ctx, in)
	}
	return s.rejectPending(ctx, in, fmt.Sprintf("unknown kind %q", in.Kind), false, DegradedRestoreRejected)
}

// rejection is step a's one rule for every kind (05-backup §5B.3.4 a;
// SEC7-5): an intent past its expires_at, with none, or staged by another
// binary is rejected, and so is one whose targets Stage would have refused
// (restorejournal.ValidateTargets, R1-21 review m4, m5: a stager that wrote
// the slot without Stage never moves restore/ or a swap's undo). It reads
// only pending.json: once the in-flight file exists no journal is rejected
// by it.
func (s *Swapper) rejection(in restorejournal.Intent) string {
	switch {
	case in.ExpiresAt.IsZero():
		return "the intent has no expires_at"
	case s.now().After(in.ExpiresAt):
		return fmt.Sprintf("the intent expired at %s", in.ExpiresAt.UTC().Format(time.RFC3339))
	case in.StagedByBuild != s.o.BuildID:
		return fmt.Sprintf("the intent was staged by binary %q, not this one", in.StagedByBuild)
	}
	if err := restorejournal.ValidateTargets(s.o.StateDir, in, s.legacySuffixes()); err != nil {
		return err.Error()
	}
	return ""
}

// ensureStaged returns the journal, writing "staged" first when a stager's
// create succeeded and its journal write did not (the intent is the truth).
func (s *Swapper) ensureStaged(ctx context.Context, in restorejournal.Intent) ([]restorejournal.Record, error) {
	recs, err := ReadJournal(s.fs, s.o.StateDir, in.ID)
	if err != nil || len(recs) > 0 {
		return recs, err
	}
	if err := mkdirAllSynced(s.fs, s.journalDir(in.ID)); err != nil {
		return nil, err
	}
	rec := restorejournal.Record{Step: StepStaged, Note: string(in.Kind), StepID: in.StepID}
	if err := s.appendRec(ctx, in.ID, rec); err != nil {
		return nil, err
	}
	return []restorejournal.Record{rec}, nil
}

// rejectPending is the rejection of an intent that never left the slot:
// journal rejected (unless a previous boot did), then pending.json to
// restore/<id>/rejected.json; the store is untouched and health reports
// degraded naming the kind: restore_rejected for steps a and b, the kind's
// failure token when the in-flight rename itself failed.
func (s *Swapper) rejectPending(ctx context.Context, in restorejournal.Intent, reason string, journaled bool, degraded string) (Outcome, error) {
	if !journaled {
		if err := s.appendRec(ctx, in.ID, restorejournal.Record{Step: StepRejected, Note: reason}); err != nil {
			return Outcome{}, err
		}
	}
	if err := bootseam.Labelled(bootseam.From(ctx), "swap.slot.rejected", func() error {
		return slotMove(s.fs, s.pendingPath(), s.rejectedPath(in.ID))
	}); err != nil {
		return Outcome{}, err
	}
	s.o.Logger.Warn("restoreswap: staged swap rejected; booting the untouched store",
		"id", in.ID, "kind", string(in.Kind), "reason", reason)
	return Outcome{ID: in.ID, Intent: in, Last: StepRejected,
		Degraded: degraded, Reason: fmt.Sprintf("%s intent %s: %s", in.Kind, in.ID, reason)}, nil
}

// rejectInvalid moves a pending.json that is no usable intent
// (errIntentInvalid, cause) out of the slot before anything moved (R1-21
// review m3): to restore/rejected-<UTC time>[-n].json, a name no earlier
// rejection holds, since it has no id to journal under and the file is its
// own record. It logs at ERROR and the boot goes on plain, degraded
// restore_rejected. A kill before the directory sync leaves pending.json in
// the slot, and the next boot rejects it again.
func (s *Swapper) rejectInvalid(ctx context.Context, cause error) (Outcome, error) {
	var dst string
	if err := bootseam.Labelled(bootseam.From(ctx), "swap.slot.invalid", func() error {
		var err error
		if dst, err = freeName(s.fs, restorejournal.Dir(s.o.StateDir), "rejected-"+s.now().UTC().Format("20060102T150405Z")); err != nil {
			return err
		}
		if err := s.fs.Rename(s.pendingPath(), dst); err != nil {
			return fmt.Errorf("restoreswap: move the unusable intent to %s: %w", dst, err)
		}
		return syncDir(s.fs, filepath.Dir(dst))
	}); err != nil {
		return Outcome{}, err
	}
	s.o.Logger.Error("restoreswap: the staged intent is not a usable swap; moved out of the slot, booting the untouched store",
		"moved_to", dst, "err", cause)
	return Outcome{Boot: BootPlain, Degraded: DegradedRestoreRejected,
		Reason: fmt.Sprintf("%v (moved to %s)", cause, filepath.Base(dst))}, nil
}

// freeName is dir/<base>.json, or dir/<base>-<n>.json for the first n that
// no file holds.
func freeName(fs vfs.FS, dir, base string) (string, error) {
	for n := 0; n < 1000; n++ {
		name := base + ".json"
		if n > 0 {
			name = fmt.Sprintf("%s-%d.json", base, n)
		}
		p := filepath.Join(dir, name)
		if ok, err := exists(fs, p); err != nil {
			return "", err
		} else if !ok {
			return p, nil
		}
	}
	return "", fmt.Errorf("restoreswap: no free name for %s in %s", base, dir)
}

// cleanupSlot removes a slot file a later file superseded.
func (s *Swapper) cleanupSlot(ctx context.Context, path string) error {
	return bootseam.Labelled(bootseam.From(ctx), "swap.slot.cleanup", func() error {
		return removeSynced(s.fs, path)
	})
}

// removeInflight deletes the in-flight file at a terminal step.
func (s *Swapper) removeInflight(ctx context.Context, id string) error {
	return bootseam.Labelled(bootseam.From(ctx), "swap.slot.remove", func() error {
		return removeSynced(s.fs, s.inflightPath(id))
	})
}

// resume continues the journal whose in-flight file exists. The acceptance
// rule: such a journal is never rejected by step a.
func (s *Swapper) resume(ctx context.Context, id string) (Outcome, error) {
	in, err := readIntent(s.fs, s.inflightPath(id))
	if err != nil {
		return Outcome{}, err
	}
	if in.ID != id {
		return Outcome{}, fmt.Errorf("%w: restore/%s/inflight.json holds intent %s", ErrSlotConflict, id, in.ID)
	}
	if in.Kind == restorejournal.KindRewrite {
		return Outcome{}, fmt.Errorf("%w (journal %s)", ErrRewriteNotInThisBuild, id)
	}
	recs, err := ReadJournal(s.fs, s.o.StateDir, id)
	if err != nil {
		return Outcome{}, err
	}
	if in.Kind == restorejournal.KindReset {
		return s.resumeReset(ctx, in, recs)
	}
	return s.resumeRestore(ctx, in, recs)
}

// Journal is restore/<id>/journal.json's records.
func (s *Swapper) Journal(id string) ([]restorejournal.Record, error) {
	return ReadJournal(s.fs, s.o.StateDir, id)
}

// last reads id's journal's last step.
func (s *Swapper) last(id string) (string, error) {
	recs, err := s.Journal(id)
	return lastStep(recs), err
}

// Opened is the kernel's step after P2 on a restore boot: journal opened
// (05-backup step g), unless the journal already reads it (a crash loop).
func (s *Swapper) Opened(ctx context.Context, id string) error {
	last, err := s.last(id)
	if err != nil || last == StepOpened {
		return err
	}
	return s.appendRec(ctx, id, restorejournal.Record{Step: StepOpened})
}

// RestoreApplied completes a restore boot of a build without P4d steps 2-3
// (R1): marker_written with HooksOwed (machine 1's one edge opened →
// marker_written), then applied, then the in-flight file deleted (05-backup
// step h; TestRestoreBootWithoutHooksReachesApplied).
func (s *Swapper) RestoreApplied(ctx context.Context, id string) error {
	last, err := s.last(id)
	if err != nil {
		return err
	}
	if last != StepMarkerWritten && last != StepApplied {
		if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepMarkerWritten, HooksOwed: true}); err != nil {
			return err
		}
		last = StepMarkerWritten
	}
	if last == StepMarkerWritten {
		if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepApplied}); err != nil {
			return err
		}
	}
	return s.removeInflight(ctx, id)
}

// CheckpointFailed is the KindCheckpoint exception of step x (DATA7-12,
// DATA8-6): the migration DAG failed on the restored checkpoint, so the
// journal records failed with this binary's ID and keeps its in-flight file.
func (s *Swapper) CheckpointFailed(ctx context.Context, id string) error {
	return s.appendRec(ctx, id, restorejournal.Record{Step: StepFailed, Note: s.o.BuildID})
}

// ResetApplied journals a reset boot's applied once the fresh install is
// done (05-storage §5S.3.15 "Journal and keyring"; in R1, with no keyring,
// at the same point).
func (s *Swapper) ResetApplied(ctx context.Context, id string) error {
	last, err := s.last(id)
	if err != nil || last != StepSetAside {
		return err
	}
	return s.appendRec(ctx, id, restorejournal.Record{Step: StepApplied})
}

// ResetDone is the kernel's completion of a reset with no reset hook (R1,
// and every ResetStore), owed only after applied: each intent Delete entry
// removed with the hook's per-entry rules (05-storage §5S.3.15 "Completion:
// hook_done", "The app's reset hook"), then hook_done and the in-flight file
// deleted. An entry whose removal would remove a kept path (rule (4),
// restorejournal.Kept: the state directory, <db parent>, restore/, a
// set-aside, the fresh store) is skipped and recorded as kept, never
// removed; a relative entry is refused and a removal that fails is
// recorded as failed. Neither fails the boot, and removing what is gone is
// success. A journal at hook_done (a kill before the in-flight file was
// deleted) only deletes it; any other step refuses, so nothing is removed
// before the fresh install is journaled.
func (s *Swapper) ResetDone(ctx context.Context, id string) error {
	in, err := readIntent(s.fs, s.inflightPath(id))
	if err != nil {
		return err
	}
	last, err := s.last(id)
	if err != nil {
		return err
	}
	switch last {
	case StepApplied:
		var failed, kept []string
		for _, p := range in.Delete {
			switch {
			case !filepath.IsAbs(p):
				s.o.Logger.Error("restoreswap: reset delete entry is not absolute; not removed", "id", id, "path", p)
				failed = append(failed, p)
				continue
			case restorejournal.Kept(s.o.StateDir, in, s.legacySuffixes(), p):
				s.o.Logger.Warn("restoreswap: reset delete entry is or holds a kept path; skipped", "id", id, "path", p)
				kept = append(kept, p)
				continue
			}
			err := bootseam.Labelled(bootseam.From(ctx), "swap.delete.remove", func() error {
				if ok, err := exists(s.fs, p); err != nil || !ok {
					return err // removing what is gone is success
				}
				if err := s.fs.RemoveAll(p); err != nil {
					return err
				}
				return syncDir(s.fs, filepath.Dir(p))
			})
			if err != nil {
				s.o.Logger.Error("restoreswap: reset could not remove an entry", "id", id, "path", p, "err", err)
				failed = append(failed, p)
			}
		}
		if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepHookDone, Failed: failed, Kept: kept}); err != nil {
			return err
		}
	case StepHookDone:
	default:
		return fmt.Errorf("restoreswap: reset %s owes hook_done only after applied; its journal reads %q", id, last)
	}
	return s.removeInflight(ctx, id)
}
