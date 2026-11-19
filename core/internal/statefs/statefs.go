// file: core/internal/statefs/statefs.go
// version: 1.0.0
// guid: 1089300d-225c-4ffd-861d-6d728bb11303
// last-edited: 2026-11-19

// Package statefs is the labelled state-directory filesystem (05-storage
// §5S.3.16, COR10-7): the way core's boot writers that are not Pebble's and
// not the swap's reach the state directory, so the crash-matrix kit
// (coretest/crashmatrix) records, labels, kills and fails their operations
// like every other boot writer's. Today that is the keyring (secrets), the
// boot record (config) and P0's creation of the state directory (the kernel).
//
// In production a Dir is an *os.Root over the directory: a name that leaves
// it, through ".." or a symbolic link, is refused as os.Root refuses it, the
// modes and the exclusive create are the kernel's, and nothing else changes.
// Under a crash-matrix seam (bootseam.Seams.FS over an in-memory
// filesystem) it is that filesystem at the same absolute path. Each mutating
// call takes its crash-point label (06 §6.5.1's writer table) and runs
// inside that label's region; reads take none.
//
// The write sequences (temporary file, fsync, rename or link, directory
// fsync) stay with their callers, which run them unchanged over either
// backend: a Dir supplies primitives only, so the matrix breaks the same
// sequence production runs.
package statefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
)

// Dir is one state directory, opened once and closed by its owner.
type Dir struct {
	path  string
	root  *os.Root // production
	mem   vfs.FS   // under an in-memory seam; root is nil then
	seams *bootseam.Seams
	// ownsRoot: Open opened root and Close closes it; FromRoot's caller
	// keeps its own.
	ownsRoot bool
}

// Open opens dir, an existing directory: an *os.Root over it in production
// (no bootseam.Seams in ctx, or Seams with no FS), the seam's filesystem when
// it is in memory. A seam filesystem that is neither is refused: a write
// through os.Root would go past it, unrecorded, and one through it could not
// keep os.Root's refusals.
func Open(ctx context.Context, dir string) (*Dir, error) {
	seams := bootseam.From(ctx)
	mem, err := memOf(seams)
	if err != nil {
		return nil, err
	}
	if mem == nil {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		return &Dir{path: dir, root: root, seams: seams, ownsRoot: true}, nil
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("statefs: %q is not an absolute path", dir)
	}
	fi, err := mem.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("statefs: open %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return nil, &fs.PathError{Op: "openat", Path: dir, Err: errNotDir}
	}
	return &Dir{path: filepath.Clean(dir), mem: mem, seams: seams}, nil
}

// errNotDir is os.OpenRoot's refusal of a path that is not a directory.
var errNotDir = errors.New("not a directory")

// FromRoot is a Dir over an *os.Root the caller opened and closes (an
// offline command's state directory, an export's destination): no seam, no
// labels. Close does not close root.
func FromRoot(root *os.Root) *Dir { return &Dir{path: root.Name(), root: root} }

// MkdirAll creates dir and its missing parents with perm, as os.MkdirAll,
// through the boot's filesystem, inside label's region.
func MkdirAll(ctx context.Context, label, dir string, perm fs.FileMode) error {
	seams := bootseam.From(ctx)
	mem, err := memOf(seams)
	if err != nil {
		return err
	}
	defer bootseam.RegionOf(seams, label)()
	if mem == nil {
		return os.MkdirAll(dir, perm)
	}
	return mem.MkdirAll(dir, perm)
}

// memOf is the seam's in-memory filesystem, nil for the real one.
func memOf(s *bootseam.Seams) (vfs.FS, error) {
	if s == nil || s.FS == nil || s.FS == vfs.Default {
		return nil, nil
	}
	if _, ok := vfs.Root(s.FS).(*vfs.MemFS); ok {
		return s.FS, nil
	}
	return nil, fmt.Errorf("statefs: the boot filesystem %T is neither the real one nor in memory", vfs.Root(s.FS))
}

// Name is the directory's path.
func (d *Dir) Name() string { return d.path }

// Close releases the directory: the root Open opened. A Dir from FromRoot
// leaves its root to the caller, and an in-memory one holds nothing.
func (d *Dir) Close() error {
	if !d.ownsRoot {
		return nil
	}
	return d.root.Close()
}

// full is name's path on the in-memory filesystem; a name that is not local
// is refused, as os.Root refuses one that leaves the directory.
func (d *Dir) full(op, name string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", &fs.PathError{Op: op, Path: name, Err: errEscapes}
	}
	return filepath.Join(d.path, name), nil
}

// errEscapes is os.Root's refusal of a name that leaves the directory.
var errEscapes = errors.New("path escapes from parent")

// region opens label's region for one operation.
func (d *Dir) region(label string) func() { return bootseam.RegionOf(d.seams, label) }

// Lstat describes name without following a final symbolic link. The
// in-memory filesystem has no links and keeps no modes, so its regular files
// report 0600 and its directories 0700, the modes every state file and
// directory is created with (SEC-9; restorejournal.ChmodFS's rule).
func (d *Dir) Lstat(name string) (fs.FileInfo, error) {
	if d.mem == nil {
		return d.root.Lstat(name)
	}
	p, err := d.full("lstat", name)
	if err != nil {
		return nil, err
	}
	fi, err := d.mem.Stat(p)
	if err != nil {
		return nil, err
	}
	return memInfo{name: filepath.Base(p), fi: fi}, nil
}

