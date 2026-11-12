// file: core/library/guardedopen.go
// version: 1.0.1
// guid: e84d1a36-7b52-4c09-9f1e-2a6c05d8b7f3
// last-edited: 2026-11-11

package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"syscall"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// hook runs the test hook, if any.
func (l *localFS) hook(stage, n string) {
	if l.race != nil {
		l.race(stage, n)
	}
}

// guardedOpen opens n, already judged by name, for a view that hides the
// reserved areas, and decides from the descriptors and never from a name
// resolved a second time. A name is only an instruction to the kernel, and a
// symlink or a directory swapped after the check makes the kernel obey it
// somewhere else, so:
//
//  1. the symlinks of n are expanded (realRel), and the directory that holds the
//     result is opened THROUGH the root, so the descriptor is confined;
//  2. that descriptor is walked upward with openat(fd, "..") to the root's own
//     directory, and every directory on the way, its own identity included, is
//     compared by device and inode with the reserved areas (libfs.Ancestry);
//  3. the last component is opened relative to that descriptor without following
//     a link (libfs.OpenAt), and what was opened is compared with the areas too
//     (an area is itself a directory of its parent).
//
// Whatever the names do meanwhile, a descriptor inside an area, or one whose
// place cannot be established (the walk fails anywhere, the platform has no
// descriptor walk), is refused: a failure to know is a refusal. What remains is
// a directory renamed into an area after the check, which no check can see: the
// descriptor still is the directory it was.
func (l *localFS) guardedOpen(op, name, n string) (*os.File, error) {
	refuse := func(err error) error { return &fs.PathError{Op: op, Path: name, Err: err} }
	if n == "." {
		f, err := l.root.Open(".")
		return f, pathErr(op, name, err)
	}
	real, ok := l.realRel(n, true)
	if !ok {
		return nil, refuse(fmt.Errorf("%w: %s does not resolve inside the root", ErrEscapesRoot, name))
	}
	if real == "" { // a link to the root itself
		f, err := l.root.Open(".")
		return f, pathErr(op, name, err)
	}
	l.hook("resolved", n)
	parent, err := libfs.OpenDir(l.root, path.Dir(real))
	if err != nil {
		return nil, pathErr(op, name, err)
	}
	defer closeLogged(l.log, parent, "the directory holding "+name)
	l.hook("parent", n)
	under, err := l.areaIdentities(parent)
	if err != nil {
		return nil, refuse(err)
	}
	f, err := libfs.OpenAt(parent, path.Base(real), os.O_RDONLY)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, refuse(fmt.Errorf("%w: %s became a symbolic link while it was opened", ErrReservedPath, name))
		}
		return nil, pathErr(op, name, err)
	}
	l.hook("opened", n)
	st, err := f.Stat()
	if err != nil {
		return nil, libfs.CloseAfter(f, pathErr(op, name, err))
	}
	if dev, ino, ok := libfs.InodeOf(st); !ok || under[libfs.ID{Dev: dev, Ino: ino}] {
		return nil, libfs.CloseAfter(f, refuse(fmt.Errorf("%w: %s", ErrReservedPath, name)))
	}
	return f, nil
}

// areaIdentities walks the directory descriptor dir up to the root and returns
// the identities of the reserved area directories. It is an error, wrapping
// ErrReservedPath, when dir or any directory above it up to the root is an area,
// or when the walk cannot be completed.
func (l *localFS) areaIdentities(dir *libfs.Dir) (map[libfs.ID]bool, error) {
	cannot := func(err error) error {
		return fmt.Errorf("%w: cannot tell where the directory lies: %w", ErrReservedPath, err)
	}
	top, err := l.root.Stat(".")
	if err != nil {
		return nil, cannot(err)
	}
	dev, ino, ok := libfs.InodeOf(top)
	if !ok {
		return nil, cannot(errors.ErrUnsupported)
	}
	chain, err := libfs.Ancestry(dir.File(), libfs.ID{Dev: dev, Ino: ino})
	if err != nil {
		return nil, cannot(err)
	}
	under, _ := l.areaIDs()
	areas := make(map[libfs.ID]bool, len(under))
	for id := range under {
		areas[libfs.ID{Dev: id[0], Ino: id[1]}] = true
	}
	for _, id := range chain {
		if areas[id] {
			return nil, fmt.Errorf("%w: the directory lies in a reserved area", ErrReservedPath)
		}
	}
	return areas, nil
}
