// file: internal/server/version_lifecycle.go
// version: 1.9.0
// guid: 5a3b4c0d-6e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-24
//
// Version lifecycle HTTP handlers. Core logic lives in internal/versions.

package server

import (
	"errors"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
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

// handleTrashVersion moves a version to trash.
// DELETE /api/v1/audiobooks/:id/versions/:vid
func (s *Server) handleTrashVersion(c *gin.Context) {
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

	wasActive := ver.Status == database.BookVersionStatusActive

	// Stamps TrashedAt: the 14-day window before the trash cleanup purges
	// this version's files is counted from now.
	//
	// A retired version is refused (409) before anything is written: the store
	// refuses to change one only while it still holds retired rows, and a
	// retired version in the trash would be purged from disk 14 days later.
	if err := versions.MarkTrashed(ver, time.Now()); err != nil {
		if !respondIfRetiredVersion(c, err) && !respondIfPurgingVersion(c, err) {
			httputil.InternalError(c, "trash version", err)
		}
		return
	}
	if err := s.Ops().UpdateBookVersion(ver); err != nil {
		if !respondIfPurgingVersion(c, err) {
			httputil.InternalError(c, "trash version", err)
		}
		return
	}

	if wasActive {
		if err := versions.AutoPromoteAlt(s.storeForWiring(), bookID); err != nil {
			slog.Warn("auto-promote after trash", "err", err)
		}
	}

	httputil.RespondWithOK(c, struct {
		Version any `json:"version"`
	}{Version: ver})
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
	if ver.Status == database.BookVersionStatusPurging {
		httputil.RespondWithConflict(c, "version cannot be restored: an earlier purge of it removed some of its files and did not finish; "+
			"the nightly cleanup (or Purge now) finishes it")
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
	// A purging version is a purge that failed part way: purge-now retries it.
	if ver.Status != database.BookVersionStatusTrash && ver.Status != database.BookVersionStatusPurging {
		httputil.RespondWithBadRequest(c, "version is not in trash; trash it before purging")
		return
	}

	if err := versions.PurgeVersion(s.Ops(), ver); err != nil {
		if respondIfRetiredVersion(c, err) {
			return
		}
		// Restored after the read above: the claim was refused, nothing was
		// deleted, and the restore stands.
		if errors.Is(err, versions.ErrVersionNotInTrash) {
			httputil.RespondWithConflict(c, "the version was restored while the purge was starting; nothing was deleted: "+err.Error())
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

// handleHardDeleteVersion removes all traces of a purged version.
// DELETE /api/v1/purged-versions/:vid
func (s *Server) handleHardDeleteVersion(c *gin.Context) {
	versionID := c.Param("vid")

	ver, err := s.Ops().GetBookVersion(versionID)
	if err != nil || ver == nil {
		httputil.RespondWithNotFound(c, "version", "")
		return
	}
	if ver.Status != database.BookVersionStatusInactivePurged {
		httputil.RespondWithBadRequest(c, "version is not purged")
		return
	}

	if err := s.Ops().DeleteBookVersion(ver.ID); err != nil {
		httputil.InternalError(c, "hard delete version", err)
		return
	}

	httputil.RespondWithOK(c, struct {
		Deleted string `json:"deleted"`
	}{Deleted: ver.ID})
}

// CleanupTrashedVersions delegates to versions.CleanupTrashedVersions.
func CleanupTrashedVersions(store trashedVersionCleaner) (purged, failed int, err error) {
	return versions.CleanupTrashedVersions(store)
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
}
