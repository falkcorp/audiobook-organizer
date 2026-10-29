// file: internal/server/version_lifecycle.go
// version: 1.17.0
// guid: 5a3b4c0d-6e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-29
//
// Version lifecycle HTTP handlers. Core logic lives in internal/versions.

package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/gin-gonic/gin"
)

// respondIfRetiredVersion answers 409 and reports true when err says the
// version is a retired one (versions.ErrRetiredVersion). A retired version is
// not part of the trash/restore/purge machine, so the refusal is a conflict
// with the version's state, not a bad request.
func respondIfRetiredVersion(c *gin.Context, err error) bool {
	if !errors.Is(err, versions.ErrRetiredVersion) {
		return false
	}
	httputil.RespondWithConflict(c, err.Error())
	return true
}

// respondIfPurgingVersion answers 409 and reports true when err says the
// version's purge has started (database.ErrVersionPurging): the version cannot
// be restored or trashed, and the cleanup or purge-now finishes it.
func respondIfPurgingVersion(c *gin.Context, err error) bool {
	if !errors.Is(err, database.ErrVersionPurging) {
		return false
	}
	httputil.RespondWithConflict(c, err.Error())
	return true
}

// respondIfPurgedVersion answers 409 and reports true when err says the version
// is already purged (database.ErrVersionPurged): its files are gone, so it cannot
// be put back in the trash.
func respondIfPurgedVersion(c *gin.Context, err error) bool {
	if !errors.Is(err, database.ErrVersionPurged) {
		return false
	}
	httputil.RespondWithConflict(c, err.Error())
	return true
}

// respondIfSwappingVersion answers 409 and reports true when err says the
// version is in swapping_out/swapping_in (versions.ErrVersionSwapping): a swap of
// the book stopped half way, and trashing either of its versions would let the
// nightly cleanup delete files the book plays. A retried DELETE after a
// VERSION_SWAP_INCOMPLETE lands here.
func respondIfSwappingVersion(c *gin.Context, err error) bool {
	if !errors.Is(err, versions.ErrVersionSwapping) {
		return false
	}
	httputil.RespondWithError(c, http.StatusConflict, err.Error(), codeVersionSwapping)
	return true
}

var versionLifecycleLog = logger.New("server.version-lifecycle")

// codeNoSwapTarget is the error code of the 409 that refuses to trash a book's
// active version because no other version can take over as the active one
// (versions.ErrNoSwapTarget). Nothing was written.
const codeNoSwapTarget = "NO_SWAP_TARGET"

// codeVersionSwapFailed is the error code of the 500 that says the swap that
// had to come before trashing the active version failed. The version was not
// trashed, and every file and status the swap changed was put back.
const codeVersionSwapFailed = "VERSION_SWAP_FAILED"

// codeVersionSwapIncomplete is the error code of the 500 that says the swap
// moved files and could not finish (or undo): the book's versions are stuck in
// swapping_out/swapping_in and need repair. The version was not trashed.
// Repeating the DELETE does not repair the book: it is refused
// (codeVersionSwapping) while any version of the book is in swapping_*.
// POST /audiobooks/:id/versions/resume-swap repairs it.
const codeVersionSwapIncomplete = "VERSION_SWAP_INCOMPLETE"

// codeVersionSwapping is the error code of the 409 for a trash of a version of a
// book that has a version in swapping_out/swapping_in (versions.ErrVersionSwapping
// for the version itself).
const codeVersionSwapping = "VERSION_SWAPPING"

// codeSwapResumeRefused is the error code of the 409 for a resume that found the
// book in a state it cannot repair without guessing (versions.ErrSwapResumeRefused);
// nothing was changed and the message says what was found.
const codeSwapResumeRefused = "SWAP_RESUME_REFUSED"

// codeNoSwapToResume is the error code of the 409 for a resume of a book none of
// whose versions is in swapping_out/swapping_in (versions.ErrNoSwapToResume).
const codeNoSwapToResume = "NO_SWAP_TO_RESUME"

