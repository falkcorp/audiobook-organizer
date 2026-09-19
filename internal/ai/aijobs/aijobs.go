// file: internal/ai/aijobs/aijobs.go
// version: 1.2.0
// guid: 8231e2ae-fa34-4594-80fd-f0f9dc60bc3b
// last-edited: 2026-09-19

package aijobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// MaxApplyAttempts is how many failed applies a job gets before it is marked
// terminally "failed". Failures of our own apply (store busy, shutdown cancel,
// a recovered callback panic) are usually transient, so one failure must not
// throw away a paid batch's results; five spread over the backoff below is
// more than an hour of retrying before giving up.
const MaxApplyAttempts = 5

// applyBackoffBase is the wait after the first failed apply; it doubles per
// further failure (5m, 10m, 20m, 40m) and is capped at applyBackoffMax.
const (
	applyBackoffBase = 5 * time.Minute
	applyBackoffMax  = time.Hour
)

// now is the clock Dispatch reads; tests replace it.
var now = time.Now

// ErrApplyBackoff is returned by Dispatch for a job whose last apply failed
// too recently to retry. It is not a failure: the poller leaves the batch
// unhandled and offers it again on a later tick.
var ErrApplyBackoff = errors.New("aijobs: apply retry backing off")

// terminalJobStatuses are the ai_jobs statuses Dispatch must never apply again.
// Listed explicitly rather than as "anything but submitted": a job row read back
// with an empty status is not evidence it was already applied.
var terminalJobStatuses = map[string]bool{
	"completed":             true,
	"completed_with_errors": true,
	"failed":                true,
	"expired":               true,
}

// IsTerminalJob reports whether a job has been dispatched to an outcome and
// must not be applied again.
//
// "failed" is terminal only once the apply budget is spent. A failed row with
// ApplyAttempts < MaxApplyAttempts is either a submit-time failure (never has
// a batch id, so Dispatch never sees it) or a row an earlier build marked
// failed on its first apply error — that build retried such rows every tick,
// so they still get the bounded retry budget rather than being dropped.
func IsTerminalJob(job database.AIJob) bool {
	if job.Status == "failed" {
		return job.ApplyAttempts >= MaxApplyAttempts
	}
	return terminalJobStatuses[job.Status]
}

// applyBackoff is the wait after the attempts-th failed apply.
func applyBackoff(attempts int) time.Duration {
	d := applyBackoffBase
	for i := 1; i < attempts && d < applyBackoffMax; i++ {
		d *= 2
	}
	if d > applyBackoffMax {
		d = applyBackoffMax
	}
	return d
}

// recordApplyFailure books one failed apply. Below the budget the job is left
// "apply_failed" for a later retry; at the budget it becomes terminal "failed"
// and is logged at error level (it also shows as failed, with the message, in
// the ai jobs list). Always returns an error so the poller does not record the
// batch handled on this tick; once terminal, the next Dispatch returns nil.
func recordApplyFailure(store database.AIJobsStore, job database.AIJob, batchID string, cause error) error {
	updated, err := store.MarkAIJobApplyFailed(job.ID, cause.Error())
	if err != nil {
		return fmt.Errorf("aijobs.Dispatch: %w (and recording the failed apply failed: %v)", cause, err)
	}
	if updated.ApplyAttempts >= MaxApplyAttempts {
		msg := fmt.Sprintf("apply failed %d times, giving up: %v", updated.ApplyAttempts, cause)
		if ferr := store.MarkAIJobFailed(job.ID, msg); ferr != nil {
			return fmt.Errorf("aijobs.Dispatch: %w (and marking the job failed failed: %v)", cause, ferr)
		}
		log.Error("job %s (type %s, batch %s) %s — its results were NOT applied", job.ID, job.Type, batchID, msg)
		return fmt.Errorf("aijobs.Dispatch: job %s: %s", job.ID, msg)
	}
	log.Warn("job %s (type %s, batch %s) apply attempt %d/%d failed, will retry after %s: %v",
		job.ID, job.Type, batchID, updated.ApplyAttempts, MaxApplyAttempts, applyBackoff(updated.ApplyAttempts), cause)
	return fmt.Errorf("aijobs.Dispatch: %w", cause)
}

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
// A failed apply (no callback registered, payload unreadable, callback fatal
// error or panic) is recorded with MarkAIJobApplyFailed and retried on later
// polls with exponential backoff; after MaxApplyAttempts the job becomes
// terminal "failed". Retrying may replay a callback that applied part of the
// batch before failing, which the replay-safety contract covers.
func Dispatch(ctx context.Context, store database.AIJobsStore, batchID string, results []RowResult) (err error) {
	job, err := store.GetAIJobByBatchID(batchID)
	if err != nil {
		return fmt.Errorf("aijobs.Dispatch: lookup batch %s: %w", batchID, err)
	}
	if IsTerminalJob(job) {
		log.Info("batch %s: job %s already %s, not applying again", batchID, job.ID, job.Status)
		return nil
	}
	if job.ApplyAttempts > 0 && !job.LastApplyAt.IsZero() {
		if wait := job.LastApplyAt.Add(applyBackoff(job.ApplyAttempts)).Sub(now()); wait > 0 {
			return fmt.Errorf("%w: job %s, attempt %d, %s left", ErrApplyBackoff, job.ID, job.ApplyAttempts+1, wait.Round(time.Second))
		}
	}

	registryMu.RLock()
	cb, ok := registry[job.Type]
	registryMu.RUnlock()
	if !ok {
		return recordApplyFailure(store, job, batchID, fmt.Errorf("no callback registered for type %q", job.Type))
	}

	payload, err := store.GetAIJobPayload(job.ID)
	if err != nil {
		return recordApplyFailure(store, job, batchID, fmt.Errorf("load payload: %w", err))
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
		return recordApplyFailure(store, job, batchID, fatalErr)
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
	Status   string // OpenAI batch status: completed, failed, expired, cancelled, ...
	Metadata map[string]string
}

// openAIFailedStatuses are batch statuses where OpenAI, not our apply, ended
// the batch. There are no results to retry, so the job is marked terminal.
var openAIFailedStatuses = map[string]bool{"failed": true, "expired": true, "cancelled": true}

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
// It also closes out jobs whose batch OpenAI itself failed, expired or
// cancelled: those never reach Dispatch (the poller dispatches completed batches
// only), so without this the job would sit "submitted" forever. That is marked
// terminal "failed" immediately, unlike an apply failure, which is retried.
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
		if job.BatchID == b.ID && openAIFailedStatuses[b.Status] && job.Status == "submitted" {
			msg := fmt.Sprintf("openai batch %s: %s", b.ID, b.Status)
			if err := store.MarkAIJobFailed(jobID, msg); err != nil {
				log.Error("mark job %s failed after %s: %v", jobID, msg, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			log.Error("job %s: %s — no results to apply", jobID, msg)
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
