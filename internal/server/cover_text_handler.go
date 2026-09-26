// file: internal/server/cover_text_handler.go
// version: 1.0.0
// guid: 2f6d9b40-8a1e-4c73-b5e2-9c4a7d0f1e68
// last-edited: 2026-09-26

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/covertext"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// coverTextImage is one cover image of a book with what was read off it. The
// on-disk path is deliberately not returned.
type coverTextImage struct {
	Hash          string           `json:"hash"`
	Source        covertext.Source `json:"source"`
	Status        covertext.Status `json:"status,omitempty"`
	Text          *covertext.Text  `json:"text,omitempty"`
	Error         string           `json:"error,omitempty"`
	Model         string           `json:"model,omitempty"`
	PromptVersion string           `json:"prompt_version,omitempty"`
	ReadAt        string           `json:"read_at,omitempty"`
}

// coverTextResponse is GET /api/v1/audiobooks/:id/cover-text.
type coverTextResponse struct {
	BookID string           `json:"book_id"`
	Images []coverTextImage `json:"images"`
}

// handleGetCoverText returns the stored cover text for a book: every cover
// image maintenance.cover-text-read indexed for it, joined with its read. A
// book never indexed answers 200 with an empty list, not 404: "nothing read
// yet" is the normal state until the op has run.
func (s *Server) handleGetCoverText(c *gin.Context) {
	id := c.Param("id")
	if s.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	rows, err := covertext.ForBook(s.store, id)
	if err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	resp := coverTextResponse{BookID: id, Images: make([]coverTextImage, 0, len(rows))}
	for _, r := range rows {
		img := coverTextImage{Hash: r.Hash, Source: r.Source}
		if rec := r.Record; rec != nil {
			img.Status, img.Text, img.Error = rec.Status, rec.Text, rec.Error
			img.Model, img.PromptVersion = rec.Model, rec.PromptVersion
			if !rec.ReadAt.IsZero() {
				img.ReadAt = rec.ReadAt.UTC().Format("2006-01-02T15:04:05Z")
			}
		}
		resp.Images = append(resp.Images, img)
	}
	httputil.RespondWithOK(c, resp)
}