// handleTrashVersion moves a version to trash.
// DELETE /api/v1/audiobooks/:id/versions/:vid
//
// Trashing the book's ACTIVE version is "swap first, then trash" (owner
// decision, 2026-10-25): the book keeps serving the files of the active
// version, and the nightly cleanup deletes a trashed version's files, so a
// version trashed while the book still plays it lost the book its audio. The
// handler first makes another version active (versions.PickSwapTarget chooses
// the newest alt that owns movable files; versions.RunVersionSwap moves the
// files and the book's file_path), and only then trashes the version that left.
// With no eligible version it answers 409 NO_SWAP_TARGET and writes nothing; if
// the swap fails the version is not trashed.
//
// The merge lock is held from the re-read of the version to the end of the
// trash, for every version, not only the active one: a trash of an alt that
// read the version as alt would otherwise write "trash" over a version a
// concurrent swap had just made active (UpdateBookVersion has no
// compare-and-swap on the status), which is the loss again. Purge takes the same
// lock. It is waited for only as long as an interactive click should hang.
func (s *Server) handleTrashVersion(c *gin.Context) {
	bookID := c.Param("id")
	versionID := c.Param("vid")

	// The cheap refusals come first, without the lock.
	ver, err := s.Ops().GetBookVersion(versionID)
	if err != nil || ver == nil {
		httputil.RespondWithNotFound(c, "version", "")
		return
	}
	if ver.BookID != bookID {
		httputil.RespondWithBadRequest(c, "version/book mismatch")
		return
	}

	unlock, err := merge.TryLockMergeRMWWithin(merge.InteractiveMergeLockWait)
	if err != nil {
		if errors.Is(err, merge.ErrMergeBusy) {
			httputil.RespondWithConflict(c, "a merge or combine is running; the version was not trashed, try again in a moment: "+err.Error())
			return
		}
		httputil.InternalError(c, "trash version", err)
		return
	}
	// Deluge is told after the lock is released: it is a network call.
	afterUnlock := func() func() {
		defer unlock()
		return s.trashVersionLocked(c, bookID, versionID)
	}()
	if afterUnlock != nil {
		afterUnlock()
	}
}

// trashVersionLocked is handleTrashVersion with the merge lock held. It answers
// the request itself and returns what is to run once the lock is released.
func (s *Server) trashVersionLocked(c *gin.Context, bookID, versionID string) (afterUnlock func()) {
	// Re-read under the lock: the version may have changed since the unlocked
	// read (another request swapped it in, trashed it, purged it).
	ver, err := s.Ops().GetBookVersion(versionID)
	if err != nil || ver == nil {
		httputil.RespondWithNotFound(c, "version", "")
		return nil
	}
	if ver.BookID != bookID {
		httputil.RespondWithBadRequest(c, "version/book mismatch")
		return nil
	}

	// A book with a version in swapping_out/swapping_in has a swap that did not
	// finish, and no trash of any of its versions is made until it is resumed:
	// the leaving version of such a swap can already be alt (the final write of
	// the entering version to active failed), where a retried DELETE would trash
	// it while the book still plays files of a version that is not active, and the
	// nightly cleanup would count its 14 days from a book that needs repair.
	// MarkTrashed refuses a swapping_* version itself; this covers the others.
	siblings, err := s.Ops().GetBookVersionsByBookID(bookID)
	if err != nil {
		httputil.InternalError(c, "trash version", err)
		return nil
	}
	for _, sib := range siblings {
		if sib.Status == database.BookVersionStatusSwappingOut || sib.Status == database.BookVersionStatusSwappingIn {
			httputil.RespondWithError(c, http.StatusConflict,
				"The version was not trashed: a swap of this book did not finish (version "+sib.ID+" is "+sib.Status+"), so the book has no settled active version. "+
					"Resume the swap first (Version Management, Unfinished swaps), then delete the version.", codeVersionSwapping)
			return nil
		}
	}

	// Only the active version needs a swap first. Every other status goes
	// straight to MarkTrashed below, which refuses a retired, purging, purged
	// or swapping_* version (a swap that stopped half way: this is where a
	// retried DELETE after VERSION_SWAP_INCOMPLETE ends) before anything is
	// written.
	var swappedTo string
	if ver.Status == database.BookVersionStatusActive {
		target, notify, ok := s.swapActiveVersionOut(c, ver)
		if !ok {
			return nil
		}
		swappedTo = target
		afterUnlock = notify
		// ver is a copy from before the swap (still "active", old row
		// version): the trash below must write the row as it is now.
		ver, err = s.Ops().GetBookVersion(versionID)
		if err != nil || ver == nil {
			if err == nil {
				err = errors.New("the version is gone")
			}
			s.respondTrashFailedAfterSwap(c, swappedTo, fmt.Errorf("re-read version %s after the swap: %w", versionID, err))
			return afterUnlock
		}
	}

	// Stamps TrashedAt: the 14-day window before the trash cleanup purges
	// this version's files is counted from now.
	//
	// A retired version is refused (409) before anything is written: the store
	// refuses to change one only while it still holds retired rows, and a
	// retired version in the trash would be purged from disk 14 days later.
	if err := versions.MarkTrashed(ver, time.Now()); err != nil {
		if !respondIfRetiredVersion(c, err) && !respondIfPurgingVersion(c, err) && !respondIfSwappingVersion(c, err) && !respondIfPurgedVersion(c, err) {
			s.respondTrashFailedAfterSwap(c, swappedTo, err)
		}
		return afterUnlock
	}
	if err := s.Ops().UpdateBookVersion(ver); err != nil {
		if !respondIfPurgingVersion(c, err) && !respondIfPurgedVersion(c, err) {
			s.respondTrashFailedAfterSwap(c, swappedTo, err)
		}
		return afterUnlock
	}

	httputil.RespondWithOK(c, struct {
		Version any `json:"version"`
		// SwappedTo is the version that became active because the trashed
		// one was; unset when the trashed version was not the active one.
		SwappedTo string `json:"swapped_to,omitempty"`
	}{Version: ver, SwappedTo: swappedTo})
	return afterUnlock
}

