// file: core/library/reserved.go
// version: 1.2.1
// guid: f5dd1d5d-73ad-4667-bbb4-561193fd1163
// last-edited: 2026-11-11

package library

import (
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// A root's AppDir (the sentinel, app state) and Quarantine area are core's.
// Whether a path is inside one is decided where the path RESOLVES, not by how
// it is spelled: a symlink inside the root, a bind mount or another case
// spelling must not open them. Resolution uses only the root's *os.Root
// (Lstat, Readlink, Stat), so it cannot leave the root and a link that points
// outside is left for os.Root to refuse.

// maxLinkHops bounds the symlinks one resolution expands; a chain that is longer
// is undecided and refused. It is NOT os.Root's limit: os.Root follows 8
// (rootMaxSymlinks) and applies that to the reads it does itself (Stat, Lstat,
// Open of a plain view). A mutation is carried out on the path this resolution
// expanded, so it can follow a chain os.Root would refuse for a read, and it is
// this limit, not os.Root's, that decides how long a chain a mutation can be
// reached through. Nothing depends on the value being large: exhaustion refuses,
// so a lower limit would only turn more long chains into refusals. 255 is
// headroom well above anything a layout needs.
const maxLinkHops = 255

// resolution is how realRel ended.
type resolution uint8

const (
	// resolved: the path with its symlinks expanded inside the root.
	resolved resolution = iota
	// leavesRoot: the expansion leaves the root; os.Root refuses the operation
	// itself, so nothing is reached.
	leavesRoot
	// undecided: the expansion gave up (too many links, a link that cannot be
	// read). os.Root may still follow it, so the path is treated as reserved.
	undecided
)

// areaMemory is every directory identity the root's AppDir and Quarantine area
// have been seen with, kept for the life of the root and shared by every view of
// it. An area renamed away by someone with access to the disk is still the area
// by inode, so it stays refused under whatever name it has.
type areaMemory struct {
	mu  sync.Mutex
	ids map[[2]uint64]bool
}

func (a *areaMemory) remember(id [2]uint64) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ids == nil {
		a.ids = map[[2]uint64]bool{}
	}
	a.ids[id] = true
}

func (a *areaMemory) each(fn func(id [2]uint64)) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for id := range a.ids {
		fn(id)
	}
}

// relWithinFold reports whether rel equals dir or lies under it, ignoring
// case. The fold matters on a case-insensitive filesystem (the default on macOS
// and Windows), where ".MediaCore" is the AppDir; on a case-sensitive one it
// only refuses a directory nobody should be creating beside it.
func relWithinFold(rel, dir string) bool {
	if dir == "" || len(rel) < len(dir) || !strings.EqualFold(rel[:len(dir)], dir) {
		return false
	}
	return len(rel) == len(dir) || rel[len(dir)] == '/'
}

// reservedDirs are the root's own areas, as rel paths.
func (l *localFS) reservedDirs() [2]string { return [2]string{l.spec.AppDir, l.spec.Quarantine} }

// reservedName reports whether rel names an area by spelling: equal to one,
// inside one, or (carries) a directory holding one, which a move would carry
// along.
func (l *localFS) reservedName(rel string, carries bool) bool {
	for _, dir := range l.reservedDirs() {
		if relWithinFold(rel, dir) || (carries && relWithinFold(dir, rel)) {
			return true
		}
	}
	return false
}

// realRel is rel with every symlink expanded inside the root: the path the
// operating system will actually touch. A component that does not exist yet
// ends the expansion (the rest is as written). ok is false when the expansion
// could not be completed (resolve says why).
//
// followLast says whether a symlink in the LAST component is expanded. An
// operation on the entry itself (a rename or a move of it, a quarantine) acts on
// the link, not on what it points at, so those pass false; an operation that
// opens, creates or lists through the name passes true.
func (l *localFS) realRel(rel string, followLast bool) (string, bool) {
	real, how := l.resolve(rel, followLast)
	return real, how == resolved
}

