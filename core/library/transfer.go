// file: core/library/transfer.go
// version: 1.0.0
// guid: 534f60c5-c088-4e70-962c-8bcddbfafc71
// last-edited: 2026-11-11

package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/falkcorp/mediacore/internal/libfs"
	"github.com/falkcorp/mediacore/storage"
)

// tmpPrefix names a copy's temp file next to its destination:
// <dst dir>/.mediacore-tmp-<id>. The ID is the journal row's, so recovery
// finds the file from the row alone.
const tmpPrefix = ".mediacore-tmp-"

// asideInfix names the file a Replace keeps until the transfer is durable:
// <dst>.mediacore-replaced-<unix>.
const asideInfix = ".mediacore-replaced-"

// Transfer puts src's bytes at dst by opts.Mode, never replacing an existing
// dst unless opts.Replace's digest matches (05-library 5L.3.3). Every durable
// step is journaled in c/library/transfer/<id> before it happens.
func (m *manager) Transfer(ctx context.Context, caller storage.Owner, src, dst Path, opts TransferOptions) (TransferResult, error) {
	return m.transfer(ctx, nil, caller, src, dst, opts)
}

// TransferIn is Transfer with the end of the journal row staged into b: the row
// is first marked done (its own commit, after the file work), then its delete
// is staged into the caller's batch, so the caller's rows and the end of the
// journal commit together (COR-4).
func (m *manager) TransferIn(ctx context.Context, b *storage.Batch, caller storage.Owner, src, dst Path, opts TransferOptions) (TransferResult, error) {
	if b == nil {
		return TransferResult{}, fmt.Errorf("%w: TransferIn needs the caller's batch", ErrInvalidOptions)
	}
	if !opts.Journal {
		return TransferResult{}, fmt.Errorf("%w: TransferIn needs a journaled transfer", ErrInvalidOptions)
	}
	return m.transfer(ctx, b, caller, src, dst, opts)
}

// xfer is one transfer in flight.
type xfer struct {
	m      *manager
	ctx    context.Context
	bg     context.Context // survives cancellation: cleanup and journal steps
	caller storage.Owner
	batch  *storage.Batch
	src    Path
	dst    Path
	sr, dr *Root
	sfs    fsOps // each root's full-rights driver; the engine has checked the caller's rights
	dfs    fsOps
	opts   TransferOptions
	row    transferRow
	info   fs.FileInfo // the source's Lstat at the start
	start  time.Time
	res    TransferResult
	begun  bool
}

func (m *manager) transfer(ctx context.Context, b *storage.Batch, caller storage.Owner, src, dst Path, opts TransferOptions) (res TransferResult, err error) {
	x := &xfer{m: m, ctx: ctx, bg: context.WithoutCancel(ctx), caller: caller, batch: b, src: src, dst: dst, opts: opts, start: m.seam.now()}
	defer func() {
		res.Duration = m.seam.now().Sub(x.start)
		m.ctr.recordTransfer(opts.Mode, res, err)
	}()
	if err = opts.validate(); err != nil {
		return TransferResult{}, err
	}
	if err = x.check(); err != nil {
		return TransferResult{}, err
	}
	unlock := m.lockPaths(src, dst)
	defer unlock()
	pool := m.slots.Load()
	if err = pool.acquire(ctx); err != nil {
		return TransferResult{}, err
	}
	defer pool.release()
	// The checks above ran before the locks; the facts that matter are read
	// again under them.
	if err = x.recheck(); err != nil {
		return TransferResult{}, err
	}
	res, err = x.run()
	return res, err
}

