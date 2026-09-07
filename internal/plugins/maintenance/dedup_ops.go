// file: internal/plugins/maintenance/dedup_ops.go
// version: 1.3.0
// guid: e1f2a3b4-c5d6-7890-4567-012345678901
// last-edited: 2026-09-07

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
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
		ID:              "maintenance.ai-dedup-batch",
		Liveness:        sdk.LivenessManual,
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

	store := p.deps.OpsStore()
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

	prog := sdk.NewProgress(reporter, maxPolls)
	prog.Start(fmt.Sprintf("Submitting %d authors to OpenAI Batch API...", len(inputs)))

	batchID, err := parser.CreateBatchAuthorDedup(ctx, inputs)
	if err != nil {
		return fmt.Errorf("failed to create batch: %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Batch created: %s — polling for completion", batchID))

	for i := range maxPolls {
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
			// Write onto this run's own v2 row via the reporter. The previous
			// store.UpdateOperationResultData(opID, ...) resolved a v1
			// `operation:` row that no longer exists for any live op, so it
			// returned "operation not found" and failed the batch right after
			// the results had been downloaded. See reconcile.go for the full
			// account.
			if err := opsregistry.ReporterSetResult(reporter, resultPayload); err != nil {
				return fmt.Errorf("failed to store results: %w", err)
			}
			prog.Done(fmt.Sprintf("Batch complete: %d suggestions", len(discoveries)))
			return nil

		case "failed", "expired", "cancelled":
			return fmt.Errorf("batch %s: %s", batchID, status)
		}
	}
	return fmt.Errorf("batch timed out after 24h")
}
