// file: internal/server/operation_text_redact.go
// version: 1.0.0
// guid: 7c2d9e41-0b86-4a35-9f13-e8a5b6d1c704
// last-edited: 2026-10-26

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// opTextFor returns op as the caller may read it: a copy whose Message and
// ErrorMessage are replaced by activity.WithheldUserText when they name another
// user and the caller does not hold users.manage (opaccess.Reader.UserText).
// It is for the handlers that serve a stored operation in the v1 shape
// (reconcile, the candidate-fetch results): the row's free text can name a
// user, and these routes serve it to every caller who may read the run. A copy,
// because op may be shared with the store's own value. nil stays nil.
func (s *Server) opTextFor(c *gin.Context, op *database.Operation) *database.Operation {
	if op == nil {
		return nil
	}
	userText := s.repairsOpAccess().For(c).UserText
	cp := *op
	cp.Message = userText(op.Message)
	if op.ErrorMessage != nil {
		msg := userText(*op.ErrorMessage)
		cp.ErrorMessage = &msg
	}
	return &cp
}
