// file: internal/server/handlers/operations_v2.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-06-03

// UOS-06: SSE event hub, /operations/timeline, single-op introspection,
// cancel, trigger-op, and /op-defs endpoints.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/gin-gonic/gin"
)

// OperationsRegistry is the narrow interface OperationsV2Handler requires from
// the operations registry. It lists only the methods the handlers call.
type OperationsRegistry interface {
	GetCurrentItem(opID string) string
	Cancel(opID string) error
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
	ActiveDefs() []opsregistry.OperationDef
}

// OperationsEventHub is the narrow interface OperationsV2Handler requires from
// the operations SSE event bus. Only Subscribe is used.
type OperationsEventHub interface {
	Subscribe() (<-chan opsregistry.Event, func())
}

// OperationsV2Handler handles the UOS-06 operations v2 endpoints: timeline,
// single-op introspection, cancel, trigger, op-def listing, and the SSE stream.
type OperationsV2Handler struct {
	opsStore database.OpsV2Store
	registry OperationsRegistry
	hub      OperationsEventHub
}

// NewOperationsV2Handler constructs an OperationsV2Handler. The opsStore may be
// nil (the store does not implement OpsV2Store); the handlers guard for it.
func NewOperationsV2Handler(opsStore database.OpsV2Store, registry OperationsRegistry, hub OperationsEventHub) *OperationsV2Handler {
	return &OperationsV2Handler{opsStore: opsStore, registry: registry, hub: hub}
}

// GetOperationTimeline implements GET /api/v1/operations/timeline?since=15m.
// It reads operations from the v2 store that were queued within the given window.
func (h *OperationsV2Handler) GetOperationTimeline(c *gin.Context) {
	sinceStr := c.DefaultQuery("since", "15m")
	dur, err := parseSinceDuration(sinceStr)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid since parameter: "+sinceStr)
		return
	}

	if h.opsStore == nil {
		httputil.RespondWithOK(c, gin.H{"operations": []OperationV2Response{}})
		return
	}

	since := time.Now().UTC().Add(-dur)
	rows, err := h.opsStore.ListOperationsV2Since(since, 200)
	if err != nil {
		httputil.InternalError(c, "failed to list operations", err)
		return
	}

	resp := make([]OperationV2Response, 0, len(rows))
	for _, r := range rows {
		item := rowToResponse(r, h.displayNameFor(r.DefID), h.notifyLevelFor(r.DefID))
		if r.Status == "running" && h.registry != nil {
			if ci := h.registry.GetCurrentItem(r.ID); ci != "" {
				item.CurrentItem = &ci
			}
		}
		resp = append(resp, item)
	}
	httputil.RespondWithOK(c, gin.H{"operations": resp})
}

// GetOperationV2 implements GET /api/v1/operations/v2/:id.
// Returns the operation plus its last 50 log lines.
func (h *OperationsV2Handler) GetOperationV2(c *gin.Context) {
	id := c.Param("id")
	if h.opsStore == nil {
		httputil.RespondWithNotFound(c, "operation", id)
		return
	}

	row, err := h.opsStore.GetOperationV2(id)
	if err != nil || row == nil {
		httputil.RespondWithNotFound(c, "operation", id)
		return
	}

	logs, err := h.opsStore.GetOpLogsV2(id, 50)
	if err != nil {
		// Non-fatal: return the op without logs.
		logs = nil
	}

	logResp := make([]OpLogV2Response, 0, len(logs))
	for _, l := range logs {
		logResp = append(logResp, logRowToResponse(l))
	}

	opResp := rowToResponse(*row, h.displayNameFor(row.DefID), h.notifyLevelFor(row.DefID))
	if row.Status == "running" && h.registry != nil {
		if ci := h.registry.GetCurrentItem(id); ci != "" {
			opResp.CurrentItem = &ci
		}
	}
	httputil.RespondWithOK(c, gin.H{
		"operation": opResp,
		"logs":      logResp,
	})
}

// CancelOperationV2 implements DELETE /api/v1/operations/v2/:id.
// Cancels the operation via the registry (if running) or marks it canceled (if queued).
func (h *OperationsV2Handler) CancelOperationV2(c *gin.Context) {
	id := c.Param("id")
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	if err := h.registry.Cancel(id); err != nil {
		httputil.InternalError(c, "cancel failed", err)
		return
	}
	httputil.RespondWithNoContent(c)
}

