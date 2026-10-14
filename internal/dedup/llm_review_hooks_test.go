// file: internal/dedup/llm_review_hooks_test.go
// version: 1.0.0
// guid: 8c4e2a17-5b93-4d06-9e1f-a3d7c0b64f52
// last-edited: 2026-10-14

package dedup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// llmReviewEngine returns an engine wired for RunLLMReview with no network: an
// enabled parser (never called), a store that knows every book, and NO aijobs
// store, so a run that reaches the submit fails with "aiJobsStore not
// configured" instead of calling out. n book candidates are seeded with
// distinct, in-window similarities.
func llmReviewEngine(t *testing.T, n int) *Engine {
	t.Helper()
	engine, mock, es := setupTestEngine(t)
	engine.llmParser = ai.NewOpenAIParser(nil, "synthetic-key", true)
	engine.LLMMaxPairsPerRun = 250
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: id, Title: "Title " + id}, nil
	}
	for i := 0; i < n; i++ {
		// 0.81 .. 0.91, all inside [LLMBookLow, LLMBookHigh].
		sim := 0.81 + 0.10*float64(i)/float64(n)
		seedCandidate(t, es, "book", fmt.Sprintf("A%04d", i), fmt.Sprintf("B%04d", i), sim)
	}
	return engine
}

// A run over more than one listing page and more than 100 pairs reports every
// phase: a heartbeat per listing page, progress every 100 pairs, and a
// heartbeat right before the submit.
//
// Mutation checks: drop the heartbeat in listAmbiguousCandidates (no listing
// frames), drop the "submitting" heartbeat, or drop the Progress call in the
// build loop; each fails an assertion here.
func TestRunLLMReview_ReportsListingBuildAndSubmitFrames(t *testing.T) {
	engine := llmReviewEngine(t, ambiguousListPageSize+20)

	var beats []string
	var progress [][2]int
	err := engine.RunLLMReviewWithHooks(context.Background(), LLMReviewHooks{
		Progress:  func(cur, total int, _ string) { progress = append(progress, [2]int{cur, total}) },
		Heartbeat: func(msg string) { beats = append(beats, msg) },
	})
	if err == nil || !strings.Contains(err.Error(), "aiJobsStore not configured") {
		t.Fatalf("err = %v, want the run to reach the submit and stop at the missing aijobs store", err)
	}

	var listing int
	for _, b := range beats {
		if strings.HasPrefix(b, "listing ambiguous book candidates") {
			listing++
		}
	}
	if listing != 2 {
		t.Errorf("listing heartbeats = %d (%q), want one per page (2)", listing, beats)
	}
	if n := len(beats); n == 0 || !strings.HasPrefix(beats[n-1], "submitting LLM review batch") {
		t.Errorf("last heartbeat = %q, want the pre-submit frame", beats)
	}
	want := [][2]int{{100, 250}, {200, 250}, {250, 250}}
	if fmt.Sprint(progress) != fmt.Sprint(want) {
		t.Errorf("progress frames = %v, want %v", progress, want)
	}
}

// A pre-canceled context stops the run with context.Canceled, not with the
// missing-aijobs-store error it would otherwise reach.
func TestRunLLMReview_PreCanceledContext(t *testing.T) {
	engine := llmReviewEngine(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := engine.RunLLMReview(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A cancel that lands after the listings, in the build loop or in the window
// before the submit, is honored there. Mutation checks: delete the ctx check at
// the top of the build loop (first case), the one after the pre-submit
// heartbeat (second case), or the one at the top of the listing's page loop
// (third case).
func TestRunLLMReview_CancelMidRunStopsBeforeSubmit(t *testing.T) {
	t.Run("during the build loop", func(t *testing.T) {
		engine := llmReviewEngine(t, 150)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var built int
		err := engine.RunLLMReviewWithHooks(ctx, LLMReviewHooks{
			Progress: func(cur, _ int, _ string) {
				built = cur
				cancel() // the 100-pair frame: the next iteration must stop.
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if built != 100 {
			t.Errorf("built = %d, want the run to stop right after the first 100-pair frame", built)
		}
	})
	t.Run("right before the submit", func(t *testing.T) {
		engine := llmReviewEngine(t, 5)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := engine.RunLLMReviewWithHooks(ctx, LLMReviewHooks{
			Heartbeat: func(msg string) {
				if strings.HasPrefix(msg, "submitting") {
					cancel()
				}
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled, not the aijobs-store error", err)
		}
	})
	t.Run("between listing pages", func(t *testing.T) {
		engine := llmReviewEngine(t, ambiguousListPageSize+5)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var beats []string
		err := engine.RunLLMReviewWithHooks(ctx, LLMReviewHooks{
			Heartbeat: func(msg string) {
				beats = append(beats, msg)
				cancel() // after the first of two book pages.
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if len(beats) != 1 {
			t.Errorf("heartbeats = %q, want the run to stop before the second page", beats)
		}
	})
}

// The paged listing keeps the order and cap of the one-shot listing it
// replaced: most similar first across page boundaries.
func TestListAmbiguousCandidates_PagedKeepsSimilarityOrder(t *testing.T) {
	engine := llmReviewEngine(t, 2*ambiguousListPageSize+7)

	got, err := engine.listAmbiguousCandidates(context.Background(), "book", 0.80, 0.92, func(string) {})
	if err != nil {
		t.Fatalf("listAmbiguousCandidates: %v", err)
	}
	if len(got) != 2*ambiguousListPageSize+7 {
		t.Fatalf("got %d candidates, want all %d", len(got), 2*ambiguousListPageSize+7)
	}
	for i := 1; i < len(got); i++ {
		if candidateSimilarity(got[i-1]) < candidateSimilarity(got[i]) {
			t.Fatalf("candidate %d (%.4f) is more similar than %d (%.4f): not sorted most-similar first",
				i, candidateSimilarity(got[i]), i-1, candidateSimilarity(got[i-1]))
		}
	}
}