// respondTrashFailedAfterSwap answers the 500 for a trash that failed. When the
// swap before it had gone through (swappedTo is set) the book now plays another
// version although this one was not trashed, and the answer says which: a client
// that only saw "500" would think nothing had changed.
func (s *Server) respondTrashFailedAfterSwap(c *gin.Context, swappedTo string, err error) {
	if swappedTo == "" {
		httputil.InternalError(c, "trash version", err)
		return
	}
	versionLifecycleLog.Error("trash version %s of book %s failed after the swap; the book already plays version %s: %v",
		c.Param("vid"), c.Param("id"), swappedTo, err)
	httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
		"the book was switched to version "+swappedTo+" but trashing this version failed; it is no longer the active version and can be trashed again",
		"INTERNAL_ERROR", map[string]any{"swapped_to": swappedTo})
}

// swapActiveVersionOut makes another version of the book active in place of
// the active version ver, which is about to be trashed. The merge lock is held.
// ok is false when it has answered the request (no eligible version: 409; the
// swap failed: 500) and the version must not be trashed. notifyDeluge, which may
// be nil, is for the caller to run once the lock is released.
func (s *Server) swapActiveVersionOut(c *gin.Context, ver *database.BookVersion) (targetID string, notifyDeluge func(), ok bool) {
	store := s.storeForWiring()
	target, err := versions.PickSwapTarget(store, ver.BookID, ver.ID)
	if err != nil {
		var none *versions.NoSwapTargetError
		if errors.As(err, &none) {
			httputil.RespondWithError(c, http.StatusConflict,
				"The version was not trashed: it is the book's active version and no other version can take over as the active one ("+
					none.Why()+"). Add or restore another version first.", codeNoSwapTarget)
			return "", nil, false
		}
		httputil.InternalError(c, "trash version", err)
		return "", nil, false
	}

	var from, to *database.BookVersion
	var bookPath string
	err = versions.RunVersionSwap(c.Request.Context(), store, versions.VersionSwapParams{
		BookID:        ver.BookID,
		FromVersionID: ver.ID,
		ToVersionID:   target.ID,
	}, nil, nil, func(fromVer, toVer *database.BookVersion, bookFilePath string) {
		from, to, bookPath = fromVer, toVer, bookFilePath
	})
	if err != nil {
		// The files moved (or could not be moved back) and the versions are left
		// in swapping_*: the book is half switched. Not a plain failure: the
		// answer says repair is needed and does not invite a retry. A retry is
		// refused for a version still in swapping_* (versions.ErrVersionSwapping),
		// but not always for the one asked about: when the final write of the
		// leaving version to alt landed and the entering version's write to
		// active failed, the leaving version is alt and a retried DELETE trashes
		// it; only the entering version stays swapping_in.
		if errors.Is(err, versions.ErrSwapIncomplete) {
			versionLifecycleLog.Error("swap before trashing the active version did not finish; book %s versions %s -> %s are left in swapping_out/swapping_in and the version was not trashed: %v",
				ver.BookID, ver.ID, target.ID, err)
			httputil.RespondWithError(c, http.StatusInternalServerError,
				"the version was not trashed. Switching the book to version "+target.ID+" moved files and then could not finish: "+
					"the book's versions are stuck mid-swap (swapping_out / swapping_in) and its files may be in new places. "+
					"This needs repair: resume the swap from Version Management (Unfinished swaps), which finishes it or puts the files back. "+
					"Deleting a version of this book is refused until then. ("+err.Error()+")",
				codeVersionSwapIncomplete)
			return "", nil, false
		}
		// Refused before anything was written (the files are not where the
		// picker saw them, a retired version): a conflict with the library's
		// state, answered like the picker's own refusal.
		if errors.Is(err, versions.ErrSwapRefused) {
			httputil.RespondWithError(c, http.StatusConflict,
				"The version was not trashed: it is the book's active version and switching the book to version "+target.ID+
					" was refused before anything was changed ("+err.Error()+"). Add or restore another version first.", codeNoSwapTarget)
			return "", nil, false
		}
		if respondIfRetiredVersion(c, err) {
			return "", nil, false
		}
		slog.Error("swap before trashing the active version failed; the version was not trashed",
			"book", ver.BookID, "from", ver.ID, "to", target.ID, "err", err)
		httputil.RespondWithError(c, http.StatusInternalServerError,
			"the version was not trashed: switching the book to version "+target.ID+" first failed: "+err.Error(),
			codeVersionSwapFailed)
		return "", nil, false
	}
	return target.ID, func() { NotifyDelugeAfterVersionSwap(store, from, to, bookPath) }, true
}

