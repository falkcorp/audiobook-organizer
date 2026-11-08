// file: core/internal/restoreswap/fsops.go
// version: 1.0.5
// guid: 441df42a-91a3-4c8c-ad83-3f4a17489cdf
// last-edited: 2026-11-08

package restoreswap

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ErrSwapAmbiguous: an idempotent rename found its source and its
// destination both present without the proof that they are one item's two
// names (see mv). Boot halts naming both paths; nothing is deleted.
var ErrSwapAmbiguous = errors.New("restoreswap: a swap rename finds both its source and its destination")

// exists reports whether path exists on fs; an error other than not-exist
// is returned.
func exists(fs vfs.FS, path string) (bool, error) {
	_, err := fs.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("restoreswap: stat %s: %w", path, err)
	}
}

// syncDir fsyncs a directory so a create, rename or remove in it is durable.
func syncDir(fs vfs.FS, dir string) error {
	d, err := fs.OpenDir(dir)
	if err != nil {
		return fmt.Errorf("restoreswap: open directory %s: %w", dir, err)
	}
	serr := d.Sync()
	cerr := d.Close()
	if err := errors.Join(serr, cerr); err != nil {
		return fmt.Errorf("restoreswap: fsync directory %s: %w", dir, err)
	}
	return nil
}

// mkdirAllSynced is restorejournal.MkdirAllSynced with dir as its own root:
// dir and its missing parents (0700), the parent of each created directory
// fsynced. A live directory, or one the swap made durable before, needs no
// more; a directory of the swap's private namespace that an earlier boot
// may have left half made goes through Swapper.mkdirDurable.
func mkdirAllSynced(fs vfs.FS, dir string) error { return restorejournal.MkdirAllSynced(fs, dir, dir) }

// mkdirDurable makes dir, a directory of the swap's private namespace, and
// syncs every level of it up to the directory that held the namespace
// before the swap (durableRoot), whether this boot created it or found it
// (R1-21 review M6).
func (s *Swapper) mkdirDurable(dir string) error {
	return restorejournal.MkdirAllSynced(s.fs, s.durableRoot(dir), dir)
}

// durableRoot is the directory above which dir's chain is known durable: the
// state directory for restore/<id>/..., the parent of the .mediacore-restore
// element for the private directory beside a target's filesystem root, and
// dir itself otherwise (a live directory).
func (s *Swapper) durableRoot(dir string) string {
	dir = filepath.Clean(dir)
	if restorejournal.InTree(dir, s.o.StateDir) {
		return filepath.Clean(s.o.StateDir)
	}
	for d := dir; filepath.Dir(d) != d; d = filepath.Dir(d) {
		if filepath.Base(d) == restorejournal.AsideDirName {
			return filepath.Dir(d)
		}
	}
	return dir
}

// mv is the idempotent rename of 05-backup §5B.3.4's step table: rename only
// if the source exists and the destination does not.
//
//	src  dst  action
//	yes  no   mkdir the destination's parent (synced), rename, fsync the
//	          destination's directory, then the source's
//	no   yes  already moved: fsync both directories again, since the boot
//	          that renamed may have failed its fsyncs (R1-21 review M6)
//	no   no   the item never existed (an absent sidecar or location file)
//	yes  yes  see below
//
// Both names present is decided by twinProven: when the two are proven one
// item's two names, the destination is discarded and the rename redone,
// which is the same end state whichever copy is kept; otherwise boot halts
// with ErrSwapAmbiguous naming both paths and deletes nothing. The
// destination's directory is fsynced before the source's, so a kill
// between them leaves both names (recoverable), never neither.
//
// Every case is decided by an explicit Stat: an in-memory rename replaces
// an existing destination silently, so Rename is never trusted to refuse.
func mv(fs vfs.FS, src, dst string, ghost func() (bool, error)) error {
	srcOK, err := exists(fs, src)
	if err != nil {
		return err
	}
	dstOK, err := exists(fs, dst)
	if err != nil {
		return err
	}
	switch {
	case !srcOK && dstOK:
		return syncRename(fs, src, dst)
	case !srcOK:
		return nil
	case dstOK:
		proven, err := twinProven(fs, src, dst, ghost)
		if err != nil {
			return err
		}
		if !proven {
			return fmt.Errorf("%w: %s and %s", ErrSwapAmbiguous, src, dst)
		}
		if err := fs.RemoveAll(dst); err != nil {
			return fmt.Errorf("restoreswap: discard the second name %s: %w", dst, err)
		}
		if err := syncDir(fs, filepath.Dir(dst)); err != nil {
			return err
		}
	}
	if err := mkdirAllSynced(fs, filepath.Dir(dst)); err != nil {
		return err
	}
	return renameSynced(fs, src, dst)
}

