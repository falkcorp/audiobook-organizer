// file: internal/plugins/maintenance/dedup_ops.go
// version: 1.8.0
// guid: e1f2a3b4-c5d6-7890-4567-012345678901
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/oklog/ulid/v2"
)

// aiDedupBatchKind is rule R3: the native kind of maintenance.ai-dedup-batch.
const aiDedupBatchKind tasks.Kind = "books.maintenance.ai-dedup-batch"

// maintenance.dedup-llm-review was removed 2026-09-25 (naming audit class 8).
// It called the same dedup engine RunLLMReview as dedup.llm-review, under a
// description ("author-dedup candidates") that was wrong, without
// CapLibraryWrite, and with its own ConcurrencyKey -- so the two could review
// the same candidates concurrently. Its ID is now a FormerID of
// dedup.llm-review (internal/plugins/dedup/llm_review.go), so anything that
// still sends it runs the one correctly-declared review op.

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
	checkpoint func(any) error,
) (string, bool, error) {
	return submitAuthorDedupOnceVia(ctx, jobs, params, buildInputs, submit, checkpoint)
}

// aiJobLister is the one read submitAuthorDedupOnceVia makes of the ai_jobs
// table: the listing that says whether a batch is already in flight.
type aiJobLister interface {
	ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]database.AIJob, error)
}

// submitAuthorDedupOnceVia is submitAuthorDedupOnce's body (rule R10 item 1): it
// reads the in-flight listing through the lister it is handed, so the core can
// give it env.Store, and writes only through the submit it is handed.
func submitAuthorDedupOnceVia(ctx context.Context, jobs aiJobLister, params aiDedupBatchParams,
	buildInputs func() ([]ai.AuthorDiscoveryInput, error), submit authorDedupSubmitFunc,
	checkpoint func(any) error,
) (string, bool, error) {
	if params.JobID != "" {
		return params.JobID, false, nil
	}
	for _, status := range []string{"submitted", "apply_failed", "pending"} {
		inflight, err := jobs.ListAIJobs(ai.AuthorDedupJobType, status, 0, 0)
		if err != nil {
			return "", false, fmt.Errorf("list %s author dedup jobs: %w", status, err)
		}
		// A pending job counts as in flight however old it is. This op never
		// writes one off itself: that is irreversible (a failed row is never
		// re-attached), so it is left to aijobs, which attaches the job's batch
		// when a listing shows it (by the ai_job_id metadata) and writes the
		// job off only when a COMPLETE, non-truncated listing confirms no batch
		// exists (aijobs.WriteOffUnlinked, run by the batch poller).
		if len(inflight) > 0 {
			return inflight[0].ID, false, nil
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

// aiDedupBatchResult is HEAD's result map (job_id, submitted) as a struct with
// the same JSON keys.
type aiDedupBatchResult struct {
	JobID     string `json:"job_id"`
	Submitted bool   `json:"submitted"`
}

// aiDedupBatchEnv is what the core needs from its engine (rules R2 and R10).
type aiDedupBatchEnv struct {
	t6g4Base
}

// runAIDedupBatch is the v2 shell around runAIDedupBatchCore (rule R2 item 2).
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
	res, err := p.runAIDedupBatchCore(ctx, params, aiDedupBatchEnv{
		t6g4Base: t6g4HeadBase(reporter, true,
			headT6G4Store{OpsStore: store, deps: p.deps, aiJobs: jobs},
			headT6G4Writer{store: store, aiParser: parser, aiJobs: jobs}),
	})
	if err != nil {
		return err
	}
	return opsregistry.ReporterSetResult(reporter, res)
}

// aiDedupBatchKind is the native shell (rule R2 item 3). HEAD has no preview for
// this definition (it submits a paid batch), so a native run refuses any mode
// but live. The checkpoint is HEAD's own struct (aiDedupBatchParams), restored
// into the params the way HEAD's registry merges it on a resume.
func (p *Plugin) aiDedupBatchKind(deps t6g4Deps, sched *tasks.Schedule) tasks.Definition {
	return tasks.Task(aiDedupBatchKind, tasks.TaskSpec[aiDedupBatchParams, aiDedupBatchResult]{
		Common: tasks.Common{
			Title:          "AI author dedup batch",
			Help:           "Submits authors to the OpenAI Batch API for dedup review (50% cheaper, up to 24h turnaround). Results land in the AI author review queue.",
			Effects:        tasks.Writes(t6g4Res("operations")...),
			Perms:          tasks.Permissions{Start: t6g4StartRequirement()},
			Resume:         tasks.ResumeContinue,
			Priority:       tasks.PriorityLow,
			ConcurrencyKey: tasks.Key("books.maintenance.ai-dedup-batch"),
			Timeout:        30 * time.Minute,
			Schedule:       sched,
			Ready:          []tasks.ReadySignal{t6g4SignalMemDB},
		},
		Run: func(rc *tasks.RC, in aiDedupBatchParams) (aiDedupBatchResult, error) {
			if err := t6g4RequireLive(rc); err != nil {
				return aiDedupBatchResult{}, err
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return aiDedupBatchResult{}, err
			}
			var cp aiDedupBatchParams
			if ok, rerr := rc.Restore(&cp); rerr != nil {
				return aiDedupBatchResult{}, fmt.Errorf("%s: restore checkpoint: %w", rc.Kind(), rerr)
			} else if ok && in.JobID == "" {
				in.JobID = cp.JobID
			}
			return p.runAIDedupBatchCore(rc.Context(), in, aiDedupBatchEnv{t6g4Base: base})
		},
	})
}

// runAIDedupBatchCore is HEAD's body (rule R2 item 1) after the shell resolved the
// store and decoded params: it reads authors and the in-flight listing through
// env.Store and submits the batch only through env.Writer.
func (p *Plugin) runAIDedupBatchCore(ctx context.Context, params aiDedupBatchParams, env aiDedupBatchEnv) (aiDedupBatchResult, error) {
	rep := env.reporter(ctx)
	store := env.Store
	if !store.HasAIParsing() {
		return aiDedupBatchResult{}, fmt.Errorf("AI parsing is not enabled")
	}

	buildInputs := func() ([]ai.AuthorDiscoveryInput, error) {
		_ = rep.Log(slog.LevelInfo, "Building author list for batch AI dedup")
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
		return env.Writer.SubmitAuthorDedupJob(ctx, sourceID, inputs)
	}

	jobID, submitted, err := submitAuthorDedupOnceVia(ctx, store, params, buildInputs, submit, rep.Checkpoint)
	if err != nil {
		return aiDedupBatchResult{}, err
	}
	switch {
	case jobID == "":
		_ = rep.Log(slog.LevelInfo, "No authors to process")
	case submitted:
		_ = rep.Log(slog.LevelInfo, fmt.Sprintf("Submitted author dedup job %s; the batch poller applies its results to the AI review queue when OpenAI finishes", jobID))
	default:
		_ = rep.Log(slog.LevelInfo, fmt.Sprintf("Author dedup job %s is already in flight; not submitting another batch", jobID))
	}
	return aiDedupBatchResult{JobID: jobID, Submitted: submitted}, nil
}
