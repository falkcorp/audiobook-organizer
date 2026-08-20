// file: cmd/dedup_bench_batch.go
// version: 1.1.1
// guid: f6a7b8c9-d0e1-2345-fabc-678901234567
// last-edited: 2026-08-20

//go:build bench

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// BatchJobInfo tracks a submitted batch job.
type BatchJobInfo struct {
	BatchID     string     `json:"batch_id"`
	Config      TestConfig `json:"config"`
	Mode        string     `json:"mode"`
	NumChunks   int        `json:"num_chunks"`
	NumRequests int        `json:"num_requests"`
	InputFileID string     `json:"input_file_id"`
	RunDir      string     `json:"run_dir"`
}

// submitBatchJobs submits all test configurations as OpenAI batch API jobs.
// Returns list of batch job info for later retrieval.
func submitBatchJobs(
	ctx context.Context,
	apiKey string,
	configs []TestConfig,
	data *AuthorData,
	groups []dedup.AuthorDedupGroup,
	modes []string,
	runDir string,
	chunkSize int,
) ([]BatchJobInfo, error) {
	clientOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := config.AppConfig.OpenAIBaseURL; baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(clientOpts...)

	var jobs []BatchJobInfo

	for _, tc := range configs {
		for _, mode := range modes {
			slog.Info("Submitting batch", "model", tc.Model, "prompt", tc.PromptVariant, "temp", tc.Temperature, "mode", mode)

			// Create run output directory
			dirName := fmt.Sprintf("%s_%s_t%.1f_%s", tc.Model, tc.PromptVariant, tc.Temperature, mode)
			outDir := filepath.Join(runDir, "runs", dirName)
			if err := os.MkdirAll(outDir, 0775); err != nil {
				return nil, fmt.Errorf("mkdir: %w", err)
			}

			// Save config
			_ = writeJSON(filepath.Join(outDir, "config.json"), map[string]interface{}{
				"model":          tc.Model,
				"prompt_variant": tc.PromptVariant,
				"temperature":    tc.Temperature,
				"top_p":          tc.TopP,
				"mode":           mode,
				"chunk_size":     chunkSize,
				"batch_mode":     true,
			})

			// Build prompts and input
			var systemPrompt string
			var inputJSON []byte

			if mode == "groups" {
				systemPrompt = getGroupsSystemPrompt(tc.PromptVariant)
				inputs := buildGroupsInput(groups, data)
				inputJSON, _ = json.Marshal(inputs)
			} else {
				systemPrompt = getFullSystemPrompt(tc.PromptVariant)
				inputs := buildFullInput(data)
				inputJSON, _ = json.Marshal(inputs)
			}

			userPromptPrefix := "Review these duplicate author groups:\n\n"
			if mode == "full" {
				userPromptPrefix = "Find duplicate authors in this list:\n\n"
			}

			// Save raw input
			_ = os.WriteFile(filepath.Join(outDir, "input_data.json"), inputJSON, 0664)

			// Chunk the input for large datasets
			chunks := chunkInput(inputJSON, chunkSize, mode)

			// Build JSONL batch file with one request per chunk
			var buf bytes.Buffer
			maxTokens := int64(32000)
			if mode == "full" {
				maxTokens = 16000
			}

			isReasoningModel := strings.HasPrefix(tc.Model, "o3") || strings.HasPrefix(tc.Model, "o4") || strings.HasPrefix(tc.Model, "o1")

			for ci, chunk := range chunks {
				userPrompt := userPromptPrefix + string(chunk)

				body := map[string]interface{}{
					"model": tc.Model,
					"messages": []map[string]string{
						{"role": "system", "content": systemPrompt},
						{"role": "user", "content": userPrompt},
					},
					"max_completion_tokens": maxTokens,
				}

				if !isReasoningModel {
					body["temperature"] = tc.Temperature
					body["top_p"] = tc.TopP
					body["response_format"] = map[string]string{"type": "json_object"}
				}

				req := ai.BatchRequest{
					CustomID: fmt.Sprintf("%s_%s_t%.1f_%s_chunk%d", tc.Model, tc.PromptVariant, tc.Temperature, mode, ci),
					Method:   "POST",
					URL:      "/v1/chat/completions",
					Body:     body,
				}

				line, err := json.Marshal(req)
				if err != nil {
					return nil, fmt.Errorf("marshal batch request: %w", err)
				}
				buf.Write(line)
				buf.WriteByte('\n')
			}

			// Save the JSONL file locally
			_ = os.WriteFile(filepath.Join(outDir, "batch_input.jsonl"), buf.Bytes(), 0664)

			// Upload to OpenAI
			file, err := client.Files.New(ctx, openai.FileNewParams{
				File:    bytes.NewReader(buf.Bytes()),
				Purpose: openai.FilePurposeBatch,
			})
			if err != nil {
				slog.Error("uploading file failed", "error", err)
				_ = writeJSON(filepath.Join(outDir, "error.json"), map[string]string{"error": err.Error()})
				continue
			}

			// Create the batch
			batch, err := client.Batches.New(ctx, openai.BatchNewParams{
				InputFileID:      file.ID,
				Endpoint:         openai.BatchNewParamsEndpointV1ChatCompletions,
				CompletionWindow: openai.BatchNewParamsCompletionWindow24h,
			})
			if err != nil {
				slog.Error("creating batch failed", "error", err)
				_ = writeJSON(filepath.Join(outDir, "error.json"), map[string]string{"error": err.Error()})
				continue
			}

			job := BatchJobInfo{
				BatchID:     batch.ID,
				Config:      tc,
				Mode:        mode,
				NumChunks:   len(chunks),
				NumRequests: len(chunks),
				InputFileID: file.ID,
				RunDir:      outDir,
			}
			jobs = append(jobs, job)

			_ = writeJSON(filepath.Join(outDir, "batch_info.json"), job)

			slog.Info("Submitted batch", "batch_id", batch.ID, "requests", len(chunks), "file_id", file.ID)
		}
	}

	return jobs, nil
}
