// file: core/library/reserved.go
// version: 1.0.0
// guid: f5dd1d5d-73ad-4667-bbb4-561193fd1163
// last-edited: 2026-11-11

package library

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// A root's AppDir (the sentinel, app state) and Quarantine area are core's.
// Whether a path is inside one is decided where the path RESOLVES, not by how
// it is spelled: a symlink inside the root, a bind mount or another case
// spelling must not open them. Resolution uses only the root's *os.Root
// (Lstat, Readlink, Stat), so it cannot leave the root and a link that points
// outside is left for os.Root to refuse.

// maxLinkHops bounds the symlinks one resolution expands (Linux allows 40).
const maxLinkHops = 40

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
// leaves the root or exceeds maxLinkHops; the operation itself then fails (os.Root
// refuses the escape), so a caller treats that as "not reserved".
//
// followLast says whether a symlink in the LAST component is expanded. An
// operation on the entry itself (a rename or a move of it, a quarantine) acts on
// the link, not on what it points at, so those pass false; an operation that
// opens, creates or lists through the name passes true.
func (l *localFS) realRel(rel string, followLast bool) (string, bool) {
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
				return "", false
			}
			done = done[:len(done)-1]
			continue
		}
		next := strings.Join(append(append([]string(nil), done...), c), "/")
		st, err := l.root.Lstat(next)
		if err != nil {
			return strings.Join(append(append(append([]string(nil), done...), c), todo...), "/"), true
		}
		if st.Mode()&fs.ModeSymlink == 0 || (len(todo) == 0 && !followLast) {
			done = append(done, c)
			continue
		}
		if hops++; hops > maxLinkHops {
			return "", false
		}
		target, err := l.root.Readlink(next)
		if err != nil {
			return "", false
		}
		if filepath.IsAbs(target) {
			// An absolute target is inside the root only if it names the root
			// (resolved) or something below it.
			inside, err := filepath.Rel(l.abs, filepath.Clean(resolveExistingPrefix(target)))
			if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return "", false
			}
			target, done = inside, nil
		}
		todo = append(strings.Split(filepath.ToSlash(target), "/"), todo...)
	}
	return strings.Join(done, "/"), true
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
			dev, ino, ok := libfs.InodeOf(st)
			if !ok {
				break
			}
			if i == len(parts)-1 {
				under[[2]uint64{dev, ino}] = true
			} else {
				above[[2]uint64{dev, ino}] = true
			}
		}
	}
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
		dev, ino, ok := libfs.InodeOf(st)
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
	real, ok := l.realRel(rel, !carries)
	if !ok {
		return false
	}
	if real != rel && l.reservedName(real, carries) {
		return true
	}
	return real != "" && l.reservedInode(real, carries)
}
