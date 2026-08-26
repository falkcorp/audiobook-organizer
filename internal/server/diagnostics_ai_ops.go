// file: internal/server/diagnostics_ai_ops.go
// version: 1.0.0
// guid: 5c8f2a91-3d6e-4b07-9e14-8a2f7c0d3b56
// last-edited: 2026-08-23

// diagnostics_ai_ops registers the diagnostics.ai-analyze OperationDef, which
// replaces the bare goroutine in handlers.SubmitAI that used to mint a legacy
// operation row and drive its status by hand.

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// Batch polling cadence, matching maintenance/dedup_ops.go: OpenAI batches are
// allowed up to 24h, so a shorter budget would abandon a run that is still
// healthy.
const (
	diagAIPollInterval = 5 * time.Minute
	diagAIMaxPolls     = 288 // 24h / 5min
)

// RegisterDiagnosticsAIOp registers the "diagnostics.ai-analyze" v2 OperationDef.
//
// The run polls its own batch to completion rather than handing that job to
// BatchPoller. That is not a style choice: BatchPoller's completion path found
// its operation by listing the 100 most recent legacy rows and matching batch_id
// inside result_data, so an op belonging to a batch that took long enough to
// scroll out of that window had its results dropped with only an info log. The
// two sibling batch producers (maintenance/dedup_ops.go and aiscan/pipeline.go)
// already poll their own batches; this makes all three consistent and lets the
// scan be deleted.
func (s *Server) RegisterDiagnosticsAIOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              handlers.DiagnosticsAIDefID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "diagnostics",
		DisplayName:     "AI Diagnostics Analysis",
		Description:     "Submits a diagnostics export to the OpenAI batch API and collects the analysis.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		// A batch may legitimately run for 24h, so the op has to outlive it.
		Timeout: 25 * time.Hour,
		// Set explicitly for the reason recorded on diagnostics.export: the
		// watchdog's default is 5 minutes, which silently overrides a longer
		// Timeout. The poll loop below reports once per interval, so three
		// consecutive missed polls is the real "this run is wedged" signal.
		ProgressTimeout: 3 * diagAIPollInterval,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  handlers.DiagnosticsAIDefID,
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapNetworkOpenAI},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p handlers.DiagnosticsAIOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("diagnostics.ai-analyze: decode params: %w", err)
				}
			}
			if p.Category == "" {
				p.Category = "general"
			}

			// Fail loudly instead of the old nil-parser branch, which marked the
			// operation completed with a "pending-<opID>" batch id and no
			// results — a run that reported success having submitted nothing.
			if config.AppConfig.OpenAIAPIKey == "" || !config.AppConfig.EnableAIParsing {
				return fmt.Errorf("diagnostics.ai-analyze: OpenAI batch submission is not enabled")
			}
			parser := ai.NewOpenAIParser(&config.AppConfig, config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)

			store := s.storeForWiring()
			ds := s.diagnosticsService
			if ds == nil {
				ds = diagnostics.NewService(store, nil, config.AppConfig.ITunes.LibraryReadPath)
			}

			_ = reporter.UpdateProgress(10, 100, "Generating export data")
			allBooks, err := ds.CollectAllBooks()
			if err != nil {
				return fmt.Errorf("collect books: %w", err)
			}
			slimBooks := make([]diagnostics.SlimBook, len(allBooks))
			for i, b := range allBooks {
				slimBooks[i] = diagnostics.ToSlimBook(b)
			}

			_ = reporter.UpdateProgress(50, 100, "Building batch JSONL")
			jsonlData, err := diagnostics.BuildBatchJSONL(p.Category, p.Description, slimBooks, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("build batch JSONL: %w", err)
			}
			requestCount := countJSONLRequests(jsonlData)

			_ = reporter.UpdateProgress(70, 100, "Submitting to OpenAI batch API")
			fileID, err := parser.UploadBatchFile(ctx, bytes.NewBuffer(jsonlData))
			if err != nil {
				return fmt.Errorf("upload batch file: %w", err)
			}
			batchID, err := parser.CreateBatchWithMetadata(ctx, fileID, "diagnostics")
			if err != nil {
				return fmt.Errorf("create batch: %w", err)
			}
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Batch %s submitted with %d request(s) — polling for completion", batchID, requestCount))
			_ = reporter.UpdateProgress(80, 100, fmt.Sprintf("Batch %s submitted, awaiting completion", batchID))

			for i := range diagAIMaxPolls {
				if reporter.IsCanceled() {
					// Best-effort: the batch keeps costing money on OpenAI's side
					// if it is left running after the op is gone.
					if cErr := parser.CancelBatch(ctx, batchID); cErr != nil {
						_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Could not cancel batch %s: %v", batchID, cErr))
					}
					return fmt.Errorf("cancelled")
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(diagAIPollInterval):
				}

				status, outputFileID, sErr := parser.CheckBatchStatus(ctx, batchID)
				if sErr != nil {
					_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("Poll error: %v", sErr))
					continue
				}

				// Creep 80 -> 99 across the poll budget so a long batch still
				// shows movement; the watchdog needs a report each interval
				// regardless of whether the status changed.
				_ = reporter.UpdateProgress(80+(i*19/diagAIMaxPolls), 100,
					fmt.Sprintf("Batch %s: %s (poll %d/%d)", batchID, status, i+1, diagAIMaxPolls))

				switch status {
				case "completed":
					rawResults, dErr := parser.DownloadBatchRaw(ctx, outputFileID)
					if dErr != nil {
						return fmt.Errorf("download diagnostics results: %w", dErr)
					}
					// The payload shape GetAIResults and ApplySuggestions read
					// back. It lands on this run's own v2 row; the result route
					// became two-keyspace aware in #2771, which is what makes it
					// readable at all.
					if err := opsregistry.ReporterSetResult(reporter, map[string]any{
						"batch_id":      batchID,
						"request_count": requestCount,
						"raw_responses": rawResults,
					}); err != nil {
						return fmt.Errorf("persist diagnostics results: %w", err)
					}
					_ = reporter.UpdateProgress(100, 100, fmt.Sprintf("Batch complete: %d response(s)", len(rawResults)))
					return nil

				case "failed", "expired", "cancelled":
					return fmt.Errorf("batch %s: %s", batchID, status)
				}
			}
			return fmt.Errorf("batch %s timed out after 24h", batchID)
		},
	})
}

// countJSONLRequests counts the request lines in a JSONL buffer, tolerating a
// missing trailing newline.
func countJSONLRequests(jsonlData []byte) int {
	if len(jsonlData) == 0 {
		return 0
	}
	count := 0
	for _, b := range jsonlData {
		if b == '\n' {
			count++
		}
	}
	if jsonlData[len(jsonlData)-1] != '\n' {
		count++
	}
	return count
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterDiagnosticsAIOp(reg) })
}
