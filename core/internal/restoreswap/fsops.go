// file: core/internal/restoreswap/fsops.go
// version: 1.0.0
// guid: 441df42a-91a3-4c8c-ad83-3f4a17489cdf
// last-edited: 2026-11-07

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

// mkdirAllSynced creates dir and its missing parents (0700) and fsyncs the
// parent of every directory it created, so the chain survives a crash
// before anything is renamed into it.
func mkdirAllSynced(fs vfs.FS, dir string) error {
	dir = filepath.Clean(dir)
	var created []string
	for d := dir; ; d = filepath.Dir(d) {
		ok, err := exists(fs, d)
		if err != nil {
			return err
		}
		if ok || filepath.Dir(d) == d {
			break
		}
		created = append(created, d)
	}
	if len(created) == 0 {
		return nil
	}
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("restoreswap: mkdir %s: %w", dir, err)
	}
	for _, d := range created {
		if err := syncDir(fs, filepath.Dir(d)); err != nil {
			return err
		}
	}
	return nil
}

// mv is the idempotent rename of 05-backup §5B.3.4's step table: rename only
// if the source exists and the destination does not.
//
//	src  dst  action
//	yes  no   mkdir the destination's parent (synced), rename, fsync the
//	          destination's directory, then the source's
//	no   yes  already moved: nothing
//	no   no   the item never existed (an absent sidecar or location file)
//	yes  yes  see below
//
// A POSIX rename never leaves both names. The in-memory crash model of the
// kit does when a kill falls between the two directory fsyncs: each
// directory's synced entries are cloned on their own. twin reports, from
// state the caller proves durable, that the two are one item's two names;
// then the destination is discarded and the rename redone, which is the
// same end state whichever copy is kept. Without that proof boot halts with
// ErrSwapAmbiguous naming both paths and deletes nothing. The destination's
// directory is fsynced before the source's, so a kill between them leaves
// both names (recoverable), never neither.
//
// Every case is decided by an explicit Stat: an in-memory rename replaces
// an existing destination silently, so Rename is never trusted to refuse.
func mv(fs vfs.FS, src, dst string, twin func() (bool, error)) error {
	srcOK, err := exists(fs, src)
	if err != nil {
		return err
	}
	dstOK, err := exists(fs, dst)
	if err != nil {
		return err
	}
	switch {
	case !srcOK:
		return nil
	case dstOK:
		proven := false
		if twin != nil {
			if proven, err = twin(); err != nil {
				return err
			}
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
	if err := fs.Rename(src, dst); err != nil {
		return fmt.Errorf("restoreswap: rename %s to %s: %w", src, dst, err)
	}
	if err := syncDir(fs, filepath.Dir(dst)); err != nil {
		return err
	}
	return syncDir(fs, filepath.Dir(src))
}

// always is mv's twin proof for a rename whose destination nothing else
// ever writes (a path in the swap's private namespace).
func always() (bool, error) { return true, nil }

// existsProof is mv's twin proof "path exists".
func existsProof(fs vfs.FS, path string) func() (bool, error) {
	return func() (bool, error) { return exists(fs, path) }
}

// copyIfExists copies src to dst atomically (temp file + fsync, rename,
// directory fsync) when src exists; an absent src is not an error (R10-6:
// backup.location.json is written only from R2). A dst that exists is
// replaced from src again.
func copyIfExists(fs vfs.FS, src, dst string) error {
	f, err := fs.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("restoreswap: open %s: %w", src, err)
	}
	data, err := io.ReadAll(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("restoreswap: read %s: %w", src, err)
	}
	return writeFileAtomic(fs, dst, data)
}

// writeFileAtomic writes data to path: a temp file in the same directory,
// fsynced, renamed over path, the directory fsynced. data is not used after
// the write (a vfs.File may take ownership of the buffer).
func writeFileAtomic(fs vfs.FS, path string, data []byte) error {
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

// removeSynced removes path when it exists and fsyncs its directory.
func removeSynced(fs vfs.FS, path string) error {
	ok, err := exists(fs, path)
	if err != nil || !ok {
		return err
	}
	if err := fs.Remove(path); err != nil {
		return fmt.Errorf("restoreswap: remove %s: %w", path, err)
	}
	return syncDir(fs, filepath.Dir(path))
}

// slotMove renames a slot file (pending.json to inflight.json or
// rejected.json, inflight.json to rejected.json) with both directories
// fsynced, destination first. When both names exist (a kill between the
// fsyncs in the in-memory model, or one boot's rename and the next boot's
// re-run) the later file wins and the earlier is removed: the two hold the
// same intent.
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
	case !srcOK:
		return nil
	}
	if err := fs.Rename(src, dst); err != nil {
		return fmt.Errorf("restoreswap: rename %s to %s: %w", src, dst, err)
	}
	if err := syncDir(fs, filepath.Dir(dst)); err != nil {
		return err
	}
	return syncDir(fs, filepath.Dir(src))
}
