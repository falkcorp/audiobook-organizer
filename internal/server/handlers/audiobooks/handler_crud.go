// file: internal/server/handlers/audiobooks/handler_crud.go
// version: 1.1.0
// guid: 7f0f10bf-7554-4af5-b2d2-ce0a6af6b46e
// last-edited: 2026-06-23

// Write-side CRUD + batch endpoints for the audiobooks domain: update
// (full-column replacement with change-history recording + file write-back),
// delete (soft/hard, event publish), batch update, and batch operations.
// Split out of handler.go for readability; one Handler, one New().

package audiobookshandler

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/gin-gonic/gin"
)

// UpdateAudiobook handles PUT /audiobooks/:id. Full-column replacement via the
// update service, then records manual change history, writes metadata back to
// the file, enqueues iTunes write-back, and invalidates caches.
func (h *Handler) UpdateAudiobook(c *gin.Context) {
	id := c.Param("id")
	store := h.store

	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	// Fetch old book for change history comparison
	var oldBook *database.Book
	if store != nil {
		oldBook, _ = store.GetBookByID(id)
	}

	updatedBook, err := h.audiobookUpdater.UpdateAudiobook(c.Request.Context(), id, payload)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "audiobook", id)
			return
		}
		httputil.InternalError(c, "failed to update audiobook", err)
		return
	}

	// Record metadata change history for manual edits
	if oldBook != nil && store != nil {
		now := time.Now()
		manualChanges := []struct {
			field  string
			oldVal string
			newVal string
		}{
			{"title", oldBook.Title, updatedBook.Title},
			{"narrator", ptrStr(oldBook.Narrator), ptrStr(updatedBook.Narrator)},
			{"publisher", ptrStr(oldBook.Publisher), ptrStr(updatedBook.Publisher)},
			{"language", ptrStr(oldBook.Language), ptrStr(updatedBook.Language)},
		}
		// Compare author names
		oldAuthor := ""
		if oldBook.AuthorID != nil {
			if a, err := store.GetAuthorByID(*oldBook.AuthorID); err == nil && a != nil {
				oldAuthor = a.Name
			}
		}
		newAuthor := ""
		if updatedBook.AuthorID != nil {
			if a, err := store.GetAuthorByID(*updatedBook.AuthorID); err == nil && a != nil {
				newAuthor = a.Name
			}
		}
		manualChanges = append(manualChanges, struct {
			field  string
			oldVal string
			newVal string
		}{"author_name", oldAuthor, newAuthor})
		// Compare year
		oldYear := ""
		if oldBook.AudiobookReleaseYear != nil {
			oldYear = strconv.Itoa(*oldBook.AudiobookReleaseYear)
		}
		newYear := ""
		if updatedBook.AudiobookReleaseYear != nil {
			newYear = strconv.Itoa(*updatedBook.AudiobookReleaseYear)
		}
		manualChanges = append(manualChanges, struct {
			field  string
			oldVal string
			newVal string
		}{"audiobook_release_year", oldYear, newYear})

		for _, ch := range manualChanges {
			if ch.newVal == "" || ch.newVal == ch.oldVal {
				continue
			}
			oldJSON, _ := json.Marshal(ch.oldVal)
			newJSON, _ := json.Marshal(ch.newVal)
			oldStr := string(oldJSON)
			newStr := string(newJSON)
			record := &database.MetadataChangeRecord{
				BookID:        id,
				Field:         ch.field,
				PreviousValue: &oldStr,
				NewValue:      &newStr,
				ChangeType:    "manual",
				Source:        "manual",
				ChangedAt:     now,
			}
			if err := store.RecordMetadataChange(record); err != nil {
				slog.Warn("failed to record manual metadata change for .", "id", id, "c", ch.field, "err", err)
			}
		}
	}

	// Write updated metadata back to the audio file
	if updatedBook.FilePath != "" {
		tagMap := make(map[string]interface{})
		if v, ok := payload["title"].(string); ok && v != "" {
			tagMap["title"] = v
		}
		if v, ok := payload["author_name"].(string); ok && v != "" {
			tagMap["artist"] = v
		}
		if v, ok := payload["publisher"].(string); ok && v != "" {
			tagMap["publisher"] = v
		}
		if v, ok := payload["narrator"].(string); ok && v != "" {
			tagMap["album_artist"] = v
		}
		if v, ok := payload["audiobook_release_year"].(float64); ok && v != 0 {
			tagMap["year"] = int(v)
		}
		// If we have multiple authors in join table, combine with " & " for file tags
		if _, hasAuthor := tagMap["artist"]; !hasAuthor && store != nil {
			if authors, err := store.GetBookAuthors(id); err == nil && len(authors) > 1 {
				names := make([]string, 0, len(authors))
				for _, ba := range authors {
					if a, err := store.GetAuthorByID(ba.AuthorID); err == nil && a != nil {
						names = append(names, a.Name)
					}
				}
				if len(names) > 0 {
					tagMap["artist"] = strings.Join(names, ", ")
				}
			}
		}
		// If we have multiple narrators in join table, combine with " & " for file tags
		if _, hasNarr := tagMap["album_artist"]; !hasNarr && store != nil {
			if narrators, err := store.GetBookNarrators(id); err == nil && len(narrators) > 1 {
				names := make([]string, 0, len(narrators))
				for _, bn := range narrators {
					if n, err := store.GetNarratorByID(bn.NarratorID); err == nil && n != nil {
						names = append(names, n.Name)
					}
				}
				if len(names) > 0 {
					tagMap["album_artist"] = strings.Join(names, " & ")
				}
			}
		}
		if len(tagMap) > 0 {
			if h.isProtectedPath(updatedBook.FilePath) {
				slog.Info("skipping write-back for protected path", "updatedBook", updatedBook.FilePath)
			} else {
				opConfig := fileops.OperationConfig{VerifyChecksums: true}
				if writeErr := metadata.WriteMetadataToFile(updatedBook.FilePath, tagMap, opConfig); writeErr != nil {
					slog.Warn("write-back failed for", "updatedBook", updatedBook.FilePath, "writeErr", writeErr)
				} else {
					// Stamp last_written_at after successful write-back.
					if stampErr := store.SetLastWrittenAt(updatedBook.ID, time.Now()); stampErr != nil {
						slog.Warn("failed to stamp last_written_at for book", "updatedBook", updatedBook.ID, "stampErr", stampErr)
					}
				}
			}
		}
	}

	// Enqueue for iTunes auto write-back if enabled
	if wb := h.resolveWriteBack(); wb != nil {
		wb.Enqueue(id)
	}

	// Invalidate caches since book-author and book-series relationships may have changed.
	// Also clear the shared audiobookService bookCache — the update service owns a
	// separate instance, so its InvalidateBookCaches() above didn't flush the GET path.
	h.authorsCache.InvalidateAll()
	h.seriesCache.InvalidateAll()
	if h.audiobookService != nil {
		h.audiobookService.InvalidateBookCaches()
	}

	httputil.RespondWithOK(c, h.enrichBook(updatedBook))
}

