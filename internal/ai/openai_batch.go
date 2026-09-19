// file: internal/ai/openai_batch.go
// version: 1.9.0
// guid: b3c4d5e6-f7a8-9b0c-1d2e-3f4a5b6c7d8e
// last-edited: 2026-09-19

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/pagination"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// Batch metadata keys that name the local record a batch belongs to. They are
// written at CreateBatch time so a batch whose id never reached the store (the
// process died between OpenAI accepting the batch and the local write that
// records its id) can still be matched back to its owner from a batch listing.
const (
	// BatchMetaScanID is the ai.author-scan scan id that submitted the batch.
	BatchMetaScanID = "scan_id"
	// BatchMetaScanPhase is the scan phase type ("groups_scan", "full_scan").
	BatchMetaScanPhase = "scan_phase"
)

// batchMetadata returns standard metadata tags for OpenAI batch creation, plus
// any caller-supplied owner keys. The standard keys always win: an extra
// "project" or "type" would hide the batch from ListProjectBatches or misroute
// it in the poller.
func batchMetadata(batchType string, extra map[string]string) shared.Metadata {
	md := shared.Metadata{}
	for k, v := range extra {
		md[k] = v
	}
	md["project"] = "audiobook-organizer"
	md["service"] = "ao-metadata-batch"
	md["type"] = batchType
	return md
}

// BatchInfo holds information about a project-tagged OpenAI batch.
type BatchInfo struct {
	ID            string
	Status        string
	Type          string
	OutputFileID  string
	ErrorFileID   string
	RequestCounts RequestCounts
	// CreatedAt is when OpenAI created the batch.
	CreatedAt time.Time
	// Metadata is the batch's full metadata map, including the owner keys
	// (aijobs.MetadataJobIDKey, BatchMetaScanID, ...) set at creation.
	Metadata map[string]string
}

// RequestCounts holds batch request count information.
type RequestCounts struct {
	Total     int
	Completed int
	Failed    int
}

// UploadBatchFile uploads a JSONL buffer as a batch input file and returns the file ID.
func (p *OpenAIParser) UploadBatchFile(ctx context.Context, data io.Reader) (string, error) {
	if !p.enabled {
		return "", fmt.Errorf("OpenAI parser is not enabled")
	}

	file, err := p.client.Files.New(ctx, openai.FileNewParams{
		File:    data,
		Purpose: openai.FilePurposeBatch,
	})
	if err != nil {
		return "", fmt.Errorf("upload batch file: %w", err)
	}
	return file.ID, nil
}

// UploadBatchFileBytes is a convenience for aijobs which already has a []byte buffer.
func (p *OpenAIParser) UploadBatchFileBytes(ctx context.Context, data []byte) (string, error) {
	return p.UploadBatchFile(ctx, bytes.NewReader(data))
}

// CreateBatchWithMetadata creates a batch tagged with the project metadata,
// batchType, and extra owner keys (nil for none).
func (p *OpenAIParser) CreateBatchWithMetadata(ctx context.Context, fileID string, batchType string, extra map[string]string) (string, error) {
	if !p.enabled {
		return "", fmt.Errorf("OpenAI parser is not enabled")
	}

	batch, err := p.client.Batches.New(ctx, openai.BatchNewParams{
		InputFileID:      fileID,
		Endpoint:         openai.BatchNewParamsEndpointV1ChatCompletions,
		CompletionWindow: openai.BatchNewParamsCompletionWindow24h,
		Metadata:         batchMetadata(batchType, extra),
	})
	if err != nil {
		return "", fmt.Errorf("create batch: %w", err)
	}
	return batch.ID, nil
}

// maxBatchListPages bounds ListProjectBatches' walk back through history. At
// 100 batches a page that is 5,000 batches — far past OpenAI's retention of
// anything a pending job could still be waiting on.
const maxBatchListPages = 50

// ListProjectBatches lists recent batches tagged with our project metadata.
//
// With a zero since it reads one page (the 100 most recent batches of the
// whole OpenAI project, filtered to ours afterwards — so fewer than 100 of
// ours). With a non-zero since it keeps paging until a page reaches batches
// created before since, so a job whose batch id was never recorded is found
// however much other traffic the project has had since.
func (p *OpenAIParser) ListProjectBatches(ctx context.Context, since time.Time) ([]BatchInfo, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}

	page, err := p.client.Batches.List(ctx, openai.BatchListParams{
		Limit: param.NewOpt[int64](100),
	})
	if err != nil {
		return nil, fmt.Errorf("list batches: %w", err)
	}

	var results []BatchInfo
	for pages := 1; page != nil; pages++ {
		reachedSince := false
		for _, b := range page.Data {
			if !since.IsZero() && time.Unix(b.CreatedAt, 0).Before(since) {
				reachedSince = true
			}
			if b.Metadata == nil || b.Metadata["project"] != "audiobook-organizer" {
				continue
			}
			results = append(results, batchInfoFrom(b))
		}
		if since.IsZero() || reachedSince || len(page.Data) == 0 || pages >= maxBatchListPages {
			break
		}
		page, err = page.GetNextPage()
		if err != nil {
			return results, fmt.Errorf("list batches (page %d): %w", pages+1, err)
		}
	}
	return results, nil
}