// check is step 0 and the mount half of step 1: paths, rights, protection.
func (x *xfer) check() error {
	m := x.m
	if x.src.IsRoot() || x.dst.IsRoot() {
		return fmt.Errorf("%w: a root directory is not a transfer endpoint", ErrBadTransfer)
	}
	if x.src == x.dst {
		return fmt.Errorf("%w: source and destination are the same path", ErrBadTransfer)
	}
	if x.src.Root == x.dst.Root && strings.HasPrefix(x.dst.Rel, x.src.Rel+"/") {
		return fmt.Errorf("%w: destination is inside the source", ErrBadTransfer)
	}
	var err error
	if x.sr, err = m.need(x.src); err != nil {
		return err
	}
	if x.dr, err = m.need(x.dst); err != nil {
		return err
	}
	if !m.canRead(x.sr, x.caller) {
		return fmt.Errorf("%w: %s", ErrNotReadable, x.src.Root)
	}
	if !m.canWrite(x.dr, x.caller) {
		return fmt.Errorf("%w: %s", ErrReadOnlyRoot, x.dst.Root)
	}
	if !m.enabled(x.dr.spec.Owner) {
		return fmt.Errorf("%w: %s", ErrOwnerDisabled, x.dr.spec.Owner)
	}
	if x.opts.Replace != nil && (x.opts.Mode == Hardlink || x.opts.Mode == Symlink) {
		return fmt.Errorf("%w: Replace does not apply to %s", ErrInvalidOptions, x.opts.Mode)
	}
	if x.opts.Mode == Move {
		if !m.canMoveOut(x.sr, x.caller) {
			return fmt.Errorf("%w: %s", ErrReadOnlyRoot, x.src.Root)
		}
		if x.sr.spec.Role != RoleDownload && !m.enabled(x.sr.spec.Owner) {
			return fmt.Errorf("%w: %s", ErrOwnerDisabled, x.sr.spec.Owner)
		}
		if x.sr.spec.Role == RoleExternal {
			return fmt.Errorf("%w: %s is an external root", ErrSourceProtected, x.src.Root)
		}
		if protected, perr := m.protectedForMove(x.ctx, x.src); protected {
			return errors.Join(fmt.Errorf("%w: %s", ErrSourceProtected, x.src), perr)
		}
	}
	x.sfs, x.dfs = x.sr.rw, x.dr.rw
	return nil
}

// recheck runs under the path locks: mounts, then the source and destination
// as they are now.
func (x *xfer) recheck() error {
	if err := x.dr.Mounted(x.ctx); err != nil {
		return err
	}
	if x.opts.Mode == Move {
		if err := x.sr.Mounted(x.ctx); err != nil {
			return err
		}
	}
	info, err := x.sfs.Lstat(x.src.Rel)
	if err != nil {
		return err
	}
	x.info = info
	regular := info.Mode().IsRegular()
	switch {
	case x.opts.Mode == Symlink, x.opts.Mode == Move:
		// A link, a file or (same filesystem) a directory.
	case !regular:
		return fmt.Errorf("%w: %s is not a regular file", ErrNotSupported, x.src)
	}
	if _, derr := x.dfs.Lstat(x.dst.Rel); derr == nil {
		if x.opts.Replace == nil {
			return &fs.PathError{Op: "transfer", Path: x.dst.String(), Err: fs.ErrExist}
		}
	} else if !errors.Is(derr, fs.ErrNotExist) {
		return derr
	} else if x.opts.Replace != nil {
		return fmt.Errorf("%w: %s no longer exists", ErrReplaceMismatch, x.dst)
	}
	return nil
}

func (x *xfer) run() (TransferResult, error) {
	x.row = transferRow{ID: x.m.seam.newID(), State: stBegun, Mode: x.opts.Mode, Owner: x.caller, In: x.batch != nil,
		Src: x.src, Dst: x.dst, Verify: x.opts.Verify, At: x.start.UTC(), Size: x.info.Size()}
	if x.opts.Replace != nil {
		x.row.Expect = x.opts.Replace.ExpectSHA256
	}
	x.m.inflight.Store(x.row.ID, struct{}{})
	defer x.m.inflight.Delete(x.row.ID)
	if err := x.persist(x.ctx, stBegun); err != nil {
		return TransferResult{}, err
	}
	x.begun = true
	var err error
	switch x.opts.Mode {
	case Move:
		err = x.doMove()
	case Copy:
		err = x.copyPath(Copy, false, false)
	case Hardlink:
		err = x.doLink()
	case Reflink:
		err = x.copyPath(Reflink, true, false)
	case Symlink:
		err = x.doSymlink()
	case Auto:
		err = x.doAuto()
	}
	if err != nil {
		return TransferResult{}, err
	}
	return x.finish()
}

