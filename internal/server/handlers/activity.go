// file: internal/server/handlers/activity.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0123-def0-234567890123
// last-edited: 2026-06-02

package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// ActivityService is the narrow interface ActivityHandler requires from the
// activity log service.
type ActivityService interface {
	Query(filter database.ActivityFilter) ([]database.ActivityEntry, int, error)
	GetDistinctSources(filter database.ActivityFilter) ([]database.SourceCount, error)
	RecompactDigests(ctx context.Context) (database.RecompactResult, error)
	CompactByDay(ctx context.Context, cutoff time.Time) (database.CompactResult, error)
}

// ActivityOpsStore is the narrow interface for op-log fallback in
// ListOperationActivity. It may be nil when the backing store does not
// implement OpsV2.
type ActivityOpsStore interface {
	GetOpLogsV2(opID string, limit int) ([]database.OpLogV2Row, error)
}

// OperationActivityEntry is the unified response shape for a single
// chronological event inside an operation transcript.
// Exported so tests outside the handlers package can unmarshal the response.
type OperationActivityEntry struct {
	Timestamp     time.Time `json:"timestamp"`
	Level         string    `json:"level"`
	OperationID   string    `json:"operation_id"`
	OperationType string    `json:"operation_type"`
	Message       string    `json:"message"`
	Details       string    `json:"details,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
}

// operationActivityEntry is an alias for the exported type, used internally.
type operationActivityEntry = OperationActivityEntry

// ActivityHandler handles activity-log HTTP endpoints.
type ActivityHandler struct {
	svc      ActivityService
	opsStore ActivityOpsStore // may be nil
}

// NewActivityHandler constructs an ActivityHandler.
// opsStore may be nil when the backing store does not implement OpsV2.
func NewActivityHandler(svc ActivityService, opsStore ActivityOpsStore) *ActivityHandler {
	return &ActivityHandler{svc: svc, opsStore: opsStore}
}

// ListActivity handles GET /api/v1/activity.
//
// Supported query parameters:
//
//	limit            – max entries to return (default 50)
//	offset           – pagination offset
//	type             – filter by entry type
//	tier             – filter by tier (realtime|background|debug|audit)
//	level            – filter by level (info|warn|error|debug)
//	operation_id     – filter by operation ID
//	book_id          – filter by book ID
//	since            – RFC3339 lower-bound timestamp (inclusive)
//	until            – RFC3339 upper-bound timestamp (inclusive)
//	tags             – comma-separated list of required tags (AND semantics)
//	search           – substring match on summary
//	source           – show only entries from this source
//	exclude_sources  – comma-separated list of sources to hide
func (h *ActivityHandler) ListActivity(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}

	filter := database.ActivityFilter{}

	params := httputil.ParsePaginationParams(c)
	filter.Limit = params.Limit
	filter.Offset = params.Offset

	filter.Type = c.Query("type")
	filter.Tier = c.Query("tier")
	filter.Level = c.Query("level")
	filter.OperationID = c.Query("operation_id")
	filter.BookID = c.Query("book_id")

	if v := c.Query("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid since: must be RFC3339")
			return
		}
		filter.Since = &t
	}

	if v := c.Query("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httputil.RespondWithBadRequest(c, "invalid until: must be RFC3339")
			return
		}
		filter.Until = &t
	}

	if v := c.Query("tags"); v != "" {
		for _, tag := range strings.Split(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				filter.Tags = append(filter.Tags, tag)
			}
		}
	}

	filter.Search = c.Query("search")
	filter.Source = c.Query("source")
	if v := c.Query("exclude_sources"); v != "" {
		for _, src := range strings.Split(v, ",") {
			src = strings.TrimSpace(src)
			if src != "" {
				filter.ExcludeSources = append(filter.ExcludeSources, src)
			}
		}
	}
	if v := c.Query("exclude_tiers"); v != "" {
		for _, tier := range strings.Split(v, ",") {
			tier = strings.TrimSpace(tier)
			if tier != "" {
				filter.ExcludeTiers = append(filter.ExcludeTiers, tier)
			}
		}
	}
	if v := c.Query("exclude_tags"); v != "" {
		for _, tag := range strings.Split(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				filter.ExcludeTags = append(filter.ExcludeTags, tag)
			}
		}
	}

	entries, total, err := h.svc.Query(filter)
	if err != nil {
		httputil.InternalError(c, "failed to query activity log", err)
		return
	}

	// Ensure entries is always a JSON array, never null.
	if entries == nil {
		entries = []database.ActivityEntry{}
	}

	httputil.RespondWithOK(c, struct {
		Entries []database.ActivityEntry `json:"entries"`
		Total   int                      `json:"total"`
	}{Entries: entries, Total: total})
}

// ListActivitySources handles GET /api/v1/activity/sources.
//
// Returns distinct sources with their entry counts, filtered by the same
// tier/level/since/until parameters as ListActivity.
func (h *ActivityHandler) ListActivitySources(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}
	filter := database.ActivityFilter{
		Tier:  c.Query("tier"),
		Level: c.Query("level"),
	}
	if v := c.Query("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Since = &t
		}
	}
	if v := c.Query("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Until = &t
		}
	}
	sources, err := h.svc.GetDistinctSources(filter)
	if err != nil {
		httputil.InternalError(c, "failed to get sources", err)
		return
	}
	if sources == nil {
		sources = []database.SourceCount{}
	}
	httputil.RespondWithOK(c, struct {
		Sources []database.SourceCount `json:"sources"`
	}{Sources: sources})
}

// ListOperationActivity handles GET /api/v1/operations/:id/activity.
//
// Returns all activity log entries for the given operation ID, ordered by
// timestamp ASC (oldest first). Falls back to op-log v2 rows when the
// activity log has no entries for that operation.
func (h *ActivityHandler) ListOperationActivity(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}
	opID := strings.TrimSpace(c.Param("id"))
	if opID == "" {
		httputil.RespondWithBadRequest(c, "operation id required")
		return
	}

	limit := 1000
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 10000 {
				n = 10000
			}
			limit = n
		}
	}

	filter := database.ActivityFilter{
		OperationID: opID,
		Limit:       limit,
	}
	entries, total, err := h.svc.Query(filter)
	if err != nil {
		httputil.InternalError(c, "failed to query activity log", err)
		return
	}

	// Query returns entries newest-first; reverse for ASC chronological order.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	if entries == nil {
		entries = []database.ActivityEntry{}
	}
	if len(entries) == 0 {
		opLogEntries, opLogErr := h.operationActivityFromOpLogs(opID, limit)
		if opLogErr != nil {
			httputil.InternalError(c, "failed to query operation logs", opLogErr)
			return
		}
		if opLogEntries != nil {
			httputil.RespondWithOK(c, struct {
				OperationID string                   `json:"operation_id"`
				Entries     []operationActivityEntry `json:"entries"`
				Total       int                      `json:"total"`
			}{OperationID: opID, Entries: opLogEntries, Total: len(opLogEntries)})
			return
		}
	}
	responseEntries := make([]operationActivityEntry, 0, len(entries))
	for _, e := range entries {
		responseEntries = append(responseEntries, activityEntryToOperationEntry(e))
	}
	httputil.RespondWithOK(c, struct {
		OperationID string                   `json:"operation_id"`
		Entries     []operationActivityEntry `json:"entries"`
		Total       int                      `json:"total"`
	}{OperationID: opID, Entries: responseEntries, Total: total})
}

// operationActivityFromOpLogs fetches op-log v2 rows as a fallback when the
// activity log has no entries for an operation. Returns nil (not an error)
// when opsStore is nil or returns no rows.
func (h *ActivityHandler) operationActivityFromOpLogs(opID string, limit int) ([]operationActivityEntry, error) {
	if h.opsStore == nil {
		return nil, nil
	}
	logs, err := h.opsStore.GetOpLogsV2(opID, limit)
	if err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		return []operationActivityEntry{}, nil
	}
	out := make([]operationActivityEntry, 0, len(logs))
	for _, l := range logs {
		attrs := opLogAttrs(l.Attrs)
		opType, _ := attrs["def_id"].(string)
		out = append(out, operationActivityEntry{
			Timestamp:     l.CreatedAt,
			Level:         l.Level,
			OperationID:   l.OperationID,
			OperationType: opType,
			Message:       l.Message,
			Details:       l.Attrs,
			Tags:          operationLogTags(l.OperationID, l.Level, opType, attrs),
		})
	}
	return out, nil
}

// RecompactDigests handles POST /api/v1/admin/recompact-digests.
//
// Re-derives type, tier, and tags on every stored daily-digest entry.
// Returns { touched, skipped }. Safe to call multiple times (idempotent).
func (h *ActivityHandler) RecompactDigests(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}

	result, err := h.svc.RecompactDigests(c.Request.Context())
	if err != nil {
		httputil.InternalError(c, "recompact digests failed", err)
		return
	}

	httputil.RespondWithOK(c, result)
}

// CompactActivity handles POST /api/v1/activity/compact.
func (h *ActivityHandler) CompactActivity(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}

	var req struct {
		OlderThanDays int `json:"older_than_days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.OlderThanDays < 0 {
		httputil.RespondWithBadRequest(c, "older_than_days must be zero or positive")
		return
	}

	// 0 means "compact everything up to now"
	cutoff := time.Now().AddDate(0, 0, -req.OlderThanDays)
	result, err := h.svc.CompactByDay(c.Request.Context(), cutoff)
	if err != nil {
		httputil.InternalError(c, "activity compaction failed", err)
		return
	}

	httputil.RespondWithOK(c, result)
}

