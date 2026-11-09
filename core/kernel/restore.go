// file: core/kernel/restore.go
// version: 1.0.0
// guid: df7b5c69-060a-442f-82e8-4d31243a2ea3
// last-edited: 2026-11-09

package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// Errors of storage restore-offline (05-storage §5S.3.12).
var (
	// ErrAcceptLossRequired: the source is a served or completed checkpoint
	// or an archive, so the restore discards every write made since it was
	// taken ("Loss"); rerun with --accept-loss.
	ErrAcceptLossRequired = errors.New("kernel: this restore discards every write made since its source was taken; rerun with --accept-loss")
	// ErrNotLatestCheckpoint: --checkpoint names a checkpoint other than the
	// latest upgrade attempt's, which would also discard a later upgrade's
	// work ("Sources").
	ErrNotLatestCheckpoint = errors.New("kernel: only the checkpoint of the latest upgrade attempt of this store is restored")
)

// RestoreOfflineRequest is storage restore-offline's flags (05-storage
// §5S.3.12): exactly one of Checkpoint and Archive (an absolute path), and
// Into for the rehearsal copy. --keyring and --gap-fill are F8-01's.
type RestoreOfflineRequest struct {
	Checkpoint  string
	Archive     string
	Into        string
	AcceptLoss  bool
	RequestedBy string
}

// RestoreOfflineResult is what RestoreOffline did, for the command to print.
type RestoreOfflineResult struct {
	Source     string // the checkpoint with its time and status, or the archive
	ID         string // the swap's journal (in place)
	Escaped    bool   // a failed checkpoint restore was finished (applied_offline)
	Superseded string // the pending intent rejected as superseded, "" when none
	Into       string // the copy (--into)
	IntoState  string // the copy's state files (--into)
	Warnings   []string
}

