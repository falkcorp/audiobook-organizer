// file: core/library/rootfs_local.go
// version: 1.2.0
// guid: cfc2969c-52ad-41ef-9e76-85efd9ec2a3e
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// fsOps is what the transfer engine needs of a root's filesystem: RootFS plus
// the three operations that name two roots (a rename, link or clone from one
// root into another on the same device). The local driver implements it; the
// tests wrap it to inject faults at every mutation.
type fsOps interface {
	RootFS
	renameNoReplaceInto(dst fsOps, oldname, newname string) error
	linkInto(dst fsOps, oldname, newname string) error
	reflinkInto(dst fsOps, oldname, newname string) error
	local() *localFS
}

// localFS is the local RootFS: every operation goes through the root's
// *os.Root, so ".." and symlinks cannot leave it, and the operations os.Root
// lacks open the parent directories THROUGH the root and use directory-relative
// syscalls on a single-component name (libfs).
type localFS struct {
	id       RootID
	root     *os.Root
	abs      string                     // the root's resolved host path
	spec     RootSpec                   // normalized
	readOnly bool                       // mutating methods return ErrReadOnlyRoot
	skip     []string                   // rel dirs Walk never enters: AppDir, Quarantine, nested same-owner roots
	mounted  func() error               // checked before any destructive operation; nil = always mounted
	target   func(Path) (string, error) // Symlink: the absolute host path of a target the caller may read
	// sealed refuses every mutation of a path that resolves into the root's
	// AppDir or Quarantine area (reserved.go); hidden also refuses reads. Both
	// are set on a view for anyone but core, never on the engine's own driver.
	sealed bool
	hidden bool
}

var _ fsOps = (*localFS)(nil)

func (l *localFS) local() *localFS { return l }

// readOnlyView is l with the mutating methods refused.
func (l *localFS) readOnlyView() *localFS {
	c := *l
	c.readOnly = true
	return &c
}

// pathErr wraps err as an *fs.PathError of op on name, mapping os.Root's
// escape error to ErrEscapesRoot and keeping every other error's identity.
func pathErr(op, name string, err error) error {
	if err == nil {
		return nil
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	if strings.Contains(err.Error(), "path escapes from parent") {
		err = fmt.Errorf("%w: %w", ErrEscapesRoot, err)
	}
	return &fs.PathError{Op: op, Path: name, Err: err}
}

// name validates a name for a read: "." (the root) or a ValidateRel path.
func (l *localFS) name(op, n string) (string, error) {
	if n == "." {
		return n, nil
	}
	if err := ValidateRel(n); err != nil {
		return "", &fs.PathError{Op: op, Path: n, Err: err}
	}
	if l.hidden && l.reserved(n, false) {
		return "", &fs.PathError{Op: op, Path: n, Err: ErrReservedPath}
	}
	return n, nil
}

// mut validates a name for a mutation: never "." and never on a read-only view.
func (l *localFS) mut(op, n string) (string, error) {
	if l.readOnly {
		return "", &fs.PathError{Op: op, Path: n, Err: ErrReadOnlyRoot}
	}
	if err := ValidateRel(n); err != nil {
		return "", &fs.PathError{Op: op, Path: n, Err: err}
	}
	if l.sealed && l.reserved(n, false) {
		return "", &fs.PathError{Op: op, Path: n, Err: ErrReservedPath}
	}
	// Every mutation is destructive in the 5L.5 sense: on an unmounted share it
	// would write to the wrong volume or act on an absent tree. Reads are not
	// gated.
	if l.mounted != nil {
		if err := l.mounted(); err != nil {
			return "", &fs.PathError{Op: op, Path: n, Err: err}
		}
	}
	return n, nil
}

func (l *localFS) Open(name string) (fs.File, error) {
	n, err := l.name("open", name)
	if err != nil {
		return nil, err
	}
	f, err := l.root.Open(n)
	if err != nil {
		return nil, pathErr("open", name, err)
	}
	return f, nil
}

func (l *localFS) Stat(name string) (fs.FileInfo, error) {
	n, err := l.name("stat", name)
	if err != nil {
		return nil, err
	}
	fi, err := l.root.Stat(n)
	return fi, pathErr("stat", name, err)
}

func (l *localFS) Lstat(name string) (fs.FileInfo, error) {
	n, err := l.name("lstat", name)
	if err != nil {
		return nil, err
	}
	fi, err := l.root.Lstat(n)
	return fi, pathErr("lstat", name, err)
}

func (l *localFS) ReadDir(name string) ([]fs.DirEntry, error) {
	n, err := l.name("readdir", name)
	if err != nil {
		return nil, err
	}
	d, err := l.root.Open(n)
	if err != nil {
		return nil, pathErr("readdir", name, err)
	}
	defer d.Close()
	ents, err := d.ReadDir(-1)
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	return ents, pathErr("readdir", name, err)
}

// writeFlags are the open flags that change a file or create one.
const writeFlags = os.O_WRONLY | os.O_RDWR | os.O_CREATE | os.O_APPEND | os.O_TRUNC

func (l *localFS) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	var n string
	var err error
	if flag&writeFlags != 0 {
		n, err = l.mut("open", name)
	} else {
		n, err = l.name("open", name)
	}
	if err != nil {
		return nil, err
	}
	f, err := l.root.OpenFile(n, flag, perm)
	if err != nil {
		return nil, pathErr("open", name, err)
	}
	return f, nil
}

