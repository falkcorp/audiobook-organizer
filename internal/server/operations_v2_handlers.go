// file: internal/server/operations_v2_handlers.go
// version: 1.3.0
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-06-23

// UOS-06: SSE event hub, /operations/timeline, single-op introspection,
// cancel, trigger-op, and /op-defs endpoints.

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// operationV2Response is the JSON shape returned by the timeline and single-op
// endpoints. It mirrors the TypeScript OperationV2 interface in api.ts.
type operationV2Response struct {
	ID              string     `json:"id"`
	DefID           string     `json:"def_id"`
	Plugin          string     `json:"plugin"`
	DisplayName     string     `json:"display_name"`
	Status          string     `json:"status"`
	Priority        int        `json:"priority"`
	NotifyLevel     int        `json:"notify_level"`
	ProgressCurrent *int       `json:"progress_current"`
	ProgressTotal   *int       `json:"progress_total"`
	ProgressMessage *string    `json:"progress_message"`
	CurrentPhase    *string    `json:"current_phase"`
	CurrentItem     *string    `json:"current_item"`
	ActorUserID     *string    `json:"actor_user_id"`
	ParentID        *string    `json:"parent_id"`
	QueuedAt        time.Time  `json:"queued_at"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	ErrorMessage    *string    `json:"error_message"`
	ResumeCount     int        `json:"resume_count"`
	TraceID         string     `json:"trace_id"`
	SpanID          string     `json:"span_id"`
}

// opLogV2Response is the JSON shape for a single log line.
type opLogV2Response struct {
	OperationID string    `json:"operation_id"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Attrs       any       `json:"attrs"`
	CreatedAt   time.Time `json:"created_at"`
}

// opDefResponse is the JSON shape returned by /op-defs.
type opDefResponse struct {
	ID           string   `json:"id"`
	Plugin       string   `json:"plugin"`
	DisplayName  string   `json:"display_name"`
	Description  string   `json:"description"`
	Cancellable  bool     `json:"cancellable"`
	Isolate      bool     `json:"isolate"`
	ResumePolicy string   `json:"resume_policy"`
	Triggers     []string `json:"triggers"`
	DependsOn    []string `json:"depends_on"`
}

// handleGetOperationTimeline implements GET /api/v1/operations/timeline?since=15m.
// It reads operations from the v2 store that were queued within the given window.
func (s *Server) handleGetOperationTimeline(c *gin.Context) {
	sinceStr := c.DefaultQuery("since", "15m")
	dur, err := parseSinceDuration(sinceStr)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid since parameter: "+sinceStr)
		return
	}

	store := s.opsV2Store()
	if store == nil {
		httputil.RespondWithOK(c, gin.H{"operations": []operationV2Response{}})
		return
	}

	since := time.Now().UTC().Add(-dur)
	rows, err := store.ListOperationsV2Since(since, 200)
	if err != nil {
		httputil.InternalError(c, "failed to list operations", err)
		return
	}

	resp := make([]operationV2Response, 0, len(rows))
	for _, r := range rows {
		item := rowToResponse(r, s.displayNameFor(r.DefID), s.notifyLevelFor(r.DefID))
		if r.Status == "running" && s.opRegistry != nil {
			if ci := s.opRegistry.GetCurrentItem(r.ID); ci != "" {
				item.CurrentItem = &ci
			}
		}
		resp = append(resp, item)
	}
	httputil.RespondWithOK(c, gin.H{"operations": resp})
}

// handleGetOperationV2 implements GET /api/v1/operations/v2/:id.
// Returns the operation plus its last 50 log lines.
func (s *Server) handleGetOperationV2(c *gin.Context) {
	id := c.Param("id")
	store := s.opsV2Store()
	if store == nil {
		httputil.RespondWithNotFound(c, "operation", id)
		return
	}

	row, err := store.GetOperationV2(id)
	if err != nil || row == nil {
		httputil.RespondWithNotFound(c, "operation", id)
		return
	}

	logs, err := store.GetOpLogsV2(id, 50)
	if err != nil {
		// Non-fatal: return the op without logs.
		logs = nil
	}

	logResp := make([]opLogV2Response, 0, len(logs))
	for _, l := range logs {
		logResp = append(logResp, logRowToResponse(l))
	}

	opResp := rowToResponse(*row, s.displayNameFor(row.DefID), s.notifyLevelFor(row.DefID))
	if row.Status == "running" && s.opRegistry != nil {
		if ci := s.opRegistry.GetCurrentItem(id); ci != "" {
			opResp.CurrentItem = &ci
		}
	}
	httputil.RespondWithOK(c, gin.H{
		"operation": opResp,
		"logs":      logResp,
	})
}