// RestoreOffline is storage restore-offline (05-storage §5S.3.12), the one
// restore that runs without booting the app, with the service stopped:
//
//   - --checkpoint restores only the checkpoint the store's pointer names,
//     the latest upgrade attempt's, and only while its store copy is still
//     the record's (open, served or completed); a record of another install
//     than the live store's is refused;
//   - --archive restores an archive through Config.ArchiveExtract into the
//     swap's staging directory, refused when the extracted store does not
//     open read-only;
//   - in place, the swap lock is taken exclusively first and held until the
//     restore returns (R1-25 review B1): a service that runs or is starting
//     holds it shared, so the restore refuses with ErrServiceRunning, and no
//     service starts while it swaps; then the swap slot is read before
//     anything is staged (a swap in flight it cannot escape refuses,
//     naming it); a served or completed checkpoint and every archive need
//     --accept-loss; the store's lock refuses an opener that takes no swap
//     lock (ErrStoreHeld); the source store opens read-only with this
//     build's guard (a store this build refuses is never staged); each
//     state file the checkpoint holds is copied into the staging directory
//     and checked against its recorded SHA-256 before anything is staged;
//     Config.BeforeSwap runs; then the journaled swap
//     runs in this process (restoreswap.Swapper.RestoreOffline: a pending
//     intent superseded, a failed checkpoint restore escaped, every rename
//     pre-validated, the journal finished at applied_offline). The
//     checkpoint's record turns consumed once the journal reached swapped
//     or applied_offline (DATA-6), and is released when a swap-back moved
//     its store copy away. What a refused restore staged is removed: the
//     swap owns the staging directory only once it linked the intent;
//   - --into copies the source into a new directory inside the scratch
//     area, the state files into <dir>.state/, modes 0700 and 0600, and
//     touches nothing else: no store lock, no swap lock, no slot read, no
//     swap, no journal, no intent; it runs beside a serving service.
//
// Every form holds the checkpoint directory's sweep lock throughout
// (storage.Checkpoints.Lock), so a sweep or storage prune-checkpoints never
// deletes what it reads.
func RestoreOffline(ctx context.Context, c Config, req RestoreOfflineRequest) (res RestoreOfflineResult, err error) {
	if (req.Checkpoint == "") == (req.Archive == "") {
		return res, errors.New("kernel: storage restore-offline restores one source: --checkpoint <name> or --archive <file>")
	}
	if req.Archive != "" && !filepath.IsAbs(req.Archive) {
		return res, fmt.Errorf("kernel: --archive %q is not an absolute path", req.Archive)
	}
	k, err := offlineKernel(c)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, k.closeOffline(ctx)) }()
	fs := bootseam.FS(ctx)
	var sw *restoreswap.Swapper
	escape := ""
	if req.Into == "" {
		// In place only. --into takes no swap lock, on purpose: it copies
		// the source into the scratch area and never touches the live
		// store, so it runs beside a serving service, and R1-27's make
		// rollback-restore relies on that (it runs restore-offline
		// --archive --into while the service is up, to read the archive's
		// storage levels; TestRestoreOfflineIntoBesideARunningService). Do
		// not take the lock above this branch, exclusively or at all.
		if err := k.lockSwap(ctx, true); err != nil {
			return res, err
		}
		if sw, err = k.swapper(ctx); err != nil {
			return res, err
		}
		kind := restorejournal.KindArchive
		if req.Checkpoint != "" {
			kind = restorejournal.KindCheckpoint
		}
		if escape, err = sw.CheckOffline(c.StoragePath, kind); err != nil {
			return res, fmt.Errorf("kernel: %w", err)
		}
	}
	cps, err := storage.CheckpointsOf(storage.FSOf(fs), c.StoragePath, "", k.coreLog("storage"))
	if err != nil {
		return res, err
	}
	// The checkpoint sweeper's lock, held to the end: storage
	// prune-checkpoints (which runs while the service serves, or with it
	// stopped) and a sweep wait, so neither deletes the checkpoint this
	// reads, copies or swaps in, nor races its record update, nor drops the
	// pointer the swap sets aside.
	unlock, err := cps.Lock(ctx)
	if err != nil {
		return res, err
	}
	defer unlock()
	var cp storage.Checkpoint
	loss := true
	if req.Checkpoint != "" {
		if cp, err = latestCheckpoint(cps, req.Checkpoint, k.escapedPointer(fs, escape)); err != nil {
			return res, err
		}
		loss = cp.NeedsAcceptLoss()
		res.Source = fmt.Sprintf("checkpoint %s (taken %s, %s)", cp.Name, cp.TakenAt.UTC().Format(time.RFC3339), cp.Status)
	} else {
		if c.ArchiveExtract == nil {
			return res, errors.New("kernel: this app has no archive extractor; --archive is not available")
		}
		res.Source = "archive " + req.Archive
	}
	if req.Into != "" {
		return k.restoreInto(ctx, fs, cps, cp, req, res)
	}
	if loss && !req.AcceptLoss {
		return res, fmt.Errorf("%w (%s)", ErrAcceptLossRequired, res.Source)
	}
	return k.restoreInPlace(ctx, fs, sw, cps, cp, req, res)
}

// escapedPointer is where an interrupted escape of journal escape (""
// when none is in flight) left the failed store's pointer: in that
// journal's failed/ directory, beside the failed store it set aside. The
// rerun that finishes the escape reads the pointer there once the live one
// is gone. "" when there is no such file.
func (k *Kernel) escapedPointer(fs vfs.FS, escape string) string {
	if escape == "" {
		return ""
	}
	live := k.cfg.StoragePath + restorejournal.CheckpointSuffix
	moved := restorejournal.FailedDest(k.cfg.StateDir, k.cfg.StoragePath, escape, live)
	if _, err := fs.Stat(moved); err != nil {
		return ""
	}
	return moved
}

// latestCheckpoint is the checkpoint the store's pointer names, which must
// be name and still restorable. With no live pointer, escaped (an
// interrupted escape's copy of it, or "") is read instead.
func latestCheckpoint(cps *storage.Checkpoints, name, escaped string) (storage.Checkpoint, error) {
	cp, ok, err := cps.Latest()
	if err == nil && !ok && escaped != "" {
		cp, ok, err = cps.LatestFrom(escaped)
	}
	switch {
	case err != nil:
		return cp, err
	case !ok:
		return cp, fmt.Errorf("kernel: no upgrade attempt of this store has a checkpoint (no pointer beside the store); nothing to restore with --checkpoint")
	case cp.Name != name:
		return cp, fmt.Errorf("%w: the latest is %s, not %s", ErrNotLatestCheckpoint, cp.Name, name)
	case !cp.Restorable():
		return cp, fmt.Errorf("kernel: checkpoint %s is %s and released; its store copy is not the record's any more", cp.Name, cp.Status)
	}
	return cp, nil
}

