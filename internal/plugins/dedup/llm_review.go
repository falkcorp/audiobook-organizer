// file: internal/plugins/dedup/llm_review.go
// version: 1.7.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) llmReviewDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID: "dedup.llm-review",
		// maintenance.dedup-llm-review was a second registration of this same
		// engine review in the maintenance namespace, removed 2026-09-25
		// (naming audit class 8). scheduler.dedup-llm-review was a third, the
		// scheduler task's copy, retired 2026-10-14 (04-P14b, D54); both IDs
		// resolve here. Soak: todo.d/2026-10-14-p14b-alias-soak.md.
		FormerIDs:    []string{"maintenance.dedup-llm-review", "scheduler.dedup-llm-review"},
		Liveness:     sdk.LivenessManual,
		Plugin:       "dedup",
		DisplayName:  "LLM review of candidates",
		Description:  llmReviewHelp,
		ResumePolicy: sdk.ResumeDrop,
		// The retired scheduler def was cancellable; this one declared no value,
		// which the registry reads as "reject Cancel" (04-P14b). RunLLMReviewWithHooks
		// checks ctx between listing pages, between the two listings, at the top
		// of every build-loop iteration and before the batch is submitted. ctx
		// also reaches the submit's HTTP calls: a cancel during the submit aborts
		// the call in flight. During the file upload the aijob is marked failed;
		// during the batch create it stays pending (the batch may already exist
		// at OpenAI) until ReconcileOrphans / WriteOffUnlinked settles it. Only
		// the aijob row write ignores ctx.
		Cancellable: true,
		// Same gate the retired scheduler def declared. Before 04-P14b this def
		// declared none and fell back to settings.manage, so this line is the
		// explicit form of the effective gate. Through the generic operations
		// endpoint it changes nobody's access; POST /dedup/scan-llm is gated by
		// scan.trigger and enqueues this op directly, and that gate is unchanged.
		Permissions: []auth.Permission{auth.PermSettingsManage},
		// Serialize against itself like every other write-declaring dedup op.
		// Without a key the scheduler can start a second review while the
		// first is mid-flight, both holding CapLibraryWrite over the same
		// candidates. runLLMReview has no internal partitioning that would
		// make two concurrent runs disjoint.
		ConcurrencyKey:  "dedup.llm-review",
		DefaultPriority: sdk.PriorityLow,
		Timeout:         120 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapNetworkOpenAI,
		},
		Run: p.runLLMReview,
	}
}

// llmReviewKind is rule R3's native kind for dedup.llm-review. HEAD's FormerIDs
// (maintenance.dedup-llm-review, scheduler.dedup-llm-review) resolve to it
// through the seams file, not through this const.
const llmReviewKind = tasks.Kind("books.dedup.llm-review")

// llmReviewHelp is HEAD's Description and the native Help (rule R5).
const llmReviewHelp = "Runs LLM review pass over ambiguous embedding-layer candidates."

// llmReviewEngine is the engine surface the core reaches: the configured check
// and the review pass (which lists candidates, builds the batch and submits it).
type llmReviewEngine interface {
	LLMReviewConfigured() bool
	RunLLMReviewWithHooks(ctx context.Context, hooks dedupengine.LLMReviewHooks) error
}

// llmReviewEnv is what the core takes from its shell. HEAD has no dry run, so
// there is no Live: the native kind is live-only.
type llmReviewEnv struct {
	kindEnv
	Engine llmReviewEngine
}

// llmReviewResult is the native run's result. HEAD stored none.
type llmReviewResult struct {
	// Skipped is true when the LLM parser was not configured.
	Skipped bool `json:"skipped,omitempty"`
	// Submitted is true when the review pass reported progress (it built a
	// batch); false when it found no ambiguous candidates.
	Submitted bool `json:"submitted"`
}

// llmReviewCore is the body of runLLMReview, shared by both shells.
func llmReviewCore(ctx context.Context, env llmReviewEnv) (llmReviewResult, error) {
	if !env.Engine.LLMReviewConfigured() {
		env.Progress(1, 1, "LLM review skipped: LLM parser not configured")
		return llmReviewResult{Skipped: true}, nil
	}

	env.Progress(0, 1, "Starting LLM review of ambiguous candidates...")

	// H9 (2026-07 error-correction sweep): the total pair count isn't known
	// until RunLLMReviewWithHooks lists ambiguous candidates internally, so the
	// sdk.Progress bar is constructed lazily on the first callback instead
	// of up front with a placeholder n=0 (which made StepN a no-op). Without
	// this, the op reported nothing between "starting" and "job submitted" --
	// indistinguishable from a hang.
	//
	// Only the build loop calls onProgress, and always with the real total. The
	// listing pages and the pre-submit window have no total, so they go through
	// Heartbeat, which stamps the liveness clock and never touches the bar: the
	// watchdog budget is the 5-minute default, not the retired 2h one, and a
	// (0, 0) phase frame through onProgress would pin the bar at n=0.
	var prog *sdk.Progress
	hooks := dedupengine.LLMReviewHooks{
		Progress: func(current, total int, message string) {
			if prog == nil {
				prog = newProgress(env.kindEnv, total)
				prog.Start(message)
			}
			prog.StepN(current, message)
		},
		Heartbeat: func(string) { registry.TouchLiveness(env.Reporter) },
	}

	if err := env.Engine.RunLLMReviewWithHooks(ctx, hooks); err != nil {
		if errors.Is(err, context.Canceled) {
			env.Logger.Info("LLM review canceled", "error", err)
		} else {
			env.Logger.Error("LLM review error", "error", err)
		}
		return llmReviewResult{}, fmt.Errorf("LLM review: %w", err)
	}
	if prog != nil {
		prog.Finalize("writing results...")
		prog.Done("LLM review complete")
	} else {
		env.Progress(1, 1, "LLM review complete (no ambiguous candidates)")
	}
	return llmReviewResult{Submitted: prog != nil}, nil
}

// runLLMReview is the v2 shell: HEAD's Run (the engine nil check) over
// llmReviewCore.
func (p *Plugin) runLLMReview(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}
	_, err := llmReviewCore(ctx, llmReviewEnv{kindEnv: headEnv(reporter, true), Engine: p.engine})
	return err
}
