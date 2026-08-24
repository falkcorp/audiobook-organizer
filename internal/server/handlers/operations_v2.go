// file: internal/server/handlers/operations_v2.go
// version: 1.5.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-08-24

// UOS-06: SSE event hub, /operations/timeline, single-op introspection,
// cancel, trigger-op, and /op-defs endpoints.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
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
	// Def looks up a single registered def. Used by TriggerOperationV2 to read
	// the def's declared Permissions before enqueueing.
	Def(id string) (opsregistry.OperationDef, bool)
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

	// AI-scan cancellation, optional. See WithAIScanCancellation.
	scanCanceler ScanCanceler
	scanLister   AIScanLister

	// enforcePerms gates the per-def permission check in TriggerOperationV2. Set
	// from config.AppConfig.EnableAuth at the wiring site and passed positionally
	// to NewOperationsV2Handler — NOT read from global config here, matching
	// NewAuthHandler's injected-flag convention.
	//
	// false must mean "do not enforce": auth.Can returns false for a caller with
	// no permission set, so enforcing when auth is disabled would 403 every
	// trigger in that deployment.
	enforcePerms bool
}

// ScanCanceler is the narrow *aiscan.PipelineManager subset needed to cancel an
// in-flight AI scan by scan id.
//
// Declared here rather than imported from handlers/operations: that package
// imports THIS one, so the dependency cannot run the other way. Go's structural
// typing means the same concrete type satisfies both declarations.
type ScanCanceler interface {
	CancelScan(scanID int) error
}

// AIScanLister is the narrow *database.AIScanStore subset needed to find the AI
// scan whose OperationID matches the operation being canceled.
type AIScanLister interface {
	ListScans() ([]database.Scan, error)
}

// OperationsV2Option configures an OperationsV2Handler at construction.
//
// Options rather than more constructor parameters: NewOperationsV2Handler has 22
// call sites, 21 of them tests that model the store and registry and have no
// opinion about AI scans. Widening the signature would edit all of them for a
// concern none of them has.
//
// That trade is right for an OPTIONAL collaborator and wrong for a security
// gate. enforcePerms is therefore positional, not an option: the cost of the
// churn is paid once, and in exchange omitting it cannot compile. See
// NewOperationsV2Handler.
type OperationsV2Option func(*OperationsV2Handler)

// WithAIScanCancellation supplies the collaborators CancelOperationV2 needs to
// cancel an operation that is really an AI scan.
//
// Without it, cancelling such an operation asks the registry to cancel an id it
// does not own and the scan keeps running. That was the state v2 shipped in;
// only the legacy DELETE /operations/:id knew about AI scans, so retiring it
// without this would have silently broken cancellation.
//
// UNVERIFIED AT THE WIRING. The handler behaviour is covered by
// TestCancelOperationV2_CancelsAnAIScanThroughThePipeline, but that nothing
// dropped this option in wire_handlers.go is not asserted anywhere: the two
// collaborators are concrete types on Server (*aiscan.PipelineManager,
// *database.AIScanStore), so no test can substitute them and drive the real
// construction path. Omitting the option here fails silently in the worst
// direction — cancel returns 204 and the scan runs on. Tracked in
// todo.d/20260816-ai-scan-cancel-wiring-unverified.md.
func WithAIScanCancellation(canceler ScanCanceler, lister AIScanLister) OperationsV2Option {
	return func(h *OperationsV2Handler) {
		h.scanCanceler = canceler
		h.scanLister = lister
	}
}

