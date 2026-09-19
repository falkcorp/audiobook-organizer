// file: internal/plugins/maintenance/dedup_ops.go
// version: 1.5.0
// guid: e1f2a3b4-c5d6-7890-4567-012345678901
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/oklog/ulid/v2"
)

// --- dedup-llm-review ---

func (p *Plugin) dedupLLMReviewDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.dedup-llm-review",
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 60 * time.Minute, // LivenessNone requires an explicit budget
		Plugin:          "maintenance",
		DisplayName:     "Dedup LLM review",
		Description:     "Runs LLM review of ambiguous author-dedup candidates.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.dedup-llm-review",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapNetworkOpenAI},
		Run:             p.runDedupLLMReview,
	}
}

func (p *Plugin) runDedupLLMReview(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if !p.deps.HasDedupEngine() {
		_ = reporter.Log(slog.LevelInfo, "Dedup engine not initialized, skipping LLM review")
		return nil
	}
	_ = reporter.Log(slog.LevelInfo, "Starting LLM review of ambiguous dedup candidates")
	return p.deps.DedupLLMReview(ctx)
}

// --- ai-dedup-batch ---

func (p *Plugin) aiDedupBatchDef() sdk.OperationDef {
	sched := "0 0 * * *" // midnight daily
	return sdk.OperationDef{
		ID:          "maintenance.ai-dedup-batch",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "AI author dedup batch",
		Description: "Submits authors to the OpenAI Batch API for dedup review (50% cheaper, up to 24h turnaround). Results land in the AI author review queue.",
		// ResumeRestart with the job id checkpointed: a resumed run sees it and
		// submits nothing. Until 2026-09-19 this was ResumeDrop and the op
		// polled its own batch for up to 24h with the id only in memory, so a
		// restart abandoned a paid batch (its results were never collected)
		// and the next midnight run paid for another.
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.ai-dedup-batch",
		Cancellable:     true,
		Isolate:         false,
		// Submission only; collection and apply belong to the batch poller.
		Timeout:      30 * time.Minute,
		Schedule:     &sched,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapNetworkOpenAI},
		Run:          p.runAIDedupBatch,
	}
}

// aiDedupBatchParams is the op's params and its checkpoint: the registry
// merges the checkpoint back into params when it resumes the op.
type aiDedupBatchParams struct {
	JobID string `json:"job_id,omitempty"`
}

// pendingJobStaleAfter is how long a "pending" author dedup job (row written,
// batch id not recorded) keeps blocking a new submission.
//
// Writing one off is irreversible: aijobs.ReconcileOrphans deliberately never
// attaches a batch to a "failed" row. A batch is listed from the moment it is
// created, so the reconciler attaches it on the first poll the server makes
// after the kill — the only way a job stays pending long is the batch never
// having been created, or the poller not running at all (server down). 72h is
// three whole 24h batch windows: a server down over a weekend still gets its
// orphan attached (the reconciler pages back to the oldest pending job) before
// the write-off can fire, and the cost of the margin is at most two skipped
// nightly runs behind a job that truly has no batch.
const pendingJobStaleAfter = 72 * time.Hour

type authorDedupSubmitFunc func(ctx context.Context, sourceID string, inputs []ai.AuthorDiscoveryInput) (jobID string, err error)