// restoreInPlace is the "In place" row, under the swap lock RestoreOffline
// took exclusively, with sw the swapper whose slot check passed.
func (k *Kernel) restoreInPlace(ctx context.Context, fs vfs.FS, sw *restoreswap.Swapper, cps *storage.Checkpoints, cp storage.Checkpoint,
	req RestoreOfflineRequest, res RestoreOfflineResult) (RestoreOfflineResult, error) {
	dbPath := k.cfg.StoragePath
	// The live store's install ID, read before the lock (the open takes it).
	var installID string
	if o, err := k.liveStore(ctx); err != nil {
		k.log.Warn("kernel: the live store does not open read-only; restoring without its install ID", "path", dbPath, "err", err)
	} else {
		installID = o.InstallID()
		// Read-only, so nothing is lost; the lock it held is released
		// either way before refuseHeldStore takes it.
		if cerr := o.Close(); cerr != nil {
			k.log.Warn("kernel: closing the read-only store", "path", dbPath, "err", cerr)
		}
	}
	if cp.Name != "" && installID != "" && cp.InstallID != "" && cp.InstallID != installID {
		return res, fmt.Errorf("kernel: checkpoint %s is of install %s, not the live store's %s", cp.Name, cp.InstallID, installID)
	}
	if err := refuseHeldStore(fs, dbPath); err != nil {
		return res, err
	}
	id := restoreswap.NewID(bootClock(ctx)())
	staging := restorejournal.StagingDir(dbPath, id)
	src, err := k.stageSource(ctx, fs, cp, req, id, staging)
	if err != nil {
		return res, errors.Join(err, removeIfExists(fs, filepath.Dir(staging)))
	}
	if src.InstallID == "" {
		src.InstallID = installID
	}
	// "Loss": the source's time, before anything moves.
	k.log.Warn("kernel: restoring; every write made since the source was taken is discarded", "source", res.Source, "id", id)
	if f := k.cfg.BeforeSwap; f != nil {
		if err := f(dbPath); err != nil {
			return res, errors.Join(fmt.Errorf("kernel: before the restore swaps: %w", err), removeIfExists(fs, filepath.Dir(staging)))
		}
	}
	r, rerr := sw.RestoreOffline(ctx, dbPath, req.RequestedBy, src)
	res.ID, res.Escaped, res.Superseded = r.ID, r.Escaped, r.Superseded
	if !r.Linked && (rerr != nil || r.Escaped) {
		// Refused before Stage linked the intent, or an escape, which
		// journals no staging path: what this restore staged is its own,
		// copies the swap no longer needs.
		rerr = errors.Join(rerr, removeIfExists(fs, filepath.Dir(staging)))
	}
	if cp.Name != "" {
		res.Warnings = append(res.Warnings, k.settleCheckpoint(ctx, fs, cps, cp, r)...)
	}
	return res, rerr
}

