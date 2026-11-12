// file: core/library/rootfs.go
// version: 1.0.0
// guid: 49b75844-2be9-4654-934d-e275341a9a12
// last-edited: 2026-11-11

package library

import (
	"context"
	"io"
	"io/fs"
	"iter"
)

// RootFS is every file operation a module may perform under one root (05-library
// 5L.3.2). The local implementation wraps *os.Root; a future remote driver
// implements the same interface (Q4). All names are root-relative and validated.
//
// The spec writes RootFS as one interface; it is declared here as two embedded
// halves, RootReader and RootWriter, so no declaration is over core's
// interface-width ceiling (max 8 entries, no baseline raise). The method set is
// the spec's, exactly.
type RootFS interface {
	RootReader
	RootWriter
}

// RootReader is the read half of RootFS.
type RootReader interface {
	fs.FS
	fs.StatFS
	fs.ReadDirFS

	Lstat(name string) (fs.FileInfo, error)
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	Walk(ctx context.Context, dir string, opts WalkOptions) iter.Seq2[Entry, error]
}

// RootWriter is the mutating half of RootFS. On a read-only RootFS every
// method returns ErrReadOnlyRoot.
type RootWriter interface {
	// CreateExclusive is O_CREATE|O_EXCL: an existing name is fs.ErrExist.
	CreateExclusive(name string, perm fs.FileMode) (File, error)
	MkdirAll(name string, perm fs.FileMode) error
	Remove(name string) error
	// RenameNoReplace never replaces an existing destination (any type,
	// dangling symlink included): fs.ErrExist and nothing changes.
	RenameNoReplace(oldname, newname string) error
	Link(oldname, newname string) error    // fs.ErrExist if newname exists
	Reflink(oldname, newname string) error // ErrReflinkUnsupported, fs.ErrExist
	// Symlink's target must itself be a Path inside a root the caller can
	// read; os.Root.Symlink does not validate targets, this does.
	Symlink(target Path, newname string) error
	// SyncDir fsyncs a directory; EINVAL and ENOTSUP are success.
	SyncDir(name string) error
}

// File is an open file under a root. It is not safe for concurrent use.
type File interface {
	fs.File
	io.Writer
	io.WriterAt
	io.Seeker
	Sync() error
	Stat() (fs.FileInfo, error) // on the descriptor, never the path
}

// WalkOptions tunes RootFS.Walk.
type WalkOptions struct {
	SkipHidden  bool     // skip dot-prefixed directories (".alternates" excepted); AppDir and Quarantine are always skipped
	Extensions  []string // case-insensitive filter on files, with the leading dot; nil = all
	FollowLinks bool     // false: symlinks are reported, never followed
}

// Entry is one walked file or directory. Info is an Lstat result.
type Entry struct {
	Path Path
	Info fs.FileInfo
}