// Open opens name for reading.
func (d *Dir) Open(name string) (io.ReadCloser, error) {
	if d.mem == nil {
		return d.root.Open(name)
	}
	p, err := d.full("open", name)
	if err != nil {
		return nil, err
	}
	return d.mem.Open(p)
}

// ReadFile reads name whole.
func (d *Dir) ReadFile(name string) ([]byte, error) {
	if d.mem == nil {
		return d.root.ReadFile(name)
	}
	f, err := d.Open(name)
	if err != nil {
		return nil, err
	}
	b, rerr := io.ReadAll(f)
	return b, errors.Join(rerr, f.Close())
}

// ReadDir lists the directory name, sorted by file name.
func (d *Dir) ReadDir(name string) ([]fs.DirEntry, error) {
	if d.mem == nil {
		return fs.ReadDir(d.root.FS(), filepath.ToSlash(filepath.Clean(name)))
	}
	p, err := d.full("readdirent", name)
	if err != nil {
		return nil, err
	}
	names, err := d.mem.List(p)
	if err != nil {
		return nil, err
	}
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		fi, err := d.mem.Stat(filepath.Join(p, n))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, fs.FileInfoToDirEntry(memInfo{name: n, fi: fi}))
	}
	slices.SortFunc(out, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return out, nil
}

// Chmod sets name's mode. The in-memory filesystem keeps none (Lstat).
func (d *Dir) Chmod(label, name string, mode fs.FileMode) error {
	defer d.region(label)()
	if d.mem == nil {
		return d.root.Chmod(name, mode)
	}
	if _, err := d.Lstat(name); err != nil {
		return err
	}
	return nil
}

// MkdirAll creates the directory name and its missing parents with perm.
func (d *Dir) MkdirAll(label, name string, perm fs.FileMode) error {
	defer d.region(label)()
	if d.mem == nil {
		return d.root.MkdirAll(name, perm)
	}
	p, err := d.full("mkdirat", name)
	if err != nil {
		return err
	}
	return d.mem.MkdirAll(p, perm)
}

// WriteNew creates name, which must not exist, with perm (os.O_EXCL), writes
// data, fsyncs and closes it. On an error after the create the file is left
// for the caller, which removes what it created. In memory the create is a
// Stat then a Create: the kit's boots are one process, and every caller
// creates under a fresh or fixed temporary name it removed first.
func (d *Dir) WriteNew(label, name string, data []byte, perm fs.FileMode) error {
	defer d.region(label)()
	if d.mem == nil {
		f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			return err
		}
		return finish(f, data)
	}
	p, err := d.full("openat", name)
	if err != nil {
		return err
	}
	if _, err := d.mem.Stat(p); err == nil {
		return &fs.PathError{Op: "openat", Path: name, Err: fs.ErrExist}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := d.mem.Create(p, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	// Pebble's MemFS inverts the buffer it was handed after copying it
	// when its invariants are on (-race): write a copy, never the caller's.
	return finish(f, bytes.Clone(data))
}

// finish writes data to f, fsyncs and closes it.
func finish(f interface {
	io.Writer
	Sync() error
	Close() error
}, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

// Link makes newname a hard link to oldname; it fails with fs.ErrExist when
// newname exists, which is the no-replace create's exclusion.
func (d *Dir) Link(label, oldname, newname string) error {
	defer d.region(label)()
	if d.mem == nil {
		return d.root.Link(oldname, newname)
	}
	op, err := d.full("linkat", oldname)
	if err != nil {
		return err
	}
	np, err := d.full("linkat", newname)
	if err != nil {
		return err
	}
	return d.mem.Link(op, np)
}

// Rename renames oldname to newname, replacing it.
func (d *Dir) Rename(label, oldname, newname string) error {
	defer d.region(label)()
	if d.mem == nil {
		return d.root.Rename(oldname, newname)
	}
	op, err := d.full("renameat", oldname)
	if err != nil {
		return err
	}
	np, err := d.full("renameat", newname)
	if err != nil {
		return err
	}
	return d.mem.Rename(op, np)
}

// Remove removes the file name.
func (d *Dir) Remove(label, name string) error {
	defer d.region(label)()
	if d.mem == nil {
		return d.root.Remove(name)
	}
	p, err := d.full("unlinkat", name)
	if err != nil {
		return err
	}
	return d.mem.Remove(p)
}

// SyncDir fsyncs the directory name, so a create, link, rename or remove in
// it is durable (in memory an unsynced entry is lost by a crash clone).
func (d *Dir) SyncDir(label, name string) error {
	defer d.region(label)()
	if d.mem == nil {
		f, err := d.root.Open(name)
		if err != nil {
			return err
		}
		return errors.Join(f.Sync(), f.Close())
	}
	p, err := d.full("open", name)
	if err != nil {
		return err
	}
	f, err := d.mem.OpenDir(p)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// memInfo is an in-memory entry's FileInfo with the private modes (Lstat).
type memInfo struct {
	name string
	fi   os.FileInfo
}

func (m memInfo) Name() string { return m.name }
func (m memInfo) Size() int64  { return m.fi.Size() }
func (m memInfo) Mode() fs.FileMode {
	if m.fi.IsDir() {
		return fs.ModeDir | 0o700
	}
	return 0o600
}
func (m memInfo) ModTime() time.Time { return m.fi.ModTime() }
func (m memInfo) IsDir() bool        { return m.fi.IsDir() }
func (m memInfo) Sys() any           { return nil }
