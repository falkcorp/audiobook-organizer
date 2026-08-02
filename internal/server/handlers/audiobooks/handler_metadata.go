// file: internal/server/handlers/audiobooks/handler_metadata.go
// version: 1.2.0
// guid: 591661c3-5e87-4559-9a08-3203eec4fb68
// last-edited: 2026-07-13

// Metadata-history / undo / field-state / path-history / external-id /
// changelog / changes endpoints for the audiobooks domain. Split out of
// handler.go for readability; one Handler, one New().

package audiobookshandler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// maxHistoryLimit caps the ?limit= query param on the per-book metadata
// history endpoints, matching the ceiling httputil.ParsePaginationParams
// applies elsewhere. An attacker-supplied huge limit could otherwise force
// an unbounded history read/allocation.
const maxHistoryLimit = 1000

// GetBookMetadataHistory handles GET /audiobooks/:id/metadata-history.
func (h *Handler) GetBookMetadataHistory(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	limit := 100
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
			if limit > maxHistoryLimit {
				limit = maxHistoryLimit
			}
		}
	}
	records, err := store.GetBookChangeHistory(id, limit)
	if err != nil {
		httputil.InternalError(c, "failed to get metadata history", err)
		return
	}
	if records == nil {
		records = []database.MetadataChangeRecord{}
	}
	httputil.RespondWithOK(c, gin.H{"items": records, "count": len(records)})
}

// GetAudiobookFieldStates handles GET /audiobooks/:id/field-states. The
// underlying LoadMetadataState returns a metafetch-private map type, so it is
// reached through the injected getFieldStates closure (surfaced as any).
func (h *Handler) GetAudiobookFieldStates(c *gin.Context) {
	id := c.Param("id")
	states, err := h.getFieldStates(id)
	if err != nil {
		httputil.InternalError(c, "failed to get field states", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"field_states": states})
}

// GetFieldMetadataHistory handles GET /audiobooks/:id/metadata-history/:field.
func (h *Handler) GetFieldMetadataHistory(c *gin.Context) {
	id := c.Param("id")
	field := c.Param("field")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	limit := 50
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
			if limit > maxHistoryLimit {
				limit = maxHistoryLimit
			}
		}
	}
	records, err := store.GetMetadataChangeHistory(id, field, limit)
	if err != nil {
		httputil.InternalError(c, "failed to get field history", err)
		return
	}
	if records == nil {
		records = []database.MetadataChangeRecord{}
	}
	httputil.RespondWithOK(c, gin.H{"items": records, "count": len(records)})
}

// UndoMetadataChange handles POST /audiobooks/:id/metadata-history/:field/undo.
func (h *Handler) UndoMetadataChange(c *gin.Context) {
	id := c.Param("id")
	field := c.Param("field")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Get the latest change for this field
	records, err := store.GetMetadataChangeHistory(id, field, 1)
	if err != nil {
		httputil.InternalError(c, "failed to get field history", err)
		return
	}
	if len(records) == 0 {
		httputil.RespondWithNotFound(c, "change history", field)
		return
	}

	latest := records[0]

	// Apply the previous value back via metadata state service
	if latest.PreviousValue != nil {
		var prevValue any
		if err := json.Unmarshal([]byte(*latest.PreviousValue), &prevValue); err != nil {
			prevValue = *latest.PreviousValue
		}
		if err := h.metadataStateService.SetOverride(id, field, prevValue, false); err != nil {
			httputil.InternalError(c, "failed to apply undo", err)
			return
		}
	} else {
		// Previous value was nil, so clear the override
		if err := h.metadataStateService.ClearOverride(id, field); err != nil {
			// Ignore "not found" errors when clearing
			if !strings.Contains(err.Error(), "not found") {
				httputil.InternalError(c, "failed to clear override", err)
				return
			}
		}
	}

	// Record the undo itself
	undoRecord := &database.MetadataChangeRecord{
		BookID:        id,
		Field:         field,
		PreviousValue: latest.NewValue,
		NewValue:      latest.PreviousValue,
		ChangeType:    "undo",
		Source:        "manual",
		ChangedAt:     time.Now(),
	}
	if err := store.RecordMetadataChange(undoRecord); err != nil {
		slog.Warn("failed to record undo change for /", "id", id, "field", field, "err", err)
	}

	// METADATA-CACHED-MATCHER: undo of a metadata field rewrites book
	// identity; invalidate cache.
	if h.metadataFetchService != nil {
		_ = h.metadataFetchService.InvalidateCachedCandidates(id)
	}

	httputil.RespondWithOK(c, gin.H{"message": "undo applied", "field": field, "reverted_to": latest.PreviousValue})
}

