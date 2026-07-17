// file: cmd/dedup_bench.go
// version: 1.3.2
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-17

//go:build bench

package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/spf13/cobra"
)

var dedupBenchCmd = &cobra.Command{
	Use:   "dedup-bench",
	Short: "Run AI author dedup comparison across models and prompts",
	Long: `Runs the same author data through multiple GPT models, prompt variations,
and parameter tweaks. Saves all raw request/response data for analysis.

Use --batch to submit as async OpenAI batch jobs (50% cheaper, results in ~24h).
Use "dedup-bench check" to retrieve completed batch results.

Requires OPENAI_API_KEY env var.`,
	RunE: runDedupBench,
}

var dedupBenchCheckCmd = &cobra.Command{
	Use:   "check [run-dir]",
	Short: "Check status and download results from batch jobs",
	Long: `Checks the status of previously submitted batch jobs and downloads
any completed results. Pass the run directory path from the submit output.`,
	Args: cobra.ExactArgs(1),
	RunE: runDedupBenchCheck,
}

var (
	benchOutputDir string
	benchModels    []string
	benchMode      string
	benchDryRun    bool
	benchServerURL string
	benchChunkSize int
	benchBatch     bool
)

func init() {
	rootCmd.AddCommand(dedupBenchCmd)
	dedupBenchCmd.AddCommand(dedupBenchCheckCmd)
	dedupBenchCmd.AddCommand(dedupBenchStatusCmd)
	dedupBenchCmd.AddCommand(dedupBenchPass2Cmd)
	dedupBenchCmd.AddCommand(dedupBenchCrossvalCmd)

	dedupBenchCmd.Flags().StringVar(&benchOutputDir, "output", "testdata/dedup-bench", "Output directory for results")
	dedupBenchCmd.Flags().StringSliceVar(&benchModels, "models", []string{
		"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-5.1", "gpt-5-mini", "gpt-5-nano", "o3-mini", "o4-mini",
	}, "Models to test")
	dedupBenchCmd.Flags().StringVar(&benchMode, "mode", "both", "Mode: groups, full, or both")
	dedupBenchCmd.Flags().BoolVar(&benchDryRun, "dry-run", false, "Extract data only, don't call API")
	dedupBenchCmd.Flags().StringVar(&benchServerURL, "server", "", "Remote server URL to fetch authors (e.g., https://<server>:8484)")
	dedupBenchCmd.Flags().IntVar(&benchChunkSize, "chunk-size", 500, "Max authors per AI request chunk")
	dedupBenchCmd.Flags().BoolVar(&benchBatch, "batch", false, "Submit as async batch jobs (50% cheaper, ~24h completion)")
}

