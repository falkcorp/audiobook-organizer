// file: core/internal/journalfs/journalfs.go
// version: 1.0.0
// guid: 9c78ddbb-cfba-47f5-a91a-8282b5264fac
// last-edited: 2026-11-19

// Package journalfs is the labelled filesystem of the revocation journal's
// file writers (05-backup §5B.3.6; 05-storage §5S.3.16): the journal file,
// backup.location.json, the journal move's temporary copy and the source it
// removes. It is the bridge between backup/journal, whose FS interface is
// standard-library only (the journal may import nothing outside entity and
// storage), and the crash-matrix kit's per-boot seam (internal/bootseam), so
// the kit records, labels, kills and fails those operations like every other
// boot writer's.
//
// In production From returns journal.OSFS(), the real filesystem with no
// region and no recorder anywhere on the path. Under a crash-matrix seam
// (bootseam.Seams.FS over an in-memory filesystem) it is that filesystem at
// the same absolute paths; each region the journal opens becomes a
// bootseam region. A seam filesystem that is neither the real one nor in
// memory is refused: a write through os would go past it, unrecorded.
//
// The in-memory filesystem has no append mode, no ftruncate, no file lock and
// no symbolic links, so this package models what the journal needs of them:
// an append is a write at the end offset, the lock is a per-boot set of
// locked paths, and a truncation is an atomic replace (a durable copy of the
// kept prefix renamed over the file and the directory fsynced), whose two
// outcomes are the two outcomes of an ftruncate. A crash between the steps
// of that replace leaves the file as it was, which is the state before the
// truncation.
package journalfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam"
)

// From is the filesystem the journal uses in the boot ctx belongs to:
// journal.OSFS() when ctx carries no seam or one with no filesystem (every
// production boot), the seam's in-memory filesystem otherwise.
func From(ctx context.Context) (journal.FS, error) {
	seams := bootseam.From(ctx)
	if seams == nil || seams.FS == nil || seams.FS == vfs.Default {
		return journal.OSFS(), nil
	}
	if _, ok := vfs.Root(seams.FS).(*vfs.MemFS); !ok {
		return nil, fmt.Errorf("journalfs: the boot filesystem %T is neither the real one nor in memory", vfs.Root(seams.FS))
	}
	return &memFS{mem: seams.FS, seams: seams, locked: map[string]bool{}}, nil
}

// memFS is journal.FS over the kit's in-memory filesystem.
type memFS struct {
	mem   vfs.FS
	seams *bootseam.Seams

	mu     sync.Mutex
	locked map[string]bool // the paths of open, locked files
}

func (m *memFS) Region(label string) func() { return bootseam.RegionOf(m.seams, label) }

func (m *memFS) Lstat(path string) (fs.FileInfo, error) {
	fi, err := m.mem.Stat(path)
	if err != nil {
		return nil, err
	}
	return memInfo{name: filepath.Base(path), fi: fi}, nil
}

func (m *memFS) ReadFile(path string) ([]byte, error) {
	f, err := m.mem.Open(path)
	if err != nil {
		return nil, err
	}
	b, rerr := io.ReadAll(f)
	return b, errors.Join(rerr, f.Close())
}

func (m *memFS) MkdirAll(path string, _ fs.FileMode) error {
	// os.MkdirAll stats first and never calls mkdir(2) on a directory that
	// exists, so no crash or error can strike there in production and the kit
	// must not record one.
	if fi, err := m.mem.Stat(path); err == nil && fi.IsDir() {
		return nil
	}
	return m.mem.MkdirAll(path, 0o700)
}

func (m *memFS) Rename(oldpath, newpath string) error { return m.mem.Rename(oldpath, newpath) }
func (m *memFS) Remove(path string) error             { return m.mem.Remove(path) }

