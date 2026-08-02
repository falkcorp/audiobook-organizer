// file: internal/server/bench.go
// version: 1.3.1
// guid: 5e6f7a8b-9c0d-1234-ef01-555555555555
// last-edited: 2026-07-03

//go:build bench

package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// setupBenchRoutes registers the bench experiment API endpoints.
func (s *Server) setupBenchRoutes(protected *gin.RouterGroup) {
	bench := protected.Group("/bench")
	{
		bench.GET("/status", s.benchStatus)
		bench.POST("/submit", s.benchSubmit)
		bench.GET("/check/:runDir", s.benchCheck)
		bench.GET("/runs", s.benchListRuns)
		bench.POST("/pass2", s.benchPass2)
		bench.POST("/crossval", s.benchCrossval)
	}
	slog.Info("Bench experiment routes enabled")
}

// benchStatus returns the status of all OpenAI batch jobs.
func (s *Server) benchStatus(c *gin.Context) {
	client, err := benchOpenAIClient()
	if err != nil {
		httputil.InternalError(c, "failed to create OpenAI client", err)
		return
	}

	// NOTE: bench.go calls the OpenAI Batches API directly to measure raw
	// performance characteristics (model latency, token cost). It deliberately
	// does NOT route through internal/ai/aijobs because the goal is unmediated
	// benchmarking. Out of scope for the aijobs migration.
	batches, err := client.Batches.List(c.Request.Context(), openai.BatchListParams{
		Limit: openai.Int(100),
	})
	if err != nil {
		httputil.InternalError(c, "failed to list batches", err)
		return
	}

	var pending, completed, failed int
	var items []gin.H
	for _, b := range batches.Data {
		status := string(b.Status)
		switch status {
		case "completed":
			completed++
		case "failed", "expired", "cancelled":
			failed++
		default:
			pending++
		}
		items = append(items, gin.H{
			"batch_id":   b.ID,
			"status":     status,
			"ok":         b.RequestCounts.Completed,
			"failed":     b.RequestCounts.Failed,
			"total":      b.RequestCounts.Total,
			"created_at": b.CreatedAt,
		})
	}

	httputil.RespondWithOK(c, gin.H{
		"items":     items,
		"pending":   pending,
		"completed": completed,
		"failed":    failed,
	})
}

// benchSubmitRequest is the JSON body for POST /bench/submit.
type benchSubmitRequest struct {
	Models    []string `json:"models"`
	Mode      string   `json:"mode"`   // groups, full, both
	Server    string   `json:"server"` // remote server URL (optional, uses self if empty)
	ChunkSize int      `json:"chunk_size"`
	Batch     bool     `json:"batch"`
}

