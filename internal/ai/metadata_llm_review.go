// file: internal/ai/metadata_llm_review.go
// version: 3.4.0
// guid: e4f92b17-3c8a-4d65-a1f3-9b2e07d84c61
// last-edited: 2026-06-22

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// MetadataLLMQuery describes the book the caller is searching metadata for.
// It's the AI-package-local view of ai.Query, kept separate so the JSON
// field names used in the LLM prompt are frozen regardless of future changes
// to the public scorer interface.
type MetadataLLMQuery struct {
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
}

// MetadataLLMCandidate is one search result the LLM ranks against the query.
type MetadataLLMCandidate struct {
	Index    int    `json:"index"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
}

// MetadataLLMScore is the LLM's judgment for a single candidate. Score is
// in [0.0, 1.0] where 1.0 means "definitely the same book." Reason is a
// short one-sentence explanation suitable for display in a debug log or UI
// tooltip.
type MetadataLLMScore struct {
	Index  int     `json:"index"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// metadataLLMBatchSize caps the number of candidates sent per chat request.
// Matches dedupReviewBatchSize in dedup_review.go — 25 is comfortably under
// the structured-JSON token limits with typical per-candidate payloads.
const metadataLLMBatchSize = 25

// ScoreMetadataCandidates asks the chat LLM to rank candidate metadata search
// results against a query book. It batches inputs internally and returns one
// score per candidate, in input order. Indices in the response are used to
// route scores back to their input slot — missing indices default to 0.0
// (the caller should treat them as "LLM didn't rank this one, use the base
// score instead").
//
// Returns (nil, err) on any failure so callers can fall back to the base
// scorer — no partial results with a nil error.
func (p *OpenAIParser) ScoreMetadataCandidates(
	ctx context.Context,
	query MetadataLLMQuery,
	candidates []MetadataLLMCandidate,
) ([]MetadataLLMScore, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Ensure every candidate carries a sequential index so the LLM can
	// reference them unambiguously. We don't trust the caller to pre-number.
	indexed := make([]MetadataLLMCandidate, len(candidates))
	for i, c := range candidates {
		c.Index = i
		indexed[i] = c
	}

	var all []MetadataLLMScore
	for start := 0; start < len(indexed); start += metadataLLMBatchSize {
		end := start + metadataLLMBatchSize
		if end > len(indexed) {
			end = len(indexed)
		}
		batch := indexed[start:end]
		scores, err := p.scoreMetadataBatch(ctx, query, batch)
		if err != nil {
			return all, fmt.Errorf("metadata LLM batch [%d:%d]: %w", start, end, err)
		}
		all = append(all, scores...)
	}
	return all, nil
}

// PRIORITY: Interactive
// scoreMetadataBatch sends a synchronous chat-completion request ranking
// candidate metadata. Called during user-initiated metadata search where
// the UI is awaiting the ranked response; latency is the primary concern,
// so this MUST stay on the sync endpoint. Bulk callers (if any are ever
// added) should use a separate SubmitMetadataReviewJob built via aijobs.
func (p *OpenAIParser) scoreMetadataBatch(
	ctx context.Context,
	query MetadataLLMQuery,
	batch []MetadataLLMCandidate,
) ([]MetadataLLMScore, error) {
	systemPrompt := `You are an expert audiobook metadata reviewer. You will receive one query book and a batch of candidate search results. For each candidate, score how well it matches the query on a scale from 0.0 to 1.0, where:

- 1.0 = definitely the same book (same title and author, allowing minor punctuation/subtitle differences)
- 0.7-0.9 = probably the same book (same title core, same author, minor edition differences)
- 0.4-0.6 = ambiguous (partial title match, unclear author)
- 0.0-0.3 = probably not the same book (different volumes in a series, unrelated titles)

Scoring rules:
- Title identity matters most. "The Way of Kings" and "Stormlight Archive 1: The Way of Kings" are the same book.
- Author match is a strong signal. Same title with a different author is usually a different book.
- Narrator differences do NOT reduce the score — re-recordings of the same book are still the same book.
- Series position mismatches (volume 6 vs volume 3) should score low (~0.2) even if the series name matches.
- Compilations and omnibus editions should score low (~0.3) unless the query is itself an omnibus.

Return ONLY valid JSON in this exact shape:
{"scores": [{"index": N, "score": 0.0-1.0, "reason": "one-sentence explanation"}]}

Include one score per input candidate, using the same index as the input.`

	payload := struct {
		Query      MetadataLLMQuery       `json:"query"`
		Candidates []MetadataLLMCandidate `json:"candidates"`
	}{
		Query:      query,
		Candidates: batch,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	userPrompt := fmt.Sprintf("Rank these candidate metadata search results against the query book:\n\n%s", string(payloadJSON))

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	var scores []MetadataLLMScore
	if err := DoWithRetry(ctx, p.maxRetries+1, 2*time.Second, func() error {
		completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(systemPrompt),
				openai.UserMessage(userPrompt),
			},
			Model:               shared.ChatModel(p.metadataReviewModel()), // metadata LLM review uses MetadataReviewModel
			MaxCompletionTokens: param.NewOpt[int64](8000),
			PromptCacheKey:       param.NewOpt("audiobook-metadata-score-v1"),
			PromptCacheRetention: openai.ChatCompletionNewParamsPromptCacheRetention24h,
			ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &jsonObjectFormat,
			},
			User: openai.String("ao-metadata-review"),
		})
		if err != nil {
			return fmt.Errorf("OpenAI API call failed: %w", err)
		}
		if len(completion.Choices) == 0 {
			return fmt.Errorf("no response from OpenAI")
		}
		var result struct {
			Scores []MetadataLLMScore `json:"scores"`
		}
		if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
			return fmt.Errorf("parse response: %w", err)
		}
		scores = result.Scores
		return nil
	}); err != nil {
		return nil, err
	}
	return scores, nil
}
