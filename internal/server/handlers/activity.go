// file: internal/server/handlers/activity.go
// version: 1.4.0
// guid: d4e5f6a7-b8c9-0123-def0-234567890123
// last-edited: 2026-09-10

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

// statusClientClosedRequest is nginx's 499: the client went away before the
// response was written. Not in net/http, so it is spelled out here.
const statusClientClosedRequest = 499

// abortIfClientGone reports whether err is a cancelled/expired request context
// and, when it is, ends the request with 499 and no body.
//
// Two things this deliberately avoids. It does NOT fall through to
// httputil.InternalError: an abandoned request is not a server fault, and
// routing every one of them through the 500 path would turn this fix into a
// 5xx alert storm. It also does NOT use a bare c.Abort(), because Gin's default
// status is 200 and a cancelled scan must never be reported as a successful
// empty result — the caller would cache or render it as real data.
func abortIfClientGone(c *gin.Context, err error, op string) bool {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	slog.Debug("[activity] request abandoned; scan cancelled",
		"op", op, "path", c.Request.URL.Path, "error", err)
	c.AbortWithStatus(statusClientClosedRequest)
	return true
}

// ActivityService is the narrow interface ActivityHandler requires from the
// activity log service.
//
// Query and GetDistinctSources take a context and every handler below passes
// c.Request.Context(). Gin cancels that context when the client disconnects,
// which is what lets an abandoned request stop scanning the activity log
// instead of running to completion against a socket nobody is reading.
type ActivityService interface {
	Query(ctx context.Context, filter database.ActivityFilter) ([]database.ActivityEntry, int, error)
	GetDistinctSources(ctx context.Context, filter database.ActivityFilter) ([]database.SourceCount, error)
	RecompactDigests(ctx context.Context) (database.RecompactResult, error)
	ClampSummaries(ctx context.Context, max int, dryRun, vacuum bool) (database.ClampSummariesResult, error)
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
		for tag := range strings.SplitSeq(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				filter.Tags = append(filter.Tags, tag)
			}
		}
	}

	filter.Search = c.Query("search")
	filter.Source = c.Query("source")
	if v := c.Query("exclude_sources"); v != "" {
		for src := range strings.SplitSeq(v, ",") {
			src = strings.TrimSpace(src)
			if src != "" {
				filter.ExcludeSources = append(filter.ExcludeSources, src)
			}
		}
	}
	if v := c.Query("exclude_tiers"); v != "" {
		for tier := range strings.SplitSeq(v, ",") {
			tier = strings.TrimSpace(tier)
			if tier != "" {
				filter.ExcludeTiers = append(filter.ExcludeTiers, tier)
			}
		}
	}
	if v := c.Query("exclude_tags"); v != "" {
		for tag := range strings.SplitSeq(v, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				filter.ExcludeTags = append(filter.ExcludeTags, tag)
			}
		}
	}

	entries, total, err := h.svc.Query(c.Request.Context(), filter)
	if err != nil {
		if abortIfClientGone(c, err, "ListActivity") {
			return
		}
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
	sources, err := h.svc.GetDistinctSources(c.Request.Context(), filter)
	if err != nil {
		if abortIfClientGone(c, err, "ListActivitySources") {
			return
		}
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

// operationActivityLimitDefault and operationActivityLimitMax bound the
// per-request entry cap shared by the single-op and merged transcript reads.
const (
	operationActivityLimitDefault = 1000
	operationActivityLimitMax     = 10000
)

// mergedOperationIDsMax caps how many members one merged read may name. The
// bell's groups are bounded by a 24-hour window of one operation kind, so this
// is a sanity ceiling against a runaway client, not a tuning knob.
const mergedOperationIDsMax = 1000

// parseOperationActivityLimit reads a positive integer limit, clamped to
// operationActivityLimitMax; anything absent or unparseable is the default.
func parseOperationActivityLimit(raw string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
		return min(n, operationActivityLimitMax)
	}
	return operationActivityLimitDefault
}

// operationActivity is the one transcript read: the newest `limit` activity
// log entries for opID in chronological order, or the op-log v2 rows when the
// activity log holds nothing for it. Both HTTP readers go through here so the
// fallback rule cannot drift between a single op and a group of them.
//
// The returned slice is never nil. total is the activity log's own count for
// the op (which can exceed len(entries) once limit bites) or, on the fallback
// path, the number of op-log rows returned.
func (h *ActivityHandler) operationActivity(ctx context.Context, opID string, limit int) ([]operationActivityEntry, int, error) {
	entries, total, err := h.svc.Query(ctx, database.ActivityFilter{OperationID: opID, Limit: limit})
	if err != nil {
		return nil, 0, err
	}
	if len(entries) == 0 {
		opLogEntries, opLogErr := h.operationActivityFromOpLogs(opID, limit)
		if opLogErr != nil {
			return nil, 0, opLogErr
		}
		if opLogEntries != nil {
			return opLogEntries, len(opLogEntries), nil
		}
		return []operationActivityEntry{}, total, nil
	}
	// Query returns entries newest-first; reverse for ASC chronological order.
	out := make([]operationActivityEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, activityEntryToOperationEntry(entries[i]))
	}
	return out, total, nil
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
	limit := parseOperationActivityLimit(c.Query("limit"))

	entries, total, err := h.operationActivity(c.Request.Context(), opID, limit)
	if err != nil {
		if abortIfClientGone(c, err, "ListOperationActivity") {
			return
		}
		httputil.InternalError(c, "failed to query operation activity", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		OperationID string                   `json:"operation_id"`
		Entries     []operationActivityEntry `json:"entries"`
		Total       int                      `json:"total"`
	}{OperationID: opID, Entries: entries, Total: total})
}

