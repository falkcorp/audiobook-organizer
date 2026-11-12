// file: core/backup/fsutil.go
// version: 1.0.1
// guid: 8a2b1f7e-d3c4-45e6-9f10-2b7c5e8d1a90
// last-edited: 2026-11-12

package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// writeFileAtomic writes data to p through a temp file created O_EXCL with mode
// 0600 from the start, fsynced, then renamed over p (a sidecar is never seen
// half-written). It refuses to follow a link at either name. The temp file's
// name is unique to the call, so a stale temp file of a crashed write (which the
// catalogue scan reaps once it is old) never makes a later write of the same
// name fail.
func writeFileAtomic(p string, data []byte) (err error) {
	f, tmp, err := createTemp(p)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if err == nil {
			return
		}
		if !closed {
			err = errors.Join(err, f.Close())
		}
		if rerr := os.Remove(tmp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	closed = true
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// createTemp creates "<p>.<random>.tmp" O_EXCL, 0600, and returns it with its
// name. A name that already exists is another call's (or a stale file's): the
// next random one is tried.
func createTemp(p string) (*os.File, string, error) {
	for range 8 {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, "", err
		}
		tmp := p + "." + hex.EncodeToString(b[:]) + ".tmp"
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return f, tmp, nil
	}
	return nil, "", fmt.Errorf("backup: no free temporary name beside %s", filepath.Base(p))
}

// syncDir fsyncs a directory so a rename or create in it survives a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// mkdirPrivate makes dir and any missing parents mode 0700 from creation.
func mkdirPrivate(dir string) error { return os.MkdirAll(dir, 0o700) }

// runDir is a private directory the artifacts and checkpoint are gathered in:
// every file created in it is O_EXCL, 0600, confined by os.Root.
type runDir struct {
	path string
	root *os.Root
}

func newRunDir(p string) (*runDir, error) {
	if err := os.Mkdir(p, 0o700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(p)
	if err != nil {
		_ = os.RemoveAll(p)
		return nil, err
	}
	return &runDir{path: p, root: r}, nil
}

func (r *runDir) Close() error { return r.root.Close() }

// Create is ArtifactWriter.Create: the mode is accepted for the interface and
// ignored, because every file in a run directory is 0600.
func (r *runDir) Create(rel string, _ fs.FileMode) (io.WriteCloser, error) {
	return r.create(rel)
}

func (r *runDir) create(rel string) (*os.File, error) {
	clean, err := backuparchive.CleanName(rel, false)
	if err != nil {
		return nil, err
	}
	if dir := path.Dir(clean); dir != "." {
		if err := r.root.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return r.root.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// copyTree copies the regular files of dir into the run directory under
// prefix. A link, device or any other non-regular file is an error: nothing is
// followed and nothing is skipped silently.
func (r *runDir) copyTree(ctx context.Context, dir, prefix string) (err error) {
	src, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, src.Close()) }()
	return fs.WalkDir(src.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == "." || d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s", backuparchive.ErrNotRegularFile, path.Join(prefix, p))
		}
		in, err := src.Open(p)
		if err != nil {
			return err
		}
		out, err := r.create(path.Join(prefix, p))
		if err != nil {
			return errors.Join(err, in.Close())
		}
		_, cerr := io.Copy(out, ctxReader{ctx, in})
		return errors.Join(cerr, out.Sync(), out.Close(), in.Close())
	})
}

// copyFile copies one regular file (never a link) into the run directory.
func (r *runDir) copyFile(ctx context.Context, src, rel string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", backuparchive.ErrNotRegularFile, filepath.Base(src))
	}
	in, _, err := backuparchive.OpenRegular(src, 0)
	if err != nil {
		return err
	}
	out, err := r.create(rel)
	if err != nil {
		return errors.Join(err, in.Close())
	}
	_, cerr := io.Copy(out, ctxReader{ctx, in})
	return errors.Join(cerr, out.Sync(), out.Close(), in.Close())
}

// putBytes writes data as rel.
func (r *runDir) putBytes(rel string, data []byte) error {
	out, err := r.create(rel)
	if err != nil {
		return err
	}
	_, werr := out.Write(data)
	return errors.Join(werr, out.Sync(), out.Close())
}

// hashTree hashes every regular file under the run directory with a bounded
// pool (workers goroutines), one file per task, and returns the entries sorted
// by path with Class unset. A non-regular file is an error. The slice is
// written by index, so the pool shares no mutable state.
func (r *runDir) hashTree(ctx context.Context, workers int) ([]Entry, error) {
	var paths []string
	err := fs.WalkDir(r.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s", backuparchive.ErrNotRegularFile, p)
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]Entry, len(paths))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, workers))
	for i, p := range paths {
		g.Go(func() error {
			f, err := r.root.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			n, err := io.Copy(h, ctxReader{gctx, f})
			if err := errors.Join(err, f.Close()); err != nil {
				return err
			}
			out[i] = Entry{Path: p, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// dirSize sums the sizes of the regular files under dir. It is an estimate for
// the free-space check: a file or directory that vanishes mid-walk (or a dir
// that does not exist yet) counts zero, but any other error (an unreadable
// directory) is returned, so the check fails closed instead of undercounting.
func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n, err
}

// closeRead closes a handle that was only read. A close error on one cannot
// change what was read or verified, so it is logged instead of returned (a
// writer's close error is always returned: it decides whether the bytes landed).
func (s *Svc) closeRead(what string, c io.Closer) {
	if err := c.Close(); err != nil {
		s.o.log().Warn("backup: closing "+what, "error", err)
	}
}

// copySum copies r to w and returns the SHA-256 of what it copied and its length.
func copySum(r io.Reader, w io.Writer) (sum string, n int64, err error) {
	h := sha256.New()
	n, err = io.Copy(io.MultiWriter(w, h), r)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