// handleRestoreVersion restores a trashed version to alt.
// POST /api/v1/audiobooks/:id/versions/:vid/restore
func (s *Server) handleRestoreVersion(c *gin.Context) {
	bookID := c.Param("id")
	versionID := c.Param("vid")

	ver, err := s.Ops().GetBookVersion(versionID)
	if err != nil || ver == nil {
		httputil.RespondWithNotFound(c, "version", "")
		return
	}
	if ver.BookID != bookID {
		httputil.RespondWithBadRequest(c, "version/book mismatch")
		return
	}
	// A retired version is answered with its own 409 before the not-in-trash
	// check, as purge-now does: it is never in the trash, so "not in trash"
	// would be a misleading 400. MarkRestored refuses it as well.
	if ver.Status == database.BookVersionStatusRetired {
		httputil.RespondWithConflict(c, "version is a retired version; it is not in the trash and cannot be restored")
		return
	}
	// A version whose purge has started is answered with its own 409 for the
	// same reason: it is not "not in trash" (it is in the trash page, with a
	// purge that did not finish), it is refused because some of its files are
	// already gone. MarkRestored refuses it as well, and so does the store, for
	// a purge claimed after the read above.
	switch ver.Status {
	case database.BookVersionStatusPurging:
		httputil.RespondWithConflict(c, "version cannot be restored: an earlier purge of it removed some of its files and did not finish; "+
			"the nightly cleanup (or Purge now) finishes it, and if it cannot it becomes purge_failed after "+
			strconv.Itoa(versions.PurgeUndecidedEscalateAfter)+" attempts, which can be hard-deleted")
		return
	case database.BookVersionStatusPurgeFailed:
		httputil.RespondWithConflict(c, "version cannot be restored: an earlier purge of it removed some of its files and could not remove the rest; "+
			"Purge now retries it, or it can be hard-deleted")
		return
	}
	if ver.Status != database.BookVersionStatusTrash {
		httputil.RespondWithBadRequest(c, "version is not in trash")
		return
	}

	if err := versions.MarkRestored(ver); err != nil {
		if !respondIfRetiredVersion(c, err) && !respondIfPurgingVersion(c, err) {
			httputil.InternalError(c, "restore version", err)
		}
		return
	}
	if err := s.Ops().UpdateBookVersion(ver); err != nil {
		if !respondIfPurgingVersion(c, err) {
			httputil.InternalError(c, "restore version", err)
		}
		return
	}

	httputil.RespondWithOK(c, struct {
		Version any `json:"version"`
	}{Version: ver})
}

