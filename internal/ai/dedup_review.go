// file: internal/ai/dedup_review.go
// version: 2.0.2
// guid: b2e7c3d1-4a58-4f96-9e0b-7d3a1c8f5b24
// last-edited: 2026-07-03

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/ai/aijobs"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"log/slog"
)

// DedupEntity describes one side of a duplicate-pair candidate for LLM review.
// Fields are optional — empty strings are elided from the prompt.
type DedupEntity struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
	Series   string `json:"series,omitempty"`
	ISBN     string `json:"isbn,omitempty"`
	ASIN     string `json:"asin,omitempty"`
	// For author entities, Title holds the author name and other fields are empty.
}

// DedupPairInput is one pair (A, B) the caller wants the LLM to adjudicate.
// The caller assigns Index so responses can be matched back.
type DedupPairInput struct {
	Index      int         `json:"index"`
	EntityType string      `json:"entity_type"` // "book" or "author"
	A          DedupEntity `json:"a"`
	B          DedupEntity `json:"b"`
	Similarity float64     `json:"similarity"`
}

// DedupPairVerdict is the LLM's judgment for a single pair.
type DedupPairVerdict struct {
	Index       int    `json:"index"`
	IsDuplicate bool   `json:"is_duplicate"`
	Confidence  string `json:"confidence"` // "high" | "medium" | "low"
	Reason      string `json:"reason"`
}

// dedupReviewBatchSize is the maximum number of pairs sent in one batch request row.
// Chosen to stay well under output-token limits with typical per-pair payloads.
const dedupReviewBatchSize = 25

// dedupReviewSystemPrompt is the system instruction for the LLM reviewer.
const dedupReviewSystemPrompt = `You are an expert audiobook metadata reviewer. You will receive a batch of candidate duplicate pairs (either book pairs or author pairs). For each pair, decide whether A and B describe the same thing.

Rules for BOOK pairs:
- Same book: same title (allowing minor punctuation/subtitle differences), same primary author. Narrator differences are allowed — different editions or re-recordings of the same book are still the same book.
- Examples of SAME book: "The Way of Kings" vs "Stormlight Archive 1 - The Way of Kings"; "Dune" vs "Dune (Unabridged)"; same ISBN or ASIN.
- Examples of DIFFERENT books: same series but different volumes; same title but different authors; "Dune" vs "Dune Messiah".

Rules for AUTHOR pairs:
- Same author: the same real person with a name variant (initial spacing, suffix, pen name, case folding).
- Examples of SAME author: "J.R.R. Tolkien" vs "J. R. R. Tolkien"; "Mark Twain" vs "Samuel Clemens" (pen name, same person).
- Examples of DIFFERENT authors: two real people who happen to share initials; a compound entry like "V. A. Lewis, Azrie" is NOT a duplicate of "V. A. Lewis" — it's a compound that needs splitting, but for THIS task, mark it as not a duplicate.

Confidence:
- "high": obvious match or obvious non-match
- "medium": likely but some ambiguity
- "low": genuinely unsure

Return ONLY valid JSON in this exact shape:
{"verdicts": [{"index": N, "is_duplicate": true|false, "confidence": "high|medium|low", "reason": "brief one-sentence explanation"}]}

Include one verdict per input pair, using the same index as the input.`

// DedupVerdictApplier applies LLM dedup verdicts back to the candidate store.
// Implemented by *dedup.Engine.
type DedupVerdictApplier interface {
	// ApplyVerdicts persists verdicts and may trigger auto-merges.
	// Returns the count of verdicts successfully applied.
	ApplyVerdicts(verdicts []DedupPairVerdict, byIndex map[int]database.DedupCandidate) int
	// LookupCandidate reloads a candidate by ID. Returns ok=false if the
	// candidate has since been deleted or purged.
	LookupCandidate(id int64) (database.DedupCandidate, bool)
}

// dedupReviewPayload is the serialized state persisted with each aijobs batch.
// It must include enough information to apply verdicts hours or days later,
// after the in-memory byIndex map is gone.
type dedupReviewPayload struct {
	Inputs  []DedupPairInput `json:"inputs"`
	ByIndex map[int]int64    `json:"by_index"` // pair Index → DedupCandidate.ID
}

var (
	dedupVerdictApplier DedupVerdictApplier
)

// SetDedupVerdictApplier registers the sink used by the completion callback.
// Must be called at startup before any batches complete.
func SetDedupVerdictApplier(applier DedupVerdictApplier) {
	dedupVerdictApplier = applier
}

