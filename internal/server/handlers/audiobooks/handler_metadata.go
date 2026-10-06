// file: internal/server/handlers/audiobooks/handler_metadata.go
// version: 1.9.0
// guid: 591661c3-5e87-4559-9a08-3203eec4fb68
// last-edited: 2026-10-05

// Metadata-history / undo / field-state / path-history / external-id /
// changelog / changes endpoints for the audiobooks domain. Split out of
// handler.go for readability; one Handler, one New().

package audiobookshandler

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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
			limit = min(parsed, maxHistoryLimit)
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
			limit = min(parsed, maxHistoryLimit)
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

	// A file position edit (PatchBookFile) is reverted on the file row; the
	// book-level override below would store it under a field nothing reads.
	if handled, revErr := revertBookFilePosition(store, id, &latest); handled {
		if revErr != nil {
			if errors.Is(revErr, errBookFilePositionChangedSince) {
				httputil.RespondWithConflict(c, revErr.Error())
				return
			}
			httputil.InternalError(c, "failed to apply undo", revErr)
			return
		}
	} else {
		// Every other field is put back on the book row, compare-and-set
		// inside ModifyBook (metafetch.Service.UndoFieldChange). It used to
		// store the previous value as a user override: the book row kept the
		// changed value, and the override froze the field against every
		// later fetch.
		h.undoBookFieldChange(c, id, field)
		return
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
		slog.Warn("failed to record undo change for /", "id", logger.SanitizeLogValue(id), "field", logger.SanitizeLogValue(field), "err", err)
	}

	// The cached candidates are not touched: a file position is not part of
	// what the metadata search asks. (This deleted them on every undo until
	// 2026-10-05.)

	httputil.RespondWithOK(c, gin.H{"message": "undo applied", "field": field, "reverted_to": latest.PreviousValue})
}

// undoBookFieldChange is UndoMetadataChange for a field of the book row. A
// field changed since its last change, or whose last change is already an
// undo, is refused with 409, not overwritten. reverted_to is the value the
// undo actually restored (UndoApplyResult.RevertedTo), not a value read
// before it ran.
func (h *Handler) undoBookFieldChange(c *gin.Context, id, field string) {
	if h.metadataFetchService == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}
	res, err := h.metadataFetchService.UndoFieldChange(id, field)
	switch {
	case errors.Is(err, metafetch.ErrNoApplyToUndo):
		httputil.RespondWithNotFound(c, "change history", field)
		return
	case errors.Is(err, metafetch.ErrFieldChangedSince), errors.Is(err, metafetch.ErrFieldNotUndoable), errors.Is(err, metafetch.ErrFieldAlreadyUndone):
		httputil.RespondWithConflict(c, err.Error())
		return
	case err != nil:
		httputil.InternalError(c, "failed to apply undo", err)
		return
	}
	if len(res.Reverted) > 0 {
		if wb := h.resolveWriteBack(); wb != nil {
			wb.Enqueue(id)
		}
		// The cached candidates are NOT deleted here. UndoFieldChange writes
		// through ModifyBook, and the store drops them in that same write when
		// the undo changes the title or the author's name
		// (database candidateSearchIdentityChanged) and stamps them when it
		// replaces or clears the ASIN. Deleting them here on every undo --
		// an ASIN, ISBN or series undo included -- destroyed candidates that
		// still answered for the book.
	}
	httputil.RespondWithOK(c, gin.H{
		"message":          "undo applied",
		"field":            field,
		"reverted_to":      res.RevertedTo,
		"already_restored": len(res.AlreadyRestored) > 0,
	})
}

// UndoLastApply reverts the most recent metadata apply for a book.
// POST /audiobooks/:id/undo-last-apply.
//
// The apply's rows are found by their batch id, not by a time window, and each
// field is put back on the book row inside ModifyBook only while it still holds
// what the apply wrote (metafetch.Service.UndoLastApply). It used to write the
// pre-apply value into the per-field override state and never touch the book
// row: the applied value stayed, HasUserOverride then froze the field against
// every later fetch, and the write-back it queued pushed the applied values to
// the files and iTunes. Write-back is now queued only when a field of the book
// row actually changed.
func (h *Handler) UndoLastApply(c *gin.Context) {
	id := c.Param("id")
	if h.metadataFetchService == nil {
		httputil.RespondWithInternalError(c, "metadata service not initialized")
		return
	}
	res, err := h.metadataFetchService.UndoLastApply(id)
	switch {
	case errors.Is(err, metafetch.ErrNoApplyToUndo):
		httputil.RespondWithNotFound(c, "changes", "none")
		return
	case errors.Is(err, metafetch.ErrApplyPredatesBatches), errors.Is(err, metafetch.ErrApplyAlreadyUndone), errors.Is(err, metafetch.ErrApplyHistoryIncomplete),
		errors.Is(err, metafetch.ErrApplyUndoneFromOperation):
		httputil.RespondWithConflict(c, err.Error())
		return
	case err != nil:
		httputil.InternalError(c, "failed to undo last apply", err)
		return
	}

	if wb := h.resolveWriteBack(); len(res.Reverted) > 0 && wb != nil {
		wb.Enqueue(id)
	}
	// The cached candidates are left to the store, as for a single-field
	// undo (undoBookFieldChange): UndoLastApply writes through ModifyBook,
	// which drops them when the reverted title or author changes what the
	// search asks, and keeps them otherwise.

	undone := res.Reverted
	if undone == nil {
		undone = []string{}
	}
	httputil.RespondWithOK(c, gin.H{
		"message":                 fmt.Sprintf("Undid %d field(s)", len(undone)),
		"undone_fields":           undone,
		"changed_since_fields":    res.ChangedSince,
		"already_restored_fields": res.AlreadyRestored,
		"locked_fields":           res.Locked,
		"failed_fields":           res.Failed,
		"author_credits_left":     res.AuthorCreditsLeft,
		"batch_id":                res.BatchID,
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
// GET /audiobooks/:id/changes. Ledger-only rows (undo.IsLedgerOnly: a
// Repairs plan record, which changes nothing about the book) are left out;
// GET /operations/:id/changes still lists them.
func (h *Handler) GetBookChanges(c *gin.Context) {
	id := c.Param("id")
	all, err := h.store.GetBookChanges(id)
	if err != nil {
		httputil.InternalError(c, "failed to get book changes", err)
		return
	}
	changes := make([]*database.OperationChange, 0, len(all))
	for _, ch := range all {
		if !undo.IsLedgerOnly(ch) {
			changes = append(changes, ch)
		}
	}
	httputil.RespondWithOK(c, gin.H{"changes": changes})
}