func runDedupBench(cmd *cobra.Command, args []string) error {
	config.InitConfig()

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		apiKey = config.AppConfig.OpenAIAPIKey
	}
	if apiKey == "" && !benchDryRun {
		return fmt.Errorf("OPENAI_API_KEY env var required")
	}

	// Create timestamped output directory
	ts := time.Now().Format("2006-01-02T15-04-05")
	runDir := filepath.Join(benchOutputDir, ts)
	if err := os.MkdirAll(filepath.Join(runDir, "runs"), 0775); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	slog.Info("Dedup bench output", "directory", runDir)

	// Extract author data
	var authorData *AuthorData
	var err error

	if benchServerURL != "" {
		slog.Info("Fetching authors from server", "server", benchServerURL)
		authorData, err = fetchAuthorsFromServer(benchServerURL)
	} else {
		slog.Info("Fetching authors from local database")
		store, initErr := initializeStore(
			config.AppConfig.DatabaseType,
			config.AppConfig.DatabasePath,
			config.AppConfig.EnableSQLite,
		)
		if initErr != nil {
			return fmt.Errorf("failed to initialize database: %w", initErr)
		}
		defer closeStore()
		authorData, err = extractAuthorData(store)
	}
	if err != nil {
		return fmt.Errorf("failed to extract author data: %w", err)
	}

	if err := writeJSON(filepath.Join(runDir, "authors.json"), authorData.Authors); err != nil {
		return err
	}

	// Compute heuristic groups
	bookCountFn := func(id int) int { return authorData.BookCounts[id] }
	groups := server.FindDuplicateAuthors(authorData.Authors, 0.90, bookCountFn)
	if err := writeJSON(filepath.Join(runDir, "groups.json"), groups); err != nil {
		return err
	}

	slog.Info("Extracted author data", "authors", len(authorData.Authors), "groups", len(groups))

	if benchDryRun {
		slog.Info("Dry run — data extracted, skipping API calls")
		return nil
	}

	configs := buildTestConfigs(benchModels)
	modes := resolveModes(benchMode)

	if benchBatch {
		// Submit as batch jobs
		jobs, err := submitBatchJobs(cmd.Context(), apiKey, configs, authorData, groups, modes, runDir, benchChunkSize)
		if err != nil {
			return fmt.Errorf("batch submission failed: %w", err)
		}

		// Save all job info
		if err := writeJSON(filepath.Join(runDir, "batch_jobs.json"), jobs); err != nil {
			return err
		}

		slog.Info("Submitted batch jobs", "count", len(jobs))
		slog.Info("Check results", "command", "dedup-bench check " + runDir)
		return nil
	}

	// Real-time mode
	var allResults []BenchRunResult
	for i, tc := range configs {
		for _, mode := range modes {
			slog.Info("Running benchmark", "step", i+1, "total", len(configs), "model", tc.Model, "prompt", tc.PromptVariant, "temp", tc.Temperature, "mode", mode)

			result, runErr := executeBenchRun(cmd.Context(), apiKey, tc, authorData, groups, mode, runDir, benchChunkSize)
			if runErr != nil {
				slog.Error("benchmark run failed", "error", runErr)
				result = &BenchRunResult{
					Config: tc,
					Mode:   mode,
					Error:  runErr.Error(),
				}
			}
			allResults = append(allResults, *result)
			time.Sleep(2 * time.Second)
		}
	}

	summary := generateSummary(allResults, len(authorData.Authors), len(groups))
	if err := writeJSON(filepath.Join(runDir, "summary.json"), summary); err != nil {
		return err
	}
	if err := writeSummaryMarkdown(filepath.Join(runDir, "summary.md"), summary); err != nil {
		return err
	}

	slog.Info("Benchmark complete", "directory", runDir)
	slog.Info("View report", "command", "cat " + runDir + "/summary.md")
	return nil
}