// stageSource builds the swap's source: a checkpoint's store copy in place
// with each state file copied into staging/state/<label> and checked
// against its SHA-256, or an archive extracted into staging/db and opened
// read-only.
func (k *Kernel) stageSource(ctx context.Context, fs vfs.FS, cp storage.Checkpoint, req RestoreOfflineRequest,
	id, staging string) (restoreswap.OfflineSource, error) {
	src := restoreswap.OfflineSource{ID: id}
	if err := mkdirPrivate(fs, staging); err != nil {
		return src, err
	}
	if cp.Name != "" {
		// The checkpoint's store copy opens with this build's guard, as an
		// extracted archive does, unless it is gone: an interrupted escape
		// already placed it (the rerun finishes that escape), and otherwise
		// the swap refuses a missing source.
		if _, err := fs.Stat(cp.Store()); err == nil {
			if err := k.verifyStore(ctx, cp.Store()); err != nil {
				return src, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return src, fmt.Errorf("kernel: checkpoint %s: %w", cp.Name, err)
		}
		src.Kind, src.Store, src.HooksOwed, src.InstallID = restorejournal.KindCheckpoint, cp.Store(), cp.HooksOwed, cp.InstallID
		if len(cp.StateFiles) > 0 {
			if err := mkdirPrivate(fs, filepath.Join(staging, "state")); err != nil {
				return src, err
			}
		}
		for _, f := range cp.StateFiles {
			staged := filepath.Join(staging, "state", f.Label)
			if err := copyVerified(fs, cp.StateCopy(f.Label), staged, f.SHA256); err != nil {
				return src, err
			}
			src.StateFiles = append(src.StateFiles, restorejournal.Target{From: staged, To: f.Path})
		}
		return src, nil
	}
	store := filepath.Join(staging, "db")
	if err := mkdirPrivate(fs, store); err != nil {
		return src, err
	}
	archiveID, err := k.cfg.ArchiveExtract(ctx, req.Archive, store)
	if err != nil {
		return src, fmt.Errorf("kernel: extract %s: %w", req.Archive, err)
	}
	if err := k.verifyStore(ctx, store); err != nil {
		return src, err
	}
	src.Kind, src.Store, src.ArchiveID = restorejournal.KindArchive, store, archiveID
	return src, nil
}

// verifyStore opens a source store (an extracted archive, a checkpoint's
// store copy) read-only with this build's guard and claims, and closes it
// (05-backup's VerifyOpen, R1's form): a store this build cannot open is
// never swapped in.
func (k *Kernel) verifyStore(ctx context.Context, path string) error {
	opts, err := k.storageOptions(ctx)
	if err != nil {
		return err
	}
	opts.Path, opts.ReadOnly, opts.MustExist, opts.StateFiles = path, true, true, nil
	o, err := storage.Open(ctx, opts)
	if err != nil {
		return fmt.Errorf("kernel: the source store %s does not open: %w", path, err)
	}
	return o.Close()
}

// settleCheckpoint writes the checkpoint record's edge (05-storage
// §5S.3.16 machine 3): consumed once this restore's journal reached swapped
// or applied_offline (an escape: applied_offline), released when a
// swap-back moved the store copy away. A record write that fails is a
// warning: the swap's outcome stands, and the record names a copy no later
// attempt can reuse (the store generation differs after any swap).
func (k *Kernel) settleCheckpoint(ctx context.Context, fs vfs.FS, cps *storage.Checkpoints, cp storage.Checkpoint, r restoreswap.OfflineResult) []string {
	if r.ID == "" {
		return nil
	}
	recs, err := restoreswap.ReadJournal(fs, k.cfg.StateDir, r.ID)
	if err != nil {
		return []string{fmt.Sprintf("the journal %s could not be read to settle checkpoint %s: %v", r.ID, cp.Name, err)}
	}
	var swapped, back bool
	for _, rec := range recs {
		switch rec.Step {
		case restoreswap.StepSwapped:
			swapped = !r.Escaped
		case restoreswap.StepAppliedOffline:
			swapped = true
		case restoreswap.StepSwappedBack:
			back = true
		}
	}
	now := bootClock(ctx)()
	var werr error
	switch {
	case swapped && !back:
		werr = cps.Consume(cp.Name, now)
	case back && !r.Escaped:
		if _, err := fs.Stat(cp.Store()); errors.Is(err, os.ErrNotExist) {
			werr = cps.Release(cp.Name, now)
		}
	}
	if werr != nil {
		k.log.Error("kernel: checkpoint record not updated after the restore", "name", cp.Name, "err", werr)
		return []string{fmt.Sprintf("checkpoint record %s was not updated: %v", cp.Name, werr)}
	}
	return nil
}

// restoreInto is the "Into an empty path" row.
func (k *Kernel) restoreInto(ctx context.Context, fs vfs.FS, cps *storage.Checkpoints, cp storage.Checkpoint,
	req RestoreOfflineRequest, res RestoreOfflineResult) (_ RestoreOfflineResult, err error) {
	into, scratch := filepath.Clean(req.Into), cps.Scratch()
	if !filepath.IsAbs(into) || into == scratch || !restorejournal.InTree(into, scratch) {
		return res, fmt.Errorf("kernel: --into %s must be a new directory inside the scratch area %s", req.Into, scratch)
	}
	state := into + ".state"
	for _, p := range []string{into, state} {
		if _, err := fs.Stat(p); err == nil {
			return res, fmt.Errorf("kernel: --into: %s already exists", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
	}
	for d := filepath.Dir(into); restorejournal.InTree(d, cps.Dir()); d = filepath.Dir(d) {
		if err := mkdirPrivate(fs, d); err != nil {
			return res, err
		}
		if d == cps.Dir() {
			break
		}
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeIfExists(fs, into), removeIfExists(fs, state))
		}
	}()
	if cp.Name != "" {
		ok, err := vfs.Clone(fs, fs, cp.Store(), into, vfs.CloneSync)
		if err != nil {
			return res, fmt.Errorf("kernel: copy checkpoint %s: %w", cp.Name, err)
		}
		if !ok {
			return res, fmt.Errorf("kernel: checkpoint %s has no store copy at %s", cp.Name, cp.Store())
		}
		if err := mkdirPrivate(fs, state); err != nil {
			return res, err
		}
		for _, f := range cp.StateFiles {
			if err := copyVerified(fs, cp.StateCopy(f.Label), filepath.Join(state, f.Label), f.SHA256); err != nil {
				return res, err
			}
		}
	} else {
		if err := mkdirPrivate(fs, into); err != nil {
			return res, err
		}
		if _, err := k.cfg.ArchiveExtract(ctx, req.Archive, into); err != nil {
			return res, fmt.Errorf("kernel: extract %s: %w", req.Archive, err)
		}
		if err := mkdirPrivate(fs, state); err != nil {
			return res, err
		}
	}
	if err := chmodTree(fs, into); err != nil {
		return res, err
	}
	res.Into, res.IntoState = into, state
	res.Warnings = append(res.Warnings, fmt.Sprintf("the copy at %s holds the store's secrets and sign-in tokens: delete it after the rehearsal (the checkpoint sweeper deletes it 7 days after its last change)", into))
	return res, nil
}

// mkdirPrivate creates dir (and any missing parent) and makes dir mode 0700.
func mkdirPrivate(fs vfs.FS, dir string) error {
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("kernel: create %s: %w", dir, err)
	}
	return restorejournal.ChmodFS(fs, dir, 0o700)
}

