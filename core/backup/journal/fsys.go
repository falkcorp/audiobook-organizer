// file: core/backup/journal/fsys.go
// version: 1.0.1
// guid: 780df8a6-dd11-4800-9f3d-b61a2a164b5b
// last-edited: 2026-11-19

package journal

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
)

// FS is the filesystem the journal's file writers and readers use: the
// journal file, backup.location.json and its temporary file, the journal
// move's temporary copy and the source it removes. It is the seam of the
// crash-matrix kit (coretest/crashmatrix; 05-storage §5S.3.16): under the kit
// core/internal/journalfs implements it over the boot's recording
// filesystem, so the kit sees every durable operation here with the label of
// the site that ran it, can kill the boot before it and can fail it.
//
// Production uses the real filesystem: every entry point takes an optional
// FS (WithFS, FileOptions.FS, ComponentOptions.FS) and a nil one is OSFS, a
// thin layer over package os that adds no behaviour. The write sequences
// (create, write, fsync, rename, directory fsync) stay in this package's
// callers of an FS, which run them unchanged over either backend, so the kit
// breaks the same sequence production runs.
//
// The names and flags are os's: Open takes os.OpenFile's flags and mode, and
// a backend that is not os interprets O_RDONLY, O_WRONLY, O_RDWR, O_CREATE,
// O_EXCL, O_TRUNC and O_APPEND (the journal's O_NOFOLLOW and O_NONBLOCK exist
// to refuse a planted link and a pipe, which an in-memory filesystem has none
// of). Reads take no label: they are not durable operations.
type FS interface {
	// Open is os.OpenFile.
	Open(path string, flag int, perm fs.FileMode) (FSFile, error)
	// Lstat is os.Lstat: a final symbolic link is described, not followed.
	Lstat(path string) (fs.FileInfo, error)
	// ReadFile is os.ReadFile.
	ReadFile(path string) ([]byte, error)
	// MkdirAll is os.MkdirAll.
	MkdirAll(path string, perm fs.FileMode) error
	// Rename is os.Rename: it replaces newpath atomically.
	Rename(oldpath, newpath string) error
	// Remove is os.Remove.
	Remove(path string) error
	// SyncDir fsyncs the directory dir, so a create, rename or remove in it
	// is durable.
	SyncDir(dir string) error
	// Region marks the durable operations until the returned exit as
	// label's, the writer-table label of 06 §6.5.1 (bootseam.Region). A
	// region opened inside another is the outer one's. OSFS returns a no-op.
	Region(label string) (exit func())
}

// FSFile is an open file of an FS: *os.File's methods the journal uses, and
// the exclusive lock.
type FSFile interface {
	io.Reader
	io.Writer
	Sync() error
	Truncate(size int64) error
	Close() error
	Stat() (fs.FileInfo, error)
	Name() string
	// Lock takes the exclusive, non-blocking lock on the file: ErrLocked
	// when another holder has it, errors.ErrUnsupported (wrapped) where the
	// platform has no file lock. It is held until Close.
	Lock() error
}

// OSFS is the real filesystem, what every entry point uses when it is given
// no FS.
func OSFS() FS { return osFS{} }

// FSOption configures the free functions that touch the journal's files
// (ReadFile, ReadLocation, WriteLocation, CreateBased, MoveFile,
// RemoveMoved).
type FSOption func(*fsConfig)

type fsConfig struct{ fs FS }

// WithFS runs the call over fsys; a nil fsys is OSFS.
func WithFS(fsys FS) FSOption { return func(c *fsConfig) { c.fs = fsys } }

// fsOf is the FS the options select: OSFS when none or only nil ones.
func fsOf(opts ...FSOption) FS {
	var c fsConfig
	for _, o := range opts {
		o(&c)
	}
	return orOS(c.fs)
}

// orOS is fsys, or OSFS when it is nil.
func orOS(fsys FS) FS {
	if fsys == nil {
		return osFS{}
	}
	return fsys
}

// osFS is FS over package os.
type osFS struct{}

func (osFS) Open(path string, flag int, perm fs.FileMode) (FSFile, error) {
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return osFile{f}, nil
}
func (osFS) Lstat(path string) (fs.FileInfo, error)       { return os.Lstat(path) }
func (osFS) ReadFile(path string) ([]byte, error)         { return os.ReadFile(path) }
func (osFS) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }
func (osFS) Rename(oldpath, newpath string) error         { return os.Rename(oldpath, newpath) }
func (osFS) Remove(path string) error                     { return os.Remove(path) }
func (osFS) Region(string) func()                         { return func() {} }