func (m *memFS) SyncDir(dir string) error {
	d, err := m.mem.OpenDir(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// Open is os.OpenFile's flags over the in-memory filesystem. O_CREATE of a
// missing file is a create; O_EXCL refuses an existing one; O_TRUNC of an
// existing one is a create of a fresh file at the name (one operation, as the
// truncating open is one); anything else opens the file where it is.
func (m *memFS) Open(path string, flag int, _ fs.FileMode) (journal.FSFile, error) {
	write := flag&(os.O_WRONLY|os.O_RDWR) != 0
	fi, serr := m.mem.Stat(path)
	exists := serr == nil
	switch {
	case !exists && !errors.Is(serr, os.ErrNotExist):
		return nil, serr
	case exists && fi.IsDir():
		if write {
			return nil, &fs.PathError{Op: "open", Path: path, Err: errors.New("is a directory")}
		}
		f, err := m.mem.Open(path)
		if err != nil {
			return nil, err
		}
		return &memFile{fs: m, path: path, f: f, dir: true}, nil
	case exists && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrExist}
	case !exists && flag&os.O_CREATE == 0:
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	var (
		f   vfs.File
		err error
	)
	switch {
	case !exists, flag&os.O_TRUNC != 0 && write:
		f, err = m.mem.Create(path, vfs.WriteCategoryUnspecified)
	case write:
		f, err = m.mem.OpenReadWrite(path, vfs.WriteCategoryUnspecified)
	default:
		f, err = m.mem.Open(path)
	}
	if err != nil {
		return nil, err
	}
	mf := &memFile{fs: m, path: path, f: f, write: write, append: flag&os.O_APPEND != 0}
	if fi, err := f.Stat(); err == nil {
		mf.end = fi.Size()
	}
	return mf, nil
}

// memFile is a journal.FSFile over a vfs.File.
type memFile struct {
	fs     *memFS
	path   string
	f      vfs.File
	write  bool
	append bool
	dir    bool
	end    int64 // the file's length, which an append writes at
	locked bool
	closed bool
}

func (f *memFile) Name() string { return f.path }

func (f *memFile) Read(p []byte) (int, error) {
	if f.dir {
		return 0, &fs.PathError{Op: "read", Path: f.path, Err: errors.New("is a directory")}
	}
	return f.f.Read(p)
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	fi, err := f.f.Stat()
	if err != nil {
		return nil, err
	}
	return memInfo{name: filepath.Base(f.path), fi: fi}, nil
}

// Write writes at the end of an O_APPEND file and at the file's position
// otherwise. A copy of p is written: Pebble's MemFS inverts the buffer it was
// handed when its invariants are on (-race).
func (f *memFile) Write(p []byte) (int, error) {
	if !f.write {
		return 0, &fs.PathError{Op: "write", Path: f.path, Err: errors.New("bad file descriptor")}
	}
	buf := append([]byte(nil), p...)
	if !f.append {
		n, err := f.f.Write(buf)
		f.end += int64(n)
		return n, err
	}
	n, err := f.f.WriteAt(buf, f.end)
	f.end += int64(n)
	return n, err
}

func (f *memFile) Sync() error { return f.f.Sync() }

// Truncate cuts the file to size as an atomic replace (the package comment).
func (f *memFile) Truncate(size int64) error {
	if size == f.end {
		return nil
	}
	data := make([]byte, size)
	if _, err := f.f.ReadAt(data, 0); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	tmp := f.path + ".truncating"
	nf, err := f.fs.mem.Create(tmp, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	if _, err := nf.Write(data); err != nil {
		return errors.Join(err, nf.Close(), f.fs.mem.Remove(tmp))
	}
	if err := errors.Join(nf.Sync(), nf.Close()); err != nil {
		return errors.Join(err, f.fs.mem.Remove(tmp))
	}
	if err := f.fs.mem.Rename(tmp, f.path); err != nil {
		return errors.Join(err, f.fs.mem.Remove(tmp))
	}
	if err := f.fs.SyncDir(filepath.Dir(f.path)); err != nil {
		return err
	}
	nf2, err := f.fs.mem.OpenReadWrite(f.path, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	old := f.f
	f.f, f.end = nf2, size
	return old.Close()
}

// Lock takes the file's exclusive lock: a second Lock of a path another open
// file holds is journal.ErrLocked, as flock(2) refuses a second open file
// description.
func (f *memFile) Lock() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.fs.locked[f.path] && !f.locked {
		return fmt.Errorf("%w (%s)", journal.ErrLocked, f.path)
	}
	f.fs.locked[f.path], f.locked = true, true
	return nil
}

func (f *memFile) Close() error {
	if f.closed {
		return fs.ErrClosed
	}
	f.closed = true
	if f.locked {
		f.fs.mu.Lock()
		delete(f.fs.locked, f.path)
		f.fs.mu.Unlock()
	}
	return f.f.Close()
}

// memInfo is an in-memory entry's FileInfo with the private modes the
// journal creates its files with (0600; a directory 0700), the in-memory
// filesystem keeping none.
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