func (l *localFS) CreateExclusive(name string, perm fs.FileMode) (File, error) {
	n, err := l.mut("create", name)
	if err != nil {
		return nil, err
	}
	f, err := l.root.OpenFile(n, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, pathErr("create", name, err)
	}
	return f, nil
}

func (l *localFS) MkdirAll(name string, perm fs.FileMode) error {
	n, err := l.mut("mkdir", name)
	if err != nil {
		return err
	}
	return pathErr("mkdir", name, l.root.MkdirAll(n, perm))
}

// Remove deletes a file or an empty directory. Like every mutation it refuses
// with ErrNotMounted when the root's sentinel does not hold (an unmounted share
// reads as an empty tree, and acting on absence is data loss, 05-library 5L.5).
func (l *localFS) Remove(name string) error {
	n, err := l.mut("remove", name)
	if err != nil {
		return err
	}
	return pathErr("remove", name, l.root.Remove(n))
}

// parent opens the directory that holds rel, through the root, and returns it
// with rel's last component.
func (l *localFS) parent(op, rel string) (*libfs.Dir, string, error) {
	dir := path.Dir(rel)
	d, err := libfs.OpenDir(l.root, dir)
	if err != nil {
		return nil, "", pathErr(op, rel, err)
	}
	return d, path.Base(rel), nil
}

func (l *localFS) RenameNoReplace(oldname, newname string) error {
	return l.renameNoReplaceInto(l, oldname, newname)
}

func (l *localFS) renameNoReplaceInto(dst fsOps, oldname, newname string) error {
	o, err := l.mut("rename", oldname)
	if err != nil {
		return err
	}
	if l.sealed && l.reserved(o, true) {
		// A directory that holds the AppDir or the Quarantine area carries it.
		return &fs.PathError{Op: "rename", Path: o, Err: ErrReservedPath}
	}
	d := dst.local()
	n, err := d.mut("rename", newname)
	if err != nil {
		return err
	}
	od, ob, err := l.parent("rename", o)
	if err != nil {
		return err
	}
	defer od.Close()
	nd, nb, err := d.parent("rename", n)
	if err != nil {
		return err
	}
	defer nd.Close()
	return libfs.RenameNoReplace(od, ob, nd, nb)
}

func (l *localFS) Link(oldname, newname string) error { return l.linkInto(l, oldname, newname) }

