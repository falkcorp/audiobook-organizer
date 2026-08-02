// file: internal/server/cover_history.go
// version: 1.2.1
// guid: 6d4e5f3a-7b8c-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-05-15
//
// HTTP handlers for cover art history browsing and restore.
// Each time a book's cover is updated, the previous cover is saved
// to covers/history/{bookID}/. This endpoint lists those versions
// so the user can browse and restore a previous cover.
//
// Business logic extracted to internal/covers.

package server

import (
	"os"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/covers"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/security/safepath"
	"github.com/gin-gonic/gin"
)

// handleListCoverHistory returns the cover art history for a book.
// GET /api/v1/audiobooks/:id/cover-history
func (s *Server) handleListCoverHistory(c *gin.Context) {
	bookID := c.Param("id")

	histCovers, err := covers.ListCoverHistory(bookID, config.AppConfig.RootDir)
	if err != nil {
		httputil.InternalError(c, "read cover history", err)
		return
	}

	httputil.RespondWithOK(c, struct {
		Covers []covers.CoverHistoryEntry `json:"covers"`
		Count  int                        `json:"count"`
	}{Covers: histCovers, Count: len(histCovers)})
}

// handleRestoreCover copies a history cover back as the book's
// current cover.
// POST /api/v1/audiobooks/:id/cover-history/restore
func (s *Server) handleRestoreCover(c *gin.Context) {
	bookID := c.Param("id")
	var req struct {
		Filename string `json:"filename" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	book, err := s.Store().GetBookByID(bookID)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "book", "")
		return
	}

	// Validate file exists before restoring
	histSP, err := safepath.Join(config.AppConfig.RootDir, "covers", "history", bookID, req.Filename)
	if err != nil {
		httputil.RespondWithNotFound(c, "cover file", "")
		return
	}
	if _, err := os.Stat(histSP.String()); os.IsNotExist(err) {
		httputil.RespondWithNotFound(c, "cover file", "")
		return
	}

	// Restore the cover file
	_, err = covers.RestoreCoverFile(bookID, req.Filename, config.AppConfig.RootDir)
	if err != nil {
		if err == os.ErrInvalid {
			httputil.RespondWithBadRequest(c, "invalid filename")
			return
		}
		if os.IsNotExist(err) {
			httputil.RespondWithNotFound(c, "cover file", "")
			return
		}
		httputil.InternalError(c, "restore cover", err)
		return
	}

	// Update book's cover_url
	ext := filepath.Ext(req.Filename)
	coverURL := "/api/v1/covers/local/" + bookID + ext
	book.CoverURL = &coverURL
	if _, err := s.Store().UpdateBook(book.ID, book); err != nil {
		httputil.InternalError(c, "update book cover", err)
		return
	}

	httputil.RespondWithOK(c, struct {
		CoverURL string `json:"cover_url"`
	}{CoverURL: coverURL})
}
