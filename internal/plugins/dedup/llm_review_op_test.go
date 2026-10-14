// file: internal/plugins/dedup/llm_review_op_test.go
// version: 1.0.0
// guid: 3e8a1c64-7d02-4b95-a1f6-5c9d2e07b831
// last-edited: 2026-10-14

package dedup

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// 04-P14b merged scheduler.dedup-llm-review into this op. The retired copy had
// no tests, so these cover the survivor's success and failure paths.

// With no ambiguous candidates (and no parser) the review has nothing to do:
// it succeeds and says so on the reporter. Mutation check: delete the final
// UpdateProgress in the prog == nil branch of runLLMReview and this fails.
func TestLLMReviewOp_SucceedsWhenNothingToReview(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	p := newCalibratePlugin(t, pebble, es)
	rep := &runAllReporter{}

	if err := p.llmReviewDef().Run(context.Background(), nil, rep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.progress) == 0 {
		t.Fatal("the op reported no progress")
	}
	last := rep.progress[len(rep.progress)-1]
	if !strings.Contains(last.msg, "LLM review complete") || last.cur != 1 || last.total != 1 {
		t.Errorf("final progress = %+v, want the 1/1 completion message", last)
	}
}

// The op fails, rather than skipping, when the dedup engine is missing.
// Mutation check: delete the p.engine == nil guard and this panics.
func TestLLMReviewOp_FailsWhenEngineMissing(t *testing.T) {
	p := &Plugin{}
	err := p.llmReviewDef().Run(context.Background(), nil, &runAllReporter{})
	if err == nil || !strings.Contains(err.Error(), "dedup engine not available") {
		t.Fatalf("err = %v, want \"dedup engine not available\"", err)
	}
}

// An engine failure surfaces as the op's error, wrapped. An enabled parser with
// no embed store makes RunLLMReview fail before any network call.
// Mutation check: return nil instead of the wrapped error in runLLMReview.
func TestLLMReviewOp_FailsWhenEngineFails(t *testing.T) {
	parser := ai.NewOpenAIParser(nil, "synthetic-key", true)
	eng, err := dedupengine.NewEngine(nil, nil, nil, parser, nil, unified.DefaultScoreConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	p := &Plugin{engine: eng}

	err = p.llmReviewDef().Run(context.Background(), nil, &runAllReporter{})
	if err == nil || !strings.Contains(err.Error(), "LLM review:") || !strings.Contains(err.Error(), "embedStore") {
		t.Fatalf("err = %v, want a wrapped \"LLM review: ... embedStore\" error", err)
	}
}

// Both retired IDs resolve to this def, and it keeps the cancel contract the
// scheduler copy had. Mutation check: drop either FormerIDs entry or the
// Cancellable line.
func TestLLMReviewDef_KeepsAliasesAndCancel(t *testing.T) {
	def := (&Plugin{}).llmReviewDef()
	for _, old := range []string{"maintenance.dedup-llm-review", "scheduler.dedup-llm-review"} {
		if !slices.Contains(def.FormerIDs, old) {
			t.Errorf("FormerIDs %v is missing %q", def.FormerIDs, old)
		}
	}
	if !def.Cancellable {
		t.Error("dedup.llm-review must stay cancellable, as the retired scheduler def was")
	}
}
