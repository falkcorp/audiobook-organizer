// file: internal/server/handlers/audiobooks/handler_crud.go
// version: 1.11.0
// guid: 7f0f10bf-7554-4af5-b2d2-ce0a6af6b46e
// last-edited: 2026-10-07

// Write-side CRUD + batch endpoints for the audiobooks domain: update
// (full-column replacement with change-history recording + file write-back),
// delete (soft/hard, event publish), batch update, and batch operations.
// Split out of handler.go for readability; one Handler, one New().

package audiobookshandler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/gin-gonic/gin"
)

// UpdateAudiobook handles PUT /audiobooks/:id. Full-column replacement via the
// update service (which records the change history), writes metadata back to
// the file, enqueues iTunes write-back, and invalidates caches.
func (h *Handler) UpdateAudiobook(c *gin.Context) {
	id := c.Param("id")
	store := h.store

	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	updatedBook, warnings, err := h.audiobookUpdater.UpdateAudiobookWithWarnings(c.Request.Context(), id, payload)
	if err != nil {
		if errors.Is(err, audiobookspkg.ErrInvalidAudiobookUpdate) {
			httputil.RespondWithBadRequest(c, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			httputil.RespondWithNotFound(c, "audiobook", id)
			return
		}
		httputil.InternalError(c, "failed to update audiobook", err)
		return
	}

	// Change history for this edit is recorded by the update service
	// (audiobooks.AudiobookService.UpdateAudiobook -> database.RecordBookEditHistory):
	// one "manual" row per changed field, clears included, diffed under the
	// book's write lock. Until 2026-09-30 this handler recorded its own rows
	// for six fields and only for non-empty values, so any other field -- or
	// a clear -- left no history, and a queued metadata apply overwrote it.

	// Write updated metadata back to the audio file
	if updatedBook.FilePath != "" {
		tagMap := make(map[string]any)
		if v, ok := payload["title"].(string); ok && v != "" {
			tagMap["title"] = v
		}
		if v, ok := payload["author_name"].(string); ok && v != "" {
			tagMap["artist"] = v
		}
		if v, ok := payload["publisher"].(string); ok && v != "" {
			tagMap["publisher"] = v
		}
		// The narrator goes to the narrator key (NARRATOR/PERFORMER). It was
		// sent as album_artist, a key no tag writer maps, so a narrator edit
		// never reached the file; ALBUMARTIST is the author, written from
		// "artist" (owner decision 2026-09-14).
		if v, ok := payload["narrator"].(string); ok && v != "" {
			tagMap["narrator"] = v
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
		if _, hasNarr := tagMap["narrator"]; !hasNarr && store != nil {
			if narrators, err := store.GetBookNarrators(id); err == nil && len(narrators) > 1 {
				names := make([]string, 0, len(narrators))
				for _, bn := range narrators {
					if n, err := store.GetNarratorByID(bn.NarratorID); err == nil && n != nil {
						names = append(names, n.Name)
					}
				}
				if len(names) > 0 {
					tagMap["narrator"] = strings.Join(names, " & ")
				}
			}
		}
		if len(tagMap) > 0 {
			if h.isProtectedPath(updatedBook.FilePath) {
				slog.Info("skipping write-back for protected path", "updatedBook", logger.SanitizeLogValue(updatedBook.FilePath))
			} else {
				opConfig := fileops.OperationConfig{VerifyChecksums: true}
				if writeErr := metadata.WriteMetadataToFile(updatedBook.FilePath, tagMap, opConfig); writeErr != nil {
					slog.Warn("write-back failed for", "updatedBook", logger.SanitizeLogValue(updatedBook.FilePath), "writeErr", logger.SanitizeLogValue(fmt.Sprint(writeErr)))
				} else {
					// Stamp last_written_at after successful write-back.
					if stampErr := store.SetLastWrittenAt(updatedBook.ID, time.Now()); stampErr != nil {
						slog.Warn("failed to stamp last_written_at for book", "updatedBook", logger.SanitizeLogValue(updatedBook.ID), "stampErr", logger.SanitizeLogValue(fmt.Sprint(stampErr)))
					}
				}
			}
		}
	}

	// Invalidate caches since book-author and book-series relationships may have changed.
	// Also clear the shared audiobookService's list cache (a no-op unless
	// config.CacheInvalidateOnBookUpdate is on) — the update service owns a
	// separate instance, so its InvalidateListCache() above didn't flush it.
	h.authorsCache.InvalidateAll()
	h.seriesCache.InvalidateAll()
	if h.audiobookService != nil {
		h.audiobookService.InvalidateListCache()
	}

	if len(warnings) == 0 {
		httputil.RespondWithOK(c, h.enrichBook(updatedBook))
		return
	}
	httputil.RespondWithOK(c, bookWithWarnings{book: h.enrichBook(updatedBook), warnings: warnings})
}

// bookWithWarnings is the PUT /audiobooks/:id response of an edit that
// committed only in part: the enriched book's own JSON object with one more
// key, "warnings" (each says what was saved and what was not, as the batch
// update reports a partial save in its per-book error). The book stays a flat
// object, so a client that does not read "warnings" sees the same shape.
type bookWithWarnings struct {
	book     any
	warnings []string
}

func (r bookWithWarnings) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(r.book)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("book response is not a JSON object: %w", err)
	}
	w, err := json.Marshal(r.warnings)
	if err != nil {
		return nil, err
	}
	obj["warnings"] = w
	return json.Marshal(obj)
}

