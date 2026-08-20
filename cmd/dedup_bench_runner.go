// file: cmd/dedup_bench_runner.go
// version: 1.1.0
// guid: d4e5f6a7-b8c9-0123-defa-456789012345
// last-edited: 2026-08-20

//go:build bench

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// executeBenchRun runs a single model+prompt+params combination and saves all raw I/O.
func executeBenchRun(
	ctx context.Context,
	apiKey string,
	tc TestConfig,
	data *AuthorData,
	groups []server.AuthorDedupGroup,
	mode string,
	runDir string,
	chunkSize int,
) (*BenchRunResult, error) {
	// Create run output directory
	dirName := fmt.Sprintf("%s_%s_t%.1f_%s", tc.Model, tc.PromptVariant, tc.Temperature, mode)
	outDir := filepath.Join(runDir, "runs", dirName)
	if err := os.MkdirAll(outDir, 0775); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	// Save config
	configWithMode := map[string]interface{}{
		"model":          tc.Model,
		"prompt_variant": tc.PromptVariant,
		"temperature":    tc.Temperature,
		"top_p":          tc.TopP,
		"mode":           mode,
		"chunk_size":     chunkSize,
	}
	if err := writeJSON(filepath.Join(outDir, "config.json"), configWithMode); err != nil {
		return nil, err
	}

	// Build the prompt and input
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

	// For large datasets, chunk the input
	chunks := chunkInput(inputJSON, chunkSize, mode)
	slog.Info("Input size", "bytes", len(inputJSON), "chunks", len(chunks))

	// Save the full request data
	request := map[string]interface{}{
		"model":             tc.Model,
		"prompt_variant":    tc.PromptVariant,
		"temperature":       tc.Temperature,
		"top_p":             tc.TopP,
		"mode":              mode,
		"system_prompt":     systemPrompt,
		"num_chunks":        len(chunks),
		"total_input_items": json.RawMessage(fmt.Sprintf("%d", countItems(inputJSON))),
	}
	if err := writeJSON(filepath.Join(outDir, "request.json"), request); err != nil {
		return nil, err
	}

	// Save raw input data
	if err := os.WriteFile(filepath.Join(outDir, "input_data.json"), inputJSON, 0664); err != nil {
		return nil, err
	}

	// Create OpenAI client
	clientOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := config.AppConfig.OpenAIBaseURL; baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(clientOpts...)

	// Execute all chunks and aggregate
	var totalInputTokens, totalOutputTokens, totalCachedTokens int64
	var allSuggestionsRaw []json.RawMessage
	totalStart := time.Now()

	for ci, chunk := range chunks {
		if len(chunks) > 1 {
			slog.Info("Processing chunk", "number", ci+1, "total", len(chunks), "bytes", len(chunk))
		}

		userPrompt := userPromptPrefix + string(chunk)

		// Save per-chunk request
		if len(chunks) > 1 {
			chunkReq := map[string]interface{}{
				"chunk_index":   ci,
				"system_prompt": systemPrompt,
				"user_prompt":   userPrompt,
			}
			_ = writeJSON(filepath.Join(outDir, fmt.Sprintf("chunk_%d_request.json", ci)), chunkReq)
		}

		maxTokens := int64(32000)
		if mode == "full" {
			maxTokens = 16000
		}

		isReasoningModel := strings.HasPrefix(tc.Model, "o3") || strings.HasPrefix(tc.Model, "o4") || strings.HasPrefix(tc.Model, "o1")

		jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()
		params := openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(systemPrompt),
				openai.UserMessage(userPrompt),
			},
			Model:               shared.ChatModel(tc.Model),
			MaxCompletionTokens: param.NewOpt(maxTokens),
		}

		// Reasoning models don't support temperature, top_p, or json_object response format
		if !isReasoningModel {
			params.Temperature = param.NewOpt(tc.Temperature)
			params.TopP = param.NewOpt(tc.TopP)
			params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &jsonObjectFormat,
			}
		}

		start := time.Now()
		completion, err := client.Chat.Completions.New(ctx, params)
		elapsed := time.Since(start)

		if err != nil {
			errResult := map[string]interface{}{
				"error":       err.Error(),
				"duration_ms": elapsed.Milliseconds(),
				"chunk":       ci,
			}
			_ = writeJSON(filepath.Join(outDir, fmt.Sprintf("chunk_%d_error.json", ci)), errResult)
			return nil, fmt.Errorf("API call failed (chunk %d): %w", ci, err)
		}

		// Save raw response
		rawResp, _ := json.Marshal(completion)
		_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("chunk_%d_response.json", ci)), rawResp, 0664)

		// Accumulate token usage
		totalInputTokens += completion.Usage.PromptTokens
		totalOutputTokens += completion.Usage.CompletionTokens
		totalCachedTokens += completion.Usage.PromptTokensDetails.CachedTokens

		// Parse suggestions from this chunk
		if len(completion.Choices) > 0 {
			content := completion.Choices[0].Message.Content
			// For reasoning models, try to extract JSON from the content
			if isReasoningModel {
				content = extractJSONFromContent(content)
			}
			var parsed struct {
				Suggestions []json.RawMessage `json:"suggestions"`
			}
			if err := json.Unmarshal([]byte(content), &parsed); err != nil {
				slog.Warn("Failed to parse suggestions", "chunk", ci, "error", err)
				_ = writeJSON(filepath.Join(outDir, fmt.Sprintf("chunk_%d_parse_error.json", ci)), map[string]string{
					"error": err.Error(),
					"raw":   content,
				})
			} else {
				allSuggestionsRaw = append(allSuggestionsRaw, parsed.Suggestions...)
			}
		}

		slog.Info("Chunk processed", "number", ci, "duration_ms", elapsed.Milliseconds(), "input_tokens", completion.Usage.PromptTokens, "output_tokens", completion.Usage.CompletionTokens)

		// Brief pause between chunks
		if ci < len(chunks)-1 {
			time.Sleep(time.Second)
		}
	}

	totalElapsed := time.Since(totalStart)

	// Save aggregated suggestions
	if err := writeJSON(filepath.Join(outDir, "suggestions.json"), allSuggestionsRaw); err != nil {
		return nil, err
	}

	// Count actions and confidence across all suggestions
	actionCounts := map[string]int{}
	confidenceCounts := map[string]int{}
	for _, raw := range allSuggestionsRaw {
		var s struct {
			Action     string `json:"action"`
			Confidence string `json:"confidence"`
		}
		if json.Unmarshal(raw, &s) == nil {
			actionCounts[s.Action]++
			confidenceCounts[s.Confidence]++
		}
	}

	costEstimate := estimateCost(tc.Model, totalInputTokens, totalOutputTokens, totalCachedTokens)

	result := &BenchRunResult{
		Config:           tc,
		Mode:             mode,
		DurationMs:       totalElapsed.Milliseconds(),
		InputTokens:      totalInputTokens,
		OutputTokens:     totalOutputTokens,
		CachedTokens:     totalCachedTokens,
		TotalTokens:      totalInputTokens + totalOutputTokens,
		CostEstimate:     costEstimate,
		NumSuggestions:   len(allSuggestionsRaw),
		ActionCounts:     actionCounts,
		ConfidenceCounts: confidenceCounts,
		NumChunks:        len(chunks),
	}

	if err := writeJSON(filepath.Join(outDir, "stats.json"), result); err != nil {
		return nil, err
	}

	slog.Info("Run complete", "duration_ms", totalElapsed.Milliseconds(), "suggestions", len(allSuggestionsRaw), "cost_usd", costEstimate)

	return result, nil
}