// chmodTree makes every directory under root 0700 and every file 0600
// (05-storage §5S.3.12 "Scratch copies").
func chmodTree(fs vfs.FS, root string) error {
	fi, err := fs.Stat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return restorejournal.ChmodFS(fs, root, 0o600)
	}
	if err := restorejournal.ChmodFS(fs, root, 0o700); err != nil {
		return err
	}
	names, err := fs.List(root)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := chmodTree(fs, filepath.Join(root, n)); err != nil {
			return err
		}
	}
	return nil
}

// copyVerified copies a checkpoint's state file to dst, mode 0600, synced,
// and refuses a copy whose SHA-256 is not the record's (removing it).
func copyVerified(fs vfs.FS, src, dst, want string) (err error) {
	if want == "" {
		return fmt.Errorf("kernel: the checkpoint record lists no SHA-256 for %s", src)
	}
	in, err := fs.Open(src)
	if err != nil {
		return fmt.Errorf("kernel: state file copy %s: %w", src, err)
	}
	defer func() { _ = in.Close() }() // read side
	out, err := fs.Create(dst, vfs.WriteCategoryUnspecified)
	if err != nil {
		return fmt.Errorf("kernel: stage %s: %w", dst, err)
	}
	h := sha256.New()
	_, cerr := io.Copy(io.MultiWriter(out, h), in)
	if err := errors.Join(cerr, out.Sync(), out.Close()); err != nil {
		return errors.Join(fmt.Errorf("kernel: stage %s: %w", dst, err), removeIfExists(fs, dst))
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return errors.Join(fmt.Errorf("kernel: state file copy %s has SHA-256 %s; the checkpoint record says %s", src, got, want),
			removeIfExists(fs, dst))
	}
	if err := restorejournal.ChmodFS(fs, dst, 0o600); err != nil {
		return err
	}
	d, err := fs.OpenDir(filepath.Dir(dst))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// removeIfExists removes path and everything under it; a path that is gone
// is success.
func removeIfExists(fs vfs.FS, path string) error {
	if _, err := fs.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fs.RemoveAll(path)
}