// renameSynced renames src to dst in another directory and fsyncs the
// destination's directory, then the source's. When the destination's
// fsync fails the rename is undone before the error returns (R1-21 review
// M6, found by the crash matrix's nested faults): until that fsync the item
// is durably in neither directory's new state, and the next fsync of the
// source's directory, which any later step may make for an item of its
// own, would leave it durable nowhere. Undone, what is visible is what is
// durable. A failed fsync of the source's directory needs no undo: the
// item is durable at its destination, and a crash leaves both names.
func renameSynced(fs vfs.FS, src, dst string) error {
	if err := fs.Rename(src, dst); err != nil {
		return fmt.Errorf("restoreswap: rename %s to %s: %w", src, dst, err)
	}
	if err := syncDir(fs, filepath.Dir(dst)); err != nil {
		if uerr := fs.Rename(dst, src); uerr != nil {
			return errors.Join(err, fmt.Errorf("restoreswap: undo the rename of %s to %s, whose directory was not synced: %w", src, dst, uerr))
		}
		return err
	}
	return syncDir(fs, filepath.Dir(src))
}

// twinProven reports whether src and dst, both present, are one item's two
// names (R1-21 review M3), which mv may collapse by discarding dst. It
// fails closed: what it cannot prove from the filesystem it is given is not
// a twin, and mv halts with ErrSwapAmbiguous.
//
// On the real filesystem (vfs.Root(fs) == vfs.Default) rename(2) is atomic
// and never leaves both names, so both present means something else wrote
// one of them: an older binary with no P0a that created a store at an empty
// live path, an operator's copy. They are twins only when they are two
// links of one file: the same inode to Lstat, neither a directory, in two
// directories (Stat of the parents, symlinks followed, differs). Two paths
// to one directory entry (a symlinked ancestor, a case-insensitive
// spelling) are the same inode as well, but one link: discarding "the
// second name" would delete the only one. Every rename of the swap moves an
// item to another directory, so two links in one directory are refused too.
// A directory is never a twin: one bind-mounted into a second parent is the
// same inode under two different parents, and RemoveAll of either name
// empties both.
// ghost is ignored, so no reasoning about what "cannot" write a path ever
// discards the only copy of an original.
//
// The crash-matrix kit's crashable in-memory filesystem (vfs.Root(fs) is a
// *vfs.MemFS) has no inode identity, and it is the only one that leaves
// both names: a kill between the two directory fsyncs of a rename clones
// each directory's synced entries on its own. There ghost, the caller's
// proof from state it shows durable, decides; nil proves nothing.
//
// Any other filesystem proves nothing (R1-21 verification review): vfs.FS
// has no Lstat, so identity through its Stat would follow a symlink at
// either name and call it its target's twin, and its paths need not be
// the operating system's, so os.Lstat would ask about other files.
func twinProven(fs vfs.FS, src, dst string, ghost func() (bool, error)) (bool, error) {
	root := vfs.Root(fs)
	if _, mem := root.(*vfs.MemFS); mem {
		if ghost == nil {
			return false, nil
		}
		return ghost()
	}
	if root != vfs.Default {
		return false, nil
	}
	a, err := os.Lstat(src)
	if err != nil {
		return false, fmt.Errorf("restoreswap: lstat %s: %w", src, err)
	}
	b, err := os.Lstat(dst)
	if err != nil {
		return false, fmt.Errorf("restoreswap: lstat %s: %w", dst, err)
	}
	if a.IsDir() || b.IsDir() || !os.SameFile(a, b) {
		return false, nil
	}
	pa, err := os.Stat(filepath.Dir(src))
	if err != nil {
		return false, fmt.Errorf("restoreswap: stat %s: %w", filepath.Dir(src), err)
	}
	pb, err := os.Stat(filepath.Dir(dst))
	if err != nil {
		return false, fmt.Errorf("restoreswap: stat %s: %w", filepath.Dir(dst), err)
	}
	return !os.SameFile(pa, pb), nil
}

// memGhost is mv's in-memory proof for a rename whose destination nothing
// but this rename writes (a path in the swap's private namespace, or a live
// path step x emptied): both names there are the crash model's second name.
// Only the in-memory filesystem reads it (twinProven).
func memGhost() (bool, error) { return true, nil }

