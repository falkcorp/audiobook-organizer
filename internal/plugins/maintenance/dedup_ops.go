// file: internal/plugins/maintenance/dedup_ops.go
// version: 1.1.0
// guid: e1f2a3b4-c5d6-7890-4567-012345678901
// last-edited: 2026-07-05

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- dedup-llm-review ---

func (p *Plugin) dedupLLMReviewDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.dedup-llm-review",
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
		ID:              "maintenance.ai-dedup-batch",
		Plugin:          "maintenance",
		DisplayName:     "AI author dedup batch",
		Description:     "Submits authors to the OpenAI Batch API for dedup review (50% cheaper, up to 24h turnaround).",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.ai-dedup-batch",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         25 * time.Hour, // 24h batch + buffer
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapNetworkOpenAI},
		Run:             p.runAIDedupBatch,
	}
}

func (p *Plugin) runAIDedupBatch(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if !p.deps.HasAIParsing() {
		return fmt.Errorf("AI parsing is not enabled")
	}

	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	parser := ai.NewOpenAIParser(&config.AppConfig, config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
	if !parser.IsEnabled() {
		return fmt.Errorf("AI parsing is not enabled")
	}

	_ = reporter.Log(slog.LevelInfo, "Building author list for batch AI dedup")
	loadProg := sdk.NewProgress(reporter, 0)
	loadProg.Start("Loading authors...")

	allAuthors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("failed to get authors: %w", err)
	}

	var inputs []ai.AuthorDiscoveryInput
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

	if len(inputs) == 0 {
		_ = reporter.Log(slog.LevelInfo, "No authors to process")
		loadProg.Done("No authors to process")
		return nil
	}

	// Poll for completion (up to 24h, check every 5 min)
	pollInterval := 5 * time.Minute
	maxPolls := 288 // 24h / 5min
	opID := ctxOpID(ctx)

	prog := sdk.NewProgress(reporter, maxPolls)
	prog.Start(fmt.Sprintf("Submitting %d authors to OpenAI Batch API...", len(inputs)))

	batchID, err := parser.CreateBatchAuthorDedup(ctx, inputs)
	if err != nil {
		return fmt.Errorf("failed to create batch: %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Batch created: %s — polling for completion", batchID))

	for i := 0; i < maxPolls; i++ {
		if reporter.IsCanceled() {
			return fmt.Errorf("cancelled")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}

		status, outputFileID, sErr := parser.CheckBatchStatus(ctx, batchID)
		if sErr != nil {
			_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Poll error: %v", sErr))
			continue
		}

		prog.StepN(i+1, fmt.Sprintf("Batch status: %s (poll %d/%d)", status, i+1, maxPolls))

		switch status {
		case "completed":
			_ = reporter.Log(slog.LevelInfo, "Batch completed, downloading results")
			discoveries, dErr := parser.DownloadBatchResults(ctx, outputFileID)
			if dErr != nil {
				return fmt.Errorf("failed to download results: %w", dErr)
			}
			resultPayload := map[string]any{
				"mode":        "batch-full",
				"suggestions": discoveries,
				"batch_id":    batchID,
			}
			resultJSON, jErr := json.Marshal(resultPayload)
			if jErr != nil {
				return fmt.Errorf("failed to marshal results: %w", jErr)
			}
			if opID != "" {
				if err := store.UpdateOperationResultData(opID, string(resultJSON)); err != nil {
					return fmt.Errorf("failed to store results: %w", err)
				}
			}
			prog.Done(fmt.Sprintf("Batch complete: %d suggestions", len(discoveries)))
			return nil

		case "failed", "expired", "cancelled":
			return fmt.Errorf("batch %s: %s", batchID, status)
		}
	}
	return fmt.Errorf("batch timed out after 24h")
}