// persist writes the row in state st. The first write failing means no file
// operation has started: the transfer is refused (fail closed, 5L.5).
func (x *xfer) persist(ctx context.Context, st rowState) error {
	x.row.State = st
	if !x.opts.Journal {
		return nil
	}
	if err := x.m.jrn.put(ctx, x.row.ID, x.row); err != nil {
		return fmt.Errorf("%w: %w", ErrJournal, err)
	}
	return nil
}

// abandon deletes the row of a transfer that changed nothing (or whose partial
// work was cleaned up), so recovery has nothing to do for it.
func (x *xfer) abandon() {
	if x.begun && x.opts.Journal {
		if err := x.m.jrn.remove(x.bg, x.row.ID); err != nil {
			x.m.log.Warn("library: could not delete an abandoned transfer row; recovery will", "id", x.row.ID, "err", err)
		}
	}
}

// finish is step 6: Transfer deletes the row in its own commit; TransferIn marks
// it done in its own commit and stages the delete into the caller's batch.
func (x *xfer) finish() (TransferResult, error) {
	x.res.Did = x.row.Did
	if x.res.Bytes == 0 && x.row.Did != Symlink {
		x.res.Bytes = x.info.Size()
	}
	if x.opts.Verify == VerifySHA256 && x.res.SHA256 == "" && x.info.Mode().IsRegular() && x.row.Did != Symlink {
		d, err := x.m.hashVia(x.ctx, x.dfs, x.dst.Rel, false)
		if err != nil {
			return x.res, fmt.Errorf("library: hash %s: %w", x.dst, err)
		}
		x.res.SHA256 = d.SHA256
	}
	switch {
	case !x.opts.Journal:
	case x.batch == nil:
		if err := x.m.jrn.remove(x.bg, x.row.ID); err != nil {
			x.m.log.Warn("library: could not delete a finished transfer row; recovery will", "id", x.row.ID, "err", err)
		}
	default:
		x.row.SrcSHA = firstNonEmpty(x.row.SrcSHA, x.res.SHA256)
		if err := x.persist(x.bg, stDone); err != nil {
			return x.res, err
		}
		if err := x.m.jrn.stageRemove(x.batch, x.row.ID); err != nil {
			return x.res, fmt.Errorf("library: stage the end of the transfer journal: %w", err)
		}
	}
	return x.res, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// dstDir is the destination's parent as a root-relative name ("" for the root).
func (x *xfer) dstDir() string { return x.dst.Dir().Rel }

func (x *xfer) srcDirPath() string { return x.src.Dir().Rel }

// mkdirDst creates the destination directory when MkdirAll is set.
func (x *xfer) mkdirDst() error {
	if !x.opts.MkdirAll || x.dstDir() == "" {
		return nil
	}
	return x.dfs.MkdirAll(x.dstDir(), DirMode)
}

// syncDir fsyncs a directory of fsys; "" is the root directory.
func syncDir(fsys fsOps, rel string) error {
	if rel == "" {
		rel = "."
	}
	return fsys.SyncDir(rel)
}

// doMove is step 2 for Move: a no-clobber rename; on a device boundary the copy
// path takes over.
func (x *xfer) doMove() error {
	x.row.Did = Move
	if err := x.mkdirDst(); err != nil {
		x.abandon()
		return err
	}
	if x.opts.Replace != nil {
		if err := x.replaceAside(); err != nil {
			x.abandon()
			return err
		}
	}
	err := x.sfs.renameNoReplaceInto(x.dfs, x.src.Rel, x.dst.Rel)
	switch {
	case err == nil:
		if err := x.syncAfterRename(); err != nil {
			return err
		}
		return x.dropAside()
	case errors.Is(err, ErrCrossDevice):
		if x.opts.Replace != nil {
			x.restoreAside()
		}
		return x.copyPath(Move, false, true)
	default:
		if x.opts.Replace != nil {
			x.restoreAside()
		}
		x.abandon()
		return err
	}
}

func (x *xfer) syncAfterRename() error {
	if err := syncDir(x.dfs, x.dstDir()); err != nil {
		return err
	}
	if x.src.Root != x.dst.Root || x.srcDirPath() != x.dstDir() {
		return syncDir(x.sfs, x.srcDirPath())
	}
	return nil
}

// doLink is Hardlink: a link straight to the final name (it refuses an existing
// name, so there is no temp and no partial state).
func (x *xfer) doLink() error {
	x.row.Did = Hardlink
	if err := x.mkdirDst(); err != nil {
		x.abandon()
		return err
	}
	if err := x.sfs.linkInto(x.dfs, x.src.Rel, x.dst.Rel); err != nil {
		x.abandon()
		return err
	}
	return syncDir(x.dfs, x.dstDir())
}

// doSymlink is Symlink: one atomic create.
func (x *xfer) doSymlink() error {
	x.row.Did = Symlink
	if err := x.mkdirDst(); err != nil {
		x.abandon()
		return err
	}
	if err := x.dfs.Symlink(x.src, x.dst.Rel); err != nil {
		x.abandon()
		return err
	}
	return syncDir(x.dfs, x.dstDir())
}

// doAuto is reflink, then hardlink, then copy: each fallback only on the one
// error that means "not here", so a real failure is not masked.
func (x *xfer) doAuto() error {
	err := x.copyPath(Reflink, true, false)
	if err == nil || !errors.Is(err, ErrReflinkUnsupported) {
		return err
	}
	if err := x.mkdirDst(); err != nil {
		x.abandon()
		return err
	}
	lerr := x.sfs.linkInto(x.dfs, x.src.Rel, x.dst.Rel)
	switch {
	case lerr == nil:
		x.row.Did = Hardlink
		return syncDir(x.dfs, x.dstDir())
	case linkUnavailable(lerr):
		return x.copyPath(Copy, false, false)
	default:
		x.abandon()
		return lerr
	}
}

// linkUnavailable is "a hard link cannot be made here": another device, or a
// filesystem that does not link (FAT and some FUSE answer EPERM, EMLINK or
// ENOTSUP). Auto then copies; a real failure to write would fail the copy too.
func linkUnavailable(err error) bool {
	return errors.Is(err, ErrCrossDevice) || errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.EMLINK) || errors.Is(err, errors.ErrUnsupported)
}

