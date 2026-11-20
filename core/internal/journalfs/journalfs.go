// file: core/internal/journalfs/journalfs.go
// version: 1.0.1
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
// an append is a write at the end offset, the lock is a set of locked paths
// shared by every adapter of one boot (the Seams.Shared they are built over; flock(2)
// conflicts between open file descriptions, so two opens in one boot exclude
// each other whichever adapter made them), and a truncation, or an O_TRUNC
// open of an existing file, replaces the file with a fresh one at the same
// name (Pebble's MemFS has no in-place truncate).
//
// A real truncation (ftruncate) leaves the old contents or the truncated ones
// after a crash, becomes durable at the file's own fsync and never fsyncs the
// directory. The replacement matches: its directory entry is unsynced until
// the file's Sync, so a minimal crash clone holds the old file and a mixed one
// (bootseamtest.Options.MixedSeeds) either. The Sync makes the entry durable
// as well, unrecorded, because that is not an operation of the journal's, with
// two limits. The entry of a file the journal created and has not yet synced
// the directory for (the adapter tracks these) stays unsynced, so a rollback
// of a new journal's header never makes its name durable ahead of the
// journal.dirsync that does. And Pebble's MemFS can only sync a whole
// directory, so a Sync that does sync the entry also makes the directory's
// other unsynced entries durable, which a real fsync does not. A truncation
// itself never syncs the directory.
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
// production boot), the seam's in-memory filesystem otherwise. Every value
// From returns over one boot's Seams.Shared shares one lock set (and one
// record of the unsynced entries it created), so the journal opened through
// two of them in a boot still excludes itself.
func From(ctx context.Context) (journal.FS, error) {
	seams := bootseam.From(ctx)
	if seams == nil || seams.FS == nil || seams.FS == vfs.Default {
		return journal.OSFS(), nil
	}
	base, ok := vfs.Root(seams.FS).(*vfs.MemFS)
	if !ok {
		return nil, fmt.Errorf("journalfs: the boot filesystem %T is neither the real one nor in memory", vfs.Root(seams.FS))
	}
	if seams.Shared == nil {
		return nil, errors.New("journalfs: the seam has an in-memory filesystem and no Shared: its adapters could not share one lock set, so two opens of one journal in a boot would not exclude each other; build the Seams with bootseamtest.Recorder.Seams or set Shared")
	}
	return &memFS{mem: seams.FS, base: base, seams: seams, state: stateOf(seams)}, nil
}

// fsState is what the adapters over one in-memory filesystem share: the paths
// of the open, locked files, and the paths of the files and renames they made
// whose directory entry no SyncDir has made durable yet.
type fsState struct {
	mu    sync.Mutex
	held  map[string]bool
	fresh map[string]bool
}

// stateOf is the fsState the adapters of one boot share. It lives on the
// boot's bootseam.Shared (which Recorder.Seams hands to every Seams it makes,
// so two Seams over one recorder, and the two From calls over them, find one
// state), not in a package variable: the state is per boot, and a process-wide
// map keyed on the filesystem would be the global state 03 R5 forbids. From
// refuses a seam with an in-memory filesystem and no Shared, so a boot cannot
// end up with a private state per adapter.
func stateOf(seams *bootseam.Seams) *fsState {
	st := &fsState{held: map[string]bool{}, fresh: map[string]bool{}}
	got, _ := seams.Shared.LoadOrStore(stateKey{}, st)
	return got.(*fsState)
}

// stateKey is the key of the fsState on a bootseam.Shared.
type stateKey struct{}

// markFresh records that path's directory entry is not durable yet.
func (s *fsState) markFresh(path string) {
	s.mu.Lock()
	s.fresh[path] = true
	s.mu.Unlock()
}

// forget drops path from the unsynced entries (it was removed or replaced).
func (s *fsState) forget(path string) {
	s.mu.Lock()
	delete(s.fresh, path)
	s.mu.Unlock()
}

// isFresh reports whether path's directory entry is not durable yet.
func (s *fsState) isFresh(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fresh[path]
}