// DeleteAudiobook handles DELETE /audiobooks/:id.
func (h *Handler) DeleteAudiobook(c *gin.Context) {
	id := c.Param("id")
	blockHash := c.Query("block_hash") == "true"
	softDelete := c.Query("soft_delete") == "true"

	opts := &audiobookspkg.DeleteAudiobookOptions{
		SoftDelete: softDelete,
		BlockHash:  blockHash,
	}

	result, err := h.audiobookService.DeleteAudiobook(c.Request.Context(), id, opts)
	if err != nil {
		if strings.Contains(err.Error(), "already soft deleted") {
			httputil.RespondWithConflict(c, err.Error())
			return
		}
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	h.publishEvent(c.Request.Context(), plugin.NewEvent(plugin.EventBookDeleted, id, map[string]any{
		"soft_delete": softDelete,
		"block_hash":  blockHash,
	}))

	// Invalidate caches since book-author and book-series relationships may have changed
	h.authorsCache.InvalidateAll()
	h.seriesCache.InvalidateAll()

	httputil.RespondWithOK(c, result)
}

// BatchUpdateAudiobooks handles POST /audiobooks/batch.
func (h *Handler) BatchUpdateAudiobooks(c *gin.Context) {
	var req batch.BatchUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	resp := h.batchService.UpdateAudiobooks(&req)

	// Enqueue all updated books for iTunes auto write-back
	if wb := h.resolveWriteBack(); wb != nil && resp != nil {
		for _, item := range resp.Results {
			if item.Success {
				wb.Enqueue(item.ID)
			}
		}
	}

	httputil.RespondWithOK(c, resp)
}

// BatchOperations handles POST /audiobooks/batch-operations.
func (h *Handler) BatchOperations(c *gin.Context) {
	var req batch.BatchOperationsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.Operations) == 0 {
		httputil.RespondWithBadRequest(c, "no operations provided")
		return
	}
	if len(req.Operations) > 10000 {
		httputil.RespondWithBadRequest(c, "max 10000 operations per request")
		return
	}

	resp := h.batchService.ExecuteOperations(&req)

	if wb := h.resolveWriteBack(); wb != nil {
		for _, r := range resp.Results {
			if r.Success {
				wb.Enqueue(r.ID)
			}
		}
	}

	httputil.RespondWithOK(c, resp)
}