// handlePurgeVersion physically deletes files and marks purged: "Purge now"
// on the trash page, i.e. empty one version out of the trash early.
// POST /api/v1/audiobooks/:id/versions/:vid/purge-now
//
// Only a trashed version may be purged. Until 2026-10-11 there was no status
// check, so a direct call on the ACTIVE version deleted the book's playable
// files at the book root, dropped its active index and promoted nothing; on
// an alt it deleted the files with no trash window at all. The only UI caller
// (the trash page) already sends trashed versions only, so the guard costs
// that flow nothing; anything else has to go through the trash first.
func (s *Server) handlePurgeVersion(c *gin.Context) {
	bookID := c.Param("id")
	versionID := c.Param("vid")

	ver, err := s.Ops().GetBookVersion(versionID)
	if err != nil || ver == nil {
		httputil.RespondWithNotFound(c, "version", "")
		return
	}
	if ver.BookID != bookID {
		httputil.RespondWithBadRequest(c, "version/book mismatch")
		return
	}
	// A retired version is refused with its own answer before the not-in-trash
	// check: "trash it first" would be wrong advice for it (trashing is refused
	// too). PurgeVersion refuses it as well, ahead of any disk write.
	if ver.Status == database.BookVersionStatusRetired {
		httputil.RespondWithConflict(c, "version is a retired version; purging it would delete files a live book keeps")
		return
	}
	// A purging version is a purge that failed part way, and a purge_failed one a
	// purge that gave up on a file it cannot remove: purge-now retries both.
	if ver.Status != database.BookVersionStatusTrash && ver.Status != database.BookVersionStatusPurging &&
		ver.Status != database.BookVersionStatusPurgeFailed {
		httputil.RespondWithBadRequest(c, "version is not in trash; trash it before purging")
		return
	}

	// The merge lock is waited for only as long as an interactive click should
	// hang: behind a running combine the answer is "try again", nothing started.
	if err := versions.PurgeVersionWithin(s.Ops(), ver, s.versionPurgeGuard(), merge.InteractiveMergeLockWait); err != nil {
		if respondIfRetiredVersion(c, err) {
			return
		}
		// A live book still serves a file of the version: nothing was deleted or
		// claimed, the version is still in the trash with every file intact.
		var served *versions.BookServesFilesError
		if errors.As(err, &served) {
			httputil.RespondWithConflict(c, "version not purged: the book still serves its file(s); restore the version or change what the book plays first")
			return
		}
		// Restored after the read above: the claim was refused, nothing was
		// deleted, and the restore stands.
		if errors.Is(err, versions.ErrVersionDeleted) {
			httputil.RespondWithConflict(c, "the version was deleted while the purge was starting; nothing more was deleted: "+err.Error())
			return
		}
		if errors.Is(err, versions.ErrVersionAlreadyPurged) {
			httputil.RespondWithConflict(c, "the version was already purged (by the nightly cleanup or another purge) while this purge was starting; nothing more was deleted: "+err.Error())
			return
		}
		if errors.Is(err, versions.ErrVersionNotInTrash) {
			httputil.RespondWithConflict(c, "the version was restored while the purge was starting; nothing was deleted: "+err.Error())
			return
		}
		if errors.Is(err, merge.ErrMergeBusy) {
			httputil.RespondWithConflict(c, "a merge or combine is running; nothing was deleted, try again in a moment: "+err.Error())
			return
		}
		// The library does not look mounted: nothing was deleted or claimed.
		if errors.Is(err, audiobooks.ErrLibraryRootNotReady) {
			httputil.RespondWithServiceUnavailable(c, "the library does not look mounted, so nothing was deleted; try again once it is: "+err.Error())
			return
		}
		// Nothing was removed because the files that are left are not there and
		// nothing vouches that they were removed (their folder is missing or
		// empty, which is also how a dataset that is not mounted looks). A trashed
		// version is still in the trash and can be restored; a purging one that
		// has not settled for several attempts becomes purge_failed (the branch
		// below) and can be hard-deleted.
		if errors.Is(err, versions.ErrPurgeCannotTell) {
			httputil.RespondWithConflict(c, err.Error())
			return
		}
		// The book serves nothing that exists on disk (its path is stale or lost,
		// or a folder with no file in it): the guard has nothing to compare the
		// version's files against, so nothing was deleted and the version is
		// still in the trash. The book's path is what to fix, or restore the
		// version; the error names it.
		if errors.Is(err, versions.ErrPurgeNothingServed) {
			httputil.RespondWithConflict(c, err.Error())
			return
		}
		// A file that cannot be removed and never will be (permission, a
		// read-only filesystem): the version is now purge_failed. It is the
		// version's state, not a server fault: the owner retries or hard-deletes it.
		if errors.Is(err, versions.ErrPurgeFailed) {
			httputil.RespondWithConflict(c, err.Error())
			return
		}
		// PurgeVersion fails closed while the book_file index cannot name every
		// owner of a path (the database is still warming up, or the table is
		// flagged incomplete): nothing was deleted and the version is still in
		// the trash, so this is "try again", not a server fault.
		if errors.Is(err, database.ErrBookFilesAtPathUnavailable) {
			httputil.RespondWithServiceUnavailable(c, "the book_file index cannot yet say whether another book uses these files (the database is still starting up, or the index is flagged incomplete until a restart); nothing was deleted, try again shortly: "+err.Error())
			return
		}
		httputil.InternalError(c, "purge version", err)
		return
	}

	httputil.RespondWithOK(c, struct {
		Version any `json:"version"`
	}{Version: ver})
}

