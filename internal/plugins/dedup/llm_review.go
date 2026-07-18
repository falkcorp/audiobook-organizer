// file: internal/plugins/dedup/llm_review.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-07-18

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

func (p *Plugin) llmReviewDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "dedup.llm-review",
		Plugin:          "dedup",
		DisplayName:     "LLM review of candidates",
		Description:     "Runs LLM review pass over ambiguous embedding-layer candidates.",
		ResumePolicy:    sdk.ResumeDrop,
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

func (p *Plugin) runLLMReview(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if p.engine == nil {
		return fmt.Errorf("dedup engine not available")
	}

	_ = reporter.UpdateProgress(0, 1, "Starting LLM review of ambiguous candidates...")

	// H9 (2026-07 error-correction sweep): the total pair count isn't known
	// until RunLLMReview lists ambiguous candidates internally, so the
	// sdk.Progress bar is constructed lazily on the first callback instead
	// of up front with a placeholder n=0 (which made StepN a no-op). Without
	// this, the op reported nothing between "starting" and "job submitted"
	// while building up to 10K pair inputs — indistinguishable from a hang.
	var prog *sdk.Progress
	onProgress := func(current, total int, message string) {
		if prog == nil {
			prog = sdk.NewProgress(reporter, total)
			prog.Start(message)
		}
		prog.StepN(current, message)
	}

	if err := p.engine.RunLLMReview(ctx, onProgress); err != nil {
		reporter.Logger().Error("LLM review error", "error", err)
		return fmt.Errorf("LLM review: %w", err)
	}
	if prog != nil {
		prog.Finalize("writing results...")
		prog.Done("LLM review complete")
	} else {
		_ = reporter.UpdateProgress(1, 1, "LLM review complete (no ambiguous candidates)")
	}
	return nil
}