// synced drops every entry of dir from the unsynced ones: a directory fsync
// made them durable.
func (s *fsState) synced(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := range s.fresh {
		if filepath.Dir(p) == dir {
			delete(s.fresh, p)
		}
	}
}

// memFS is journal.FS over the kit's in-memory filesystem.
type memFS struct {
	mem   vfs.FS
	base  *vfs.MemFS // the in-memory filesystem under mem's wrappers
	seams *bootseam.Seams
	state *fsState
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

func (m *memFS) Rename(oldpath, newpath string) error {
	if err := m.mem.Rename(oldpath, newpath); err != nil {
		return err
	}
	m.state.forget(oldpath)
	m.state.markFresh(newpath) // the renamed entry is durable at the directory's fsync
	return nil
}

// Remove is os.Remove. A name that is not there is os.ErrNotExist, answered
// here: errorfs reports a remove of a missing name as nil before it reaches
// the in-memory filesystem, and the journal's callers (RemoveMoved, the
// cleanups) must see the error os gives.
func (m *memFS) Remove(path string) error {
	if _, err := m.mem.Stat(path); errors.Is(err, os.ErrNotExist) {
		return &fs.PathError{Op: "remove", Path: path, Err: fs.ErrNotExist}
	}
	if err := m.mem.Remove(path); err != nil {
		return err
	}
	m.state.forget(path)
	return nil
}

func (m *memFS) SyncDir(dir string) error {
	d, err := m.mem.OpenDir(dir)
	if err != nil {
		return err
	}
	err = errors.Join(d.Sync(), d.Close())
	if err == nil {
		m.state.synced(dir)
	}
	return err
}

// Open is os.OpenFile's flags over the in-memory filesystem. O_CREATE of a
// missing file is a create; O_EXCL refuses an existing one; O_TRUNC of an
// existing one replaces it with a fresh file at the name (one operation, as the
// truncating open is one, whose directory entry the file's Sync makes durable:
// the package comment); anything else opens the file where it is.
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
	if !exists {
		m.state.markFresh(path)
	}
	mf := &memFile{fs: m, path: path, f: f, write: write, append: flag&os.O_APPEND != 0, pending: exists && flag&os.O_TRUNC != 0 && write}
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
	// pending is a directory entry that replaced the file's and is not yet
	// synced: a truncation's, which the file's Sync makes durable.
	pending bool
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

// Sync fsyncs the file, and its directory entry when a truncation replaced it
// (the package comment). The directory sync is made on the filesystem under
// the recorder's wrappers: it is part of the file's fsync and not an operation
// the journal makes, so it is neither recorded nor a kill point.
func (f *memFile) Sync() error {
	if err := f.f.Sync(); err != nil {
		return err
	}
	if !f.pending {
		return nil
	}
	f.pending = false
	if f.fs.state.isFresh(f.path) {
		return nil // the name is unsynced: the journal's own directory fsync makes it durable
	}
	dir := filepath.Dir(f.path)
	d, err := f.fs.base.OpenDir(dir)
	if err != nil {
		return err
	}
	if err := errors.Join(d.Sync(), d.Close()); err != nil {
		return err
	}
	f.fs.state.synced(dir)
	return nil
}

// Truncate cuts the file to size as a replace (the package comment).
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
	nf2, err := f.fs.mem.OpenReadWrite(f.path, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	old := f.f
	f.f, f.end, f.pending = nf2, size, true
	return old.Close()
}

// Lock takes the file's exclusive lock: a second Lock of a path another open
// file holds is journal.ErrLocked, as flock(2) refuses a second open file
// description.
func (f *memFile) Lock() error {
	st := f.fs.state
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.held[f.path] && !f.locked {
		return fmt.Errorf("%w (%s)", journal.ErrLocked, f.path)
	}
	st.held[f.path], f.locked = true, true
	return nil
}

func (f *memFile) Close() error {
	if f.closed {
		return fs.ErrClosed
	}
	f.closed = true
	if f.locked {
		st := f.fs.state
		st.mu.Lock()
		delete(st.held, f.path)
		st.mu.Unlock()
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