// TriggerOperationV2 implements POST /api/v1/operations/v2.
// Body: { "def_id": "...", "params": {...} }
func (h *OperationsV2Handler) TriggerOperationV2(c *gin.Context) {
	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}

	var body struct {
		DefID  string `json:"def_id"`
		Params any    `json:"params"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.DefID == "" {
		httputil.RespondWithBadRequest(c, "body must include def_id")
		return
	}

	opID, err := h.registry.EnqueueOp(c.Request.Context(), body.DefID, body.Params)
	if err != nil {
		httputil.InternalError(c, "enqueue failed", err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"op_id": opID})
}

// ListOpDefs implements GET /api/v1/op-defs.
// Returns the set of registered OperationDefs.
func (h *OperationsV2Handler) ListOpDefs(c *gin.Context) {
	if h.registry == nil {
		httputil.RespondWithOK(c, gin.H{"defs": []OpDefResponse{}})
		return
	}
	defs := h.registry.ActiveDefs()
	resp := make([]OpDefResponse, 0, len(defs))
	for _, d := range defs {
		resp = append(resp, defToResponse(d))
	}
	httputil.RespondWithOK(c, gin.H{"defs": resp})
}

// GetOpDef implements GET /api/v1/op-defs/:id.
func (h *OperationsV2Handler) GetOpDef(c *gin.Context) {
	id := c.Param("id")
	if h.registry == nil {
		httputil.RespondWithNotFound(c, "op-def", id)
		return
	}
	for _, d := range h.registry.ActiveDefs() {
		if d.ID == id {
			httputil.RespondWithOK(c, gin.H{"def": defToResponse(d)})
			return
		}
	}
	httputil.RespondWithNotFound(c, "op-def", id)
}

// OperationsSSE implements GET /api/v1/operations/events.
// Streams SSE events from the opHub to the client until the client disconnects.
func (h *OperationsV2Handler) OperationsSSE(c *gin.Context) {
	if h.hub == nil {
		// Hub not initialised; return 503 rather than hanging.
		httputil.RespondWithServiceUnavailable(c, "operations event hub not initialized")
		return
	}

	ch, unsubscribe := h.hub.Subscribe()
	defer unsubscribe()

	// Required SSE headers.
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // disable nginx buffering

	// Send a heartbeat immediately so the client knows the connection is live.
	fmt.Fprintf(c.Writer, ": heartbeat\n\n")
	c.Writer.Flush()

	notify := c.Request.Context().Done()
	for {
		select {
		case <-notify:
			// Client disconnected.
			return
		case ev, ok := <-ch:
			if !ok {
				// Hub closed the channel (server shutdown).
				return
			}
			b, err := json.Marshal(ev.Payload)
			if err != nil {
				continue
			}
			fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", ev.Name, b)
			c.Writer.Flush()
		}
	}
}

// --- helpers ---

// displayNameFor looks up the human-readable display name for a def ID.
// Falls back to the ID itself if the def is not registered.
func (h *OperationsV2Handler) displayNameFor(defID string) string {
	if h.registry == nil {
		return defID
	}
	for _, d := range h.registry.ActiveDefs() {
		if d.ID == defID {
			return d.DisplayName
		}
	}
	return defID
}

// notifyLevelFor looks up the NotifyLevel for a registered def ID.
// Returns 0 (NotifyAlert) if the def is not found, preserving old behaviour.
func (h *OperationsV2Handler) notifyLevelFor(defID string) int {
	if h.registry == nil {
		return 0
	}
	for _, d := range h.registry.ActiveDefs() {
		if d.ID == defID {
			return int(d.NotifyLevel)
		}
	}
	return 0
}

// rowToResponse converts a database.OperationV2Row to the HTTP response shape.
func rowToResponse(r database.OperationV2Row, displayName string, notifyLevel int) OperationV2Response {
	resp := OperationV2Response{
		ID:           r.ID,
		DefID:        r.DefID,
		Plugin:       r.Plugin,
		DisplayName:  displayName,
		Status:       r.Status,
		Priority:     r.Priority,
		NotifyLevel:  notifyLevel,
		ActorUserID:  r.ActorUserID,
		ParentID:     r.ParentID,
		QueuedAt:     r.QueuedAt,
		StartedAt:    r.StartedAt,
		CompletedAt:  r.CompletedAt,
		ErrorMessage: r.ErrorMessage,
		ResumeCount:  r.ResumeCount,
		TraceID:      r.TraceID,
		SpanID:       r.SpanID,
		CurrentPhase: r.CurrentPhase,
	}
	// Convert scalar progress fields to nullable pointers for the JSON contract.
	cur := r.ProgressCurrent
	resp.ProgressCurrent = &cur
	tot := r.ProgressTotal
	resp.ProgressTotal = &tot
	if r.ProgressMessage != "" {
		resp.ProgressMessage = &r.ProgressMessage
	}
	return resp
}

// logRowToResponse converts a database.OpLogV2Row to the HTTP response shape.
func logRowToResponse(l database.OpLogV2Row) OpLogV2Response {
	var attrsAny any
	if l.Attrs != "" && l.Attrs != "{}" {
		var m map[string]any
		if err := json.Unmarshal([]byte(l.Attrs), &m); err == nil {
			attrsAny = m
		} else {
			attrsAny = l.Attrs
		}
	} else {
		attrsAny = map[string]any{}
	}
	return OpLogV2Response{
		OperationID: l.OperationID,
		Level:       l.Level,
		Message:     l.Message,
		Attrs:       attrsAny,
		CreatedAt:   l.CreatedAt,
	}
}

// defToResponse converts a registry.OperationDef to the HTTP response shape.
func defToResponse(d opsregistry.OperationDef) OpDefResponse {
	triggers := make([]string, 0, len(d.Triggers))
	for _, t := range d.Triggers {
		triggers = append(triggers, t.EventName)
	}
	depends := make([]string, len(d.DependsOn))
	copy(depends, d.DependsOn)

	rp := "unspecified"
	switch d.ResumePolicy {
	case opsregistry.ResumeRestart:
		rp = "restart"
	case opsregistry.ResumeRequeue:
		rp = "requeue"
	case opsregistry.ResumeDrop:
		rp = "drop"
	case opsregistry.ResumeAsk:
		rp = "ask"
	}

	return OpDefResponse{
		ID:           d.ID,
		Plugin:       d.Plugin,
		DisplayName:  d.DisplayName,
		Description:  d.Description,
		Cancellable:  d.Cancellable,
		Isolate:      d.Isolate,
		ResumePolicy: rp,
		Triggers:     triggers,
		DependsOn:    depends,
	}
}

// parseSinceDuration parses strings like "15m", "1h", "30s", "2h30m".
func parseSinceDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}