// benchSubmit submits a new benchmark run.
func (s *Server) benchSubmit(c *gin.Context) {
	var req benchSubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.Models) == 0 {
		req.Models = []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "gpt-5.1", "gpt-5-mini", "gpt-5-nano", "o3-mini", "o4-mini"}
	}
	if req.Mode == "" {
		req.Mode = "both"
	}
	if req.ChunkSize == 0 {
		req.ChunkSize = 500
	}

	apiKey := benchAPIKey()
	if apiKey == "" {
		httputil.RespondWithInternalError(c, "OPENAI_API_KEY not configured")
		return
	}

	// Use self as server if not specified
	serverURL := req.Server
	if serverURL == "" {
		serverURL = fmt.Sprintf("https://127.0.0.1:%d", 8484)
	}

	ts := time.Now().Format("2006-01-02T15-04-05")
	runDir := filepath.Join("testdata/dedup-bench", ts)
	if err := os.MkdirAll(filepath.Join(runDir, "runs"), 0775); err != nil {
		httputil.InternalError(c, "failed to create run directory", err)
		return
	}

	// Run in background
	go func() {
		ctx := context.Background()
		slog.Info("bench Starting run models mode server", "models", req.Models, "mode", req.Mode, "serverURL", serverURL)

		authorData, err := benchFetchAuthors(serverURL)
		if err != nil {
			slog.Info("bench ERROR fetching authors", "err", err)
			return
		}

		bookCountFn := func(id int) int { return authorData.BookCounts[id] }
		groups := dedup.FindDuplicateAuthors(authorData.Authors, 0.90, bookCountFn)

		_ = benchWriteJSON(filepath.Join(runDir, "authors.json"), authorData.Authors)
		_ = benchWriteJSON(filepath.Join(runDir, "groups.json"), groups)

		slog.Info("bench authors, groups", "count", len(authorData.Authors), "groups_count", len(groups))

		configs := benchBuildConfigs(req.Models)
		modes := benchResolveModes(req.Mode)

		client, _ := benchOpenAIClient()
		var jobs []gin.H

		for _, tc := range configs {
			for _, mode := range modes {
				dirName := fmt.Sprintf("%s_%s_t%.1f_%s", tc.Model, tc.Prompt, tc.Temperature, mode)
				outDir := filepath.Join(runDir, "runs", dirName)
				os.MkdirAll(outDir, 0775)

				systemPrompt := benchGetSystemPrompt(mode, tc.Prompt)
				var userData []byte
				maxTokens := int64(16000)

				if mode == "groups" {
					userData, _ = json.Marshal(benchBuildGroupsInput(groups, authorData))
					maxTokens = 32000
				} else {
					userData, _ = json.Marshal(benchBuildFullInput(authorData))
				}

				// Cap tokens per model
				if cap, ok := benchMaxTokens[tc.Model]; ok && maxTokens > cap {
					maxTokens = cap
				}

				chunks := benchChunkData(userData, req.ChunkSize, mode)
				for ci, chunk := range chunks {
					chunkDir := outDir
					if len(chunks) > 1 {
						chunkDir = fmt.Sprintf("%s_chunk%d", outDir, ci)
						os.MkdirAll(chunkDir, 0775)
					}

					prefix := "Review these duplicate author groups:\n\n"
					if mode == "full" {
						prefix = "Find duplicate authors in this list:\n\n"
					}

					job, err := benchSubmitBatchJob(ctx, client, tc, mode, systemPrompt, prefix+string(chunk), maxTokens,
						fmt.Sprintf("%s_%s_t%.1f_%s_chunk%d", tc.Model, tc.Prompt, tc.Temperature, mode, ci), chunkDir)
					if err != nil {
						slog.Info("bench ERROR submitting", "dirName", dirName, "err", err)
						continue
					}
					jobs = append(jobs, job)
				}
			}
		}

		_ = benchWriteJSON(filepath.Join(runDir, "batch_jobs.json"), jobs)
		slog.Info("bench Submitted batch jobs to", "jobs_count", len(jobs), "runDir", runDir)
	}()

	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{
		"message": "Benchmark run started in background",
		"run_dir": runDir,
	})
}

// benchCheck checks the status of a specific run's batch jobs.
func (s *Server) benchCheck(c *gin.Context) {
	runDir := c.Param("runDir")
	// URL-decode the runDir in case it has special chars
	runDir = filepath.Join("testdata/dedup-bench", runDir)

	jobsData, err := os.ReadFile(filepath.Join(runDir, "batch_jobs.json"))
	if err != nil {
		httputil.RespondWithNotFound(c, "batch_jobs.json not found in "+runDir, "")
		return
	}

	var jobs []json.RawMessage
	json.Unmarshal(jobsData, &jobs)

	client, err := benchOpenAIClient()
	if err != nil {
		httputil.InternalError(c, "failed to create OpenAI client", err)
		return
	}

	var results []gin.H
	var pending, completed, failed int

	for _, raw := range jobs {
		var job struct {
			BatchID string `json:"batch_id"`
			Model   string `json:"model"`
			Mode    string `json:"mode"`
			RunDir  string `json:"run_dir"`
		}
		json.Unmarshal(raw, &job)

		batch, err := client.Batches.Get(c.Request.Context(), job.BatchID)
		if err != nil {
			results = append(results, gin.H{"batch_id": job.BatchID, "status": "error", "error": err.Error()})
			failed++
			continue
		}

		status := string(batch.Status)
		switch status {
		case "completed":
			completed++
		case "failed", "expired", "cancelled":
			failed++
		default:
			pending++
		}

		results = append(results, gin.H{
			"batch_id": job.BatchID,
			"status":   status,
			"ok":       batch.RequestCounts.Completed,
			"failed":   batch.RequestCounts.Failed,
			"total":    batch.RequestCounts.Total,
		})
	}

	httputil.RespondWithOK(c, gin.H{
		"run_dir":   runDir,
		"items":     results,
		"pending":   pending,
		"completed": completed,
		"failed":    failed,
	})
}