// mergedOperationActivityRequest is the body of POST /operations/activity/merged.
type mergedOperationActivityRequest struct {
	IDs   []string `json:"ids"`
	Limit int      `json:"limit"`
}

// ListMergedOperationActivity handles POST /api/v1/operations/activity/merged.
//
// It is the transcript of a GROUP: the bell and the Activity page fold runs
// of same-kind operations into one synthetic row (web/src/stores/
// operationGrouping.ts), and that row's id names no record, so opening it
// needs a read that takes the members' ids instead. The body is
// {"ids": [...], "limit": N}. POST rather than GET only because a group can
// hold several hundred ULIDs, which does not fit a query string; it writes
// nothing.
//
// Each member is read exactly the way the single-op endpoint reads it —
// activity log first, op-log v2 fallback per member — and the results are
// merged into one chronological timeline. `limit` caps the MERGED timeline and
// drops the oldest entries: a reader opening a group wants to see how it
// ended. `total` is the sum of the members' own totals, so it says how much
// exists rather than how much came back; `truncated` says whether the cap bit.
func (h *ActivityHandler) ListMergedOperationActivity(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}
	var req mergedOperationActivityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body: expected {\"ids\": [...], \"limit\": n}")
		return
	}
	ids := dedupeOperationIDs(req.IDs)
	if len(ids) == 0 {
		httputil.RespondWithBadRequest(c, "at least one operation id required")
		return
	}
	if len(ids) > mergedOperationIDsMax {
		httputil.RespondWithBadRequest(c, "too many operation ids: at most "+strconv.Itoa(mergedOperationIDsMax))
		return
	}
	limit := operationActivityLimitDefault
	if req.Limit > 0 {
		limit = min(req.Limit, operationActivityLimitMax)
	}

	// One indexed read per member, fanned out over a bounded pool: a group can
	// name hundreds of members, and each read is a store round-trip. Results
	// land in their own slot so the merge below is deterministic regardless of
	// which read finished first.
	perOp := make([][]operationActivityEntry, len(ids))
	totals := make([]int, len(ids))
	g, ctx := errgroup.WithContext(c.Request.Context())
	g.SetLimit(runtime.NumCPU())
	for i, id := range ids {
		g.Go(func() error {
			entries, total, err := h.operationActivity(ctx, id, limit)
			if err != nil {
				return err
			}
			perOp[i], totals[i] = entries, total
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		if abortIfClientGone(c, err, "ListMergedOperationActivity") {
			return
		}
		httputil.InternalError(c, "failed to query operation activity", err)
		return
	}

	total, n := 0, 0
	for i := range ids {
		total += totals[i]
		n += len(perOp[i])
	}
	merged := make([]operationActivityEntry, 0, n)
	for _, entries := range perOp {
		merged = append(merged, entries...)
	}
	// Stable, so two members' entries with the same timestamp keep the caller's
	// member order (the group lists its members oldest first).
	sort.SliceStable(merged, func(a, b int) bool { return merged[a].Timestamp.Before(merged[b].Timestamp) })
	truncated := false
	if len(merged) > limit {
		merged = merged[len(merged)-limit:]
		truncated = true
	}

	httputil.RespondWithOK(c, struct {
		OperationIDs []string                 `json:"operation_ids"`
		Entries      []operationActivityEntry `json:"entries"`
		Total        int                      `json:"total"`
		Truncated    bool                     `json:"truncated"`
	}{OperationIDs: ids, Entries: merged, Total: total, Truncated: truncated})
}

// dedupeOperationIDs trims, drops blanks, and keeps the first occurrence of
// each id in the caller's order.
func dedupeOperationIDs(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

// POST /api/v1/activity/compact moved to ActivityCompactHandler
// (activity_compact.go) on 2026-09-10: it enqueues
// maintenance.compact-activity-log instead of compacting inside the request.

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

// ClampActivitySummaries handles POST /api/v1/activity/clamp-summaries.
//
// Retroactively applies the write-path summary cap to rows written before that
// cap existed. Defaults to a dry run: this rewrites historical rows, so the
// safe default is to measure and report rather than to mutate, and the caller
// must opt in explicitly with {"apply": true}.
func (h *ActivityHandler) ClampActivitySummaries(c *gin.Context) {
	if h.svc == nil {
		httputil.RespondWithInternalError(c, "activity log not available")
		return
	}

	var req struct {
		Apply  bool `json:"apply"`  // false (default) = dry run
		Max    int  `json:"max"`    // 0 = no cap
		Vacuum bool `json:"vacuum"` // reclaim file space after a real pass
	}
	// An absent body is a valid request: it means "dry run, no cap".
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body")
		return
	}
	if req.Max < 0 {
		httputil.RespondWithBadRequest(c, "max must be zero or positive")
		return
	}

	res, err := h.svc.ClampSummaries(c.Request.Context(), req.Max, !req.Apply, req.Vacuum)
	if errors.Is(err, activity.ErrSummaryClampUnsupported) {
		httputil.RespondWithBadRequest(c, "summary clamp requires the SQLite activity backend")
		return
	}
	if err != nil {
		httputil.InternalError(c, "activity summary clamp failed", err)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"dry_run":         !req.Apply,
		"scanned":         res.Scanned,
		"clamped":         res.Clamped,
		"bytes_before":    res.BytesBefore,
		"bytes_after":     res.BytesAfter,
		"bytes_reclaimed": res.Reclaimed(),
		"truncated":       res.Truncated,
	})
}
