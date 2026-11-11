// file: core/internal/libfs/dir.go
// version: 1.0.0
// guid: e1038704-5e43-4928-8fc7-b6e806cfdfb8
// last-edited: 2026-11-11

package libfs

import (
	"fmt"
	"os"
	"syscall"
)

// Dir is a directory opened through an *os.Root. Every operation in this
// package is relative to a Dir, so it can only reach what the root let the
// caller open. Close it when the operation is done; a Dir is not safe for
// concurrent Close.
type Dir struct {
	f *os.File
}

// OpenDir opens the directory rel (or the root itself, ".") through root. The
// path is resolved by os.Root, which refuses ".." and any symlink that leaves
// the root. It fails with ENOTDIR when rel is not a directory.
func OpenDir(root *os.Root, rel string) (*Dir, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !fi.IsDir() {
		_ = f.Close()
		return nil, &os.PathError{Op: "opendir", Path: rel, Err: syscall.ENOTDIR}
	}
	return &Dir{f: f}, nil
}

// Close releases the directory descriptor.
func (d *Dir) Close() error {
	if d == nil || d.f == nil {
		return nil
	}
	return d.f.Close()
}

// File is the underlying descriptor, for a caller that needs its Stat or a
// directory listing. It must not be closed separately.
func (d *Dir) File() *os.File { return d.f }

// Name is the path the directory was opened under, for error messages only. It
// is never used to address a file.
func (d *Dir) Name() string { return d.f.Name() }

// linkErr wraps err the way os.Link does so errors.Is(err, fs.ErrExist) holds
// for EEXIST.
func linkErr(op string, oldName, newName string, err error) error {
	return &os.LinkError{Op: op, Old: oldName, New: newName, Err: err}
}

// require makes the error of a nil Dir explicit instead of a nil dereference.
func require(ds ...*Dir) error {
	for _, d := range ds {
		if d == nil || d.f == nil {
			return fmt.Errorf("libfs: nil Dir")
		}
	}
	return nil
}
