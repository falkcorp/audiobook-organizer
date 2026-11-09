// file: core/storage/checkpoint_sweep.go
// version: 1.0.1
// guid: 5b0f3c7e-6a1d-4f0e-9a52-2d8f61c4b7a3
// last-edited: 2026-11-08

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The checkpoint sweeper and storage prune-checkpoints (05-storage §5S.3.12
// "Retention", "Scratch copies" and "Modes"; §5S.4 step 15).
//
// Every checkpoint holds the whole store, plaintext third-party secrets and
// raw session tokens among it, so each is deleted 7 days after it stops
// being needed, and the period is fixed: checkpointRetention is a constant
// and no option of this file lengthens it. A deletion is always the record
// first, then the copy, then the .migration-checkpoint pointer when it names
// that checkpoint, so a crash in the middle leaves a directory without a
// record (garbage the next sweep removes), never a record naming a partial
// copy.

// The sweeper's rows of 06 §6.5.1's writer table (§5S.4 step 15), as the
// crash-matrix kit's labels: stamping delete_after, deleting an expired
// checkpoint, removing garbage (record-less directories, dangling records
// and a dangling pointer) and deleting an expired scratch entry.
const (
	labelSweepRelease = "storage.sweeper.release"
	labelSweepDelete  = "storage.sweeper.delete"
	labelSweepGarbage = "storage.sweeper.garbage"
	labelSweepScratch = "storage.sweeper.scratch"
)

// SweepLabels are the crash-matrix labels of the checkpoint sweeper's
// durable operations, for the kit's writer table (coretest/crashmatrix).
func SweepLabels() []string {
	return []string{labelSweepRelease, labelSweepDelete, labelSweepGarbage, labelSweepScratch}
}

// checkpointScratch is the scratch area's name under the checkpoint
// directory (§5S.3.12 "Scratch copies").
const checkpointScratch = "scratch"

// sweepLockName is the lock file in the checkpoint directory that the
// sweeper and storage prune-checkpoints hold while they change records.
// Prune runs while the service serves (SEC10-9), so without it the hourly
// sweep could rewrite a record (its delete_after) that a prune has just
// deleted, and leave a record naming a copy the prune is removing. It is
// not in the checkpoint grammar, so neither the garbage sweep nor a reuse
// ever reads it.
const sweepLockName = ".sweep.lock"

// pruneLockWait is how long storage prune-checkpoints waits for a sweep in
// progress before it gives up.
const pruneLockWait = 30 * time.Second

// maxLooseReported caps the paths a sweep names in storage_checkpoint_mode:
// a copy loosened by hand can hold thousands of files, and the count says
// the rest.
const maxLooseReported = 20

// ErrCheckpointSweepBusy: another sweep or prune holds the checkpoint
// directory's lock. The sweeper tries again at its next period; prune
// reports it after waiting pruneLockWait.
var ErrCheckpointSweepBusy = errors.New("storage: another checkpoint sweep or prune is running")

// ErrPruneSwapInFlight: storage prune-checkpoints refuses while an offline
// restore or any other swap is staged or in flight (restore/pending.json
// or a restore/<id>/inflight.json exists), because that swap may be about
// to read the checkpoint it would delete (§5S.3.12 "Retention").
var ErrPruneSwapInFlight = errors.New("storage: a restore is staged or in flight; boot the current build once to finish it, then prune")

// CheckpointInfo is one checkpoint as health reports it (§5S.3.12
// "Retention"; 05-health §5H.3.1): storage_checkpoint_retained with its
// DeleteAfter while it exists, and storage_checkpoint_open while Rollback.
type CheckpointInfo struct {
	Name    string // <YYYYMMDD>T<HHMMSS>Z-<8 hex>
	Dir     string // the directory holding it: the checkpoint directory, or a restore's failed/
	Status  string // open, served, completed, released, restore_left, consumed
	TakenAt time.Time
	// DeleteAfter is when the sweeper deletes it; nil while it is kept
	// because it is still the live store's rollback (Rollback), or until the
	// next sweep stamps one.
	DeleteAfter *time.Time
	// Rollback: an open or served checkpoint of the live store that has
	// neither completed a later migration nor been restored since. It is
	// kept, and health raises storage_checkpoint_open with its age.
	Rollback bool
}

// ScratchInfo is one entry of the scratch area (a dry run's directory, an
// --into copy or its .state/ directory): storage_checkpoint_open with its
// age, deleted by the sweeper 7 days after ModTime.
type ScratchInfo struct {
	Path    string
	ModTime time.Time
}

// CheckpointHealth is what health reports about checkpoints, on
// authenticated routes only (05-health §5H.3.1): every checkpoint, every
// scratch entry, and the paths the last sweep found group- or
// world-accessible (storage_checkpoint_mode, SEC-9). Loose is at most
// maxLooseReported paths; LooseTotal counts them all.
type CheckpointHealth struct {
	Checkpoints []CheckpointInfo
	Scratch     []ScratchInfo
	Loose       []string
	LooseTotal  int
}

// SweepReport is one sweep's outcome: what it released (stamped
// delete_after on), deleted and removed, and the health it leaves.
type SweepReport struct {
	CheckpointHealth
	Released       []string // checkpoint names stamped delete_after = now + 7 days
	Deleted        []string // checkpoint names deleted (record, copy, pointer)
	Garbage        []string // record-less directories, dangling records and a dangling pointer removed
	ScratchDeleted []string // scratch entries deleted 7 days after their mtime
}

// cpArea is the files one sweep or prune works on: the checkpoint directory
// of a store, its scratch area and the failed/ directories of the restores
// beside it. The DB's sweep builds one from its boot options; prune, which
// opens no store, from PruneOptions.
type cpArea struct {
	fs       vfs.FS
	dbPath   string
	cpDir    string
	stateDir string // "" = no state directory: the restore/ tree is not read
	log      *slog.Logger
	now      time.Time
	chmod    func(path string, dir bool) error
	walk     func(root string, recursive bool, visit func(path string, mode iofs.FileMode)) error
	seams    *bootseam.Seams
}

// pointerPath is <db path>.migration-checkpoint.
func (a *cpArea) pointerPath() string { return a.dbPath + ipebble.CheckpointSuffix }

func (a *cpArea) labelled(label string, fn func() error) error {
	return bootseam.Labelled(a.seams, label, fn)
}

// readRecords reads every <name>.json in dir. A record that cannot be read
// fails the call; one removed since the listing is skipped, and one that
// reads but does not parse is logged and skipped (it is never reused,
// completed, released or deleted).
func readRecords(fs vfs.FS, dir string, log *slog.Logger) ([]namedRecord, error) {
	names, err := fs.List(dir)
	if err != nil {
		if oserrorNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: list checkpoints: %w", err)
	}
	slices.Sort(names)
	var out []namedRecord
	for _, n := range names {
		base, ok := strings.CutSuffix(n, ".json")
		if !ok || !isCheckpointName(base) {
			continue
		}
		data, ok, err := ipebble.ReadFile(fs, filepath.Join(dir, n))
		if err != nil {
			// An EIO or a permission error: skipping the record would take
			// a second full checkpoint beside it, or orphan it under a new
			// install ID, so the caller refuses (review MINOR-1).
			return nil, fmt.Errorf("storage: checkpoint record %s: %w", n, err)
		}
		if !ok {
			continue // removed since the listing
		}
		var rec checkpointRecord
		if json.Unmarshal(data, &rec) != nil {
			// Records are written atomically, so this is a hand edit or
			// corruption.
			log.Error("storage: checkpoint record does not parse; never reused or deleted", "name", base)
			continue
		}
		out = append(out, namedRecord{name: base, rec: rec})
	}
	return out, nil
}

// garbage removes, in dir, the checkpoint directories without a record and
// the <name>.tmp directories (a crash before the record, or a deletion cut
// between the record and the copy), and drops the records whose directory
// is gone (§5S.3.12 "How"). It touches only names in the checkpoint
// grammar, so the scratch area, the lock file and anything else stay. It
// returns what it removed.
func (a *cpArea) garbage(dir, label string) ([]string, error) {
	names, err := a.fs.List(dir)
	if err != nil {
		if oserrorNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: list checkpoints: %w", err)
	}
	has := map[string]bool{}
	for _, n := range names {
		has[n] = true
	}
	var removed []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		base, isRecord := strings.CutSuffix(n, ".json")
		base, isTmp := strings.CutSuffix(base, ".tmp")
		if !isCheckpointName(base) || (isRecord && isTmp) {
			// The scratch area, the lock, or a name takeCheckpoint never
			// writes: not the sweep's to delete, whatever it holds.
			continue
		}
		switch {
		case isRecord:
			if !has[base] {
				a.log.Warn("storage: checkpoint record without its directory dropped", "name", n, "dir", dir)
				if err := a.labelled(label, func() error { return a.fs.Remove(p) }); err != nil && !oserrorNotExist(err) {
					return removed, fmt.Errorf("storage: drop record %s: %w", n, err)
				}
				removed = append(removed, p)
			}
		case isTmp || !has[base+".json"]:
			if fi, err := a.fs.Stat(p); err != nil || !fi.IsDir() {
				continue
			}
			a.log.Warn("storage: partial checkpoint swept", "name", n, "dir", dir)
			if err := a.labelled(label, func() error { return a.fs.RemoveAll(p) }); err != nil {
				return removed, fmt.Errorf("storage: sweep %s: %w", n, err)
			}
			removed = append(removed, p)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	return removed, a.labelled(label, func() error { return ipebble.SyncDir(a.fs, dir) })
}

// deleteCheckpoint deletes one checkpoint in dir: the record first (and the
// directory entry's removal made durable), then the copy, then the pointer
// when it names this checkpoint and no record of that name is left in the
// checkpoint directory. A kill after the record leaves garbage for the next
// sweep; a kill after the copy leaves a dangling pointer, which the next
// sweep removes too (sweepPointer).
func (a *cpArea) deleteCheckpoint(dir string, r namedRecord, label string) error {
	return a.labelled(label, func() error {
		rec := filepath.Join(dir, r.name+".json")
		if err := a.fs.Remove(rec); err != nil && !oserrorNotExist(err) {
			return fmt.Errorf("storage: delete checkpoint record %s: %w", r.name, err)
		}
		if err := ipebble.SyncDir(a.fs, dir); err != nil {
			return fmt.Errorf("storage: delete checkpoint record %s: %w", r.name, err)
		}
		if err := a.fs.RemoveAll(filepath.Join(dir, r.name)); err != nil {
			return fmt.Errorf("storage: delete checkpoint %s: %w", r.name, err)
		}
		if err := ipebble.SyncDir(a.fs, dir); err != nil {
			return fmt.Errorf("storage: delete checkpoint %s: %w", r.name, err)
		}
		p, ok, err := readPointerFile(a.fs, a.pointerPath())
		if err != nil || !ok || p.Name != r.name || a.recordExists(r.name) {
			return err
		}
		if err := a.fs.Remove(a.pointerPath()); err != nil && !oserrorNotExist(err) {
			return fmt.Errorf("storage: delete %s: %w", ipebble.CheckpointSuffix, err)
		}
		return ipebble.SyncDir(a.fs, filepath.Dir(a.dbPath))
	})
}

// recordExists reports a record of that name in the checkpoint directory.
func (a *cpArea) recordExists(name string) bool {
	_, err := a.fs.Stat(filepath.Join(a.cpDir, name+".json"))
	return err == nil
}

// sweepPointer removes a .migration-checkpoint that names a checkpoint with
// neither a record nor a directory in the checkpoint directory: a deletion
// killed after the copy, or a prune that raced a boot's completion. A
// pointer naming no checkpoint would send make rollback-restore after a
// copy that is gone.
func (a *cpArea) sweepPointer() (bool, error) {
	p, ok, err := readPointerFile(a.fs, a.pointerPath())
	if err != nil || !ok {
		// An unreadable pointer is left for the operator: the sweep never
		// deletes a file it cannot read.
		return false, err
	}
	if _, err := a.fs.Stat(filepath.Join(a.cpDir, p.Name)); err == nil || a.recordExists(p.Name) {
		return false, nil
	}
	a.log.Warn("storage: checkpoint pointer naming no checkpoint removed", "name", p.Name)
	if err := a.labelled(labelSweepGarbage, func() error {
		if err := a.fs.Remove(a.pointerPath()); err != nil && !oserrorNotExist(err) {
			return err
		}
		return ipebble.SyncDir(a.fs, filepath.Dir(a.dbPath))
	}); err != nil {
		return false, fmt.Errorf("storage: remove %s: %w", ipebble.CheckpointSuffix, err)
	}
	return true, nil
}

// readPointerFile reads <db path>.migration-checkpoint; ok is false when it is
// absent.
func readPointerFile(fs vfs.FS, path string) (p checkpointPointer, ok bool, err error) {
	data, ok, err := ipebble.ReadFile(fs, path)
	if err != nil || !ok {
		return p, false, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, false, fmt.Errorf("storage: %s unreadable: %w", path, err)
	}
	return p, true, nil
}

// writeRecordAt writes dir/<name>.json atomically, mode 0600.
func (a *cpArea) writeRecordAt(dir, name string, rec checkpointRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, name+".json")
	if err := ipebble.WriteFileAtomic(a.fs, p, append(data, '\n')); err != nil {
		return fmt.Errorf("storage: checkpoint record: %w", err)
	}
	return a.chmod(p, false)
}

// failedDirs are the existing directories a failed restore's step x sends a
// checkpoint to, for every restore id under the state directory's restore/
// and under each .mediacore-restore/ a swap of this store can create:
// restorejournal.FailedDest of the checkpoint directory and of a checkpoint
// inside it (R1-21 decision D3 put DB-side items under
// <db parent>/.mediacore-restore/<id>/failed/, beside the store), and
// restore/<id>/failed/ itself with and without .mediacore-checkpoints/
// (05-backup §5B.3.4 step x's text). No build moves a checkpoint there yet;
// the sweep reads every place one can land. Sorted.
func (a *cpArea) failedDirs() ([]string, error) {
	roots := []string{
		filepath.Join(filepath.Dir(a.dbPath), restorejournal.AsideDirName),
		filepath.Join(filepath.Dir(a.cpDir), restorejournal.AsideDirName),
		filepath.Join(a.cpDir, restorejournal.AsideDirName),
	}
	if a.stateDir != "" {
		roots = append(roots, restorejournal.Dir(a.stateDir))
	}
	ids := map[string]bool{}
	for _, root := range roots {
		names, err := a.fs.List(root)
		if err != nil {
			if oserrorNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			return nil, fmt.Errorf("storage: list %s: %w", root, err)
		}
		for _, n := range names {
			if !restorejournal.ValidID(n) || strings.HasPrefix(n, ".") {
				continue
			}
			if fi, err := a.fs.Stat(filepath.Join(root, n)); err == nil && fi.IsDir() {
				ids[n] = true
			}
		}
	}
	var out []string
	for id := range ids {
		cands := []string{
			restorejournal.FailedDest(a.stateDir, a.dbPath, id, a.cpDir),
			filepath.Dir(restorejournal.FailedDest(a.stateDir, a.dbPath, id, filepath.Join(a.cpDir, "x"))),
		}
		if a.stateDir != "" {
			// 05-backup step x's own words, restore/<id>/failed/, with or
			// without the checkpoint directory's name kept.
			f := filepath.Join(restorejournal.JournalDir(a.stateDir, id), "failed")
			cands = append(cands, f, filepath.Join(f, checkpointLeaf))
		}
		for _, d := range cands {
			if fi, err := a.fs.Stat(d); err == nil && fi.IsDir() && !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// lock takes the checkpoint directory's sweep lock. ok is false when the
// directory does not exist (nothing to sweep or prune). A lock another
// process or another sweep in this one holds is ErrCheckpointSweepBusy.
//
// The lock is the OS's (vfs.Default's fcntl lock, which also excludes a
// second holder in this process), so a non-OS filesystem takes none, as the
// chmod and directory-identity helpers do nothing there: its files are one
// process's memory, which no prune-checkpoints process can reach, and
// MemFS.Lock re-takes MemFS's clone lock inside Create, so it deadlocks
// against a CrashClone another goroutine starts between the two (the
// crash-matrix kit clones from Pebble's own goroutines).
func (a *cpArea) lock() (unlock func(), ok bool, err error) {
	if fi, err := a.fs.Stat(a.cpDir); err != nil || !fi.IsDir() {
		if err != nil && !oserrorNotExist(err) {
			return nil, false, fmt.Errorf("storage: checkpoint directory: %w", err)
		}
		return func() {}, false, nil
	}
	if vfs.Root(a.fs) != vfs.Default {
		return func() {}, true, nil
	}
	p := filepath.Join(a.cpDir, sweepLockName)
	l, err := a.fs.Lock(p)
	if err != nil {
		return nil, false, fmt.Errorf("%w (%s: %v)", ErrCheckpointSweepBusy, p, err)
	}
	// The lock file holds nothing, but it sits in a 0700 directory of
	// secrets; the sweeper sets the mode it creates it with.
	if err := a.chmod(p, false); err != nil {
		_ = l.Close()
		return nil, false, err
	}
	return func() { _ = l.Close() }, true, nil
}

// liveStore is the identity a sweep compares records with: the live store's
// install ID and generation.
type liveStore struct {
	installID string
	epoch     epochRecord
}

// rollback reports whether r is still the live store's rollback: open or
// served, of the live store (same install ID, epoch and directory
// identity), with no later record of the same store that completed
// (§5S.3.12 "Retention", the open row).
func (l liveStore) rollback(r namedRecord, all []namedRecord) bool {
	if !r.rec.pending() || r.rec.InstallID != l.installID || !r.rec.ofStore(l.epoch) {
		return false
	}
	for _, o := range all {
		if o.name != r.name && o.rec.CompletedAt != nil && o.rec.InstallID == r.rec.InstallID &&
			o.rec.Epoch == r.rec.Epoch && o.rec.DirFSID == r.rec.DirFSID && o.rec.DirIno == r.rec.DirIno &&
			o.rec.TakenAt.After(r.rec.TakenAt) {
			return false
		}
	}
	return true
}

// deleteAfter is the delete_after a record without one gets now, or nil
// when it gets none yet (§5S.3.12 "Retention"): completed, completed_at + 7
// days (completeRecord writes both, so this repairs a hand edit); in a
// failed/ directory, left by a failed restore (restore_left) or no longer
// the rollback, now + 7 days; the live store's rollback, none.
func (l liveStore) deleteAfter(r namedRecord, all []namedRecord, failed bool, now time.Time) *time.Time {
	switch {
	case r.rec.DeleteAfter != nil:
		return nil
	case r.rec.CompletedAt != nil:
		t := r.rec.CompletedAt.Add(checkpointRetention)
		return &t
	case failed || r.rec.Status == "restore_left" || !l.rollback(r, all):
		t := now.Add(checkpointRetention)
		return &t
	}
	return nil
}

// SweepCheckpoints is the checkpoint sweeper's one pass (§5S.3.12
// "Retention"; §5S.4 step 15), which the kernel runs after readiness and
// then hourly. In the checkpoint directory and every restore's failed/
// directory (stateDir names the state directory whose restore/ it reads;
// "" reads none) it removes garbage, writes the delete_after of every
// record that has stopped being needed, and deletes every checkpoint whose
// delete_after has passed, record first. It drops a pointer naming no
// checkpoint, deletes scratch entries 7 days after their mtime, and checks
// every checkpoint directory and file for a group- or world-accessible mode,
// which it reports and never changes. ctx is checked between checkpoints:
// a deletion, once begun, finishes. A DB whose Migrate did not finish is
// refused.
func (db *DB) SweepCheckpoints(ctx context.Context, stateDir string) (SweepReport, error) {
	st := db.boot
	if st == nil || !st.finished {
		return SweepReport{}, errors.New("storage: SweepCheckpoints on a store whose boot did not finish Migrate")
	}
	a := db.area(stateDir)
	live := liveStore{installID: st.installID, epoch: st.epoch}
	var rep SweepReport
	unlock, ok, err := a.lock()
	if err != nil {
		return rep, err
	}
	defer unlock()
	if ok {
		if err := a.sweepDir(ctx, a.cpDir, false, live, &rep); err != nil {
			return rep, err
		}
	}
	failed, err := a.failedDirs()
	if err != nil {
		return rep, err
	}
	for _, d := range failed {
		if err := a.sweepDir(ctx, d, true, live, &rep); err != nil {
			return rep, err
		}
	}
	if removed, err := a.sweepPointer(); err != nil {
		return rep, err
	} else if removed {
		rep.Garbage = append(rep.Garbage, a.pointerPath())
	}
	if ok {
		if rep.ScratchDeleted, err = a.sweepScratch(ctx); err != nil {
			return rep, err
		}
	}
	rep.Loose, rep.LooseTotal, err = a.loose(failed)
	if err != nil {
		return rep, err
	}
	if rep.Checkpoints, rep.Scratch, err = a.health(live, failed); err != nil {
		return rep, err
	}
	return rep, nil
}

// sweepDir is one directory's pass: garbage, then each record's
// delete_after, then each expired checkpoint's deletion.
func (a *cpArea) sweepDir(ctx context.Context, dir string, failed bool, live liveStore, rep *SweepReport) error {
	removed, err := a.garbage(dir, labelSweepGarbage)
	rep.Garbage = append(rep.Garbage, removed...)
	if err != nil {
		return err
	}
	recs, err := readRecords(a.fs, dir, a.log)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if err := ctx.Err(); err != nil {
			return err
		}
		del := live.deleteAfter(r, recs, failed, a.now)
		if del == nil {
			continue
		}
		r.rec.DeleteAfter = del
		if r.rec.CompletedAt == nil && r.rec.Status != "restore_left" {
			r.rec.Status = "released"
		}
		if err := a.labelled(labelSweepRelease, func() error { return a.writeRecordAt(dir, r.name, r.rec) }); err != nil {
			return err
		}
		a.log.Info("storage: checkpoint released; it is deleted 7 days after it stopped being needed",
			"name", r.name, "dir", dir, "delete_after", del.Format(time.RFC3339))
		rep.Released = append(rep.Released, r.name)
	}
	if recs, err = readRecords(a.fs, dir, a.log); err != nil {
		return err
	}
	for _, r := range recs {
		if r.rec.DeleteAfter == nil || a.now.Before(*r.rec.DeleteAfter) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.deleteCheckpoint(dir, r, labelSweepDelete); err != nil {
			return err
		}
		a.log.Info("storage: checkpoint deleted", "name", r.name, "dir", dir,
			"delete_after", r.rec.DeleteAfter.Format(time.RFC3339), "status", r.rec.Status)
		rep.Deleted = append(rep.Deleted, r.name)
	}
	return nil
}

// sweepScratch deletes every scratch entry whose mtime is more than 7 days
// old (§5S.3.12 "Scratch copies"). The scratch area is not a checkpoint:
// the garbage rule does not apply to it.
func (a *cpArea) sweepScratch(ctx context.Context) ([]string, error) {
	dir := filepath.Join(a.cpDir, checkpointScratch)
	entries, err := a.scratch()
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	var deleted []string
	for _, e := range entries {
		if a.now.Before(e.ModTime.Add(checkpointRetention)) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if err := a.labelled(labelSweepScratch, func() error { return a.fs.RemoveAll(e.Path) }); err != nil {
			return deleted, fmt.Errorf("storage: delete scratch entry %s: %w", e.Path, err)
		}
		a.log.Info("storage: scratch copy deleted 7 days after its last change", "path", e.Path,
			"mtime", e.ModTime.UTC().Format(time.RFC3339))
		deleted = append(deleted, e.Path)
	}
	if len(deleted) == 0 {
		return nil, nil
	}
	return deleted, a.labelled(labelSweepScratch, func() error { return ipebble.SyncDir(a.fs, dir) })
}

// scratch lists the scratch area's entries; none when it does not exist.
func (a *cpArea) scratch() ([]ScratchInfo, error) {
	dir := filepath.Join(a.cpDir, checkpointScratch)
	names, err := a.fs.List(dir)
	if err != nil {
		if oserrorNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: list the scratch area: %w", err)
	}
	slices.Sort(names)
	var out []ScratchInfo
	for _, n := range names {
		p := filepath.Join(dir, n)
		fi, err := a.fs.Stat(p)
		if err != nil {
			if oserrorNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("storage: scratch entry %s: %w", p, err)
		}
		out = append(out, ScratchInfo{Path: p, ModTime: fi.ModTime()})
	}
	return out, nil
}

// health lists every checkpoint of the checkpoint directory and of failed,
// and every scratch entry. It reads; it writes nothing.
func (a *cpArea) health(live liveStore, failed []string) ([]CheckpointInfo, []ScratchInfo, error) {
	var cps []CheckpointInfo
	for _, dir := range append([]string{a.cpDir}, failed...) {
		recs, err := readRecords(a.fs, dir, a.log)
		if err != nil {
			return nil, nil, err
		}
		isFailed := dir != a.cpDir
		for _, r := range recs {
			cps = append(cps, CheckpointInfo{
				Name: r.name, Dir: dir, Status: r.rec.Status, TakenAt: r.rec.TakenAt,
				DeleteAfter: r.rec.DeleteAfter, Rollback: !isFailed && live.rollback(r, recs),
			})
		}
	}
	scratch, err := a.scratch()
	if err != nil {
		return nil, nil, err
	}
	return cps, scratch, nil
}

// loose walks every checkpoint directory and file (the checkpoint directory
// itself, each checkpoint and record in it, the scratch area, and each
// failed/ directory) and returns the paths that are group- or
// world-accessible, the first maxLooseReported of them, and their count
// (§5S.3.12 "Modes", SEC-9). It reads modes; it never changes one it did
// not set. It is a no-op where modes do not apply (an in-memory filesystem,
// a platform without unix modes).
func (a *cpArea) loose(failed []string) ([]string, int, error) {
	var paths []string
	total := 0
	visit := func(p string, mode iofs.FileMode) {
		if mode.Perm()&0o077 == 0 {
			return
		}
		total++
		if len(paths) < maxLooseReported {
			paths = append(paths, p)
		}
	}
	var roots []string
	names, err := a.fs.List(a.cpDir)
	switch {
	case err == nil:
		// The directory itself, not recursively: only names this package
		// writes are walked below it.
		if err := a.walk(a.cpDir, false, visit); err != nil {
			return nil, 0, err
		}
		slices.Sort(names)
		for _, n := range names {
			base, _ := strings.CutSuffix(n, ".json")
			base, _ = strings.CutSuffix(base, ".tmp")
			if n == checkpointScratch || isCheckpointName(base) {
				roots = append(roots, filepath.Join(a.cpDir, n))
			}
		}
	case !oserrorNotExist(err):
		return nil, 0, fmt.Errorf("storage: list checkpoints: %w", err)
	}
	roots = append(roots, failed...)
	for _, r := range roots {
		if err := a.walk(r, true, visit); err != nil {
			return nil, 0, err
		}
	}
	return paths, total, nil
}

// osModeWalk is the mode walk over the real filesystem: root, and with
// recursive every entry under it, without following a symbolic link, which
// it neither reports nor descends (its own mode bits mean nothing). Over any
// other filesystem, or where modes do not apply, it visits nothing: an
// in-memory file reports 0755 whatever it was given.
func osModeWalk(fs vfs.FS) func(root string, recursive bool, visit func(string, iofs.FileMode)) error {
	return func(root string, recursive bool, visit func(string, iofs.FileMode)) error {
		if !unixModes || vfs.Root(fs) != vfs.Default {
			return nil
		}
		if !recursive {
			fi, err := os.Lstat(root)
			switch {
			case errors.Is(err, os.ErrNotExist):
				return nil
			case err != nil:
				return fmt.Errorf("storage: checkpoint mode check: %w", err)
			case fi.Mode()&iofs.ModeSymlink == 0:
				visit(root, fi.Mode())
			}
			return nil
		}
		err := filepath.WalkDir(root, func(p string, d iofs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil // removed while walking
				}
				return err
			}
			if d.Type()&iofs.ModeSymlink != 0 {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			visit(p, fi.Mode())
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("storage: checkpoint mode check: %w", err)
		}
		return nil
	}
}

// area is the DB's checkpoint area for a sweep.
func (db *DB) area(stateDir string) *cpArea {
	o := db.boot.opts
	walk := o.modeWalk
	if walk == nil {
		walk = osModeWalk(o.fs)
	}
	return &cpArea{
		fs: o.fs, dbPath: o.Path, cpDir: o.cpDir, stateDir: stateDir, log: db.log,
		now: o.Clock().UTC(), chmod: o.chmod, walk: walk, seams: o.seams,
	}
}

// CheckpointHealth reads what health reports about this store's
// checkpoints now (§5S.3.12; 05-health §5H.3.1), without the sweep's mode
// findings, which only a sweep makes (the kernel adds the last sweep's). It
// writes nothing and takes no lock: records are replaced atomically, and
// one deleted while it reads is skipped.
func (db *DB) CheckpointHealth(stateDir string) (CheckpointHealth, error) {
	st := db.boot
	if st == nil || !st.finished {
		return CheckpointHealth{}, errors.New("storage: CheckpointHealth on a store whose boot did not finish Migrate")
	}
	a := db.area(stateDir)
	failed, err := a.failedDirs()
	if err != nil {
		return CheckpointHealth{}, err
	}
	cps, scratch, err := a.health(liveStore{installID: st.installID, epoch: st.epoch}, failed)
	if err != nil {
		return CheckpointHealth{}, err
	}
	return CheckpointHealth{Checkpoints: cps, Scratch: scratch}, nil
}

// PruneOptions is storage prune-checkpoints' input. It opens no store, so
// it names the store's files directly.
type PruneOptions struct {
	Path          string // core.storage.path, absolute
	StateDir      string // core.paths.state_dir, absolute: its restore/ is probed for a swap in flight
	CheckpointDir string // core.storage.checkpoint_dir; "" = the DB's parent
	// IncludeOpen also deletes open and served checkpoints, the live
	// store's rollback among them (--include-open --confirm).
	IncludeOpen bool
	FS          FS // zero = the OS's
	Logger      *slog.Logger
	Clock       func() time.Time // nil = time.Now

	// lockWait replaces pruneLockWait in this package's tests.
	lockWait time.Duration
}

// PruneResult is what a prune deleted, and the open checkpoints it kept
// because IncludeOpen was not set.
type PruneResult struct {
	Deleted  []string
	KeptOpen []string
}

// PruneCheckpoints is storage prune-checkpoints (§5S.3.12 "Retention"; 06
// §6.4.5): it deletes every completed and released checkpoint now, before
// its delete_after, and with IncludeOpen the open and served ones too,
// through the sweeper's delete order (record, copy, pointer). It opens no
// store, so it runs with the service stopped or while it serves; it waits
// for a sweep in progress, then, holding the lock, refuses while a swap is
// staged or in flight (ErrPruneSwapInFlight). It deletes records only: garbage, scratch entries
// and modes stay the sweeper's, because a store that is booting may be
// writing a checkpoint whose record does not exist yet.
func PruneCheckpoints(ctx context.Context, o PruneOptions) (PruneResult, error) {
	var res PruneResult
	if !filepath.IsAbs(o.Path) {
		return res, fmt.Errorf("storage: prune-checkpoints: the store path %q is not absolute", o.Path)
	}
	if o.CheckpointDir != "" && !filepath.IsAbs(o.CheckpointDir) {
		return res, fmt.Errorf("storage: prune-checkpoints: core.storage.checkpoint_dir %q is not absolute", o.CheckpointDir)
	}
	fs := o.FS.fs
	if fs == nil {
		fs = vfs.Default
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.lockWait == 0 {
		o.lockWait = pruneLockWait
	}
	parent := o.CheckpointDir
	if parent == "" {
		parent = filepath.Dir(o.Path)
	}
	a := &cpArea{
		fs: fs, dbPath: filepath.Clean(o.Path), cpDir: filepath.Join(parent, checkpointLeaf), stateDir: o.StateDir,
		log: o.Logger, now: o.Clock().UTC(), chmod: osChmod(fs), walk: osModeWalk(fs),
	}
	unlock, ok, err := a.lockWithin(ctx, o.lockWait)
	if err != nil {
		return res, err
	}
	defer unlock()
	// The swap probe runs under the lock, not before it: a restore that
	// stages or starts while this prune waits for the lock is seen. A
	// storage restore-offline that holds the same lock while it stages and
	// swaps (R1-25's seam) can then never race a prune.
	if err := swapProbe(fs, o.StateDir); err != nil {
		return res, err
	}
	failed, err := a.failedDirs()
	if err != nil {
		return res, err
	}
	dirs := failed
	if ok {
		dirs = append([]string{a.cpDir}, failed...)
	}
	for _, dir := range dirs {
		recs, err := readRecords(fs, dir, a.log)
		if err != nil {
			return res, err
		}
		for _, r := range recs {
			if dir == a.cpDir && r.rec.pending() && !o.IncludeOpen {
				res.KeptOpen = append(res.KeptOpen, r.name)
				continue
			}
			if err := ctx.Err(); err != nil {
				return res, err
			}
			if err := a.deleteCheckpoint(dir, r, labelSweepDelete); err != nil {
				return res, err
			}
			a.log.Info("storage: checkpoint deleted by prune-checkpoints", "name", r.name, "dir", dir, "status", r.rec.Status)
			res.Deleted = append(res.Deleted, r.name)
		}
	}
	return res, nil
}

// swapProbe is prune's refusal while a swap is staged or in flight:
// restore/pending.json, which the next boot swaps in and which may name a
// checkpoint to restore (a KindCheckpoint intent), or any
// restore/<id>/inflight.json. With no state directory there is no swap
// slot to read.
func swapProbe(fs vfs.FS, stateDir string) error {
	if stateDir == "" {
		return nil
	}
	pending := restorejournal.PendingPath(stateDir)
	if _, err := fs.Stat(pending); err == nil {
		return fmt.Errorf("%w (restore/%s is staged)", ErrPruneSwapInFlight, restorejournal.PendingName)
	} else if !oserrorNotExist(err) {
		return fmt.Errorf("storage: prune-checkpoints: read the swap slot: %w", err)
	}
	ids, err := restorejournal.InFlightIDs(fs, stateDir)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return fmt.Errorf("%w (restore/%s/%s)", ErrPruneSwapInFlight, ids[0], restorejournal.InflightName)
	}
	return nil
}

// lockWithin takes the sweep lock, retrying while another sweep or prune
// holds it, for up to wait.
func (a *cpArea) lockWithin(ctx context.Context, wait time.Duration) (func(), bool, error) {
	deadline := time.Now().Add(wait)
	for {
		unlock, ok, err := a.lock()
		if err == nil || !errors.Is(err, ErrCheckpointSweepBusy) || !time.Now().Before(deadline) {
			return unlock, ok, err
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