// existsProof is mv's in-memory proof "path exists" (step d: the live path
// beside its staged source is the source's second name only when step c
// moved the live item away). Only the in-memory filesystem reads it
// (twinProven).
func existsProof(fs vfs.FS, path string) func() (bool, error) {
	return func() (bool, error) { return exists(fs, path) }
}

// privateFile is the mode of every file the swap creates (SEC-9: state and
// store copies, which can hold plaintext secrets, are 0600 in 0700
// directories), set by restorejournal.ChmodFS before the first byte: on a
// filesystem that is neither the real one nor in memory the create fails.
const privateFile os.FileMode = 0o600

// copyIfExists copies src to dst atomically (temp file + fsync, rename,
// directory fsync) with src's permission bits, so step x's rename of the
// copy back restores the original file as it was, when src exists; an
// absent src is not an error (R10-6: backup.location.json is written only
// from R2). A dst that exists is replaced from src again.
func copyIfExists(fs vfs.FS, src, dst string) error {
	f, err := fs.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("restoreswap: open %s: %w", src, err)
	}
	mode := privateFile
	if fi, err := f.Stat(); err != nil {
		_ = f.Close()
		return fmt.Errorf("restoreswap: stat %s: %w", src, err)
	} else if vfs.Root(fs) == vfs.Default {
		mode = fi.Mode().Perm()
	}
	data, err := io.ReadAll(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("restoreswap: read %s: %w", src, err)
	}
	return writeFileAtomic(fs, dst, data, mode)
}

// writeFileAtomic writes data to path with mode (set on the temp file before
// any byte is written): a temp file in the same directory, fsynced, renamed
// over path, the directory fsynced. data is not used after the write (a
// vfs.File may take ownership of the buffer).
func writeFileAtomic(fs vfs.FS, path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := mkdirAllSynced(fs, dir); err != nil {
		return err
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("restoreswap: temp name: %w", err)
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(suffix[:]))
	f, err := fs.Create(tmp, vfs.WriteCategoryUnspecified)
	if err != nil {
		return fmt.Errorf("restoreswap: create %s: %w", tmp, err)
	}
	if err := restorejournal.ChmodFS(fs, tmp, mode); err != nil {
		return errors.Join(fmt.Errorf("restoreswap: create %s: %w", tmp, err), f.Close(), fs.Remove(tmp))
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return errors.Join(fmt.Errorf("restoreswap: write %s: %w", tmp, err), fs.Remove(tmp))
	}
	if err := fs.Rename(tmp, path); err != nil {
		return errors.Join(fmt.Errorf("restoreswap: rename %s: %w", path, err), fs.Remove(tmp))
	}
	return syncDir(fs, dir)
}

// removeSynced removes path when it exists and fsyncs its directory, which
// it also does when path is already gone: the boot that removed it may have
// failed that fsync (R1-21 review M6).
func removeSynced(fs vfs.FS, path string) error {
	ok, err := exists(fs, path)
	if err != nil {
		return err
	}
	if ok {
		if err := fs.Remove(path); err != nil {
			return fmt.Errorf("restoreswap: remove %s: %w", path, err)
		}
	}
	return syncDirIfExists(fs, filepath.Dir(path))
}

// syncRename fsyncs a rename's directories, the destination's first: the
// rename of src to dst found done is made durable again. A source directory
// that does not exist holds no name to bring back (an item that was never
// moved, its private directory never made).
func syncRename(fs vfs.FS, src, dst string) error {
	if err := syncDir(fs, filepath.Dir(dst)); err != nil {
		return err
	}
	return syncDirIfExists(fs, filepath.Dir(src))
}

// syncDirIfExists is syncDir for a directory that may be absent.
func syncDirIfExists(fs vfs.FS, dir string) error {
	if ok, err := exists(fs, dir); err != nil || !ok {
		return err
	}
	return syncDir(fs, dir)
}

// slotMove renames a slot file (pending.json to inflight.json or
// rejected.json, inflight.json to rejected.json) with both directories
// fsynced, destination first. When both names exist (a kill between the
// fsyncs in the in-memory model, or one boot's rename and the next boot's
// re-run) the later file wins and the earlier is removed: the two hold the
// same intent. A rename found done has its directories fsynced again.
func slotMove(fs vfs.FS, src, dst string) error {
	srcOK, err := exists(fs, src)
	if err != nil {
		return err
	}
	dstOK, err := exists(fs, dst)
	if err != nil {
		return err
	}
	switch {
	case srcOK && dstOK:
		return removeSynced(fs, src)
	case dstOK:
		return syncRename(fs, src, dst)
	case !srcOK:
		return nil
	}
	return renameSynced(fs, src, dst)
}