// runDedupBenchCheck retrieves results from previously submitted batch jobs.
func runDedupBenchCheck(cmd *cobra.Command, args []string) error {
	runDir := args[0]

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		config.InitConfig()
		apiKey = config.AppConfig.OpenAIAPIKey
	}
	if apiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY env var required")
	}

	// Load batch job info
	jobsData, err := os.ReadFile(filepath.Join(runDir, "batch_jobs.json"))
	if err != nil {
		return fmt.Errorf("couldn't read batch_jobs.json: %w (is this a batch run directory?)", err)
	}

	var jobs []BatchJobInfo
	if err := json.Unmarshal(jobsData, &jobs); err != nil {
		return fmt.Errorf("couldn't parse batch_jobs.json: %w", err)
	}

	clientOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(clientOpts...)

	completed := 0
	failed := 0
	pending := 0
	var allResults []BenchRunResult

	for _, job := range jobs {
		batch, err := client.Batches.Get(cmd.Context(), job.BatchID)
		if err != nil {
			slog.Error("Error checking batch", "batch_id", job.BatchID, "error", err)
			failed++
			continue
		}

		status := string(batch.Status)
		slog.Info("Batch status", "batch_id", job.BatchID, "model", job.Config.Model, "variant", job.Config.PromptVariant, "mode", job.Mode, "status", status)

		if status == "completed" {
			completed++

			// Download results
			if batch.OutputFileID == "" {
				slog.Warn("No output file ID")
				continue
			}

			result, err := downloadBatchResult(cmd.Context(), &client, batch.OutputFileID, job)
			if err != nil {
				slog.Error("Error downloading results", "error", err)
				continue
			}
			allResults = append(allResults, *result)
		} else if status == "failed" || status == "expired" || status == "cancelled" {
			failed++
			errMsg := "unknown"
			if len(batch.Errors.Data) > 0 {
				errMsg = batch.Errors.Data[0].Message
			}
			slog.Error("Batch failed", "reason", errMsg)
			allResults = append(allResults, BenchRunResult{
				Config: job.Config,
				Mode:   job.Mode,
				Error:  fmt.Sprintf("%s: %s", status, errMsg),
			})
		} else {
			pending++
			if batch.RequestCounts.Completed > 0 || batch.RequestCounts.Total > 0 {
				slog.Info("Batch progress", "completed", batch.RequestCounts.Completed, "total", batch.RequestCounts.Total)
			}
		}
	}

	slog.Info("Batch check summary", "completed", completed, "failed", failed, "pending", pending)

	if completed > 0 {
		// Load author/group counts from saved data
		authorCount := 0
		groupCount := 0
		if authorsData, err := os.ReadFile(filepath.Join(runDir, "authors.json")); err == nil {
			var authors []json.RawMessage
			if json.Unmarshal(authorsData, &authors) == nil {
				authorCount = len(authors)
			}
		}
		if groupsData, err := os.ReadFile(filepath.Join(runDir, "groups.json")); err == nil {
			var groups []json.RawMessage
			if json.Unmarshal(groupsData, &groups) == nil {
				groupCount = len(groups)
			}
		}

		summary := generateSummary(allResults, authorCount, groupCount)
		if err := writeJSON(filepath.Join(runDir, "summary.json"), summary); err != nil {
			return err
		}
		if err := writeSummaryMarkdown(filepath.Join(runDir, "summary.md"), summary); err != nil {
			return err
		}
		slog.Info("Summary updated", "file", runDir + "/summary.md")
	}

	if pending > 0 {
		slog.Info("Run this command again later to check remaining jobs")
	}

	return nil
}