// resolve is realRel with the reason it gave up.
func (l *localFS) resolve(rel string, followLast bool) (string, resolution) {
	var done []string
	todo := strings.Split(rel, "/")
	hops := 0
	for len(todo) > 0 {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(done) == 0 {
				return "", leavesRoot
			}
			done = done[:len(done)-1]
			continue
		}
		next := strings.Join(append(append([]string(nil), done...), c), "/")
		st, err := l.root.Lstat(next)
		if err != nil {
			return strings.Join(append(append(append([]string(nil), done...), c), todo...), "/"), resolved
		}
		if st.Mode()&fs.ModeSymlink == 0 || (len(todo) == 0 && !followLast) {
			done = append(done, c)
			continue
		}
		if hops++; hops > maxLinkHops {
			return "", undecided
		}
		target, err := l.root.Readlink(next)
		if err != nil {
			return "", undecided
		}
		if filepath.IsAbs(target) {
			// An absolute target is inside the root only if it names the root
			// (resolved) or something below it.
			inside, err := filepath.Rel(l.abs, filepath.Clean(resolveExistingPrefix(target)))
			if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return "", leavesRoot
			}
			target, done = inside, nil
		}
		todo = append(strings.Split(filepath.ToSlash(target), "/"), todo...)
	}
	return strings.Join(done, "/"), resolved
}

// inode is st's device and inode. A test replaces it (the seams.inodeOf hook) to
// fake two names for one directory, which a case-sensitive filesystem cannot
// produce: only a case-insensitive volume or a bind mount does.
func (l *localFS) inode(st fs.FileInfo) (dev, ino uint64, ok bool) {
	if l.inodeOf != nil {
		return l.inodeOf(st)
	}
	return libfs.InodeOf(st)
}

// areaIDs are the device and inode of the existing area directories (under) and
// of every directory above them inside the root (above), for the by-inode test.
func (l *localFS) areaIDs() (under, above map[[2]uint64]bool) {
	under, above = map[[2]uint64]bool{}, map[[2]uint64]bool{}
	for _, dir := range l.reservedDirs() {
		parts := strings.Split(dir, "/")
		for i := range parts {
			st, err := l.root.Stat(strings.Join(parts[:i+1], "/"))
			if err != nil {
				break
			}
			dev, ino, ok := l.inode(st)
			if !ok {
				break
			}
			if i == len(parts)-1 {
				under[[2]uint64{dev, ino}] = true
				l.areas.remember([2]uint64{dev, ino})
			} else {
				above[[2]uint64{dev, ino}] = true
			}
		}
	}
	l.areas.each(func(id [2]uint64) { under[id] = true })
	return under, above
}

// reservedInode reports whether any directory on the way to real, or real itself
// (carries: or real holds an area), is the same directory as an area by device
// and inode: a bind mount or a second name for it. With carries the entry
// itself is not followed if it is a link.
func (l *localFS) reservedInode(real string, carries bool) bool {
	under, above := l.areaIDs()
	parts := strings.Split(real, "/")
	for i := range parts {
		stat := l.root.Stat
		if carries && i == len(parts)-1 {
			stat = l.root.Lstat
		}
		st, err := stat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return false
		}
		dev, ino, ok := l.inode(st)
		if !ok {
			return false
		}
		id := [2]uint64{dev, ino}
		if under[id] || (carries && i == len(parts)-1 && above[id]) {
			return true
		}
	}
	return false
}

// reserved reports whether rel is in one of the root's areas: by its spelling,
// by where its symlinks lead, or by the directories' identity. carries: a
// directory holding an area counts too (a move would carry it).
func (l *localFS) reserved(rel string, carries bool) bool {
	if rel == "" || rel == "." {
		return false
	}
	if l.reservedName(rel, carries) {
		return true
	}
	real, how := l.resolve(rel, !carries)
	switch how {
	case leavesRoot:
		return false // os.Root refuses the operation itself
	case undecided:
		return true // a failure to know is a refusal
	}
	if real != rel && l.reservedName(real, carries) {
		return true
	}
	return real != "" && l.reservedInode(real, carries)
}
