// file: internal/server/entity_tag_handlers.go
// version: 2.1.0
// guid: 7e5f6a4b-8c9d-4a70-b8c5-3d7e0f1b9a99
//
// HTTP endpoints for author and series tags (backlog 7.7).
// Store methods already exist — this wires them to HTTP.

package server

import (
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// entityTagOps bundles the store operations for one tagged-entity type
// (author or series). It lets a single generic handler serve both without
// plumbing the entity name and each function pointer through separately.
type entityTagOps struct {
	name          string // "author" or "series" — used in error messages
	getDetailed   func(id int) ([]database.BookTag, error)
	add           func(id int, tag string) error
	addWithSource func(id int, tag, source string) error
}

func (s *Server) authorTagOps() entityTagOps {
	return entityTagOps{
		name:          "author",
		getDetailed:   s.Store().GetAuthorTagsDetailed,
		add:           s.Store().AddAuthorTag,
		addWithSource: s.Store().AddAuthorTagWithSource,
	}
}

func (s *Server) seriesTagOps() entityTagOps {
	return entityTagOps{
		name:          "series",
		getDetailed:   s.Store().GetSeriesTagsDetailed,
		add:           s.Store().AddSeriesTag,
		addWithSource: s.Store().AddSeriesTagWithSource,
	}
}

// parseEntityID parses the :id path param as an int, responding with a 400 on
// failure. Returns (id, ok) so callers can bail early.
func parseEntityID(c *gin.Context, entityName string) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		httputil.RespondWithValidationError(c, entityName+" id", "must be an integer")
		return 0, false
	}
	return id, true
}

// handleGetEntityTags returns tags for an author or series.
func (s *Server) handleGetEntityTags(c *gin.Context, ops entityTagOps) {
	id, ok := parseEntityID(c, ops.name)
	if !ok {
		return
	}
	tags, err := ops.getDetailed(id)
	if err != nil {
		httputil.InternalError(c, "get "+ops.name+" tags", err)
		return
	}
	if tags == nil {
		tags = []database.BookTag{}
	}
	httputil.RespondWithOK(c, struct {
		Tags []database.BookTag `json:"tags"`
	}{Tags: tags})
}

// handleAddEntityTag adds a tag to an author or series, optionally with a
// source. See TODO below — this is where your input shapes the behavior.
func (s *Server) handleAddEntityTag(c *gin.Context, ops entityTagOps) {
	id, ok := parseEntityID(c, ops.name)
	if !ok {
		return
	}
	var req struct {
		Tag    string `json:"tag" binding:"required"`
		Source string `json:"source,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	var err error
	if req.Source != "" {
		err = ops.addWithSource(id, req.Tag, req.Source)
	} else {
		err = ops.add(id, req.Tag)
	}
	if err != nil {
		httputil.InternalError(c, "add "+ops.name+" tag", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Added string `json:"added"`
	}{Added: req.Tag})
}

// registerEntityTagRoutes wires the author/series tag endpoints.
func (s *Server) registerEntityTagRoutes(protected *gin.RouterGroup) {
	protected.GET("/authors/:id/tags", s.perm(auth.PermLibraryView),
		func(c *gin.Context) { s.handleGetEntityTags(c, s.authorTagOps()) })
	protected.POST("/authors/:id/tags", s.perm(auth.PermLibraryEditMetadata),
		func(c *gin.Context) { s.handleAddEntityTag(c, s.authorTagOps()) })
	protected.GET("/series/:id/tags", s.perm(auth.PermLibraryView),
		func(c *gin.Context) { s.handleGetEntityTags(c, s.seriesTagOps()) })
	protected.POST("/series/:id/tags", s.perm(auth.PermLibraryEditMetadata),
		func(c *gin.Context) { s.handleAddEntityTag(c, s.seriesTagOps()) })
}
