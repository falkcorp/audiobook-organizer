// file: internal/server/handlers/audiobooks/handler_tags.go
// version: 1.2.0
// guid: ff2e3609-5ce3-4414-a18b-976d21b929fb
// last-edited: 2026-07-13

// Tag read/write, alternative-title CRUD, and batch tag-update endpoints for
// the audiobooks domain. Split out of handler.go for readability; one Handler,
// one New().

package audiobookshandler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// GetAudiobookTags handles GET /audiobooks/:id/tags.
func (h *Handler) GetAudiobookTags(c *gin.Context) {
	id := c.Param("id")
	compareID := c.Query("compare_id")
	snapshotTS := c.Query("snapshot_ts")
	if snapshotTS != "" {
		if _, err := time.Parse(time.RFC3339Nano, snapshotTS); err != nil {
			httputil.RespondWithBadRequest(c, "invalid snapshot_ts format, use RFC3339Nano")
			return
		}
	}
	resp, err := h.audiobookService.GetAudiobookTags(c.Request.Context(), id, compareID, snapshotTS)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "audiobook", id)
			return
		}
		httputil.InternalError(c, "failed to get tags", err)
		return
	}

	httputil.RespondWithOK(c, resp)
}

// ListAllUserTags handles GET /tags.
func (h *Handler) ListAllUserTags(c *gin.Context) {
	tags, err := h.audiobookService.ListAllUserTags()
	if err != nil {
		httputil.InternalError(c, "failed to list user tags", err)
		return
	}
	if tags == nil {
		tags = []database.TagWithCount{}
	}
	httputil.RespondWithOK(c, gin.H{"tags": tags})
}

// GetBookUserTags handles GET /audiobooks/:id/user-tags.
func (h *Handler) GetBookUserTags(c *gin.Context) {
	id := c.Param("id")
	tags, err := h.audiobookService.GetBookUserTags(id)
	if err != nil {
		httputil.InternalError(c, "failed to get book user tags", err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	httputil.RespondWithOK(c, gin.H{"tags": tags})
}

// GetBookTagsDetailed returns a book's tags with their source attribution
// ('user' vs 'system'). GET /audiobooks/:id/tags-detailed.
func (h *Handler) GetBookTagsDetailed(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "book id is required")
		return
	}
	tags, err := h.store.GetBookTagsDetailed(id)
	if err != nil {
		httputil.InternalError(c, "failed to get detailed book tags", err)
		return
	}
	if tags == nil {
		tags = []database.BookTag{}
	}
	httputil.RespondWithOK(c, gin.H{"tags": tags})
}

// GetBookAlternativeTitles handles GET /audiobooks/:id/alternative-titles.
func (h *Handler) GetBookAlternativeTitles(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "id is required")
		return
	}
	alts, err := h.store.GetBookAlternativeTitles(id)
	if err != nil {
		httputil.InternalError(c, "failed to get alternative titles", err)
		return
	}
	if alts == nil {
		alts = []database.BookAlternativeTitle{}
	}
	httputil.RespondWithOK(c, gin.H{"alternative_titles": alts})
}

// AddBookAlternativeTitle handles POST /audiobooks/:id/alternative-titles.
// Idempotent on (book_id, title).
func (h *Handler) AddBookAlternativeTitle(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "id is required")
		return
	}
	var body struct {
		Title    string `json:"title"`
		Source   string `json:"source,omitempty"`
		Language string `json:"language,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Title == "" {
		httputil.RespondWithBadRequest(c, "title is required")
		return
	}
	store := h.store
	// Confirm the book exists before inserting — avoids orphan alt
	// title rows for deleted books.
	if book, err := store.GetBookByID(id); err != nil || book == nil {
		httputil.RespondWithNotFound(c, "book", id)
		return
	}
	if err := store.AddBookAlternativeTitle(id, body.Title, body.Source, body.Language); err != nil {
		httputil.InternalError(c, "failed to add alternative title", err)
		return
	}
	alts, _ := store.GetBookAlternativeTitles(id)
	httputil.RespondWithOK(c, gin.H{"alternative_titles": alts})
}

// RemoveBookAlternativeTitle handles DELETE /audiobooks/:id/alternative-titles.
func (h *Handler) RemoveBookAlternativeTitle(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		httputil.RespondWithBadRequest(c, "id is required")
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Title == "" {
		httputil.RespondWithBadRequest(c, "title is required")
		return
	}
	store := h.store
	if err := store.RemoveBookAlternativeTitle(id, body.Title); err != nil {
		httputil.InternalError(c, "failed to remove alternative title", err)
		return
	}
	alts, _ := store.GetBookAlternativeTitles(id)
	httputil.RespondWithOK(c, gin.H{"alternative_titles": alts})
}

// BatchUpdateTags handles POST /audiobooks/batch-tags.
func (h *Handler) BatchUpdateTags(c *gin.Context) {
	var body struct {
		BookIDs    []string `json:"book_ids"`
		AddTags    []string `json:"add_tags"`
		RemoveTags []string `json:"remove_tags"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if len(body.BookIDs) == 0 {
		httputil.RespondWithBadRequest(c, "book_ids is required")
		return
	}
	if len(body.AddTags) == 0 && len(body.RemoveTags) == 0 {
		httputil.RespondWithBadRequest(c, "at least one of add_tags or remove_tags is required")
		return
	}
	// Filter out empty strings from tag arrays
	filterEmpty := func(tags []string) []string {
		out := make([]string, 0, len(tags))
		for _, t := range tags {
			if strings.TrimSpace(t) != "" {
				out = append(out, t)
			}
		}
		return out
	}
	body.AddTags = filterEmpty(body.AddTags)
	body.RemoveTags = filterEmpty(body.RemoveTags)
	updated, err := h.audiobookService.BatchUpdateUserTags(body.BookIDs, body.AddTags, body.RemoveTags)
	if err != nil {
		httputil.InternalError(c, "failed to batch update tags", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"updated": updated})
}