// Stable error codes the delete and purge endpoints answer with, for the web
// to branch on instead of the message text.
const (
	// CodeOwnsFiles: 409, the book still owns book_file rows
	// (database.ErrBookOwnsFiles); nothing was deleted.
	CodeOwnsFiles = "OWNS_FILES"
	// CodeCarryFailed: 500, moving the listening state to the version
	// Audiobookshelf lists did not complete; the book was kept with its
	// state.
	CodeCarryFailed = "CARRY_FAILED"
)

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
		// A hard delete refused because the book still owns file rows is a
		// conflict with the book's state, not a missing book. The stable
		// OWNS_FILES code lets the web tell it apart without matching the
		// message text.
		if errors.Is(err, database.ErrBookOwnsFiles) {
			httputil.RespondWithError(c, http.StatusConflict, err.Error(), CodeOwnsFiles)
			return
		}
		// Refused because users have listening progress on it and there is
		// no copy in the Audiobookshelf library to move it to. The stable
		// code lets the trash page offer "Discard progress and purge".
		if errors.Is(err, audiobookspkg.ErrBookHasProgress) {
			httputil.RespondWithError(c, http.StatusConflict, err.Error(), "HAS_PROGRESS")
			return
		}
		// A carry of the listening state that did not complete (the state is
		// back on the book, which was kept). "Purge now" reports it as
		// ErrPurgeCarryFailed; the hard delete of a live book returns the
		// merge errors themselves (merge.HardDeleteKeepingUserState): an
		// incomplete carry, or a target whose listing could not be re-read.
		// Until 2026-10-06 those two answered DELETE_FAILED.
		if errors.Is(err, audiobookspkg.ErrPurgeCarryFailed) || errors.Is(err, merge.ErrStateCarryIncomplete) ||
			errors.Is(err, merge.ErrCarryTargetUnreadable) {
			httputil.RespondWithError(c, http.StatusInternalServerError, err.Error(), CodeCarryFailed)
			return
		}
		if err.Error() == "audiobook not found" {
			httputil.RespondWithNotFound(c, "audiobook", id)
			return
		}
		// Anything else is a failure, not a missing book: until 2026-10-05
		// every other error answered 404, hiding why a delete did not run.
		httputil.RespondWithError(c, http.StatusInternalServerError, err.Error(), "DELETE_FAILED")
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
	// Fail-safe cap (internal/applycap). This is the endpoint the web UI's
	// bulk edit actually calls (web/src/services/api.ts), so of every gate in
	// this PR it is the one a "filter matched everything" click reaches first.
	if ex := applycap.Refuse("audiobooks/batch", len(req.IDs), config.AppConfig.BulkApplyMaxItems); ex != nil {
		httputil.RespondWithApplyCapExceeded(c, ex)
		return
	}

	resp := h.batchService.UpdateAudiobooks(&req)

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
	// Fail-safe cap (internal/applycap). The old fixed 10,000 ceiling sat
	// ABOVE the cap and the operation list includes hard_delete; the cap is
	// the operator-facing number now, and the code says which one refused.
	if ex := applycap.Refuse("audiobooks/batch-operations", len(req.Operations), config.AppConfig.BulkApplyMaxItems); ex != nil {
		httputil.RespondWithApplyCapExceeded(c, ex)
		return
	}

	resp := h.batchService.ExecuteOperations(&req)

	httputil.RespondWithOK(c, resp)
}