// downloadBatchResult downloads and parses results from a completed batch.
func downloadBatchResult(ctx context.Context, client *openai.Client, outputFileID string, job BatchJobInfo) (*BenchRunResult, error) {
	resp, err := client.Files.Content(ctx, outputFileID)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	// Save raw response
	_ = os.WriteFile(filepath.Join(job.RunDir, "batch_output.jsonl"), body, 0664)

	// Parse JSONL responses
	actionCounts := map[string]int{}
	confidenceCounts := map[string]int{}
	numSuggestions := 0
	var totalInputTokens, totalOutputTokens int64

	lines := splitJSONLines(body)
	for _, line := range lines {
		var batchResp struct {
			CustomID string `json:"custom_id"`
			Response struct {
				StatusCode int             `json:"status_code"`
				Body       json.RawMessage `json:"body"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &batchResp) != nil || batchResp.Response.StatusCode != 200 {
			continue
		}

		var completion struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(batchResp.Response.Body, &completion) != nil || len(completion.Choices) == 0 {
			continue
		}

		totalInputTokens += completion.Usage.PromptTokens
		totalOutputTokens += completion.Usage.CompletionTokens

		content := completion.Choices[0].Message.Content
		content = extractJSONFromContent(content)

		var parsed struct {
			Suggestions []struct {
				Action     string `json:"action"`
				Confidence string `json:"confidence"`
			} `json:"suggestions"`
		}
		if json.Unmarshal([]byte(content), &parsed) == nil {
			for _, s := range parsed.Suggestions {
				actionCounts[s.Action]++
				confidenceCounts[s.Confidence]++
				numSuggestions++
			}
		}
	}

	// Save parsed suggestions
	_ = writeJSON(filepath.Join(job.RunDir, "parsed_stats.json"), map[string]interface{}{
		"action_counts":     actionCounts,
		"confidence_counts": confidenceCounts,
		"num_suggestions":   numSuggestions,
	})

	costEstimate := estimateCost(job.Config.Model, totalInputTokens, totalOutputTokens, 0)

	result := &BenchRunResult{
		Config:           job.Config,
		Mode:             job.Mode,
		InputTokens:      totalInputTokens,
		OutputTokens:     totalOutputTokens,
		TotalTokens:      totalInputTokens + totalOutputTokens,
		CostEstimate:     costEstimate,
		NumSuggestions:   numSuggestions,
		ActionCounts:     actionCounts,
		ConfidenceCounts: confidenceCounts,
		NumChunks:        job.NumChunks,
	}

	_ = writeJSON(filepath.Join(job.RunDir, "stats.json"), result)

	slog.Info("Downloaded batch results", "suggestions", numSuggestions, "cost_usd", costEstimate)
	return result, nil
}

// splitJSONLines splits JSONL bytes into individual lines.
func splitJSONLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// fetchAuthorsFromServer pulls author data from a remote audiobook-organizer server.
func fetchAuthorsFromServer(serverURL string) (*AuthorData, error) {
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 30 * time.Second,
	}

	// Fetch authors
	authorsResp, err := httpClient.Get(serverURL + "/api/v1/authors")
	if err != nil {
		return nil, fmt.Errorf("fetch authors: %w", err)
	}
	defer authorsResp.Body.Close()

	if authorsResp.StatusCode != 200 {
		body, _ := io.ReadAll(authorsResp.Body)
		return nil, fmt.Errorf("authors API returned %d: %s", authorsResp.StatusCode, string(body))
	}

	var authorList struct {
		Items []database.Author `json:"items"`
		Count int               `json:"count"`
	}
	if err := json.NewDecoder(authorsResp.Body).Decode(&authorList); err != nil {
		return nil, fmt.Errorf("decode authors: %w", err)
	}

	slog.Info("Fetched authors", "count", authorList.Count)

	// Fetch audiobooks with pagination
	bookCounts := map[int]int{}
	sampleBooks := map[int][]string{}
	offset := 0
	pageSize := 1000
	for {
		url := fmt.Sprintf("%s/api/v1/audiobooks?limit=%d&offset=%d", serverURL, pageSize, offset)
		booksResp, err := httpClient.Get(url)
		if err != nil {
			slog.Warn("Could not fetch books", "offset", offset, "error", err)
			break
		}

		var page struct {
			Items []json.RawMessage `json:"items"`
			Count int               `json:"count"`
		}
		decErr := json.NewDecoder(booksResp.Body).Decode(&page)
		booksResp.Body.Close()
		if decErr != nil {
			slog.Warn("Could not decode books", "offset", offset, "error", decErr)
			break
		}

		for _, rawBook := range page.Items {
			var book struct {
				Title    string `json:"title"`
				AuthorID int    `json:"author_id"`
			}
			if json.Unmarshal(rawBook, &book) != nil {
				continue
			}
			bookCounts[book.AuthorID]++
			if len(sampleBooks[book.AuthorID]) < 3 {
				sampleBooks[book.AuthorID] = append(sampleBooks[book.AuthorID], book.Title)
			}
		}

		slog.Info("Fetched books page", "count", len(page.Items), "offset", offset)
		if len(page.Items) < pageSize {
			break
		}
		offset += pageSize
	}
	slog.Info("Book fetch complete", "mappings", len(bookCounts), "authors_with_samples", len(sampleBooks))

	return &AuthorData{
		Authors:     authorList.Items,
		BookCounts:  bookCounts,
		SampleBooks: sampleBooks,
	}, nil
}

func resolveModes(mode string) []string {
	switch mode {
	case "groups":
		return []string{"groups"}
	case "full":
		return []string{"full"}
	default:
		return []string{"groups", "full"}
	}
}