// versionPurgeGuard is the book purge's file safety for a version purge, or
// nil when the audiobook service is not wired (PurgeVersion then deletes
// nothing and says so).
func (s *Server) versionPurgeGuard() versions.PurgeGuard {
	if s.audiobookService == nil {
		return nil
	}
	return s.audiobookService.VersionPurgeGuard()
}

// handleListTrashedVersions lists the versions in the trash page's Trash tab:
// trashed ones, purges that did not finish (purging) and purges that gave up
// (purge_failed, with the files and reasons in purge_failed).
// GET /api/v1/audiobooks/trashed-versions
func (s *Server) handleListTrashedVersions(c *gin.Context) {
	list, err := s.Ops().ListTrashedBookVersions()
	if err != nil {
		httputil.InternalError(c, "list trashed versions", err)
		return
	}
	respondVersionList(c, list)
}

// handleListPurgedVersions lists the Purged tab: versions whose files were
// removed (purge_kept names the files a purge left because another row or book
// names them).
// GET /api/v1/audiobooks/purged-versions
func (s *Server) handleListPurgedVersions(c *gin.Context) {
	list, err := s.Ops().ListPurgedBookVersions()
	if err != nil {
		httputil.InternalError(c, "list purged versions", err)
		return
	}
	respondVersionList(c, list)
}

func respondVersionList(c *gin.Context, list []database.BookVersion) {
	if list == nil {
		list = []database.BookVersion{}
	}
	httputil.RespondWithOK(c, struct {
		Versions []database.BookVersion `json:"versions"`
		Count    int                    `json:"count"`
	}{Versions: list, Count: len(list)})
}

// handleHardDeleteVersion removes all traces of a purged version (or of one
// whose purge failed for good).
// DELETE /api/v1/purged-versions/:vid
func (s *Server) handleHardDeleteVersion(c *gin.Context) {
	versionID := c.Param("vid")

	ver, err := s.Ops().GetBookVersion(versionID)
	if err != nil || ver == nil {
		httputil.RespondWithNotFound(c, "version", "")
		return
	}
	// A purging version has a purge that is not finished: its record is what tells
	// the retry which files it still owes, so it stays until the purge ends. A
	// purge that cannot finish becomes purge_failed after
	// versions.PurgeUndecidedEscalateAfter attempts, and that is hard-deletable.
	if ver.Status == database.BookVersionStatusPurging {
		httputil.RespondWithConflict(c, "version's purge is not finished, so it cannot be deleted yet; the nightly cleanup (or Purge now) finishes it, "+
			"and if it cannot it becomes purge_failed after "+strconv.Itoa(versions.PurgeUndecidedEscalateAfter)+" attempts, which can be hard-deleted")
		return
	}
	// A purge_failed version is hard-deletable: its record goes, the files named
	// in purge_failed stay where they are (the owner was told which).
	if ver.Status != database.BookVersionStatusInactivePurged && ver.Status != database.BookVersionStatusPurgeFailed {
		httputil.RespondWithBadRequest(c, "version is not purged")
		return
	}

	if err := s.Ops().DeleteBookVersion(ver.ID); err != nil {
		// A purge-now that claimed the version after the read above: its purge
		// is running, so the record stays until it ends (database.ErrVersionPurging).
		if respondIfPurgingVersion(c, err) {
			return
		}
		httputil.InternalError(c, "hard delete version", err)
		return
	}

	httputil.RespondWithOK(c, struct {
		Deleted string `json:"deleted"`
	}{Deleted: ver.ID})
}

