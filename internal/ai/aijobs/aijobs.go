// file: internal/ai/aijobs/aijobs.go
// version: 1.1.2
// guid: 8231e2ae-fa34-4594-80fd-f0f9dc60bc3b
// last-edited: 2026-07-03

package aijobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
)

// BatchClient is the subset of internal/ai.OpenAIParser methods aijobs uses.
// Defined as an interface so tests can inject fakes without depending on the real client.
type BatchClient interface {
	UploadBatchFile(ctx context.Context, data []byte) (string, error)
	CreateBatchWithMetadata(ctx context.Context, fileID, batchType string) (string, error)
}

// Deps is the runtime dependency set for Submit.
type Deps struct {
	Store  database.AIJobsStore
	Client BatchClient
}

// BatchRequest is one row that will be serialized as JSONL for OpenAI's Batch API.
// Body is the raw /v1/chat/completions request body (messages, model, response_format, etc.).
type BatchRequest struct {
	Body      map[string]any
	MaxTokens int64 // informational only; callers put max_completion_tokens inside Body if needed
}

// SubmitRequest is the caller-facing payload for a new aijobs batch.
type SubmitRequest struct {
	Type        string // feature type, e.g. "dedup_review"
	ItemCount   int    // len(items) — mirrored on the ai_jobs row for visibility
	PayloadJSON []byte // serialized items slice — redelivered to the callback at Dispatch time
	Build       func(i int) (BatchRequest, error)
}

// RowResult is one parsed line from an OpenAI batch output file.
// Content is the raw model output (already extracted from choices[0].message.content).
// Err is non-empty if OpenAI reported an error for this row.
type RowResult struct {
	CustomID string
	Content  string
	Err      string
}

// CompletionCallback applies a batch's results. It must:
//  1. Deserialize itemsJSON into its feature-specific item slice
//  2. Match each result to an item by CustomID (the convention: "<prefix>-<index>")
//  3. Apply the result (DB write, etc.), catching per-row errors into the returned slice
//  4. Return (successCount, errorCount, rowErrors, fatalErr). A non-nil fatalErr means the
//     whole batch could not be processed and the job row is marked failed.
type CompletionCallback func(ctx context.Context, itemsJSON []byte, results []RowResult) (successCount, errorCount int, rowErrors []database.AIJobRowError, fatalErr error)

var (
	registry   = map[string]CompletionCallback{}
	registryMu sync.RWMutex
)

// Register associates a type string with its completion callback. Call at package-init
// time from each feature's package (e.g. init() in internal/ai/dedup_review.go).
func Register(typ string, cb CompletionCallback) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[typ] = cb
}

// ClearRegistryForTest resets the registry — test-only helper.
func ClearRegistryForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]CompletionCallback{}
}

// Submit persists a new ai_jobs row, uploads a JSONL batch file, and creates an
// OpenAI batch. Returns the job ID. On upload/create failure the job row is
// marked "failed" and the original error is returned.
func Submit(ctx context.Context, deps Deps, req SubmitRequest) (string, error) {
	if req.ItemCount == 0 {
		return "", fmt.Errorf("aijobs.Submit: no items")
	}
	if req.Build == nil {
		return "", fmt.Errorf("aijobs.Submit: nil Build")
	}

	jobID := ulid.Make().String()
	customIDPrefix := jobID

	// 1. Build the JSONL body up front — if Build returns an error, we haven't
	//    touched the store yet.
	var buf bytes.Buffer
	for i := 0; i < req.ItemCount; i++ {
		br, err := req.Build(i)
		if err != nil {
			return "", fmt.Errorf("aijobs.Submit: build row %d: %w", i, err)
		}
		line := map[string]any{
			"custom_id": fmt.Sprintf("%s-%d", customIDPrefix, i),
			"method":    "POST",
			"url":       "/v1/chat/completions",
			"body":      br.Body,
		}
		b, err := json.Marshal(line)
		if err != nil {
			return "", fmt.Errorf("aijobs.Submit: marshal row %d: %w", i, err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}

	// 2. Insert the pending job row with its payload.
	job := database.AIJob{
		ID:             jobID,
		Type:           req.Type,
		CustomIDPrefix: customIDPrefix,
		Status:         "pending",
		ItemCount:      req.ItemCount,
		CreatedAt:      time.Now(),
	}
	if err := deps.Store.CreateAIJob(job, req.PayloadJSON); err != nil {
		return "", fmt.Errorf("aijobs.Submit: store.CreateAIJob: %w", err)
	}

	// 3. Upload + create batch. On any failure, mark the row failed and return.
	fileID, err := deps.Client.UploadBatchFile(ctx, buf.Bytes())
	if err != nil {
		_ = deps.Store.MarkAIJobFailed(jobID, fmt.Sprintf("upload: %v", err))
		return jobID, err
	}
	batchID, err := deps.Client.CreateBatchWithMetadata(ctx, fileID, "aijobs")
	if err != nil {
		_ = deps.Store.MarkAIJobFailed(jobID, fmt.Sprintf("create batch: %v", err))
		return jobID, err
	}

	if err := deps.Store.MarkAIJobSubmitted(jobID, batchID); err != nil {
		return jobID, fmt.Errorf("aijobs.Submit: mark submitted: %w", err)
	}
	slog.Info("aijobs submitted job type batch items", "jobID", jobID, "reqType", req.Type, "batchID", batchID, "itemCount", req.ItemCount)
	return jobID, nil
}

// Dispatch is called by the BatchPoller when an aijobs batch completes.
// It looks up the ai_jobs row, loads the payload, invokes the registered callback,
// and records the outcome.
func Dispatch(ctx context.Context, store database.AIJobsStore, batchID string, results []RowResult) (err error) {
	job, err := store.GetAIJobByBatchID(batchID)
	if err != nil {
		return fmt.Errorf("aijobs.Dispatch: lookup batch %s: %w", batchID, err)
	}

	registryMu.RLock()
	cb, ok := registry[job.Type]
	registryMu.RUnlock()
	if !ok {
		msg := fmt.Sprintf("no callback registered for type %q", job.Type)
		_ = store.MarkAIJobFailed(job.ID, msg)
		return fmt.Errorf("aijobs.Dispatch: %s", msg)
	}

	payload, err := store.GetAIJobPayload(job.ID)
	if err != nil {
		_ = store.MarkAIJobFailed(job.ID, fmt.Sprintf("load payload: %v", err))
		return fmt.Errorf("aijobs.Dispatch: load payload: %w", err)
	}

	// Recover from panics in the callback so one bad feature cannot crash the poller.
	var successCount, errorCount int
	var rowErrors []database.AIJobRowError
	var fatalErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				fatalErr = fmt.Errorf("callback panic: %v", r)
			}
		}()
		successCount, errorCount, rowErrors, fatalErr = cb(ctx, payload, results)
	}()

	if fatalErr != nil {
		_ = store.MarkAIJobFailed(job.ID, fatalErr.Error())
		return fatalErr
	}

	status := "completed"
	if errorCount > 0 {
		status = "completed_with_errors"
	}
	if err := store.MarkAIJobCompleted(job.ID, status, successCount, errorCount, rowErrors); err != nil {
		return fmt.Errorf("aijobs.Dispatch: mark completed: %w", err)
	}
	slog.Info("aijobs dispatched job type success errors", "jobID", job.ID, "jobType", job.Type, "successCount", successCount, "errorCount", errorCount)
	return nil
}