// NewOperationsV2Handler constructs an OperationsV2Handler. The opsStore may be
// nil (the store does not implement OpsV2Store); the handlers guard for it.
//
// enforcePerms is a REQUIRED positional parameter, deliberately not an
// OperationsV2Option. Pass config.AppConfig.EnableAuth at the production wiring
// site; tests that do not exercise authorization pass false.
//
// Why not an option: an option that is forgotten defaults to false and the
// permission gate is silently inert — it would still return 202 on a request it
// was added to reject. This file already carries one instance of that exact
// failure (WithAIScanCancellation, "UNVERIFIED AT THE WIRING" above, tracked in
// todo.d/20260816-ai-scan-cancel-wiring-unverified.md), where a dropped option
// makes cancel answer 204 while the scan runs on. A positional parameter makes
// omission a compile error instead, so the type checker verifies the wiring that
// no test can reach.
func NewOperationsV2Handler(opsStore database.OpsV2Store, registry OperationsRegistry, hub OperationsEventHub, enforcePerms bool, opts ...OperationsV2Option) *OperationsV2Handler {
	h := &OperationsV2Handler{opsStore: opsStore, registry: registry, hub: hub, enforcePerms: enforcePerms}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Timeline query bounds.
//
// timelineScanBound is deliberately much larger than timelineMaxLimit: def_id is
// filtered HERE, and ListOperationsV2Since sorts then truncates before this
// handler ever sees a row. Asking the store for `limit` rows and filtering the
// result would answer "the rows for this def that happen to fall in the newest N
// overall" — a plausible wrong answer, which is the defect this endpoint is being
// fixed for. Same reasoning, same shape as recentReconcileScans in
// internal/server/reconcile_ops_index.go, whose comment notes that a small limit
// pushed down to the store drops QUEUED rows first (they have no StartedAt and
// sort last), so a just-enqueued op is the first thing to vanish from a view whose
// job is to show it.
const (
	timelineDefaultLimit = 200
	timelineMaxLimit     = 1000
	timelineScanBound    = 5000
)

// GetOperationTimeline implements
// GET /api/v1/operations/timeline?since=15m[&def_id=X][&limit=N].
//
// It reads operations from the v2 store that were queued within the given window,
// optionally restricted to a single operation def.
//
// The response describes its own scope — since, window_start, def_id, limit,
// matched, truncated — because the failure mode this endpoint actually produced
// was not an error but a confident undercount. Until 2026-08-24 `def_id` and
// `limit` were not parameters at all: Gin drops unknown query keys silently, so
// `?def_id=X&limit=200` read as "200 rows of op X" and asked for the last quarter
// hour of everything. On a quiet system that returns one unrelated row, which
// looks exactly like "this op has never run" — a reading that produced three wrong
// conclusions in two days, including a maintenance.window failure count recorded
// as 3 nights when it was 7 for 7. An answer that states what it looked at cannot
// be misread as a census.
func (h *OperationsV2Handler) GetOperationTimeline(c *gin.Context) {
	sinceStr := c.DefaultQuery("since", "15m")
	dur, err := parseSinceDuration(sinceStr)
	if err != nil {
		httputil.RespondWithBadRequest(c, "invalid since parameter: "+sinceStr)
		return
	}
	// A negative duration puts the window boundary in the FUTURE, which silently
	// yields a near-empty list plus whatever is still running. Rejecting beats
	// answering something no caller could mean.
	if dur < 0 {
		httputil.RespondWithBadRequest(c, "since must not be negative: "+sinceStr)
		return
	}

	// An unparseable or non-positive limit is rejected rather than ignored. Falling
	// back to the default would repeat the exact bug being fixed: the caller asked
	// for something specific, got the default, and had no way to tell.
	limit := timelineDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 {
			httputil.RespondWithBadRequest(c, "limit must be a positive integer: "+raw)
			return
		}
		// Clamped with an explicit branch rather than the min builtin. Both bound
		// limit to timelineMaxLimit identically, but CodeQL's Go dataflow does not
		// model the Go 1.21 builtin, so it traced strconv.Atoi(c.Query("limit"))
		// straight into the make below and raised go/uncontrolled-allocation-size
		// on an allocation that was already capped at 1000. Written this way the
		// bound is visible to the analyzer as well as to a reader.
		limit = n
		if limit > timelineMaxLimit {
			limit = timelineMaxLimit
		}
	}

	defID := c.Query("def_id")

	since := time.Now().UTC().Add(-dur)
	scope := gin.H{
		"since":        sinceStr,
		"window_start": since.Format(time.RFC3339),
		"def_id":       defID,
		"limit":        limit,
	}

	if h.opsStore == nil {
		scope["operations"] = []OperationV2Response{}
		scope["matched"] = 0
		scope["in_flight_before_window"] = 0
		scope["truncated"] = false
		scope["scan_capped"] = false
		httputil.RespondWithOK(c, scope)
		return
	}

	rows, err := h.opsStore.ListOperationsV2Since(since, timelineScanBound)
	if err != nil {
		httputil.InternalError(c, "failed to list operations", err)
		return
	}

	// matched counts every row in the window that satisfies def_id, BEFORE the
	// limit is applied, so `truncated` is a fact rather than the usual
	// len(rows)==limit guess — which cannot tell "exactly limit existed" from
	// "there were more".
	matched := 0
	// Operations still in flight are returned NO MATTER how old they are, so for
	// those rows `window_start` does not describe why they are here.
	//
	// The store admits every row with a nil CompletedAt unconditionally
	// (pebble_store_ops_v2.go, `row.CompletedAt == nil || !row.QueuedAt.Before(since)`),
	// deliberately: an operation that has not finished is current however long ago
	// it was queued, and filtering on QueuedAt alone meant an op simply had to RUN
	// longer than the window to vanish from its own timeline. A library.scan
	// running 1h50m returned an empty timeline in production while it was logging
	// once a second.
	//
	// That is right, and it makes a bare `matched` misleading in exactly the way
	// this endpoint is being fixed for. A scan queued three weeks ago and never
	// completed answers `?since=1h` with matched=1, which reads as "it ran once in
	// the last hour". Counting those separately is what keeps the stated scope
	// true: an answer that describes itself has to describe the rows that escape
	// its own window too.
	inFlightBeforeWindow := 0
	// Same reason as the clamp above: an explicit branch, so the ceiling on this
	// allocation (timelineMaxLimit, 1000) is reachable by dataflow analysis.
	capHint := len(rows)
	if capHint > limit {
		capHint = limit
	}
	resp := make([]OperationV2Response, 0, capHint)
	for _, r := range rows {
		if defID != "" && r.DefID != defID {
			continue
		}
		matched++
		if r.CompletedAt == nil && r.QueuedAt.Before(since) {
			inFlightBeforeWindow++
		}
		if len(resp) >= limit {
			continue
		}
		item := rowToResponse(r, h.displayNameFor(r.DefID), h.notifyLevelFor(r.DefID))
		if r.Status == "running" && h.registry != nil {
			if ci := h.registry.GetCurrentItem(r.ID); ci != "" {
				item.CurrentItem = &ci
			}
		}
		resp = append(resp, item)
	}

	scope["operations"] = resp
	scope["matched"] = matched
	scope["in_flight_before_window"] = inFlightBeforeWindow
	scope["truncated"] = matched > len(resp)
	// The store itself trims to timelineScanBound after sorting, so a full scan
	// means `matched` is a FLOOR, not a total, and no "it never happened before X"
	// claim can rest on it. Saying so is the difference between a bounded answer
	// and a wrong one.
	//
	// Always present, never omitted-when-false. This object exists to be read by
	// someone who might misread it, and two booleans with two different presence
	// conventions is an invitation to treat a missing `scan_capped` as unknown
	// rather than false.
	scope["scan_capped"] = len(rows) >= timelineScanBound
	httputil.RespondWithOK(c, scope)
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

	// ?limit= makes this a superset of the retired GET /operations/:id/logs?tail=,
	// which is what let that route be deleted rather than kept alongside. The 50
	// default preserves the previous behaviour for callers that pass nothing.
	//
	// Capped: the log window is unbounded per op (the running scan had emitted
	// tens of thousands of lines by the time this was written), so an
	// unvalidated limit is an easy way to ask the server to marshal all of them.
	logLimit := 50
	if raw := c.Query("limit"); raw != "" {
		if n, convErr := strconv.Atoi(raw); convErr == nil && n > 0 {
			logLimit = min(n, 5000)
		}
	}

	logs, err := h.opsStore.GetOpLogsV2(id, logLimit)
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

// GetOperationLogs serves an operation's log lines on their own, satisfying
// system.OperationLogsProvider.
//
// It exists because the legacy operations handler used to provide this and the
// system/diagnostics routes consumed it from there. That implementation fell back
// to the legacy `operations` table, which never moves rows out of `pending`; this
// one reads v2 only. Same interface, honest source.
func (h *OperationsV2Handler) GetOperationLogs(c *gin.Context) {
	id := c.Param("id")
	if h.opsStore == nil {
		httputil.RespondWithOK(c, gin.H{"items": []OpLogV2Response{}})
		return
	}

	// ?tail= is accepted as an alias for ?limit=: the retired route spelled it
	// that way and diagnostics callers may still send it.
	limit := 50
	for _, key := range []string{"limit", "tail"} {
		if raw := c.Query(key); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				limit = min(n, 5000)
				break
			}
		}
	}

	logs, err := h.opsStore.GetOpLogsV2(id, limit)
	if err != nil {
		httputil.InternalError(c, "failed to fetch operation logs", err)
		return
	}
	resp := make([]OpLogV2Response, 0, len(logs))
	for _, l := range logs {
		resp = append(resp, logRowToResponse(l))
	}
	// Both keys: the retired route answered `items`, the v2 single-op route
	// answers `logs`, and callers of this provider read one or the other.
	httputil.RespondWithOK(c, gin.H{"items": resp, "logs": resp})
}