// activityEntryToOperationEntry converts an ActivityEntry to the operation
// transcript response shape.
func activityEntryToOperationEntry(e database.ActivityEntry) operationActivityEntry {
	details := ""
	if len(e.Details) > 0 {
		if b, err := json.Marshal(e.Details); err == nil {
			details = string(b)
		}
	}
	return operationActivityEntry{
		Timestamp:     e.Timestamp,
		Level:         e.Level,
		OperationID:   e.OperationID,
		OperationType: e.Type,
		Message:       e.Summary,
		Details:       details,
		Tags:          e.Tags,
	}
}

func opLogAttrs(raw string) map[string]any {
	if raw == "" || raw == "{}" {
		return map[string]any{}
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(raw), &attrs); err != nil {
		return map[string]any{}
	}
	return attrs
}

func operationLogTags(opID, level, opType string, attrs map[string]any) []string {
	entry := database.ActivityEntry{
		Tier:        "info",
		Type:        opType,
		Level:       level,
		OperationID: opID,
		Details:     attrs,
		Tags:        []string{"operation"},
	}
	if plugin, ok := attrs["plugin"].(string); ok {
		entry.Source = plugin
		entry.Tags = append(entry.Tags, "plugin:"+plugin)
	}
	if opType != "" {
		entry.Tags = append(entry.Tags, "def:"+opType)
	}
	activity.EnrichTags(&entry)
	return entry.Tags
}
