// file: internal/server/handlers/reading.go
// version: 1.1.0
// guid: b8c9d0e1-f2a3-4567-bcde-567890123456
// last-edited: 2026-06-02

package handlers

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/readstatus"
	svrmw "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// SetPositionRequest is the JSON body for POST /api/v1/books/:id/position.
type SetPositionRequest struct {
	SegmentID       string  `json:"segment_id" binding:"required"`
	PositionSeconds float64 `json:"position_seconds"`
}

// PatchStatusRequest is the JSON body for PATCH /api/v1/books/:id/status.
type PatchStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

// ReadingStore is the narrow database interface ReadingHandler requires.
// It must satisfy the implicit interface expected by readstatus.RecomputeUserBookState
// and readstatus.SetManualStatus (interface{ database.BookFileStore; database.UserPositionStore })
// plus ListUserBookStatesByStatus for the list-by-status endpoint.
type ReadingStore interface {
	database.BookFileStore
	database.UserPositionStore
	ListUserBookStatesByStatus(userID, status string, limit, offset int) ([]database.UserBookState, error)
}

// ReadingHandler handles per-user read/progress tracking endpoints.
type ReadingHandler struct {
	store ReadingStore
}

// NewReadingHandler constructs a ReadingHandler backed by the given store.
func NewReadingHandler(store ReadingStore) *ReadingHandler {
	return &ReadingHandler{store: store}
}

// CallingUserID pulls the authenticated user's ID from context.
// Falls back to "_local" for unauthenticated / bootstrap mode so
// single-user installs can exercise the endpoints before running
// the 3.7 setup wizard.
func CallingUserID(c *gin.Context) string {
	if u, ok := auth.UserFromContext(c.Request.Context()); ok && u != nil {
		return u.ID
	}
	if u, ok := svrmw.CurrentUser(c); ok && u != nil {
		return u.ID
	}
	return "_local"
}

// SetPosition records one position heartbeat and recomputes derived UserBookState.
// POST /api/v1/books/:id/position
func (h *ReadingHandler) SetPosition(c *gin.Context) {
	bookID := c.Param("id")
	if bookID == "" {
		httputil.RespondWithBadRequest(c, "book id required")
		return
	}
	var req SetPositionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	userID := CallingUserID(c)
	if err := h.store.SetUserPosition(userID, bookID, req.SegmentID, req.PositionSeconds); err != nil {
		httputil.InternalError(c, "failed to record position", err)
		return
	}
	state, err := readstatus.RecomputeUserBookState(h.store, userID, bookID)
	if err != nil {
		httputil.InternalError(c, "failed to recompute book state", err)
		return
	}
	httputil.RespondWithOK(c, state)
}

// GetPosition returns the latest position for the calling user.
// GET /api/v1/books/:id/position
func (h *ReadingHandler) GetPosition(c *gin.Context) {
	bookID := c.Param("id")
	if bookID == "" {
		httputil.RespondWithBadRequest(c, "book id required")
		return
	}
	pos, err := h.store.GetUserPosition(CallingUserID(c), bookID)
	if err != nil {
		httputil.InternalError(c, "failed to load position", err)
		return
	}
	httputil.RespondWithOK(c, pos)
}

// GetBookState returns the derived UserBookState for the calling user.
// GET /api/v1/books/:id/state
func (h *ReadingHandler) GetBookState(c *gin.Context) {
	bookID := c.Param("id")
	if bookID == "" {
		httputil.RespondWithBadRequest(c, "book id required")
		return
	}
	state, err := h.store.GetUserBookState(CallingUserID(c), bookID)
	if err != nil {
		httputil.InternalError(c, "failed to load state", err)
		return
	}
	httputil.RespondWithOK(c, state)
}

// SetBookStatus sets a manual status override.
// PATCH /api/v1/books/:id/status
func (h *ReadingHandler) SetBookStatus(c *gin.Context) {
	bookID := c.Param("id")
	if bookID == "" {
		httputil.RespondWithBadRequest(c, "book id required")
		return
	}
	var req PatchStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	switch req.Status {
	case database.UserBookStatusFinished,
		database.UserBookStatusInProgress,
		database.UserBookStatusUnstarted,
		database.UserBookStatusAbandoned:
		// ok
	default:
		httputil.RespondWithBadRequest(c, "invalid status: "+req.Status)
		return
	}
	state, err := readstatus.SetManualStatus(h.store, CallingUserID(c), bookID, req.Status)
	if err != nil {
		httputil.InternalError(c, "failed to set status", err)
		return
	}
	httputil.RespondWithOK(c, state)
}

// ClearBookStatus clears the manual override.
// DELETE /api/v1/books/:id/status
func (h *ReadingHandler) ClearBookStatus(c *gin.Context) {
	bookID := c.Param("id")
	if bookID == "" {
		httputil.RespondWithBadRequest(c, "book id required")
		return
	}
	state, err := readstatus.SetManualStatus(h.store, CallingUserID(c), bookID, "")
	if err != nil {
		httputil.InternalError(c, "failed to clear status", err)
		return
	}
	httputil.RespondWithOK(c, state)
}

// ListByStatus returns the calling user's books filtered by status, paginated.
// GET /api/v1/me/:status
func (h *ReadingHandler) ListByStatus(c *gin.Context) {
	status := c.Param("status")
	switch status {
	case database.UserBookStatusInProgress,
		database.UserBookStatusFinished,
		database.UserBookStatusAbandoned,
		database.UserBookStatusUnstarted:
	default:
		httputil.RespondWithBadRequest(c, "invalid status")
		return
	}
	p := httputil.ParsePaginationParams(c)
	list, err := h.store.ListUserBookStatesByStatus(CallingUserID(c), status, p.Limit, p.Offset)
	if err != nil {
		httputil.InternalError(c, "failed to list states", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"states": list, "count": len(list), "limit": p.Limit, "offset": p.Offset})
}
