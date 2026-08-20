// file: cmd/dedup_bench_pass2.go
// version: 1.1.0
// guid: 2b3c4d5e-6f7a-8901-bcde-222222222222
// last-edited: 2026-08-20

//go:build bench

package cmd

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
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/spf13/cobra"
)

var (
	pass2ResultsPath string
	pass2GroupsPath  string
	pass2Model       string
	pass2Threshold   string
)

var dedupBenchPass2Cmd = &cobra.Command{
	Use:   "pass2",
	Short: "Second-pass enrichment for uncertain suggestions",
	Long: `Takes medium/low confidence results from a first-pass groups run,
enriches them with book title data from the server, and resubmits
for a more informed decision via batch API.`,
	RunE: runDedupBenchPass2,
}

func init() {
	dedupBenchPass2Cmd.Flags().StringVar(&pass2ResultsPath, "results", "", "Path to first-pass batch_output.jsonl (required)")
	dedupBenchPass2Cmd.Flags().StringVar(&pass2GroupsPath, "groups", "", "Path to groups.json from first pass (required)")
	dedupBenchPass2Cmd.Flags().StringVar(&pass2Model, "model", "gpt-5.1", "Model for second pass")
	dedupBenchPass2Cmd.Flags().StringVar(&pass2Threshold, "threshold", "medium", "Include suggestions at this confidence and below (medium or low)")
	_ = dedupBenchPass2Cmd.MarkFlagRequired("results")
	_ = dedupBenchPass2Cmd.MarkFlagRequired("groups")
}

const pass2SystemPrompt = `You are an expert audiobook metadata reviewer performing a SECOND-PASS verification.

You previously reviewed author groups and flagged some as uncertain. Now you are given:
1. The original group of author name variants
2. Your previous suggestion and reasoning
3. NEW DATA: Book titles for each author variant, so you can determine if they are the same person

Use the book titles to make a more informed decision:
- If both variants have books in the same series or genre, they are likely the same person → merge
- If they have completely unrelated books, they are likely different people → split
- Narrators tend to have books across many unrelated genres/authors
- Publishers have many unrelated books

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".

Return ONLY valid JSON: {"suggestions": [{"group_index": N, "action": "merge|split|rename|skip|alias|reclassify", "canonical_name": "Correct Name", "reason": "brief explanation using book title evidence", "confidence": "high|medium|low", "roles": {"author": {"name": "Name", "variants": ["V1"], "reason": "why"}, "narrator": {"name": "Name", "ids": [], "reason": "why"}, "publisher": {"name": "Name", "ids": [], "reason": "why"}}}]}`

func runDedupBenchPass2(cmd *cobra.Command, args []string) error {
	config.InitConfig()
	apiKey := config.AppConfig.OpenAIAPIKey
	if apiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY env var required")
	}
	if benchServerURL == "" {
		return fmt.Errorf("--server flag required on parent command")
	}

	// Load first-pass results
	suggestions, err := loadSuggestionsFromJSONL(pass2ResultsPath)
	if err != nil {
		return fmt.Errorf("load results: %w", err)
	}
	slog.Info("Loaded first-pass suggestions", "count", len(suggestions))

	// Filter uncertain
	thresholds := map[string]bool{"low": true}
	if pass2Threshold == "medium" {
		thresholds["medium"] = true
	}
	var uncertain []map[string]interface{}
	for _, s := range suggestions {
		conf, _ := s["confidence"].(string)
		if thresholds[conf] {
			uncertain = append(uncertain, s)
		}
	}
	slog.Info("Found uncertain suggestions", "count", len(uncertain), "threshold", pass2Threshold)

	if len(uncertain) == 0 {
		slog.Info("No uncertain suggestions to enrich")
		return nil
	}

	// Load groups
	groupsData, err := os.ReadFile(pass2GroupsPath)
	if err != nil {
		return fmt.Errorf("read groups: %w", err)
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(groupsData, &groups); err != nil {
		return fmt.Errorf("parse groups: %w", err)
	}

	// Collect author IDs for book enrichment
	authorIDs := collectAuthorIDsFromGroups(uncertain, groups)

	// Fetch book data
	slog.Info("Fetching books for authors", "count", len(authorIDs), "server", benchServerURL)
	booksByAuthor, err := fetchBooksForAuthorIDs(benchServerURL, authorIDs)
	if err != nil {
		return fmt.Errorf("fetch books: %w", err)
	}

	// Build enriched items
	var enriched []map[string]interface{}
	for _, s := range uncertain {
		gi, _ := s["group_index"].(float64)
		idx := int(gi)
		if idx < 0 || idx >= len(groups) {
			continue
		}

		var group map[string]interface{}
		json.Unmarshal(groups[idx], &group)

		evidence := buildBookEvidence(group, booksByAuthor)

		enriched = append(enriched, map[string]interface{}{
			"group_index":         idx,
			"original_group":      group,
			"previous_suggestion": s,
			"book_evidence":       evidence,
		})
	}

	// Create run directory
	ts := time.Now().Format("2006-01-02T15-04-05")
	runDir := filepath.Join(benchOutputDir, ts+"-pass2")
	if err := os.MkdirAll(runDir, 0775); err != nil {
		return err
	}

	userContent, _ := json.Marshal(enriched)
	_ = writeJSON(filepath.Join(runDir, "enriched_input.json"), enriched)

	// Submit batch
	return submitSingleBatch(cmd.Context(), apiKey, pass2Model, pass2SystemPrompt,
		"Review these uncertain suggestions with the new book title evidence:\n\n"+string(userContent),
		fmt.Sprintf("pass2_%s_%s", pass2Model, ts), runDir)
}