// GetBatch fetches one batch by id, whatever its age. The poller uses it for
// batches the store is still waiting on, so a retry never depends on the batch
// being in the recent listing.
func (p *OpenAIParser) GetBatch(ctx context.Context, batchID string) (BatchInfo, error) {
	if !p.enabled {
		return BatchInfo{}, fmt.Errorf("OpenAI parser is not enabled")
	}
	b, err := p.client.Batches.Get(ctx, batchID)
	if err != nil {
		return BatchInfo{}, fmt.Errorf("get batch %s: %w", batchID, err)
	}
	return batchInfoFrom(*b), nil
}

func batchInfoFrom(b openai.Batch) BatchInfo {
	md := make(map[string]string, len(b.Metadata))
	for k, v := range b.Metadata {
		md[k] = v
	}
	return BatchInfo{
		Metadata:     md,
		ID:           b.ID,
		Status:       string(b.Status),
		Type:         md["type"],
		OutputFileID: b.OutputFileID,
		ErrorFileID:  b.ErrorFileID,
		CreatedAt:    time.Unix(b.CreatedAt, 0),
		RequestCounts: RequestCounts{
			Total:     int(b.RequestCounts.Total),
			Completed: int(b.RequestCounts.Completed),
			Failed:    int(b.RequestCounts.Failed),
		},
	}
}

// Ensure pagination import is used.
var _ = (*pagination.CursorPage[openai.Batch])(nil)

// BatchRequest represents a single request in a JSONL batch file.
type BatchRequest struct {
	CustomID string         `json:"custom_id"`
	Method   string         `json:"method"`
	URL      string         `json:"url"`
	Body     map[string]any `json:"body"`
}

// BatchResponse represents a single response line from a batch result file.
type BatchResponse struct {
	ID       string `json:"id"`
	CustomID string `json:"custom_id"`
	Response struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
	} `json:"response"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// CreateBatchAuthorDedup creates a batch job for author dedup (full mode).
// Returns the batch ID for polling. extra carries owner keys for the batch
// metadata (nil for none).
func (p *OpenAIParser) CreateBatchAuthorDedup(ctx context.Context, inputs []AuthorDiscoveryInput, extra map[string]string) (string, error) {
	if !p.enabled {
		return "", fmt.Errorf("OpenAI parser is not enabled")
	}
	if len(inputs) == 0 {
		return "", fmt.Errorf("no inputs provided")
	}

	body, err := authorDedupFullBody(p.metadataReviewModel(), inputs) // batch author dedup uses MetadataReviewModel
	if err != nil {
		return "", err
	}

	// Build JSONL with a single request
	req := BatchRequest{
		CustomID: "author-dedup-full",
		Method:   "POST",
		URL:      "/v1/chat/completions",
		Body:     body,
	}

	var buf bytes.Buffer
	line, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal batch request: %w", err)
	}
	buf.Write(line)
	buf.WriteByte('\n')

	// Upload the JSONL file
	file, err := p.client.Files.New(ctx, openai.FileNewParams{
		File:    &buf,
		Purpose: openai.FilePurposeBatch,
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload batch file: %w", err)
	}

	// Create the batch
	batch, err := p.client.Batches.New(ctx, openai.BatchNewParams{
		InputFileID:      file.ID,
		Endpoint:         openai.BatchNewParamsEndpointV1ChatCompletions,
		CompletionWindow: openai.BatchNewParamsCompletionWindow24h,
		Metadata:         batchMetadata("author_dedup", extra),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create batch: %w", err)
	}

	return batch.ID, nil
}

// CheckBatchStatus checks the status of a batch job.
func (p *OpenAIParser) CheckBatchStatus(ctx context.Context, batchID string) (string, string, error) {
	if !p.enabled {
		return "", "", fmt.Errorf("OpenAI parser is not enabled")
	}

	batch, err := p.client.Batches.Get(ctx, batchID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get batch: %w", err)
	}

	return string(batch.Status), batch.OutputFileID, nil
}

// CancelBatch cancels a pending or in-progress batch job.
func (p *OpenAIParser) CancelBatch(ctx context.Context, batchID string) error {
	if !p.enabled {
		return fmt.Errorf("OpenAI parser is not enabled")
	}

	_, err := p.client.Batches.Cancel(ctx, batchID)
	if err != nil {
		return fmt.Errorf("failed to cancel batch %s: %w", batchID, err)
	}
	return nil
}

// DownloadBatchResults downloads and parses results from a completed batch.
func (p *OpenAIParser) DownloadBatchResults(ctx context.Context, outputFileID string) ([]AuthorDiscoverySuggestion, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}

	resp, err := p.client.Files.Content(ctx, outputFileID)
	if err != nil {
		return nil, fmt.Errorf("failed to download batch results: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read batch results: %w", err)
	}

	var allSuggestions []AuthorDiscoverySuggestion

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line
	for scanner.Scan() {
		var batchResp BatchResponse
		if err := json.Unmarshal(scanner.Bytes(), &batchResp); err != nil {
			continue
		}
		if batchResp.Error != nil {
			continue
		}
		if batchResp.Response.StatusCode != 200 {
			continue
		}

		// Parse the chat completion response
		var completion struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(batchResp.Response.Body, &completion); err != nil {
			continue
		}
		if len(completion.Choices) == 0 {
			continue
		}

		var result struct {
			Suggestions []AuthorDiscoverySuggestion `json:"suggestions"`
		}
		if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
			continue
		}
		allSuggestions = append(allSuggestions, result.Suggestions...)
	}

	return allSuggestions, nil
}

// CreateBatchAuthorReview creates a batch job for author dedup groups (groups mode).
// Returns the batch ID for polling. extra carries owner keys for the batch
// metadata (nil for none).
func (p *OpenAIParser) CreateBatchAuthorReview(ctx context.Context, groups []AuthorDedupInput, extra map[string]string) (string, error) {
	if !p.enabled {
		return "", fmt.Errorf("OpenAI parser is not enabled")
	}
	if len(groups) == 0 {
		return "", fmt.Errorf("no groups provided")
	}

	// Reuse the same system prompt as reviewAuthorBatch
	systemPrompt := `You are an expert audiobook metadata reviewer. You will receive groups of potentially duplicate author names. For each group, determine the correct action:

- "merge": The variants are the same author with different name formats. Provide the correct canonical name.
- "split": The names represent different people incorrectly grouped together.
- "rename": The canonical name needs correction.
- "skip": The group is fine as-is or you're unsure.
- "reclassify": Entry is not an author at all (narrator/publisher misclassified as author).
- "alias": Pen names or stage names for the same person.

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".

Return ONLY valid JSON: {"suggestions": [{"group_index": N, "action": "merge|split|rename|skip|alias|reclassify", "canonical_name": "Correct Name", "reason": "brief explanation", "confidence": "high|medium|low", "is_narrator": [indices], "is_publisher": [indices]}]}`

	batchJSON, err := json.Marshal(groups)
	if err != nil {
		return "", fmt.Errorf("failed to marshal groups: %w", err)
	}

	userPrompt := fmt.Sprintf("Review these duplicate author groups:\n\n%s", string(batchJSON))

	req := BatchRequest{
		CustomID: "author-dedup-groups",
		Method:   "POST",
		URL:      "/v1/chat/completions",
		Body: map[string]any{
			"model": p.metadataReviewModel(), // batch author dedup uses MetadataReviewModel
			"messages": []map[string]string{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": userPrompt},
			},
			"max_completion_tokens": 32000,
			"response_format":       map[string]string{"type": "json_object"},
		},
	}

	var buf bytes.Buffer
	line, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal batch request: %w", err)
	}
	buf.Write(line)
	buf.WriteByte('\n')

	file, err := p.client.Files.New(ctx, openai.FileNewParams{
		File:    &buf,
		Purpose: openai.FilePurposeBatch,
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload batch file: %w", err)
	}

	batch, err := p.client.Batches.New(ctx, openai.BatchNewParams{
		InputFileID:      file.ID,
		Endpoint:         openai.BatchNewParamsEndpointV1ChatCompletions,
		CompletionWindow: openai.BatchNewParamsCompletionWindow24h,
		Metadata:         batchMetadata("author_review", extra),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create batch: %w", err)
	}

	return batch.ID, nil
}

// DownloadBatchGroupsResults downloads and parses results from a completed groups batch.
func (p *OpenAIParser) DownloadBatchGroupsResults(ctx context.Context, outputFileID string) ([]AuthorDedupSuggestion, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}

	resp, err := p.client.Files.Content(ctx, outputFileID)
	if err != nil {
		return nil, fmt.Errorf("failed to download batch results: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read batch results: %w", err)
	}

	var allSuggestions []AuthorDedupSuggestion

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		var batchResp BatchResponse
		if err := json.Unmarshal(scanner.Bytes(), &batchResp); err != nil {
			continue
		}
		if batchResp.Error != nil {
			continue
		}
		if batchResp.Response.StatusCode != 200 {
			continue
		}

		var completion struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(batchResp.Response.Body, &completion); err != nil {
			continue
		}
		if len(completion.Choices) == 0 {
			continue
		}

		var result struct {
			Suggestions []AuthorDedupSuggestion `json:"suggestions"`
		}
		if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
			continue
		}
		allSuggestions = append(allSuggestions, result.Suggestions...)
	}

	return allSuggestions, nil
}

// BatchRawResult holds one response from a batch API result file.
type BatchRawResult struct {
	CustomID string `json:"custom_id"`
	Content  string `json:"content"`
	Error    string `json:"error,omitempty"`
}

// DownloadBatchRaw downloads batch results and returns raw response content.
func (p *OpenAIParser) DownloadBatchRaw(ctx context.Context, outputFileID string) ([]BatchRawResult, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}

	resp, err := p.client.Files.Content(ctx, outputFileID)
	if err != nil {
		return nil, fmt.Errorf("download batch output: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read batch output: %w", err)
	}

	return ParseBatchRawResults(data)
}

// ParseBatchRawResults parses JSONL batch output into raw results.
func ParseBatchRawResults(data []byte) ([]BatchRawResult, error) {
	var results []BatchRawResult
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry struct {
			CustomID string `json:"custom_id"`
			Response struct {
				Body struct {
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
				} `json:"body"`
			} `json:"response"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		result := BatchRawResult{CustomID: entry.CustomID}
		if entry.Error != nil {
			result.Error = entry.Error.Message
		} else if len(entry.Response.Body.Choices) > 0 {
			result.Content = entry.Response.Body.Choices[0].Message.Content
		}
		results = append(results, result)
	}
	return results, scanner.Err()
}