// handleListSwapIncompleteBooks lists the books whose version swap did not
// finish (a version in swapping_out/swapping_in), each with its path, its
// versions' file rows and what a resume would do or why it would refuse.
// GET /api/v1/audiobooks/swap-incomplete
func (s *Server) handleListSwapIncompleteBooks(c *gin.Context) {
	books, err := versions.ListSwapIncompleteBooks(s.storeForWiring())
	if err != nil {
		httputil.InternalError(c, "list unfinished swaps", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Books []versions.SwapIncompleteBook `json:"books"`
		Count int                           `json:"count"`
	}{Books: books, Count: len(books)})
}

// notifyDelugeAfterSwapResume is how the resume handler tells Deluge; a
// variable so a test can see what a resume hands it without a Deluge to call.
var notifyDelugeAfterSwapResume = NotifyDelugeAfterVersionSwap

// handleResumeVersionSwap finishes or undoes a version swap that did not finish.
// POST /api/v1/audiobooks/:id/versions/resume-swap
//
// versions.ResumeVersionSwap reads the version rows, the file rows and the disk
// and either completes the swap or moves the files back, whichever the disk
// allows; it changes nothing and answers 409 when the state is not one it can
// repair without guessing (SWAP_RESUME_REFUSED, with the reason) or the book has
// no unfinished swap (NO_SWAP_TO_RESUME). A lookup the plan could not complete
// (database.ErrBookFilesAtPathUnavailable) is a fault, not a refusal: it is
// answered 500. It holds the merge lock, like the swap it repairs; the answer is
// sent first and Deluge is told after the lock is released.
func (s *Server) handleResumeVersionSwap(c *gin.Context) {
	bookID := c.Param("id")
	store := s.storeForWiring()
	book, err := store.GetBookByID(bookID)
	if err != nil {
		httputil.InternalError(c, "resume version swap", err)
		return
	}
	if book == nil {
		httputil.RespondWithNotFound(c, "book", bookID)
		return
	}

	unlock, err := merge.TryLockMergeRMWWithin(merge.InteractiveMergeLockWait)
	if err != nil {
		if errors.Is(err, merge.ErrMergeBusy) {
			httputil.RespondWithConflict(c, "a merge or combine is running; the swap was not resumed, try again in a moment: "+err.Error())
			return
		}
		httputil.InternalError(c, "resume version swap", err)
		return
	}
	res, err := versions.ResumeVersionSwap(c.Request.Context(), store, bookID)
	unlock()

	if err != nil {
		switch {
		case errors.Is(err, versions.ErrNoSwapToResume):
			httputil.RespondWithError(c, http.StatusConflict, err.Error(), codeNoSwapToResume)
		case errors.Is(err, versions.ErrSwapResumeRefused):
			httputil.RespondWithError(c, http.StatusConflict, err.Error(), codeSwapResumeRefused)
		case errors.Is(err, versions.ErrSwapIncomplete):
			versionLifecycleLog.Error("resuming the swap of book %s did not finish: %v", bookID, err)
			httputil.RespondWithError(c, http.StatusInternalServerError,
				"resuming the swap started and could not finish; the book is still listed as unfinished and resuming it again reads the disk anew ("+err.Error()+")",
				codeVersionSwapIncomplete)
		default:
			httputil.InternalError(c, "resume version swap", err)
		}
		return
	}
	httputil.RespondWithOK(c, struct {
		Action            string `json:"action"`
		BookID            string `json:"book_id"`
		LeavingVersionID  string `json:"leaving_version_id,omitempty"`
		EnteringVersionID string `json:"entering_version_id,omitempty"`
		ActiveVersionID   string `json:"active_version_id,omitempty"`
		BookPath          string `json:"book_path"`
		FilesMoved        int    `json:"files_moved"`
	}{res.Action, res.BookID, res.LeavingVersionID, res.EnteringVersionID, res.ActiveVersionID, res.BookPath, res.Moved})
	// Deluge is told after the answer is on its way, as the trash handler does:
	// the swap is done and Deluge is a network call that can take its time. A
	// finished swap hands back the entering version always, and the leaving one
	// when it was still swapping_out too (a swap that stopped at its status
	// writes may have only the entering one); the notification copes with either.
	if res.To != nil {
		c.Writer.Flush()
		notifyDelugeAfterSwapResume(store, res.From, res.To, res.BookPath)
	}
}

