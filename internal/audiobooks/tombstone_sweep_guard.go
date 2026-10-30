// file: internal/audiobooks/tombstone_sweep_guard.go
// version: 1.0.1
// guid: 0e6a4d83-b15c-4f27-9c80-71d3a5e2f946
// last-edited: 2026-10-30

package audiobooks

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Tombstone sweep safety.
//
// sweep.SweepTombstones unlinks the file a leftover tombstone names. Until
// 2026-10-30 it did so with a bare os.Remove: no library-root check, no
// protected-path check, no look at whether a live, trashed or retired book still
// named the path. TombstoneSweepGuard gives it the purge's own decision
// (planPurgePathIn, the function the book purge and the version purge decide a
// path with), the purge's last-moment "does any book row still name it" question
// (otherBookRowAt), and a comparison of the file now on disk with the stamp the
// purge recorded when it committed to removing it (writeTombstone). The sweep
// holds none of these rules itself: it asks.

// stampOf is the TombstoneFileStamp of a purge verdict's file or empty
// directory.
func stampOf(id fileIdentity, isDir bool) database.TombstoneFileStamp {
	return database.TombstoneFileStamp{HasInode: id.HasInode, Ino: id.Ino, Size: id.Size, MtimeNS: id.MtimeNS, IsDir: isDir}
}

// identityFromStamp is the fileIdentity a stamp records. A stamp always came
// from an identityOf, so it is Known.
func identityFromStamp(s database.TombstoneFileStamp) fileIdentity {
	return fileIdentity{Known: true, HasInode: s.HasInode, Ino: s.Ino, Size: s.Size, MtimeNS: s.MtimeNS}
}

// writeTombstone writes book's tombstone. When the purge has committed to
// removing the book's file (a plan that is to be journaled, made for this very
// path, that found a file to remove) the tombstone carries a stamp of that file.
// Otherwise it is a plain tombstone, and the sweeper never removes its file.
//
// That condition is the point of the stamp. A purge WITHOUT delete_files also
// writes a tombstone, and if it dies before the tombstone is cleaned up, the
// file named in it is one the owner chose to keep: every other check the sweeper
// makes (root, protected, referenced) would pass for an ordinary library file,
// and a stamp written unconditionally would match it. The stamp exists only when
// the purge decided the file was to go.
//
// A store that cannot keep a stamp (a decorator that does not forward the
// capability) gets the plain tombstone: the file then stays, which is the safe
// side.
func (svc *AudiobookService) writeTombstone(book *database.Book, plan *filePlan) error {
	if stamped(book, plan) {
		if w, ok := database.AsCapability[database.StampedTombstoneWriter](svc.store); ok {
			return w.CreateStampedBookTombstone(book, stampOf(plan.v.id, plan.v.isDir))
		}
	}
	return svc.store.CreateBookTombstone(book)
}

// stamped says whether writeTombstone records a file stamp for book under plan:
// the purge has committed to removing the file.
func stamped(book *database.Book, plan *filePlan) bool {
	return plan != nil && plan.journal && plan.isFor(book) && plan.v.remove != "" && plan.v.id.Known
}

// withdrawTombstoneStamp takes the file stamp back after the purge's row delete
// failed: the book is still here, so no file removal is decided any more, and a
// tombstone still saying "this file is to go" would disagree with the live row
// (the sweep guard refuses the path anyway, because the row names it). The
// tombstone is rewritten plain, not deleted, which is what the code before
// stamps left on this path ("the sweeper will clean up the tombstone"). Returns
// text for the error line when the rewrite fails too, empty otherwise.
func (svc *AudiobookService) withdrawTombstoneStamp(book *database.Book, plan *filePlan) string {
	if !stamped(book, plan) {
		return ""
	}
	if err := svc.store.CreateBookTombstone(book); err != nil {
		singleLog.Warn("purge %s: the delete failed and the file stamp of its tombstone could not be withdrawn: %v",
			logger.SanitizeLogValue(book.ID), err)
		return fmt.Sprintf("; the file stamp of its tombstone could not be withdrawn either (%v), so the stamped tombstone stays; the sweep still keeps the file while this book names it", err)
	}
	return ""
}

// TombstoneSweepGuard decides whether the tombstone sweeper may unlink one path.
// It satisfies sweep.FileGuard structurally (builtin and database types only),
// so package sweep and package audiobooks do not import each other: the server
// hands one to the operations handler (WithTombstoneSweepGuard).
type TombstoneSweepGuard struct{ svc *AudiobookService }

// TombstoneSweepGuard returns the guard for the tombstone sweeper.
func (svc *AudiobookService) TombstoneSweepGuard() TombstoneSweepGuard {
	return TombstoneSweepGuard{svc: svc}
}

// RootReady is the library-root check (libraryRootReady) on the configured
// root, the same one a version purge makes before it unlinks anything: a library
// that is not mounted reads as "every file already gone".
func (g TombstoneSweepGuard) RootReady() error {
	return g.svc.libraryRootReady(config.Snapshot().RootDir)
}

// Check says what the sweeper may do about the file at path, named by a
// tombstone that carries stamp (nil when it carries none).
//
//   - remove true, err nil: the path holds the very file the purge decided to
//     remove, and nothing protects it or names it. Unlink it.
//   - remove false, err nil: nothing is at the path (or it is an empty-directory
//     case already gone). There is nothing to unlink; the tombstone can go.
//   - err non-nil: the file stays and so does the tombstone. err says why, and
//     is also the answer for a lookup that could not be made (fail closed).
func (g TombstoneSweepGuard) Check(path string, stamp *database.TombstoneFileStamp) (remove bool, err error) {
	svc := g.svc
	v := svc.planPurgePathIn(path, nil, svc.purgePathStillReferenced)
	switch {
	case v.protected:
		return false, fmt.Errorf("%s is under an import path, the iTunes library or a .failed folder, which is never deleted", path)
	case v.keptErr != nil:
		return false, v.keptErr
	case v.notEmptyDir:
		return false, fmt.Errorf("%s is a directory that still holds files, so it is not removed", path)
	case v.remove == "":
		return false, nil
	}
	// Something is there. A book row of any kind (a trashed one a restore would
	// bring back included) naming it is checked last, as the purge does just
	// before it unlinks.
	if err := svc.otherBookRowAt(v.remove); err != nil {
		return false, err
	}
	if stamp == nil {
		return false, fmt.Errorf("%s: the tombstone carries no stamp of a file removal a purge decided on (it predates stamps, or its purge did not delete files), so the file is kept", path)
	}
	if stamp.IsDir != v.isDir || !identityFromStamp(*stamp).matches(v.id, v.isDir) {
		return false, fmt.Errorf("%s is not the file the purge decided to remove (it was replaced or changed since), so it is kept", path)
	}
	return true, nil
}
