// file: internal/plugins/maintenance/dedup_ops.go
// version: 1.8.3
// guid: e1f2a3b4-c5d6-7890-4567-012345678901
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/taskmanage"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
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

// t6g4AIJobLister is the one read submitAuthorDedupOnce makes of the ai_jobs
// table: the listing that says whether a batch is already in flight.
type t6g4AIJobLister interface {
	ListAIJobs(typeFilter, statusFilter string, limit, offset int) ([]database.AIJob, error)
}

// submitAuthorDedupOnce submits the whole-library author dedup batch unless one
// is already in flight, and checkpoints the job id. It returns the job id that
// owns the work and whether this call submitted it.
//
// "In flight" is read from the durable ai_jobs rows, not from memory: a job
// submitted, or whose apply is being retried, means the batch poller will
// still collect and apply it, so paying for another batch would only produce a
// second copy of the same suggestions.
//
// It reads the in-flight listing through the lister it is handed, so the core
// can give it env.Store, and writes only through the submit it is handed (rule
// R10 item 1).
func submitAuthorDedupOnce(ctx context.Context, jobs t6g4AIJobLister, params aiDedupBatchParams,
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
	cfg := config.Snapshot()
	parser := ai.NewOpenAIParser(&cfg, cfg.OpenAIAPIKey, cfg.EnableAIParsing)
	if !parser.IsEnabled() {
		return fmt.Errorf("AI parsing is not enabled")
	}
	return p.runAIDedupBatchHEAD(ctx, raw, reporter, store, jobs, parser)
}

// t6g4AIBatchSubmitter is the one call HEAD's body made on the OpenAI parser: the
// submission of the author dedup batch. It is an interface so a test can drive
// HEAD's shell with a submitter that pays for nothing.
type t6g4AIBatchSubmitter interface {
	SubmitAuthorDedupJob(ctx context.Context, jobs database.AIJobsStore, sourceID string, inputs []ai.AuthorDiscoveryInput) (string, error)
}

// runAIDedupBatchHEAD is runAIDedupBatch after it resolved the store, the
// ai_jobs capability and the parser: it decodes HEAD's params and runs the core
// with HEAD's checkpoint. HEAD checkpointed the submitted job id through
// reporter.Checkpoint (submitAuthorDedupOnce's checkpoint argument), and the
// registry merged that checkpoint back into the params of a resumed run, so the
// env's SaveCheckpoint is the reporter's.
func (p *Plugin) runAIDedupBatchHEAD(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter,
	store OpsStore, jobs database.AIJobsStore, submitter t6g4AIBatchSubmitter,
) error {
	var params aiDedupBatchParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("decode params: %w", err)
		}
	}
	base := t6g4HeadBase(reporter, true,
		headT6G4Store{OpsStore: store, deps: p.deps, aiJobs: jobs},
		headT6G4Writer{store: store, aiParser: submitter, aiJobs: jobs})
	base.SaveCheckpoint = reporter.Checkpoint
	res, err := p.runAIDedupBatchCore(ctx, params, aiDedupBatchEnv{t6g4Base: base})
	if err != nil {
		return err
	}
	return opsregistry.ReporterSetResult(reporter, res)
}

// aiDedupBatchNativeParams is the native run's params: the job id HEAD's params
// carried (a resumed run's checkpoint is merged into it) and the three mode
// keys, accepted only to be refused. aiDedupBatchParams stays HEAD's params and
// checkpoint shape, so the checkpoint bytes are HEAD's.
type aiDedupBatchNativeParams struct {
	JobID string `json:"job_id,omitempty"`
	t6g4ModeKeys
}

// aiDedupBatchKind is the native shell (rule R2 item 3). HEAD has no preview for
// this definition (it submits a paid batch), so a native run refuses any mode
// but live. The checkpoint is HEAD's own struct (aiDedupBatchParams), restored
// into the params the way HEAD's registry merges it on a resume.
func (p *Plugin) aiDedupBatchKind(deps t6g4Deps, sched *tasks.Schedule) tasks.Definition {
	return tasks.Task(aiDedupBatchKind, tasks.TaskSpec[aiDedupBatchNativeParams, aiDedupBatchResult]{
		Title: "AI author dedup batch",
		Help:  "Submits authors to the OpenAI Batch API for dedup review (50% cheaper, up to 24h turnaround). Results land in the AI author review queue.",
		// The batch submission writes one ai_jobs row (SubmitAuthorDedupJob).
		// pkg/ops has no ai_jobs constant and "operations" is the undo
		// journal only, so the nearest valid name is review_items: the AI
		// author review queue the batch's results land in. Listed under
		// "Review decisions" in the PR.
		Effects:        tasks.Writes(t6g4Res(ops.ResReviewItems)...),
		Perms:          tasks.Permissions{Start: t6g4StartRequirement(), Manage: taskmanage.Requirement()},
		Resume:         tasks.ResumeContinue,
		Priority:       tasks.PriorityLow,
		ConcurrencyKey: tasks.Key("books.maintenance.ai-dedup-batch"),
		Timeout:        30 * time.Minute,
		Schedule:       sched,
		Ready:          []tasks.ReadySignal{t6g4SignalMemDB},
		Run: func(rc *tasks.RC, in aiDedupBatchNativeParams) (aiDedupBatchResult, error) {
			if err := t6g4RefuseModeKeys(rc.Kind(), in.t6g4ModeKeys); err != nil {
				return aiDedupBatchResult{}, err
			}
			if err := t6g4RequireLive(rc); err != nil {
				return aiDedupBatchResult{}, err
			}
			base, err := t6g4NativeBase(rc, deps, true)
			if err != nil {
				return aiDedupBatchResult{}, err
			}
			params := aiDedupBatchParams{JobID: in.JobID}
			var cp aiDedupBatchParams
			if ok, rerr := rc.Restore(&cp); rerr != nil {
				return aiDedupBatchResult{}, fmt.Errorf("%s: restore checkpoint: %w", rc.Kind(), rerr)
			} else if ok && params.JobID == "" {
				params.JobID = cp.JobID
			}
			return p.runAIDedupBatchCore(rc.Context(), params, aiDedupBatchEnv{t6g4Base: base})
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

	jobID, submitted, err := submitAuthorDedupOnce(ctx, store, params, buildInputs, submit, rep.Checkpoint)
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
