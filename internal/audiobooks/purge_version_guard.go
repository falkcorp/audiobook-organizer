// file: internal/audiobooks/purge_version_guard.go
// version: 1.0.1
// guid: 3c8e1f5a-9d27-4b60-a4e3-7f1b2c9d6e58
// last-edited: 2026-10-27

package audiobooks

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// VersionPurgeGuard is the book purge's file safety, as versions.PurgeVersion
// asks it. A version purge unlinks files from disk, and until 2026-10-27 it did
// so with none of the checks the book purge makes: a file under an import path,
// the iTunes library or .failed was its to delete, and a library that is not
// mounted read as "every file already removed". The decision about one path is
// planPurgePathIn, the function the book purge decides its own file with, so
// the two cannot disagree about protected paths, the library root or an
// unmounted folder. What stays a version purge's own is the owners question
// (another book_file row, another book), which PurgeVersion asks itself because
// it has to tell the version's own rows apart from others.
func (svc *AudiobookService) VersionPurgeGuard() versions.PurgeGuard {
	return versionPurgeGuard{svc: svc}
}

type versionPurgeGuard struct{ svc *AudiobookService }

// RootReady is the library-root check (libraryRootReady) on the configured root.
// A version purge always deletes files, so unlike the book purge it has no
// "delete files is off" case in which to skip it.
func (g versionPurgeGuard) RootReady() error {
	return g.svc.libraryRootReady(config.Snapshot().RootDir)
}

func (g versionPurgeGuard) NewDecider(seedDirs []string) versions.PurgeDecider {
	d := &versionPurgeDecider{svc: g.svc, emptied: &dirSet{}}
	for _, dir := range seedDirs {
		d.emptied.add(dir)
	}
	return d
}

// versionPurgeDecider decides the paths of one version purge. emptied holds the
// folders this purge is (or, on a retry, was) unlinking in, so a folder it
// emptied is not mistaken for the mount point of a filesystem that is not up.
type versionPurgeDecider struct {
	svc     *AudiobookService
	emptied *dirSet
}

func (d *versionPurgeDecider) Unlinked(path string) { d.emptied.add(filepath.Dir(path)) }

func (d *versionPurgeDecider) Decide(path string) (versions.PurgeDecision, string, error) {
	v := d.svc.planPurgePathIn(path, d.emptied, nil)
	switch {
	case v.protected:
		return versions.PurgeKeep, "it is under an import path, the iTunes library or a .failed folder, which a purge never deletes", nil
	case v.keptErr != nil:
		// A lookup that failed for a reason that may pass (a stat error, a folder
		// that cannot vouch for "gone") is a question that could not be answered:
		// transient, the version is retried. Every other refusal (outside the
		// library root, a root that does not resolve, a trailing separator) will
		// not change by asking again.
		if errors.Is(v.keptErr, errPathLookupFailed) && !errors.Is(v.keptErr, errPathUnresolvable) {
			return versions.PurgeKeep, "", v.keptErr
		}
		// The library root itself is not there (fs.ErrNotExist out of resolving
		// it in purgePathInsideRoot): the library went away after RootReady
		// passed, an unmount, which a remount undoes. The book purge leaves this
		// to libraryRootReady; a version purge that has already passed that check
		// can meet it between the check and a path, and "kept for good" would
		// record every file as kept and end the version while its files sit on
		// the disk that comes back. It is the same question as an empty mount
		// point: cannot tell yet, so transient.
		if errors.Is(v.keptErr, fs.ErrNotExist) {
			return versions.PurgeKeep, "", v.keptErr
		}
		return versions.PurgeKeep, v.keptErr.Error(), nil
	case v.notEmptyDir:
		return versions.PurgeKeep, fmt.Sprintf("%s is a directory that still holds files, so it is not removed", path), nil
	case v.remove == "":
		return versions.PurgeGone, "", nil
	}
	return versions.PurgeRemove, "", nil
}