// copyPath is steps 3 to 5: the temp, the verify, the no-clobber publish, and
// (Move) the source's removal. clone makes the temp by clone instead of by
// reading; a clone that is not available is ErrReflinkUnsupported and leaves no
// trace.
func (x *xfer) copyPath(did TransferMode, clone, removeSrc bool) error {
	if !clone {
		if err := x.checkSpace(x.info.Size()); err != nil {
			x.abandon()
			return err
		}
	}
	x.row.Did = did
	x.row.RemoveSrc = removeSrc
	x.row.Tmp = path2(x.dstDir(), tmpPrefix+x.row.ID)
	if err := x.mkdirDst(); err != nil {
		x.abandon()
		return err
	}
	tmp := x.row.Tmp
	sha, size, err := x.fillTemp(tmp, clone)
	if err != nil {
		x.removeTemp(tmp)
		x.abandon()
		return err
	}
	x.row.SrcSHA, x.row.Size = sha, size
	x.res.Bytes = size
	if sha != "" {
		x.res.SHA256 = sha
	}
	if err := x.persist(x.ctx, stCopied); err != nil {
		x.removeTemp(tmp)
		return err
	}
	if x.opts.Replace != nil {
		if err := x.replaceAside(); err != nil {
			x.removeTemp(tmp)
			x.abandon()
			return err
		}
	}
	// Publish: Link refuses an existing name, so a creator that got there first
	// wins and our temp goes (never a rename over it).
	if err := x.dfs.Link(tmp, x.dst.Rel); err != nil {
		if x.opts.Replace != nil {
			x.restoreAside()
		}
		x.removeTemp(tmp)
		x.abandon()
		return err
	}
	if err := x.persist(x.ctx, stPublished); err != nil {
		return err
	}
	if err := x.dfs.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := syncDir(x.dfs, x.dstDir()); err != nil {
		return err
	}
	if err := x.dropAside(); err != nil {
		return err
	}
	if removeSrc {
		return x.removeSource()
	}
	return nil
}