// CleanupTrashedVersions delegates to versions.CleanupTrashedVersions.
func CleanupTrashedVersions(store trashedVersionCleaner, guard versions.PurgeGuard) (versions.CleanupResult, error) {
	return versions.CleanupTrashedVersions(store, guard)
}

// registerVersionLifecycleRoutes wires the version lifecycle endpoints.
//
// Canonical noun is /audiobooks/, matching every other per-book version
// route (see wire_library_routes.go's /audiobooks/:id/versions). The
// /books/:id/versions/:vid/... paths were DEPRECATED aliases of the same
// handlers (naming-audit 2026-09-25, class 1: docs/audits/2026-09-25-interface-naming-consistency.md);
// the web client only ever called the /audiobooks/ paths.
func (s *Server) registerVersionLifecycleRoutes(protected *gin.RouterGroup) {
	protected.DELETE("/audiobooks/:id/versions/:vid", s.perm(auth.PermLibraryDelete), s.handleTrashVersion)
	protected.POST("/audiobooks/:id/versions/:vid/restore", s.perm(auth.PermLibraryOrganize), s.handleRestoreVersion)
	protected.POST("/audiobooks/:id/versions/:vid/purge-now", s.perm(auth.PermLibraryDelete), s.handlePurgeVersion)
	// The three /books/:id/versions/:vid... aliases had no caller in the repo
	// (01-P81k). They answer 410 Gone for one release (owner decision D9) and
	// are then removed; the handlers stay because the /audiobooks/ routes above
	// use them. Each stub keeps its original s.perm guard, so an unauthenticated
	// caller still gets 401, a caller without the permission 403, and only an
	// allowed caller learns the alias is gone.
	protected.DELETE("/books/:id/versions/:vid", s.perm(auth.PermLibraryDelete), gone("DELETE /api/v1/audiobooks/:id/versions/:vid"))
	protected.POST("/books/:id/versions/:vid/restore", s.perm(auth.PermLibraryOrganize), gone("POST /api/v1/audiobooks/:id/versions/:vid/restore"))
	protected.POST("/books/:id/versions/:vid/purge-now", s.perm(auth.PermLibraryDelete), gone("POST /api/v1/audiobooks/:id/versions/:vid/purge-now"))
	protected.DELETE("/purged-versions/:vid", s.perm(auth.PermLibraryDelete), s.handleHardDeleteVersion)
	// The trash page's two tabs (web/src/pages/TrashedVersions.tsx). Read-only,
	// so the library read permission, like the per-book version list.
	protected.GET("/audiobooks/trashed-versions", s.perm(auth.PermLibraryView), s.handleListTrashedVersions)
	protected.GET("/audiobooks/purged-versions", s.perm(auth.PermLibraryView), s.handleListPurgedVersions)
	// Version swaps that did not finish (a version in swapping_out/swapping_in):
	// the list is the trash page's third tab, and resume-swap is its button. Resume
	// moves files back and forth but deletes nothing, so it takes the organize
	// permission, like restore, not the delete one.
	protected.GET("/audiobooks/swap-incomplete", s.perm(auth.PermLibraryView), s.handleListSwapIncompleteBooks)
	protected.POST("/audiobooks/:id/versions/resume-swap", s.perm(auth.PermLibraryOrganize), s.handleResumeVersionSwap)
}