// chunkInput splits a JSON array into smaller chunks for large datasets.
func chunkInput(inputJSON []byte, chunkSize int, mode string) [][]byte {
	if mode == "groups" {
		var items []ai.AuthorDedupInput
		if err := json.Unmarshal(inputJSON, &items); err != nil || len(items) <= chunkSize {
			return [][]byte{inputJSON}
		}
		var chunks [][]byte
		for i := 0; i < len(items); i += chunkSize {
			end := i + chunkSize
			if end > len(items) {
				end = len(items)
			}
			// Reindex within the chunk
			chunk := make([]ai.AuthorDedupInput, end-i)
			copy(chunk, items[i:end])
			for j := range chunk {
				chunk[j].Index = i + j
			}
			data, _ := json.Marshal(chunk)
			chunks = append(chunks, data)
		}
		return chunks
	}

	// Full mode
	var items []ai.AuthorDiscoveryInput
	if err := json.Unmarshal(inputJSON, &items); err != nil || len(items) <= chunkSize {
		return [][]byte{inputJSON}
	}
	var chunks [][]byte
	for i := 0; i < len(items); i += chunkSize {
		end := i + chunkSize
		if end > len(items) {
			end = len(items)
		}
		data, _ := json.Marshal(items[i:end])
		chunks = append(chunks, data)
	}
	return chunks
}

// countItems returns the number of items in a JSON array.
func countItems(inputJSON []byte) int {
	var items []json.RawMessage
	if json.Unmarshal(inputJSON, &items) == nil {
		return len(items)
	}
	return 0
}

// extractJSONFromContent tries to extract JSON from a response that may contain
// markdown code blocks or reasoning text (common with o3/o4 models).
func extractJSONFromContent(content string) string {
	// Try direct parse first
	if json.Valid([]byte(content)) {
		return content
	}
	// Look for ```json ... ``` blocks
	start := strings.Index(content, "```json")
	if start >= 0 {
		start += 7 // skip ```json
		end := strings.Index(content[start:], "```")
		if end >= 0 {
			return strings.TrimSpace(content[start : start+end])
		}
	}
	// Look for ``` ... ``` blocks
	start = strings.Index(content, "```")
	if start >= 0 {
		start += 3
		end := strings.Index(content[start:], "```")
		if end >= 0 {
			candidate := strings.TrimSpace(content[start : start+end])
			if json.Valid([]byte(candidate)) {
				return candidate
			}
		}
	}
	// Look for first { to last }
	first := strings.Index(content, "{")
	last := strings.LastIndex(content, "}")
	if first >= 0 && last > first {
		candidate := content[first : last+1]
		if json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	return content
}

// estimateCost estimates the USD cost of a run based on model pricing.
func estimateCost(model string, inputTokens, outputTokens, cachedTokens int64) float64 {
	type pricing struct{ input, output, cached float64 }
	prices := map[string]pricing{
		"gpt-4o":      {2.50, 10.00, 1.25},
		"gpt-4o-mini": {0.15, 0.60, 0.075},
		"gpt-5":       {10.00, 30.00, 2.50},
		"gpt-5-mini":  {0.30, 1.25, 0.15},
		"o3-mini":     {1.10, 4.40, 0.55},
		"o4-mini":     {1.10, 4.40, 0.55},
	}

	p, ok := prices[model]
	if !ok {
		p = pricing{5.0, 15.0, 2.5}
	}

	uncachedInput := inputTokens - cachedTokens
	cost := float64(uncachedInput)/1_000_000*p.input +
		float64(cachedTokens)/1_000_000*p.cached +
		float64(outputTokens)/1_000_000*p.output

	return cost
}
