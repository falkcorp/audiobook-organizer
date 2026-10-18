// file: internal/server/version_lifecycle.go
// version: 1.6.0
// guid: 5a3b4c0d-6e7f-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-18
//
// Version lifecycle HTTP handlers. Core logic lives in internal/versions.

package server

import (
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/gin-gonic/gin"
)

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
	versions.MarkTrashed(ver, time.Now())
	if err := s.Ops().UpdateBookVersion(ver); err != nil {
		httputil.InternalError(c, "trash version", err)
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
	if ver.Status != database.BookVersionStatusTrash {
		httputil.RespondWithBadRequest(c, "version is not in trash")
		return
	}

	versions.MarkRestored(ver)
	if err := s.Ops().UpdateBookVersion(ver); err != nil {
		httputil.InternalError(c, "restore version", err)
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
	if ver.Status != database.BookVersionStatusTrash {
		httputil.RespondWithBadRequest(c, "version is not in trash; trash it before purging")
		return
	}

	if err := versions.PurgeVersion(s.Ops(), ver); err != nil {
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
func CleanupTrashedVersions(store trashedVersionCleaner) (purged int) {
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