// submitAuthorDedupOnce submits the whole-library author dedup batch unless one
// is already in flight, and checkpoints the job id. It returns the job id that
// owns the work and whether this call submitted it.
//
// "In flight" is read from the durable ai_jobs rows, not from memory: a job
// submitted, or whose apply is being retried, means the batch poller will
// still collect and apply it, so paying for another batch would only produce a
// second copy of the same suggestions.
func submitAuthorDedupOnce(ctx context.Context, jobs database.AIJobsStore, params aiDedupBatchParams,
	buildInputs func() ([]ai.AuthorDiscoveryInput, error), submit authorDedupSubmitFunc,
	checkpoint func(any) error, now time.Time,
) (string, bool, error) {
	if params.JobID != "" {
		return params.JobID, false, nil
	}
	for _, status := range []string{"submitted", "apply_failed", "pending"} {
		inflight, err := jobs.ListAIJobs(ai.AuthorDedupJobType, status, 0, 0)
		if err != nil {
			return "", false, fmt.Errorf("list %s author dedup jobs: %w", status, err)
		}
		for _, j := range inflight {
			if status != "pending" || now.Sub(j.CreatedAt) < pendingJobStaleAfter {
				return j.ID, false, nil
			}
			if err := jobs.MarkAIJobFailed(j.ID, fmt.Sprintf("no batch recorded %s after submit; written off", pendingJobStaleAfter)); err != nil {
				return "", false, fmt.Errorf("write off stale job %s: %w", j.ID, err)
			}
		}
	}

	inputs, err := buildInputs()
	if err != nil {
		return "", false, err
	}
	if len(inputs) == 0 {
		return "", false, nil
	}
	jobID, err := submit(ctx, ulid.Make().String(), inputs)
	if err != nil {
		return jobID, false, fmt.Errorf("submit author dedup batch: %w", err)
	}
	// A failed checkpoint is not fatal: the ai_jobs row already gates a
	// duplicate submission; the checkpoint only saves a resumed run the lookup.
	_ = checkpoint(aiDedupBatchParams{JobID: jobID})
	return jobID, true, nil
}

func (p *Plugin) runAIDedupBatch(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	if !p.deps.HasAIParsing() {
		return fmt.Errorf("AI parsing is not enabled")
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	jobs := database.GetAIJobs(store)
	if jobs == nil {
		return fmt.Errorf("store %T has no ai_jobs capability; the batch could not be collected after a restart", store)
	}
	parser := ai.NewOpenAIParser(&config.AppConfig, config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
	if !parser.IsEnabled() {
		return fmt.Errorf("AI parsing is not enabled")
	}

	var params aiDedupBatchParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("decode params: %w", err)
		}
	}

	buildInputs := func() ([]ai.AuthorDiscoveryInput, error) {
		_ = reporter.Log(slog.LevelInfo, "Building author list for batch AI dedup")
		allAuthors, err := store.GetAllAuthors()
		if err != nil {
			return nil, fmt.Errorf("failed to get authors: %w", err)
		}
		inputs := make([]ai.AuthorDiscoveryInput, 0, len(allAuthors))
		for _, author := range allAuthors {
			var sampleTitles []string
			books, bErr := store.GetBooksByAuthorIDWithRoleCore(author.ID)
			if bErr == nil {
				for j, b := range books {
					if j >= 3 {
						break
					}
					sampleTitles = append(sampleTitles, b.Title)
				}
			}
			inputs = append(inputs, ai.AuthorDiscoveryInput{
				ID: author.ID, Name: author.Name,
				BookCount: len(books), SampleTitles: sampleTitles,
			})
		}
		return inputs, nil
	}
	submit := func(ctx context.Context, sourceID string, inputs []ai.AuthorDiscoveryInput) (string, error) {
		return parser.SubmitAuthorDedupJob(ctx, jobs, sourceID, inputs)
	}

	jobID, submitted, err := submitAuthorDedupOnce(ctx, jobs, params, buildInputs, submit, reporter.Checkpoint, time.Now())
	if err != nil {
		return err
	}
	switch {
	case jobID == "":
		_ = reporter.Log(slog.LevelInfo, "No authors to process")
	case submitted:
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Submitted author dedup job %s; the batch poller applies its results to the AI review queue when OpenAI finishes", jobID))
	default:
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Author dedup job %s is already in flight; not submitting another batch", jobID))
	}
	return opsregistry.ReporterSetResult(reporter, map[string]any{"job_id": jobID, "submitted": submitted})
}