// path2 joins a root-relative directory and a name.
func path2(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// removeTemp deletes a temp file, best effort on a path that is already a
// rollback: the journal still names it, so recovery retries.
func (x *xfer) removeTemp(tmp string) {
	if err := x.dfs.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		x.m.log.Warn("library: could not remove a transfer temp file; recovery will", "root", x.dst.Root, "err", err)
	}
}

// fillTemp creates the temp file next to the destination and fills it, by clone
// or by streaming src through a SHA-256 (the digest is of the bytes READ, one
// descriptor), then fsyncs and verifies it. It returns the source digest ("" for
// a clone that was not hashed) and the size.
func (x *xfer) fillTemp(tmp string, clone bool) (sha string, size int64, err error) {
	if clone {
		return x.cloneTemp(tmp)
	}
	src, err := x.sfs.Open(x.src.Rel)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()
	sst, err := src.Stat()
	if err != nil {
		return "", 0, err
	}
	if !sst.Mode().IsRegular() || !os.SameFile(sst, x.info) {
		return "", 0, fmt.Errorf("%w: %s changed under the transfer", ErrVerifyFailed, x.src)
	}
	perm := libfs.LibraryFileMode
	if x.opts.Perm == PermFromSource {
		perm = sst.Mode().Perm()
	}
	f, err := x.dfs.CreateExclusive(tmp, perm)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := libfs.Copy(f, src, libfs.CopyOptions{Hash: h, Check: x.ctx.Err})
	if err == nil && x.opts.Perm == PermFromSource {
		err = chmodIfDifferent(f, perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	sha = hex.EncodeToString(h.Sum(nil))
	if err := x.verify(tmp, sst, n, sha); err != nil {
		return "", 0, err
	}
	return sha, n, nil
}

// chmodIfDifferent sets the file's permission bits only when they differ from
// want (the umask may have already produced them), as fileops did.
func chmodIfDifferent(f File, want fs.FileMode) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Mode().Perm() == want.Perm() {
		return nil
	}
	c, ok := f.(interface{ Chmod(fs.FileMode) error })
	if !ok {
		return fmt.Errorf("%w: cannot set permission bits", ErrNotSupported)
	}
	return c.Chmod(want.Perm())
}

// cloneTemp makes the temp by clone: nothing is read, nothing is written.
func (x *xfer) cloneTemp(tmp string) (string, int64, error) {
	if err := x.sfs.reflinkInto(x.dfs, x.src.Rel, tmp); err != nil {
		return "", 0, err
	}
	st, err := x.dfs.Lstat(tmp)
	if err != nil {
		return "", 0, err
	}
	var sha string
	if x.opts.Verify == VerifySHA256 {
		want, herr := x.m.hashVia(x.ctx, x.sfs, x.src.Rel, false)
		if herr != nil {
			return "", 0, herr
		}
		got, herr := x.m.hashVia(x.ctx, x.dfs, tmp, false)
		if herr != nil {
			return "", 0, herr
		}
		if want.SHA256 != got.SHA256 || want.Size != got.Size {
			return "", 0, fmt.Errorf("%w: %s", ErrVerifyFailed, x.dst)
		}
		sha = got.SHA256
	}
	if st.Size() != x.info.Size() {
		return "", 0, fmt.Errorf("%w: %s is %d bytes, the source %d", ErrVerifyFailed, x.dst, st.Size(), x.info.Size())
	}
	return sha, st.Size(), nil
}

// verify is step 4's check: the temp's size, or its SHA-256 re-read from disk,
// against what was read from the source, and the source unchanged meanwhile.
func (x *xfer) verify(tmp string, sst fs.FileInfo, n int64, sha string) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrVerifyFailed, fmt.Sprintf(format, a...))
	}
	st, err := x.dfs.Lstat(tmp)
	if err != nil {
		return err
	}
	if st.Size() != n {
		return bad("the temp file is %d bytes, %d were read", st.Size(), n)
	}
	now, err := x.sfs.Lstat(x.src.Rel)
	if err != nil {
		return bad("the source is gone: %v", err)
	}
	if now.Size() != n || !now.ModTime().Equal(sst.ModTime()) {
		return bad("%s changed while it was copied", x.src)
	}
	if x.opts.Verify == VerifySHA256 {
		d, err := x.m.hashVia(x.ctx, x.dfs, tmp, false)
		if err != nil {
			return err
		}
		if d.SHA256 != sha || d.Size != n {
			return bad("the temp file's SHA-256 differs from the source's")
		}
	}
	return nil
}

