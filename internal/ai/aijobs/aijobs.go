// file: internal/ai/aijobs/aijobs.go
// version: 1.2.0
// guid: 8231e2ae-fa34-4594-80fd-f0f9dc60bc3b
// last-edited: 2026-09-19

package aijobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/oklog/ulid/v2"
)

var log = logger.New("aijobs")

// MetadataJobIDKey is the batch metadata key carrying the ai_jobs row id. It is
// written at CreateBatch time so ReconcileOrphans can re-attach a batch whose
// id never reached the job row.
const MetadataJobIDKey = "ai_job_id"

// BatchClient is the subset of internal/ai.OpenAIParser methods aijobs uses.
// Defined as an interface so tests can inject fakes without depending on the real client.
type BatchClient interface {
	UploadBatchFile(ctx context.Context, data []byte) (string, error)
	// CreateBatchWithMetadata creates the batch tagged with batchType plus the
	// extra metadata keys (Submit passes MetadataJobIDKey).
	CreateBatchWithMetadata(ctx context.Context, fileID, batchType string, extra map[string]string) (string, error)
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
//
// A callback must be idempotent. The job row is marked completed only after the
// callback returns, so a process killed between the callback's writes and that
// mark leaves the job "submitted", and the next Dispatch runs the callback again
// on the same results. Dispatch never runs it for a job already completed or
// failed, so that interrupted-apply replay is the only repeat a callback sees.
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
	// The job id rides in the batch metadata: if the process dies after OpenAI
	// accepts the batch but before MarkAIJobSubmitted lands, the row is left
	// pending with no batch id and ReconcileOrphans re-attaches it from a
	// batch listing. Without it the paid batch's results are never applied.
	batchID, err := deps.Client.CreateBatchWithMetadata(ctx, fileID, "aijobs", map[string]string{MetadataJobIDKey: jobID})
	if err != nil {
		_ = deps.Store.MarkAIJobFailed(jobID, fmt.Sprintf("create batch: %v", err))
		return jobID, err
	}

	if err := deps.Store.MarkAIJobSubmitted(jobID, batchID); err != nil {
		return jobID, fmt.Errorf("aijobs.Submit: mark submitted: %w", err)
	}
	log.Info("submitted job %s type %s batch %s (%d items)", jobID, req.Type, batchID, req.ItemCount)
	return jobID, nil
}

// terminalJobStatuses are the ai_jobs statuses Dispatch must never apply again.
// Listed explicitly rather than as "anything but submitted": a job row read back
// with an empty status is not evidence it was already applied.
var terminalJobStatuses = map[string]bool{
	"completed":             true,
	"completed_with_errors": true,
	"failed":                true,
	"expired":               true,
}

// IsTerminalStatus reports whether a job in this status has been dispatched to
// an outcome and must not be applied again.
func IsTerminalStatus(status string) bool { return terminalJobStatuses[status] }

// Dispatch is called by the BatchPoller when an aijobs batch completes.
// It looks up the ai_jobs row, loads the payload, invokes the registered callback,
// and records the outcome.
//
// A job already in a terminal status is a no-op that returns nil. The poller's
// durable "handled" mark is written after Dispatch returns, so a restart in that
// gap re-delivers the batch; the job row, marked first, is what stops the
// results being applied a second time. Returning nil (not an error) lets the
// poller record the batch as handled instead of retrying it every tick.
//
// "failed" is terminal too: a fatal callback error is recorded on the row and
// surfaced in the jobs list rather than retried blind every poll, since the
// callback may have applied part of the batch before failing.
func Dispatch(ctx context.Context, store database.AIJobsStore, batchID string, results []RowResult) (err error) {
	job, err := store.GetAIJobByBatchID(batchID)
	if err != nil {
		return fmt.Errorf("aijobs.Dispatch: lookup batch %s: %w", batchID, err)
	}
	if IsTerminalStatus(job.Status) {
		log.Info("batch %s: job %s already %s, not applying again", batchID, job.ID, job.Status)
		return nil
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
	log.Info("dispatched job %s type %s: %d succeeded, %d errors", job.ID, job.Type, successCount, errorCount)
	return nil
}

// OrphanBatch is one listed OpenAI batch as ReconcileOrphans needs it.
type OrphanBatch struct {
	ID       string
	Metadata map[string]string
}

// ReconcileOrphans re-attaches batches whose id never reached their job row.
//
// Submit creates the job row "pending", creates the OpenAI batch, then records
// the batch id with MarkAIJobSubmitted. A process killed between the last two
// steps leaves a pending row with no batch id while OpenAI runs (and bills) the
// batch; Dispatch looks jobs up by batch id, so the results would never be
// applied. Each batch carries its job id in metadata (MetadataJobIDKey), so for
// every listed batch naming a job that is still pending without a batch id,
// the id is attached here and the normal Dispatch path collects the results.
//
// Only pending rows are touched. A "failed" row with no batch id is left alone:
// Submit's caller saw that failure and may already have resubmitted the same
// items, and attaching the old batch would apply them twice.
//
// Returns the number of jobs attached. Per-batch errors are logged and skipped
// so one bad row cannot block the rest; the returned error is the first one.
func ReconcileOrphans(store database.AIJobsStore, batches []OrphanBatch) (int, error) {
	attached := 0
	var firstErr error
	for _, b := range batches {
		jobID := b.Metadata[MetadataJobIDKey]
		if jobID == "" || b.ID == "" {
			continue
		}
		job, err := store.GetAIJob(jobID)
		if err != nil || job.ID == "" {
			// A batch naming a job this store never had (another install
			// sharing the OpenAI project, or a deleted row) is not ours.
			continue
		}
		if job.BatchID != "" {
			if job.BatchID != b.ID {
				log.Warn("batch %s names job %s, which is already attached to batch %s; leaving it", b.ID, jobID, job.BatchID)
			}
			continue
		}
		if job.Status != "pending" {
			continue
		}
		if err := store.MarkAIJobSubmitted(jobID, b.ID); err != nil {
			log.Error("re-attach orphaned batch %s to job %s: %v", b.ID, jobID, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		attached++
		log.Warn("re-attached orphaned batch %s to job %s (its batch id was never recorded)", b.ID, jobID)
	}
	return attached, firstErr
}
