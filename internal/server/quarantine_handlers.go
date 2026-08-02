// file: internal/server/quarantine_handlers.go
// version: 2.2.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// quarantineBook handles POST /api/v1/audiobooks/:id/quarantine
func (s *Server) quarantineBook(c *gin.Context) {
	id := c.Param("id")
	if s.Store() == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if req.Reason == "" {
		req.Reason = "manually quarantined"
	}

	if err := s.quarantineSvc.QuarantineBook(id, req.Reason); err != nil {
		httputil.InternalError(c, "quarantine failed", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Status string `json:"status"`
		BookID string `json:"book_id"`
	}{Status: "quarantined", BookID: id})
}

// unquarantineBook handles DELETE /api/v1/audiobooks/:id/quarantine
func (s *Server) unquarantineBook(c *gin.Context) {
	id := c.Param("id")
	if s.Store() == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	if err := s.quarantineSvc.UnquarantineBook(id); err != nil {
		httputil.InternalError(c, "unquarantine failed", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Status string `json:"status"`
		BookID string `json:"book_id"`
	}{Status: "unquarantined", BookID: id})
}

// listQuarantinedBooks handles GET /api/v1/audiobooks/quarantined
func (s *Server) listQuarantinedBooks(c *gin.Context) {
	if s.Store() == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	params := httputil.ParsePaginationParams(c)
	books, err := s.Store().GetQuarantinedBooks(params.Limit, params.Offset)
	if err != nil {
		httputil.InternalError(c, "list quarantined books failed", err)
		return
	}
	if books == nil {
		books = []database.Book{}
	}
	total, _ := s.Store().CountQuarantinedBooks()
	httputil.RespondWithOK(c, struct {
		Books []database.Book `json:"books"`
		Total int             `json:"total"`
	}{Books: books, Total: total})
}