// SubmitDedupReviewJob enqueues an aijobs batch for dedup LLM review.
// Inputs are split into sub-batches of dedupReviewBatchSize pairs.
// Returns the job ID immediately; verdicts apply asynchronously when the batch completes.
func SubmitDedupReviewJob(ctx context.Context, deps aijobs.Deps, model string, inputs []DedupPairInput, byIndex map[int]int64) (string, error) {
	if len(inputs) == 0 {
		return "", fmt.Errorf("no inputs to review")
	}

	// Persist the payload (inputs + candidate IDs) so the async callback can rebuild state.
	payload := dedupReviewPayload{
		Inputs:  inputs,
		ByIndex: byIndex,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	// Calculate sub-batch count for the SubmitRequest.ItemCount.
	subBatchCount := (len(inputs) + dedupReviewBatchSize - 1) / dedupReviewBatchSize

	// Build function that constructs JSONL rows (one per sub-batch).
	buildFn := func(i int) (aijobs.BatchRequest, error) {
		start := i * dedupReviewBatchSize
		end := start + dedupReviewBatchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		batch := inputs[start:end]

		batchJSON, err := json.Marshal(batch)
		if err != nil {
			return aijobs.BatchRequest{}, fmt.Errorf("marshal sub-batch %d: %w", i, err)
		}

		userPrompt := fmt.Sprintf("Review these candidate duplicate pairs:\n\n%s", string(batchJSON))

		body := map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "system", "content": dedupReviewSystemPrompt},
				{"role": "user", "content": userPrompt},
			},
			"max_completion_tokens": 8000,
			"response_format":       map[string]any{"type": "json_object"},
		}

		return aijobs.BatchRequest{Body: body, MaxTokens: 8000}, nil
	}

	return aijobs.Submit(ctx, deps, aijobs.SubmitRequest{
		Type:        "dedup_review",
		ItemCount:   subBatchCount,
		PayloadJSON: payloadJSON,
		Build:       buildFn,
	})
}

// dedupReviewCallback is the completion handler for dedup review batches.
// It deserializes the payload, reloads candidates fresh from the store,
// and applies verdicts through the injected applier.
func dedupReviewCallback(ctx context.Context, itemsJSON []byte, results []aijobs.RowResult) (successCount, errorCount int, rowErrors []database.AIJobRowError, fatalErr error) {
	// Deserialize the payload.
	var payload dedupReviewPayload
	if err := json.Unmarshal(itemsJSON, &payload); err != nil {
		return 0, 0, nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	// Rebuild byIndex by reloading candidates fresh from the store.
	// The batch may have taken hours or days; the original in-memory map is gone.
	byIndex := make(map[int]database.DedupCandidate, len(payload.ByIndex))
	for pairIdx, candID := range payload.ByIndex {
		c, ok := dedupVerdictApplier.LookupCandidate(candID)
		if !ok {
			slog.Info("dedup_review candidate (pair) not found — skipping", "candID", candID, "pairIdx", pairIdx)
			continue
		}
		byIndex[pairIdx] = c
	}

	// Process each row result.
	var allVerdicts []DedupPairVerdict
	for _, r := range results {
		if r.Err != "" {
			errorCount++
			rowErrors = append(rowErrors, database.AIJobRowError{
				CustomID: r.CustomID,
				Error:    r.Err,
			})
			continue
		}

		// Parse the row's JSON content.
		var parsed struct {
			Verdicts []DedupPairVerdict `json:"verdicts"`
		}
		if err := json.Unmarshal([]byte(r.Content), &parsed); err != nil {
			errorCount++
			rowErrors = append(rowErrors, database.AIJobRowError{
				CustomID: r.CustomID,
				Error:    fmt.Sprintf("parse: %v", err),
			})
			continue
		}

		allVerdicts = append(allVerdicts, parsed.Verdicts...)
		successCount++
	}

	// Apply verdicts through the injected applier.
	if dedupVerdictApplier == nil {
		return successCount, errorCount, rowErrors, fmt.Errorf("dedupVerdictApplier not set")
	}

	applied := dedupVerdictApplier.ApplyVerdicts(allVerdicts, byIndex)
	slog.Info("dedup_review callback applied verdicts (from successful rows, errors)", "applied", applied, "successCount", successCount, "errorCount", errorCount)

	return successCount, errorCount, rowErrors, nil
}

// init registers the callback at package load time.
func init() {
	aijobs.Register("dedup_review", dedupReviewCallback)
}
