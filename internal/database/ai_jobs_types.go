// file: internal/database/ai_jobs_types.go
// version: 1.1.0
// guid: eb57745a-4c4f-4545-be8f-8d78b1e318f3
// last-edited: 2026-09-19

package database

import "time"

// AIJob is one tracked bulk LLM job submitted through the aijobs package.
// Previously the CRUD methods lived in ai_jobs_store.go (SQLite); they were
// removed in fable5 TASK-022. The type and the AIJobsStore interface are kept
// here so existing PebbleStore stubs and MockStore implementations continue
// to compile without changes.
type AIJob struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	BatchID        string `json:"batch_id,omitempty"`
	CustomIDPrefix string `json:"custom_id_prefix"`
	// Status is one of:
	//   pending               row written, batch id not yet recorded
	//   submitted             batch created, results not yet applied
	//   apply_failed          applying the results failed; retried with backoff
	//   completed             every row applied
	//   completed_with_errors applied; some rows carried per-item errors, recorded
	//                         in RowErrors. Terminal: never re-applied.
	//   failed                terminal: submit failed, OpenAI failed/expired the
	//                         batch, or the apply failed MaxApplyAttempts times
	//   expired               terminal
	Status       string    `json:"status"`
	ItemCount    int       `json:"item_count"`
	SuccessCount int       `json:"success_count"`
	ErrorCount   int       `json:"error_count"`
	RowErrors    string    `json:"row_errors,omitempty"` // JSON-encoded []AIJobRowError
	ErrorMsg     string    `json:"error_msg,omitempty"`
	SubmittedAt  time.Time `json:"submitted_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	// ApplyAttempts counts failed attempts to apply this job's results.
	ApplyAttempts int `json:"apply_attempts,omitempty"`
	// LastApplyError / LastApplyAt describe the most recent failed apply.
	LastApplyError string    `json:"last_apply_error,omitempty"`
	LastApplyAt    time.Time `json:"last_apply_at,omitempty"`
}

// AIJobRowError is one failed row within an otherwise successful batch.
type AIJobRowError struct {
	CustomID string `json:"custom_id"`
	Error    string `json:"error"`
}