// benchListRuns lists all bench run directories.
func (s *Server) benchListRuns(c *gin.Context) {
	benchDir := "testdata/dedup-bench"
	entries, err := os.ReadDir(benchDir)
	if err != nil {
		httputil.RespondWithOK(c, gin.H{"runs": []string{}})
		return
	}

	var runs []gin.H
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, _ := e.Info()
		run := gin.H{"name": e.Name()}
		if info != nil {
			run["modified"] = info.ModTime()
		}
		// Check if batch_jobs.json exists
		if _, err := os.Stat(filepath.Join(benchDir, e.Name(), "batch_jobs.json")); err == nil {
			run["has_jobs"] = true
		}
		runs = append(runs, run)
	}

	httputil.RespondWithOK(c, gin.H{"runs": runs})
}

// benchPass2 runs second-pass enrichment via API.
func (s *Server) benchPass2(c *gin.Context) {
	var req struct {
		ResultsPath string `json:"results_path" binding:"required"`
		GroupsPath  string `json:"groups_path" binding:"required"`
		Model       string `json:"model"`
		ServerURL   string `json:"server"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if req.Model == "" {
		req.Model = "gpt-5.1"
	}
	if req.ServerURL == "" {
		req.ServerURL = fmt.Sprintf("https://127.0.0.1:%d", 8484)
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{
		"message": "Pass2 enrichment started — use CLI for now: ./audiobook-organizer dedup-bench pass2",
		"hint":    fmt.Sprintf("./audiobook-organizer dedup-bench pass2 --results %s --groups %s --model %s --server %s", req.ResultsPath, req.GroupsPath, req.Model, req.ServerURL),
	})
}

// benchCrossval runs cross-validation via API.
func (s *Server) benchCrossval(c *gin.Context) {
	var req struct {
		ResultsA  string `json:"results_a" binding:"required"`
		ModelA    string `json:"model_a" binding:"required"`
		ModeA     string `json:"mode_a"`
		InputData string `json:"input_data"`
		ModelB    string `json:"model_b" binding:"required"`
		Variant   string `json:"variant"`
		ServerURL string `json:"server"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{
		"message": "Crossval started — use CLI for now: ./audiobook-organizer dedup-bench crossval",
		"hint":    fmt.Sprintf("./audiobook-organizer dedup-bench crossval --results-a %s --model-a %s --model-b %s", req.ResultsA, req.ModelA, req.ModelB),
	})
}

// --- Bench helper types and functions ---

type benchTestConfig struct {
	Model       string
	Prompt      string
	Temperature float64
}

var benchMaxTokens = map[string]int64{
	"gpt-4o":       16384,
	"gpt-4o-mini":  16384,
	"gpt-4.1":      32768,
	"gpt-4.1-mini": 32768,
	"o3-mini":      65536,
	"o4-mini":      65536,
	"gpt-5.1":      32768,
	"gpt-5-mini":   32768,
	"gpt-5-nano":   16384,
}

type benchAuthorData struct {
	Authors     []database.Author
	BookCounts  map[int]int
	SampleBooks map[int][]string
}

func benchAPIKey() string {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		key = config.AppConfig.OpenAIAPIKey
	}
	return key
}

func benchOpenAIClient() (*openai.Client, error) {
	apiKey := benchAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)
	return &client, nil
}

func benchFetchAuthors(serverURL string) (*benchAuthorData, error) {
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 30 * time.Second,
	}

	// Fetch authors
	resp, err := httpClient.Get(serverURL + "/api/v1/authors")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var authorList struct {
		Items []database.Author `json:"items"`
	}
	json.NewDecoder(resp.Body).Decode(&authorList)

	// Fetch books
	bookCounts := map[int]int{}
	sampleBooks := map[int][]string{}
	offset := 0
	for {
		url := fmt.Sprintf("%s/api/v1/audiobooks?limit=1000&offset=%d", serverURL, offset)
		bResp, err := httpClient.Get(url)
		if err != nil {
			break
		}
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		json.NewDecoder(bResp.Body).Decode(&page)
		bResp.Body.Close()

		for _, raw := range page.Items {
			var book struct {
				Title    string `json:"title"`
				AuthorID int    `json:"author_id"`
			}
			json.Unmarshal(raw, &book)
			bookCounts[book.AuthorID]++
			if len(sampleBooks[book.AuthorID]) < 3 {
				sampleBooks[book.AuthorID] = append(sampleBooks[book.AuthorID], book.Title)
			}
		}
		if len(page.Items) < 1000 {
			break
		}
		offset += 1000
	}

	return &benchAuthorData{
		Authors:     authorList.Items,
		BookCounts:  bookCounts,
		SampleBooks: sampleBooks,
	}, nil
}