func (l *localFS) linkInto(dst fsOps, oldname, newname string) error {
	o, err := l.mut("link", oldname)
	if err != nil {
		return err
	}
	d := dst.local()
	n, err := d.mut("link", newname)
	if err != nil {
		return err
	}
	od, ob, err := l.parent("link", o)
	if err != nil {
		return err
	}
	defer od.Close()
	nd, nb, err := d.parent("link", n)
	if err != nil {
		return err
	}
	defer nd.Close()
	return libfs.Link(od, ob, nd, nb)
}

func (l *localFS) Reflink(oldname, newname string) error { return l.reflinkInto(l, oldname, newname) }

func (l *localFS) reflinkInto(dst fsOps, oldname, newname string) error {
	o, err := l.mut("reflink", oldname)
	if err != nil {
		return err
	}
	d := dst.local()
	n, err := d.mut("reflink", newname)
	if err != nil {
		return err
	}
	od, ob, err := l.parent("reflink", o)
	if err != nil {
		return err
	}
	defer od.Close()
	nd, nb, err := d.parent("reflink", n)
	if err != nil {
		return err
	}
	defer nd.Close()
	return libfs.Reflink(od, ob, nd, nb, libfs.LibraryFileMode)
}

func (l *localFS) Symlink(target Path, newname string) error {
	n, err := l.mut("symlink", newname)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return &fs.PathError{Op: "symlink", Path: newname, Err: err}
	}
	if l.target == nil {
		return &fs.PathError{Op: "symlink", Path: newname, Err: ErrNotSupported}
	}
	abs, err := l.target(target)
	if err != nil {
		return &fs.PathError{Op: "symlink", Path: newname, Err: err}
	}
	return pathErr("symlink", newname, l.root.Symlink(abs, n))
}

func (l *localFS) SyncDir(name string) error {
	n, err := l.name("sync", name)
	if err != nil {
		return err
	}
	if l.readOnly {
		return &fs.PathError{Op: "sync", Path: name, Err: ErrReadOnlyRoot}
	}
	d, err := libfs.OpenDir(l.root, n)
	if err != nil {
		return pathErr("sync", name, err)
	}
	defer d.Close()
	return libfs.SyncDir(d)
}

// excluded reports whether rel is inside a directory Walk never enters.
func (l *localFS) excluded(rel string) bool {
	for _, s := range l.skip {
		if rel == s || strings.HasPrefix(rel, s+"/") {
			return true
		}
	}
	return false
}

// hiddenDir reports whether a directory name is skipped under SkipHidden:
// dot-prefixed, except ".alternates", which is library content
// (pathutil.IsVisibleHiddenDir).
func hiddenDir(base string) bool {
	return strings.HasPrefix(base, ".") && base != "." && base != ".." && base != ".alternates"
}

// dirReadChunk is how many entries one ReadDir call returns, so a directory of
// a hundred thousand files streams instead of being held whole.
const dirReadChunk = 256

