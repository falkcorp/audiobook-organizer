// file: core/library/rootfs_local.go
// version: 1.7.1
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
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// fsOps is what the transfer engine needs of a root's filesystem: RootFS plus
// the three operations that name two roots (a rename, link or clone from one
// root into another on the same device). The local driver implements it; the
// tests wrap it to inject faults at every mutation.
type fsOps interface {
	RootFS
	renameNoReplaceInto(dst fsOps, oldname, newname string) error
	// renameOverInto replaces whatever is at newname. Only the placeholder
	// protocol (placeholder.go) calls it, on a name it created itself.
	renameOverInto(dst fsOps, oldname, newname string) error
	// mkdirExclusive creates the one directory newname; any existing entry is
	// fs.ErrExist (MkdirAll treats an existing directory as success).
	mkdirExclusive(name string, perm fs.FileMode) error
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
	log      *slog.Logger               // handles that fail to close are logged here
	readOnly bool                       // mutating methods return ErrReadOnlyRoot
	skip     []string                   // rel dirs Walk never enters: AppDir, Quarantine, nested same-owner roots
	mounted  func() error               // checked before any destructive operation; nil = always mounted
	target   func(Path) (string, error) // Symlink: the absolute host path of a target the caller may read
	// sealed refuses every mutation of a path that resolves into the root's
	// AppDir or Quarantine area (reserved.go); hidden also refuses reads. Both
	// are set on a view for anyone but core, never on the engine's own driver.
	sealed bool
	hidden bool
	// areas remembers the identities of the root's areas, shared by every view.
	areas *areaMemory
	// inodeOf replaces libfs.InodeOf in the by-identity area test; nil outside tests.
	inodeOf func(fs.FileInfo) (dev, ino uint64, ok bool)
	// race is a test hook, nil outside tests. A hidden view's read open (guardedOpen)
	// calls it at "checked" (the name was judged), "resolved" (the symlinks were
	// expanded), "parent" (the directory holding the name is open, not yet
	// examined) and "opened" (the file is open, not yet examined), where a test
	// swaps a symlink or a directory to prove the decision comes from the
	// descriptors and not from the names.
	race func(stage, name string)
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
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
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
	if l.hidden {
		l.hook("checked", n)
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
	if l.sealed {
		l.hook("checked", n) // the name was judged; the descriptor judgement comes next
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
	if l.hidden {
		f, err := l.guardedOpen("open", name, n)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	f, err := l.openRead("open", name, n, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// openRead opens n for reading and never waits on a special file. A FIFO opened
// for reading blocks until a writer appears, uninterruptibly (ctx cannot reach
// it), and four of them would hold every transfer slot and path lock the
// library has; so a name that is not a regular file or a directory is refused
// before it is opened (an open can have effects on a device node), and the open
// itself is O_NONBLOCK with the descriptor examined afterwards, the check no
// swap can race.
func (l *localFS) openRead(op, name, n string, flag int) (*os.File, error) {
	if st, err := l.root.Stat(n); err == nil && !st.Mode().IsRegular() && !st.IsDir() {
		return nil, notReadable(op, name)
	}
	f, err := l.root.OpenFile(n, flag|openNonblock, 0)
	if err != nil {
		return nil, pathErr(op, name, err)
	}
	st, err := f.Stat()
	if err != nil {
		return nil, libfs.CloseAfter(f, pathErr(op, name, err))
	}
	if !st.Mode().IsRegular() && !st.IsDir() {
		return nil, libfs.CloseAfter(f, notReadable(op, name))
	}
	return f, nil
}

// notReadable is the refusal of a FIFO, socket or device where a file is read.
func notReadable(op, name string) error {
	return &fs.PathError{Op: op, Path: name, Err: fmt.Errorf("%w: not a regular file or a directory", ErrNotSupported)}
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
	var d *os.File
	if l.hidden {
		d, err = l.guardedOpen("readdir", name, n)
		if err != nil {
			return nil, err
		}
	} else if d, err = l.openRead("readdir", name, n, os.O_RDONLY); err != nil {
		return nil, err
	}
	defer closeLogged(l.log, d, name)
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
	if l.hidden && flag&writeFlags == 0 {
		f, err := l.guardedOpen("open", name, n)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	if flag&writeFlags == 0 {
		f, err := l.openRead("open", name, n, flag)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	if l.sealed {
		return l.openAt("open", name, n, flag, perm)
	}
	f, err := l.root.OpenFile(n, flag, perm)
	if err != nil {
		return nil, pathErr("open", name, err)
	}
	return f, nil
}

// openAt opens or creates n on a sealed view from the descriptor of the
// directory that holds it, after that directory was found to lie outside the
// root's areas, so a name swapped for a link meanwhile cannot move the write
// into one. A link in the last component is not followed.
func (l *localFS) openAt(op, name, n string, flag int, perm fs.FileMode) (File, error) {
	d, base, err := l.mutParent(op, n)
	if err != nil {
		return nil, err
	}
	defer closeLogged(l.log, d, "the directory holding "+name)
	f, err := libfs.OpenFileAt(d, base, flag, perm)
	if err != nil {
		return nil, pathErr(op, name, err)
	}
	return f, nil
}

func (l *localFS) CreateExclusive(name string, perm fs.FileMode) (File, error) {
	n, err := l.mut("create", name)
	if err != nil {
		return nil, err
	}
	if l.sealed {
		return l.openAt("create", name, n, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
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
	if l.sealed {
		return l.mkdirAllSealed(name, n, perm)
	}
	return pathErr("mkdir", name, l.root.MkdirAll(n, perm))
}

// mkdirAllSealed creates each missing component from the descriptor of the
// directory that holds it, so no component is created inside an area through a
// name that was swapped after the check.
func (l *localFS) mkdirAllSealed(name, n string, perm fs.FileMode) error {
	parts := strings.Split(n, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		d, base, err := l.mutParent("mkdir", p)
		if err != nil {
			return err
		}
		merr := libfs.MkdirAt(d, base, perm)
		closeLogged(l.log, d, "the directory holding "+p)
		if merr != nil && !errors.Is(merr, fs.ErrExist) {
			return pathErr("mkdir", name, merr)
		}
	}
	st, err := l.root.Stat(n)
	if err != nil {
		return pathErr("mkdir", name, err)
	}
	if !st.IsDir() {
		return &fs.PathError{Op: "mkdir", Path: name, Err: syscall.ENOTDIR}
	}
	return nil
}

// Remove deletes a file or an empty directory. Like every mutation it refuses
// with ErrNotMounted when the root's sentinel does not hold (an unmounted share
// reads as an empty tree, and acting on absence is data loss, 05-library 5L.5).
func (l *localFS) Remove(name string) error {
	n, err := l.mut("remove", name)
	if err != nil {
		return err
	}
	if l.sealed {
		d, base, err := l.mutParent("remove", n)
		if err != nil {
			return err
		}
		defer closeLogged(l.log, d, "the directory holding "+name)
		return pathErr("remove", name, libfs.RemoveAt(d, base))
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

// mutParent is parent for a mutation. On a sealed view the directory that holds
// rel is judged from its descriptor: if it, or any directory above it up to the
// root, is one of the root's areas, the mutation is refused. The caller then
// acts through that descriptor, so the decision and the act are about the same
// directory whatever happens to the names (a failure to know is a refusal).
func (l *localFS) mutParent(op, rel string) (*libfs.Dir, string, error) {
	d, base, err := l.parent(op, rel)
	if err != nil {
		return nil, "", err
	}
	if l.sealed {
		if _, aerr := l.areaIdentities(d); aerr != nil {
			closeLogged(l.log, d, rel)
			return nil, "", &fs.PathError{Op: op, Path: rel, Err: aerr}
		}
	}
	return d, base, nil
}

// leafReserved reports whether the entry base of dir is, by identity and not by
// name, one of the root's areas, or (carries) holds one: moving it would carry
// the area along. A name that is not there is nothing to protect.
func (l *localFS) leafReserved(dir *libfs.Dir, base string, carries bool) (bool, error) {
	id, _, err := libfs.IdentityAt(dir, base)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return true, err
	}
	under, above := l.areaIDs()
	key := [2]uint64{id.Dev, id.Ino}
	return under[key] || (carries && above[key]), nil
}

func (l *localFS) RenameNoReplace(oldname, newname string) error {
	return l.renameNoReplaceInto(l, oldname, newname)
}

func (l *localFS) renameNoReplaceInto(dst fsOps, oldname, newname string) error {
	return l.renameInto(dst, oldname, newname, false)
}

func (l *localFS) renameOverInto(dst fsOps, oldname, newname string) error {
	return l.renameInto(dst, oldname, newname, true)
}

// renameInto is the rename of oldname in l to newname in dst, with every check a
// mutation has. over lets it replace; it is for the placeholder protocol only.
func (l *localFS) renameInto(dst fsOps, oldname, newname string, over bool) error {
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
	od, ob, err := l.mutParent("rename", o)
	if err != nil {
		return err
	}
	defer closeLogged(l.log, od, o)
	if l.sealed {
		// The entry itself, from the descriptor: an area, or a directory holding
		// one (a Move carries it), whatever name led here.
		if hit, lerr := l.leafReserved(od, ob, true); hit || lerr != nil {
			return &fs.PathError{Op: "rename", Path: o, Err: errors.Join(ErrReservedPath, lerr)}
		}
	}
	nd, nb, err := d.mutParent("rename", n)
	if err != nil {
		return err
	}
	defer closeLogged(l.log, nd, n)
	if over {
		return libfs.RenameOver(od, ob, nd, nb)
	}
	return libfs.RenameNoReplace(od, ob, nd, nb)
}

// mkdirExclusive creates the single directory name. Like every mutation it is
// judged from the descriptor of the directory that holds it on a sealed view.
func (l *localFS) mkdirExclusive(name string, perm fs.FileMode) error {
	n, err := l.mut("mkdir", name)
	if err != nil {
		return err
	}
	if l.sealed {
		d, base, err := l.mutParent("mkdir", n)
		if err != nil {
			return err
		}
		defer closeLogged(l.log, d, "the directory holding "+name)
		return pathErr("mkdir", name, libfs.MkdirAt(d, base, perm))
	}
	return pathErr("mkdir", name, l.root.Mkdir(n, perm))
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
	od, ob, err := l.mutParent("link", o)
	if err != nil {
		return err
	}
	defer closeLogged(l.log, od, o)
	nd, nb, err := d.mutParent("link", n)
	if err != nil {
		return err
	}
	defer closeLogged(l.log, nd, n)
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
	od, ob, err := l.mutParent("reflink", o)
	if err != nil {
		return err
	}
	defer closeLogged(l.log, od, o)
	nd, nb, err := d.mutParent("reflink", n)
	if err != nil {
		return err
	}
	defer closeLogged(l.log, nd, n)
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
	if l.sealed {
		d, base, err := l.mutParent("symlink", n)
		if err != nil {
			return err
		}
		defer closeLogged(l.log, d, "the directory holding "+newname)
		return pathErr("symlink", newname, libfs.SymlinkAt(abs, d, base))
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
	defer closeLogged(l.log, d, name)
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
				closeLogged(l.log, d, cur.rel)
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
					closeLogged(l.log, d, cur.rel)
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
							closeLogged(l.log, d, cur.rel)
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
						closeLogged(l.log, d, cur.rel)
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
						closeLogged(l.log, d, cur.rel)
						return
					}
					stop = true
				}
			}
			closeLogged(l.log, d, cur.rel)
			// Push in reverse so the stack pops the directories in listing order.
			for _, sd := range slices.Backward(subdirs) {
				stack = append(stack, sd)
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