// loadSuggestionsFromJSONL loads suggestions from a batch_output.jsonl file.
func loadSuggestionsFromJSONL(path string) ([]map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var suggestions []map[string]interface{}
	for _, line := range splitJSONLines(data) {
		var resp struct {
			Response struct {
				StatusCode int             `json:"status_code"`
				Body       json.RawMessage `json:"body"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.Response.StatusCode != 200 {
			continue
		}

		var completion struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(resp.Response.Body, &completion) != nil || len(completion.Choices) == 0 {
			continue
		}

		content := extractJSONFromContent(completion.Choices[0].Message.Content)
		var parsed struct {
			Suggestions []map[string]interface{} `json:"suggestions"`
		}
		if json.Unmarshal([]byte(content), &parsed) == nil {
			suggestions = append(suggestions, parsed.Suggestions...)
		}
	}
	return suggestions, nil
}

func collectAuthorIDsFromGroups(uncertain []map[string]interface{}, groups []json.RawMessage) map[int]bool {
	ids := map[int]bool{}
	for _, s := range uncertain {
		gi, _ := s["group_index"].(float64)
		idx := int(gi)
		if idx < 0 || idx >= len(groups) {
			continue
		}
		var group struct {
			Canonical struct {
				ID int `json:"id"`
			} `json:"canonical"`
			Variants []struct {
				ID int `json:"id"`
			} `json:"variants"`
		}
		if json.Unmarshal(groups[idx], &group) == nil {
			if group.Canonical.ID > 0 {
				ids[group.Canonical.ID] = true
			}
			for _, v := range group.Variants {
				if v.ID > 0 {
					ids[v.ID] = true
				}
			}
		}
	}
	return ids
}

func buildBookEvidence(group map[string]interface{}, booksByAuthor map[int][]BookInfo) map[string][]string {
	evidence := map[string][]string{}
	if canonical, ok := group["canonical"].(map[string]interface{}); ok {
		if id, ok := canonical["id"].(float64); ok {
			name, _ := canonical["name"].(string)
			if books, ok := booksByAuthor[int(id)]; ok {
				var titles []string
				for _, b := range books {
					t := b.Title
					if b.Series != "" {
						t += " (" + b.Series + ")"
					}
					titles = append(titles, t)
				}
				evidence[name] = titles
			}
		}
	}
	if variants, ok := group["variants"].([]interface{}); ok {
		for _, vi := range variants {
			v, ok := vi.(map[string]interface{})
			if !ok {
				continue
			}
			if id, ok := v["id"].(float64); ok {
				name, _ := v["name"].(string)
				if books, ok := booksByAuthor[int(id)]; ok {
					var titles []string
					for _, b := range books {
						t := b.Title
						if b.Series != "" {
							t += " (" + b.Series + ")"
						}
						titles = append(titles, t)
					}
					evidence[name] = titles
				}
			}
		}
	}
	return evidence
}

// BookInfo holds a book title and series for enrichment.
type BookInfo struct {
	Title  string
	Series string
}

func fetchBooksForAuthorIDs(serverURL string, authorIDs map[int]bool) (map[int][]BookInfo, error) {
	httpClient := newInsecureClient()

	books := map[int][]BookInfo{}
	offset := 0
	pageSize := 1000
	for {
		url := fmt.Sprintf("%s/api/v1/audiobooks?limit=%d&offset=%d", serverURL, pageSize, offset)
		resp, err := httpClient.Get(url)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()

		for _, raw := range page.Items {
			var book struct {
				Title    string `json:"title"`
				Series   string `json:"series"`
				AuthorID int    `json:"author_id"`
			}
			if json.Unmarshal(raw, &book) != nil {
				continue
			}
			if authorIDs[book.AuthorID] && len(books[book.AuthorID]) < 10 {
				books[book.AuthorID] = append(books[book.AuthorID], BookInfo{Title: book.Title, Series: book.Series})
			}
		}

		if len(page.Items) < pageSize {
			break
		}
		offset += pageSize
	}
	return books, nil
}

// newInsecureClient creates an HTTP client that skips TLS verification.
func newInsecureClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 30 * time.Second,
	}
}

// submitSingleBatch submits a single request as a batch job.
func submitSingleBatch(ctx context.Context, apiKey, model, systemPrompt, userPrompt, customID, runDir string) error {
	clientOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := config.AppConfig.OpenAIBaseURL; baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(clientOpts...)

	isReasoning := strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "gpt-5")

	maxTokens := int64(16000)
	body := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_completion_tokens": maxTokens,
	}
	if !isReasoning {
		body["temperature"] = 0.0
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

	_ = os.WriteFile(filepath.Join(runDir, "batch_input.jsonl"), buf.Bytes(), 0664)

	file, err := client.Files.New(ctx, openai.FileNewParams{
		File:    bytes.NewReader(buf.Bytes()),
		Purpose: openai.FilePurposeBatch,
	})
	if err != nil {
		return fmt.Errorf("upload file: %w", err)
	}

	batch, err := client.Batches.New(ctx, openai.BatchNewParams{
		InputFileID:      file.ID,
		Endpoint:         openai.BatchNewParamsEndpointV1ChatCompletions,
		CompletionWindow: openai.BatchNewParamsCompletionWindow24h,
	})
	if err != nil {
		return fmt.Errorf("create batch: %w", err)
	}

	jobInfo := []BatchJobInfo{{
		BatchID:     batch.ID,
		Config:      TestConfig{Model: model, PromptVariant: customID},
		Mode:        "pass2",
		NumChunks:   1,
		NumRequests: 1,
		InputFileID: file.ID,
		RunDir:      runDir,
	}}
	_ = writeJSON(filepath.Join(runDir, "batch_jobs.json"), jobInfo)

	slog.Info("Submitted batch", "batch_id", batch.ID)
	slog.Info("Check results later", "command", "dedup-bench check " + runDir)
	return nil
}
