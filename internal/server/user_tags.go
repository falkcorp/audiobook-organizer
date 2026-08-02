// file: internal/server/user_tags.go
// version: 2.4.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef0123456789
// last-edited: 2026-07-13

package server

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// normalizeTag normalizes a single tag by trimming whitespace, converting to lowercase,
// and returning an empty string if the result is empty.
func normalizeTag(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	return t
}

// setupUserTagRoutes registers the user tag API routes on the given router group.
//
// These mutate global per-book tags (SetBookTags/AddBookTag/RemoveBookTag, not
// user-scoped), so they require library.edit_metadata — the same guard used by
// the sibling write POST /audiobooks/batch-tags, and consistent with the read
// side (GET .../user-tags requiring library.view).
func (s *Server) setupUserTagRoutes(protected *gin.RouterGroup) {
	protected.PUT("/audiobooks/:id/user-tags", s.perm(auth.PermLibraryEditMetadata), s.setBookUserTags)
	protected.POST("/audiobooks/:id/user-tags", s.perm(auth.PermLibraryEditMetadata), s.addBookUserTag)
	protected.DELETE("/audiobooks/:id/user-tags/:tag", s.perm(auth.PermLibraryEditMetadata), s.removeBookUserTag)
}

// setBookUserTags replaces all user-defined tags on a book.
func (s *Server) setBookUserTags(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	id := c.Param("id")
	// Verify the book exists before creating tag entries to prevent orphaned rows.
	if _, err := store.GetBookByID(id); err != nil {
		httputil.RespondWithNotFound(c, "book", id)
		return
	}
	var req struct {
		Tags []string `json:"tags" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	// Filter empty and normalize to lowercase
	validTags := make([]string, 0, len(req.Tags))
	for _, t := range req.Tags {
		t = normalizeTag(t)
		if t != "" {
			validTags = append(validTags, t)
		}
	}
	if err := store.SetBookTags(id, validTags); err != nil {
		httputil.InternalError(c, "failed to set tags", err)
		return
	}
	tags, err := store.GetBookTags(id)
	if err != nil {
		httputil.InternalError(c, "failed to get tags after set", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Tags []string `json:"tags"`
	}{Tags: tags})
}

// addBookUserTag adds a single user-defined tag to a book.
func (s *Server) addBookUserTag(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	id := c.Param("id")
	// Verify the book exists before creating tag entries to prevent orphaned rows.
	if _, err := store.GetBookByID(id); err != nil {
		httputil.RespondWithNotFound(c, "book", id)
		return
	}
	var req struct {
		Tag string `json:"tag" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	tag := normalizeTag(req.Tag)
	if err := store.AddBookTag(id, tag); err != nil {
		httputil.InternalError(c, "failed to add tag", err)
		return
	}
	tags, err := store.GetBookTags(id)
	if err != nil {
		httputil.InternalError(c, "failed to get tags after add", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Tags []string `json:"tags"`
	}{Tags: tags})
}

// removeBookUserTag removes a single user-defined tag from a book.
func (s *Server) removeBookUserTag(c *gin.Context) {
	store := s.Store()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	id := c.Param("id")
	// Verify the book exists before modifying tag entries to prevent orphaned rows.
	if _, err := store.GetBookByID(id); err != nil {
		httputil.RespondWithNotFound(c, "book", id)
		return
	}
	tag := c.Param("tag")
	if tag == "" {
		httputil.RespondWithBadRequest(c, "tag parameter required")
		return
	}
	if err := store.RemoveBookTag(id, tag); err != nil {
		httputil.InternalError(c, "failed to remove tag", err)
		return
	}
	tags, err := store.GetBookTags(id)
	if err != nil {
		httputil.InternalError(c, "failed to get tags after remove", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Tags []string `json:"tags"`
	}{Tags: tags})
}