func benchBuildConfigs(models []string) []benchTestConfig {
	var configs []benchTestConfig
	for _, m := range models {
		configs = append(configs, benchTestConfig{Model: m, Prompt: "baseline", Temperature: 0})
	}
	return configs
}

func benchResolveModes(mode string) []string {
	switch mode {
	case "groups":
		return []string{"groups"}
	case "full":
		return []string{"full"}
	default:
		return []string{"groups", "full"}
	}
}

func benchBuildGroupsInput(groups []dedup.AuthorDedupGroup, data *benchAuthorData) []map[string]interface{} {
	var input []map[string]interface{}
	for _, g := range groups {
		canonical := map[string]interface{}{
			"id":   g.Canonical.ID,
			"name": g.Canonical.Name,
		}
		var variants []map[string]interface{}
		for _, v := range g.Variants {
			variants = append(variants, map[string]interface{}{
				"id":   v.ID,
				"name": v.Name,
			})
		}
		input = append(input, map[string]interface{}{
			"canonical": canonical,
			"variants":  variants,
		})
	}
	return input
}

func benchBuildFullInput(data *benchAuthorData) []map[string]interface{} {
	var input []map[string]interface{}
	for _, a := range data.Authors {
		input = append(input, map[string]interface{}{
			"id":            a.ID,
			"name":          a.Name,
			"book_count":    data.BookCounts[a.ID],
			"sample_titles": data.SampleBooks[a.ID],
		})
	}
	return input
}

func benchChunkData(data []byte, chunkSize int, mode string) [][]byte {
	if mode == "groups" {
		return [][]byte{data}
	}
	var items []json.RawMessage
	json.Unmarshal(data, &items)
	if len(items) <= chunkSize {
		return [][]byte{data}
	}
	var chunks [][]byte
	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		chunk, _ := json.Marshal(items[i:end])
		chunks = append(chunks, chunk)
	}
	return chunks
}

func benchGetSystemPrompt(mode, variant string) string {
	// Simplified prompt — the full prompts are in cmd/dedup_bench_prompts.go
	if mode == "groups" {
		return `You are an expert audiobook metadata reviewer. Review groups of potentially duplicate author names. For each group, determine the action: merge, split, rename, skip, alias, or reclassify. Return ONLY valid JSON: {"suggestions": [...]}`
	}
	return `You are an expert audiobook metadata reviewer. Find groups of authors that are likely the same person. Return ONLY valid JSON: {"suggestions": [...]}`
}

func benchSubmitBatchJob(ctx context.Context, client *openai.Client, tc benchTestConfig, mode, systemPrompt, userPrompt string, maxTokens int64, customID, outDir string) (gin.H, error) {
	isReasoning := strings.HasPrefix(tc.Model, "o3") || strings.HasPrefix(tc.Model, "o4") || strings.HasPrefix(tc.Model, "o1") || strings.HasPrefix(tc.Model, "gpt-5")

	body := map[string]interface{}{
		"model": tc.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_completion_tokens": maxTokens,
	}
	if !isReasoning {
		body["temperature"] = tc.Temperature
		body["top_p"] = 1.0
		body["response_format"] = map[string]string{"type": "json_object"}
	}

	req := ai.BatchRequest{
		CustomID: customID,
		Method:   "POST",
		URL:      "/v1/chat/completions",
		Body:     body,
	}

	var buf bytes.Buffer
	line, _ := json.Marshal(req)
	buf.Write(line)
	buf.WriteByte('\n')

	os.MkdirAll(outDir, 0775)
	os.WriteFile(filepath.Join(outDir, "batch_input.jsonl"), buf.Bytes(), 0664)

	file, err := client.Files.New(ctx, openai.FileNewParams{
		File:    bytes.NewReader(buf.Bytes()),
		Purpose: openai.FilePurposeBatch,
	})
	if err != nil {
		return nil, err
	}

	batch, err := client.Batches.New(ctx, openai.BatchNewParams{
		InputFileID:      file.ID,
		Endpoint:         openai.BatchNewParamsEndpointV1ChatCompletions,
		CompletionWindow: openai.BatchNewParamsCompletionWindow24h,
		Metadata: map[string]string{
			"service": "ao-bench",
		},
	})
	if err != nil {
		return nil, err
	}

	job := gin.H{
		"batch_id": batch.ID,
		"model":    tc.Model,
		"mode":     mode,
		"prompt":   tc.Prompt,
		"run_dir":  outDir,
	}
	benchWriteJSON(filepath.Join(outDir, "batch_info.json"), job)
	return job, nil
}

func benchWriteJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0664)
}