// removeSource is step 5 of a cross-device Move: only now, after the destination
// is published, fsynced and verified, the source goes.
func (x *xfer) removeSource() error {
	if err := x.sfs.Remove(x.src.Rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(x.sfs, x.srcDirPath())
}

// ---- space ----

// checkSpace is step 1's free-space rule: Avail - size must stay at or above
// the root's floor, before a byte is written. A statfs that fails refuses.
func (x *xfer) checkSpace(size int64) error {
	dir := filepath.Join(x.dr.abs, filepath.FromSlash(x.dstDir()))
	u, err := x.m.seam.usage(nearestExisting(dir))
	if err != nil {
		return fmt.Errorf("library: free space of %s: %w", x.dst.Root, err)
	}
	floor := x.dr.minFree()
	if size < 0 || floor < 0 {
		return fmt.Errorf("%w: negative size or floor", ErrInsufficientSpace)
	}
	need := uint64(size) + uint64(floor)
	if need < uint64(size) || u.Avail < need {
		return fmt.Errorf("%w: %s has %d bytes available, the transfer needs %d plus a floor of %d", ErrInsufficientSpace, x.dst.Root, u.Avail, size, floor)
	}
	return nil
}

// nearestExisting is p, or its nearest existing ancestor.
func nearestExisting(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

func statUsage(abs string) (Usage, error) {
	total, free, err := Stats(abs)
	return Usage{Total: total, Avail: free}, err
}

// ---- Replace (compare-and-swap) ----

// replaceAside is the CAS: dst's current SHA-256 must equal Replace.ExpectSHA256.
// The old file is renamed to <dst>.mediacore-replaced-<unix> (the journal says so
// first), and the bytes actually moved aside are hashed once more, so a writer
// that slipped in between the first hash and the rename is caught.
func (x *xfer) replaceAside() error {
	cur, err := x.m.hashVia(x.ctx, x.dfs, x.dst.Rel, false)
	if err != nil {
		return err
	}
	if cur.SHA256 != x.opts.Replace.ExpectSHA256 {
		return fmt.Errorf("%w: %s", ErrReplaceMismatch, x.dst)
	}
	aside := fmt.Sprintf("%s%s%d", x.dst.Rel, asideInfix, x.m.seam.now().Unix())
	if _, err := x.dfs.Lstat(aside); err == nil {
		aside += "-" + x.row.ID
	}
	x.row.Aside = aside
	if err := x.persist(x.ctx, stReplacing); err != nil {
		x.row.Aside = ""
		return err
	}
	if err := x.dfs.RenameNoReplace(x.dst.Rel, aside); err != nil {
		return err
	}
	moved, err := x.m.hashVia(x.ctx, x.dfs, aside, false)
	if err != nil || moved.SHA256 != x.opts.Replace.ExpectSHA256 {
		x.restoreAside()
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s changed while it was being replaced", ErrReplaceMismatch, x.dst)
	}
	return nil
}

// restoreAside puts the replaced file back when the replacement did not happen.
func (x *xfer) restoreAside() {
	if x.row.Aside == "" {
		return
	}
	if err := x.dfs.RenameNoReplace(x.row.Aside, x.dst.Rel); err != nil {
		x.m.log.Error("library: could not restore a replaced file; recovery will try", "root", x.dst.Root, "err", err)
		return
	}
	x.row.Aside = ""
}

// dropAside removes the kept old file once the new one is durable.
func (x *xfer) dropAside() error {
	if x.row.Aside == "" {
		return nil
	}
	if err := x.dfs.Remove(x.row.Aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	x.row.Aside = ""
	return syncDir(x.dfs, x.dstDir())
}