// Walk streams the entries below dir, pre-order, directories before their
// contents. Symlinks are reported and never followed unless
// WalkOptions.FollowLinks. A directory is entered only if it is still the
// directory the listing reported: a directory swapped for a symlink between the
// listing and the descent is refused (a yielded error), and os.Root refuses a
// symlink that leaves the root in any case. A failing directory yields its
// error and the walk goes on with its siblings; ctx cancellation ends the walk.
func (l *localFS) Walk(ctx context.Context, dir string, opts WalkOptions) iter.Seq2[Entry, error] {
	exts := normalizeExtensions(opts.Extensions)
	return func(yield func(Entry, error) bool) {
		start, err := l.name("walk", dir)
		if err != nil {
			yield(Entry{}, err)
			return
		}
		rootInfo, err := l.root.Stat(start)
		if err != nil {
			yield(Entry{}, pathErr("walk", dir, err))
			return
		}
		// real is the symlink-free path of the directory: a directory reached
		// through a followed link is excluded by where it is, not by the name
		// of the link.
		type frame struct {
			rel  string
			real string
			info fs.FileInfo
		}
		startReal := start
		if r, ok := l.realRel(start, true); ok {
			startReal = r
		}
		stack := []frame{{rel: start, real: startReal, info: rootInfo}}
		visited := map[[2]uint64]bool{}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if err := ctx.Err(); err != nil {
				yield(Entry{}, err)
				return
			}
			if !cur.info.IsDir() {
				continue
			}
			if dev, ino, ok := libfs.InodeOf(cur.info); ok {
				key := [2]uint64{dev, ino}
				if visited[key] {
					continue // a link cycle
				}
				visited[key] = true
			}
			d, err := l.root.Open(cur.rel)
			if err != nil {
				if !yield(Entry{}, pathErr("walk", cur.rel, err)) {
					return
				}
				continue
			}
			opened, serr := d.Stat()
			if serr != nil || !os.SameFile(cur.info, opened) {
				_ = d.Close()
				if serr == nil {
					serr = fmt.Errorf("%w: %s changed during the walk", ErrEscapesRoot, cur.rel)
				}
				if !yield(Entry{}, pathErr("walk", cur.rel, serr)) {
					return
				}
				continue
			}
			var subdirs []frame
			stop := false
			for !stop {
				if err := ctx.Err(); err != nil {
					_ = d.Close()
					yield(Entry{}, err)
					return
				}
				ents, rerr := d.ReadDir(dirReadChunk)
				for _, de := range ents {
					rel := de.Name()
					if cur.rel != "." {
						rel = cur.rel + "/" + de.Name()
					}
					info, ierr := de.Info()
					if ierr != nil {
						if !yield(Entry{}, pathErr("walk", rel, ierr)) {
							_ = d.Close()
							return
						}
						continue
					}
					isDir := info.IsDir()
					realChild := de.Name()
					if cur.real != "." && cur.real != "" {
						realChild = cur.real + "/" + de.Name()
					}
					if isDir && (l.excluded(rel) || l.excluded(realChild) || opts.SkipHidden && hiddenDir(de.Name())) {
						continue
					}
					if !isDir && info.Mode()&fs.ModeSymlink == 0 && !matchExt(exts, de.Name()) {
						continue
					}
					if !yield(Entry{Path: Path{Root: l.id, Rel: rel}, Info: info}, nil) {
						_ = d.Close()
						return
					}
					switch {
					case isDir:
						subdirs = append(subdirs, frame{rel: rel, real: realChild, info: info})
					case info.Mode()&fs.ModeSymlink != 0 && opts.FollowLinks:
						target, terr := l.root.Stat(rel)
						if terr != nil || !target.IsDir() || l.excluded(rel) {
							break
						}
						// Where the link leads decides: a link into an area the
						// walk never enters (the AppDir, the Quarantine area, a
						// nested root, the application's own directory) is
						// reported as the link it is and not followed.
						real, ok := l.realRel(rel, true)
						if !ok || l.excluded(real) || l.reservedInode(real, false) {
							break
						}
						subdirs = append(subdirs, frame{rel: rel, real: real, info: target})
					}
				}
				switch {
				case rerr == nil:
				case errors.Is(rerr, io.EOF):
					stop = true
				default:
					if !yield(Entry{}, pathErr("walk", cur.rel, rerr)) {
						_ = d.Close()
						return
					}
					stop = true
				}
			}
			_ = d.Close()
			// Push in reverse so the stack pops the directories in listing order.
			for i := len(subdirs) - 1; i >= 0; i-- {
				stack = append(stack, subdirs[i])
			}
		}
	}
}

func normalizeExtensions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.ToLower(e)
		if e != "" && !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		out = append(out, e)
	}
	return out
}

func matchExt(exts []string, name string) bool {
	if exts == nil {
		return true
	}
	return slices.Contains(exts, strings.ToLower(filepath.Ext(name)))
}
