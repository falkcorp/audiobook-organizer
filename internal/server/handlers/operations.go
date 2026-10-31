// file: internal/server/handlers/operations.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1234-efab-234567890123
// last-edited: 2026-10-30

package handlers

import "time"

// MaintenanceWindowConfigReq is the JSON body for the maintenance window config endpoint.
type MaintenanceWindowConfigReq struct {
	Enabled     bool `json:"enabled"`
	WindowStart int  `json:"window_start"`
	WindowEnd   int  `json:"window_end"`
}

// OperationV2Response is the JSON shape returned by the timeline and single-op
// endpoints. It mirrors the TypeScript OperationV2 interface in api.ts.
type OperationV2Response struct {
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
	// CanFinish is true on an interrupted_ask row whose def can take the third
	// decision, "Finish without resuming" (POST .../finish). The Activity page
	// shows the button from it.
	CanFinish bool `json:"can_finish,omitempty"`
}

// OpLogV2Response is the JSON shape for a single operation log line.
type OpLogV2Response struct {
	OperationID string    `json:"operation_id"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Attrs       any       `json:"attrs"`
	CreatedAt   time.Time `json:"created_at"`
}

// OpDefResponse is the JSON shape returned by /op-defs.
type OpDefResponse struct {
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
