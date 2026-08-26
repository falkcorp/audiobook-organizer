// file: internal/database/ai_jobs_types.go
// version: 1.0.0
// guid: eb57745a-4c4f-4545-be8f-8d78b1e318f3
// last-edited: 2026-06-10

package database

import "time"

// AIJob is one tracked bulk LLM job submitted through the aijobs package.
// Previously the CRUD methods lived in ai_jobs_store.go (SQLite); they were
// removed in fable5 TASK-022. The type and the AIJobsStore interface are kept
// here so existing PebbleStore stubs and MockStore implementations continue
// to compile without changes.
type AIJob struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	BatchID        string    `json:"batch_id,omitempty"`
	CustomIDPrefix string    `json:"custom_id_prefix"`
	Status         string    `json:"status"` // pending|submitted|completed|completed_with_errors|failed|expired
	ItemCount      int       `json:"item_count"`
	SuccessCount   int       `json:"success_count"`
	ErrorCount     int       `json:"error_count"`
	RowErrors      string    `json:"row_errors,omitempty"` // JSON-encoded []AIJobRowError
	ErrorMsg       string    `json:"error_msg,omitempty"`
	SubmittedAt    time.Time `json:"submitted_at"`
	CompletedAt    time.Time `json:"completed_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// AIJobRowError is one failed row within an otherwise successful batch.
type AIJobRowError struct {
	CustomID string `json:"custom_id"`
	Error    string `json:"error"`
}