// handleCancelOperationV2 implements DELETE /api/v1/operations/v2/:id.
// Cancels the operation via the registry (if running) or marks it canceled (if queued).
func (s *Server) handleCancelOperationV2(c *gin.Context) {
	id := c.Param("id")
	if s.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	if err := s.opRegistry.Cancel(id); err != nil {
		httputil.InternalError(c, "cancel failed", err)
		return
	}
	httputil.RespondWithNoContent(c)
}

// handleTriggerOperationV2 implements POST /api/v1/operations/v2.
// Body: { "def_id": "...", "params": {...} }
func (s *Server) handleTriggerOperationV2(c *gin.Context) {
	if s.opRegistry == nil {
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

	opID, err := s.opRegistry.EnqueueOp(c.Request.Context(), body.DefID, body.Params)
	if err != nil {
		httputil.InternalError(c, "enqueue failed", err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"op_id": opID})
}

// handleListOpDefs implements GET /api/v1/op-defs.
// Returns the set of registered OperationDefs.
func (s *Server) handleListOpDefs(c *gin.Context) {
	if s.opRegistry == nil {
		httputil.RespondWithOK(c, gin.H{"defs": []opDefResponse{}})
		return
	}
	defs := s.opRegistry.ActiveDefs()
	resp := make([]opDefResponse, 0, len(defs))
	for _, d := range defs {
		resp = append(resp, defToResponse(d))
	}
	httputil.RespondWithOK(c, gin.H{"defs": resp})
}

// handleGetOpDef implements GET /api/v1/op-defs/:id.
func (s *Server) handleGetOpDef(c *gin.Context) {
	id := c.Param("id")
	if s.opRegistry == nil {
		httputil.RespondWithNotFound(c, "op-def", id)
		return
	}
	for _, d := range s.opRegistry.ActiveDefs() {
		if d.ID == id {
			httputil.RespondWithOK(c, gin.H{"def": defToResponse(d)})
			return
		}
	}
	httputil.RespondWithNotFound(c, "op-def", id)
}

// handleOperationsSSE implements GET /api/v1/operations/events.
// Streams SSE events from the opHub to the client until the client disconnects.
func (s *Server) handleOperationsSSE(c *gin.Context) {
	if s.opHub == nil {
		// Hub not initialised; return 503 rather than hanging.
		httputil.RespondWithServiceUnavailable(c, "operations event hub not initialized")
		return
	}

	ch, unsubscribe := s.opHub.Subscribe()
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

// opsV2Store returns the OpsV2Store from the server's composite Store, or nil
// if the store does not implement it.
func (s *Server) opsV2Store() database.OpsV2Store {
	return database.GetOpsV2(s.Store())
}

// displayNameFor looks up the human-readable display name for a def ID.
// Falls back to the ID itself if the def is not registered.
func (s *Server) displayNameFor(defID string) string {
	if s.opRegistry == nil {
		return defID
	}
	for _, d := range s.opRegistry.ActiveDefs() {
		if d.ID == defID {
			return d.DisplayName
		}
	}
	return defID
}

// notifyLevelFor looks up the NotifyLevel for a registered def ID.
// Returns 0 (NotifyAlert) if the def is not found, preserving old behaviour.
func (s *Server) notifyLevelFor(defID string) int {
	if s.opRegistry == nil {
		return 0
	}
	for _, d := range s.opRegistry.ActiveDefs() {
		if d.ID == defID {
			return int(d.NotifyLevel)
		}
	}
	return 0
}

// rowToResponse converts a database.OperationV2Row to the HTTP response shape.
func rowToResponse(r database.OperationV2Row, displayName string, notifyLevel int) operationV2Response {
	resp := operationV2Response{
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
func logRowToResponse(l database.OpLogV2Row) opLogV2Response {
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
	return opLogV2Response{
		OperationID: l.OperationID,
		Level:       l.Level,
		Message:     l.Message,
		Attrs:       attrsAny,
		CreatedAt:   l.CreatedAt,
	}
}

// defToResponse converts a registry.OperationDef to the HTTP response shape.
func defToResponse(d opsregistry.OperationDef) opDefResponse {
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

	return opDefResponse{
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