// CancelOperationV2 implements DELETE /api/v1/operations/v2/:id.
// Cancels the operation via the registry (if running) or marks it canceled (if queued).
func (h *OperationsV2Handler) CancelOperationV2(c *gin.Context) {
	id := c.Param("id")

	// An AI scan is driven by the pipeline manager, not the ops registry, and
	// asking the registry to cancel it does nothing. This branch is ported from
	// the legacy DELETE /operations/:id, which was the only route that knew:
	// retiring that route without carrying this over would have left the cancel
	// button returning 204 while the scan carried on.
	//
	// The scan is found by matching the operation id against each scan's
	// OperationID, which is the only link between the two records.
	if h.scanCanceler != nil && h.scanLister != nil {
		scans, err := h.scanLister.ListScans()
		if err != nil {
			slog.Warn("cancel: could not list AI scans; falling through to the registry",
				"op_id", id, "error", err)
		}
		for _, scan := range scans {
			if scan.OperationID != id {
				continue
			}
			// A cancel error is logged, not returned: the scan has been asked to
			// stop and the caller's request is answered either way. This matches
			// the legacy behaviour exactly.
			if cerr := h.scanCanceler.CancelScan(scan.ID); cerr != nil {
				slog.Info("cancel: AI scan cancel warning", "scan", scan.ID, "error", cerr)
			}
			httputil.RespondWithNoContent(c)
			return
		}
	}

	if h.registry == nil {
		httputil.RespondWithInternalError(c, "operations registry not initialized")
		return
	}
	if err := h.registry.Cancel(id); err != nil {
		if errors.Is(err, opsregistry.ErrOpNotFound) {
			httputil.RespondWithNotFound(c, "operation", id)
			return
		}
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

	// Enforce the def's declared Permissions.
	//
	// The route-level guard on POST /operations/v2 is a single blanket
	// scan.trigger for EVERY op (wire_operations_routes.go), so without this the
	// per-def Permissions field is written to op_definitions_v2 and never read —
	// it reads like a gate and behaves like a comment. The seeded editor role
	// holds scan.trigger but not settings.manage, so the 37 maintenance ops were
	// reachable by a role the v1 maintenance route rejects.
	//
	// This lives in the handler and NOT in registry.EnqueueOp on purpose:
	// EnqueueOp has ~20 non-HTTP callers (internal/scheduler/tasks.go alone
	// enqueues 15 op types from context.Background(), plus internal/importer and
	// the dedup/maintenance plugins). Those carry no user and no permission set,
	// so a check down there would fail closed on every scheduled run.
	//
	// Semantics are AND: every permission the def declares must be held. All defs
	// carry exactly one today, so this is untestable by behaviour — it is stated
	// here so the first two-permission def is not a coin flip.
	//
	// An unknown def_id deliberately skips the check and falls through to
	// EnqueueOp, preserving today's error response. Nothing runs either way.
	if h.enforcePerms {
		if def, ok := h.registry.Def(body.DefID); ok {
			for _, p := range def.Permissions {
				if !auth.Can(c.Request.Context(), p) {
					httputil.RespondWithForbidden(c, "permission denied: "+string(p))
					return
				}
			}
		}
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