func (osFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", dir, err)
	}
	return errors.Join(d.Sync(), d.Close())
}

// osFile is an *os.File with the lock.
type osFile struct{ *os.File }

func (f osFile) Lock() error { return lockJournal(f.File) }

// The writer-table labels of the journal's file writers (06 §6.5.1; the crash
// kit's crash points). Every durable operation of this package runs inside
// exactly one of them (FS.Region), error paths included.
const (
	// LabelCreate: OpenFile's open of the journal file, which creates it
	// when absent.
	LabelCreate = "journal.create"
	// LabelHeader: the header line of a new journal, written and fsynced.
	LabelHeader = "journal.header"
	// LabelDirSync: the directory fsync that makes a new journal's name
	// durable (OpenFile).
	LabelDirSync = "journal.dirsync"
	// LabelAppend: a line, or several, written and fsynced (Append,
	// AppendLine).
	LabelAppend = "journal.append"
	// LabelRollback: the cut back of a failed append to its previous length.
	LabelRollback = "journal.rollback"
	// LabelCut: the cut of an incomplete last line at open.
	LabelCut = "journal.cut"
	// LabelBaseCreate: CreateBased's exclusive create of the file, its
	// header write and fsync (and the removal of a file that failed them).
	LabelBaseCreate = "journal.base.create"
	// LabelBaseDirSync: CreateBased's directory fsync.
	LabelBaseDirSync = "journal.base.dirsync"
	// LabelLocationTmp: backup.location.json's temporary file: created,
	// written and fsynced.
	LabelLocationTmp = "journal.location.tmp"
	// LabelLocationRename: the rename of the temporary file over
	// backup.location.json.
	LabelLocationRename = "journal.location.rename"
	// LabelLocationDirSync: the state directory's fsync after the rename.
	LabelLocationDirSync = "journal.location.dirsync"
	// LabelLocationCleanup: the removal of a temporary file whose write or
	// rename failed.
	LabelLocationCleanup = "journal.location.cleanup"
	// LabelMoveMkdir: MoveFile's creation of the destination directory.
	LabelMoveMkdir = "journal.move.mkdir"
	// LabelMoveClear: the removal of a crash's leftover temporary copy.
	LabelMoveClear = "journal.move.clear"
	// LabelMoveCopy: the temporary copy: created exclusively, written and
	// fsynced.
	LabelMoveCopy = "journal.move.copy"
	// LabelMoveRename: the rename of the verified copy into place.
	LabelMoveRename = "journal.move.rename"
	// LabelMoveDirSync: the destination directory's fsync.
	LabelMoveDirSync = "journal.move.dirsync"
	// LabelMoveCleanup: the removal of a temporary copy that failed
	// verification or the rename.
	LabelMoveCleanup = "journal.move.cleanup"
	// LabelMoveSource: MoveFile's open of the source journal. The open is
	// read-write and creates a missing file (it is OpenFile's), so a source
	// that vanished between MoveFile's existence check and the open would be
	// created here: under this label and not LabelCreate's, so the kit never
	// takes a move's open for a first open's.
	LabelMoveSource = "journal.move.source"
	// LabelMoveRemoveSource: RemoveMoved, the removal of the old journal once
	// backup.location.json points at the new one.
	LabelMoveRemoveSource = "journal.move.remove_source"
)

// FSLabels is every label of this package's file writers, in the order of the
// writer table's row.
func FSLabels() []string {
	return slices.Clone([]string{
		LabelCreate, LabelHeader, LabelDirSync, LabelAppend, LabelRollback, LabelCut,
		LabelBaseCreate, LabelBaseDirSync,
		LabelLocationTmp, LabelLocationRename, LabelLocationDirSync, LabelLocationCleanup,
		LabelMoveSource, LabelMoveMkdir, LabelMoveClear, LabelMoveCopy, LabelMoveRename, LabelMoveDirSync, LabelMoveCleanup,
		LabelMoveRemoveSource,
	})
}

// RemoveMoved removes the old journal file after a move, once
// backup.location.json points at the new one (05-backup §5B.3.6 row "Where"):
// os.Remove, in LabelMoveRemoveSource's region. A file already gone is
// os.ErrNotExist, which the caller treats as done.
func RemoveMoved(path string, opts ...FSOption) error {
	fsys := fsOf(opts...)
	defer fsys.Region(LabelMoveRemoveSource)()
	return fsys.Remove(path)
}