// UndoLastApply reverts all fields changed in the most recent metadata apply
// for a book. POST /audiobooks/:id/undo-last-apply.
func (h *Handler) UndoLastApply(c *gin.Context) {
	id := c.Param("id")
	store := h.store
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Get recent history for this book (enough to find the last apply batch)
	history, err := store.GetBookChangeHistory(id, 50)
	if err != nil {
		httputil.InternalError(c, "failed to get change history", err)
		return
	}
	if len(history) == 0 {
		httputil.RespondWithNotFound(c, "change history", id)
		return
	}

	// Find the most recent non-undo change timestamp to identify the batch
	var batchTime time.Time
	for _, rec := range history {
		if rec.ChangeType != "undo" {
			batchTime = rec.ChangedAt
			break
		}
	}
	if batchTime.IsZero() {
		httputil.RespondWithNotFound(c, "changes", "none")
		return
	}

	// Collect all changes from this batch (within 2 seconds of each other)
	var batchRecords []*database.MetadataChangeRecord
	for i := range history {
		rec := &history[i]
		if rec.ChangeType == "undo" {
			continue
		}
		diff := batchTime.Sub(rec.ChangedAt)
		if diff < 0 {
			diff = -diff
		}
		if diff <= 2*time.Second {
			batchRecords = append(batchRecords, rec)
		}
	}

	if len(batchRecords) == 0 {
		httputil.RespondWithNotFound(c, "changes", "none")
		return
	}

	// Undo each field in the batch
	undoneFields := []string{}
	for _, rec := range batchRecords {
		if rec.PreviousValue != nil {
			var prevValue any
			if jsonErr := json.Unmarshal([]byte(*rec.PreviousValue), &prevValue); jsonErr != nil {
				prevValue = *rec.PreviousValue
			}
			if setErr := h.metadataStateService.SetOverride(id, rec.Field, prevValue, false); setErr != nil {
				slog.Warn("undo-last-apply failed to revert for", "rec", rec.Field, "id", id, "setErr", setErr)
				continue
			}
		} else {
			if clrErr := h.metadataStateService.ClearOverride(id, rec.Field); clrErr != nil {
				if !strings.Contains(clrErr.Error(), "not found") {
					slog.Warn("undo-last-apply failed to clear for", "rec", rec.Field, "id", id, "clrErr", clrErr)
					continue
				}
			}
		}
		undoneFields = append(undoneFields, rec.Field)

		// Record the undo
		undoRec := &database.MetadataChangeRecord{
			BookID:        id,
			Field:         rec.Field,
			PreviousValue: rec.NewValue,
			NewValue:      rec.PreviousValue,
			ChangeType:    "undo",
			Source:        "bulk-search-undo",
			ChangedAt:     time.Now(),
		}
		if recErr := store.RecordMetadataChange(undoRec); recErr != nil {
			slog.Warn("undo-last-apply failed to record undo for /", "id", id, "rec", rec.Field, "recErr", recErr)
		}
	}

	// Re-write tags to files if write-back is enabled, restoring original values
	if wb := h.resolveWriteBack(); len(undoneFields) > 0 && wb != nil {
		wb.Enqueue(id)
	}

	// METADATA-CACHED-MATCHER: undo restores the prior identity. Drop the
	// cache so the next read fetches against the reverted title/author.
	if len(undoneFields) > 0 && h.metadataFetchService != nil {
		_ = h.metadataFetchService.InvalidateCachedCandidates(id)
	}

	httputil.RespondWithOK(c, gin.H{
		"message":       fmt.Sprintf("Undid %d field(s)", len(undoneFields)),
		"undone_fields": undoneFields,
	})
}

// GetBookPathHistory handles GET /audiobooks/:id/path-history.
func (h *Handler) GetBookPathHistory(c *gin.Context) {
	id := c.Param("id")
	history, err := h.store.GetBookPathHistory(id)
	if err != nil {
		httputil.RespondWithOK(c, gin.H{"history": []any{}})
		return
	}
	httputil.RespondWithOK(c, gin.H{"history": history})
}

// GetAudiobookExternalIDs handles GET /audiobooks/:id/external-ids. The
// external-ID adapter (asExternalIDStore) stays in package server and is reached
// through the injected getExternalIDStore closure.
func (h *Handler) GetAudiobookExternalIDs(c *gin.Context) {
	id := c.Param("id")
	eidStore := h.getExternalIDStore()
	if eidStore == nil {
		httputil.RespondWithOK(c, gin.H{"external_ids": []any{}, "itunes_linked": false})
		return
	}
	extIDs, err := eidStore.GetExternalIDsForBook(id)
	if err != nil {
		httputil.RespondWithOK(c, gin.H{"external_ids": []any{}, "itunes_linked": false})
		return
	}
	itunesLinked := false
	for _, eid := range extIDs {
		if eid.Source == "itunes" && !eid.Tombstoned {
			itunesLinked = true
			break
		}
	}
	httputil.RespondWithOK(c, gin.H{
		"external_ids":  extIDs,
		"itunes_linked": itunesLinked,
		"total":         len(extIDs),
	})
}

// GetBookChangelog handles GET /audiobooks/:id/changelog.
func (h *Handler) GetBookChangelog(c *gin.Context) {
	id := c.Param("id")
	entries, err := h.changelogService.GetBookChangelog(id)
	if err != nil {
		httputil.InternalError(c, "failed to get changelog", err)
		return
	}
	if entries == nil {
		entries = []activity.ChangeLogEntry{}
	}
	httputil.RespondWithOK(c, gin.H{"entries": entries})
}

// GetBookChanges returns change tracking records for a book.
// GET /audiobooks/:id/changes.
func (h *Handler) GetBookChanges(c *gin.Context) {
	id := c.Param("id")
	changes, err := h.store.GetBookChanges(id)
	if err != nil {
		httputil.InternalError(c, "failed to get book changes", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"changes": changes})
}