// BuildBatchAuthorDedupRequest creates the messages for a batch-compatible request.
// Used by both real-time and batch paths.
func (p *OpenAIParser) BuildAuthorDedupMessages(inputs []AuthorDiscoveryInput) (system string, user string, model shared.ChatModel) {
	system = `You are an expert audiobook metadata reviewer. Find groups of authors that are likely the same person.

Return ONLY valid JSON: {"suggestions": [{"author_ids": [1, 42], "action": "merge|rename|split|skip|alias", "canonical_name": "Correct Name", "reason": "brief explanation", "confidence": "high|medium|low"}]}`

	batchJSON, _ := json.Marshal(inputs)
	user = fmt.Sprintf("Find duplicate authors in this list:\n\n%s", string(batchJSON))
	model = shared.ChatModel(p.metadataReviewModel()) // BuildAuthorDedupMessages uses MetadataReviewModel
	return
}

// authorDedupFullSystemPrompt is the whole-library author dedup prompt, shared
// by the ai.author-scan full_scan batch and maintenance.ai-dedup-batch.
const authorDedupFullSystemPrompt = `You are an expert audiobook metadata reviewer. You will receive a list of authors with their IDs, book counts, and sample book titles. Find groups of authors that are likely the same person (different name formats, typos, abbreviations, last-name-first, etc).

CRITICAL RULES:
- COMPOUND NAMES: Many author entries contain multiple people separated by commas, ampersands, "and", or semicolons. When you find a compound entry that matches an individual author entry, suggest a merge with the individual as canonical.
- Use sample_titles to distinguish authors from narrators.
- NEVER merge two genuinely different people.
- Only merge when names clearly refer to the same person.
- If unsure, use action "skip".
- Identify narrators or publishers incorrectly listed as authors.
- INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".
- PEN NAMES & ALIASES: When names are clearly pen names or handles, use action "alias" instead of "merge".

Return ONLY valid JSON: {"suggestions": [{"author_ids": [1, 42], "action": "merge|rename|split|skip|alias", "canonical_name": "Correct Name", "reason": "brief explanation", "confidence": "high|medium|low", "is_narrator": [ids], "is_publisher": [ids]}]}

Only include groups where you find actual duplicates or issues.`

// authorDedupFullBody is the /v1/chat/completions body for one whole-library
// author dedup request.
func authorDedupFullBody(model string, inputs []AuthorDiscoveryInput) (map[string]any, error) {
	batchJSON, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal inputs: %w", err)
	}
	return map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": authorDedupFullSystemPrompt},
			{"role": "user", "content": fmt.Sprintf("Find duplicate authors in this list:\n\n%s", string(batchJSON))},
		},
		"max_completion_tokens": 16000,
		"response_format":       map[string]string{"type": "json_object"},
	}, nil
}
