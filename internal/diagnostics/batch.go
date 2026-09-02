// file: internal/diagnostics/batch.go
// version: 1.1.1
// guid: b4c5d6e7-f8a9-40b1-c2d3-e4f5a6b7c8d9
// last-edited: 2026-09-02

package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	BatchChunkBooks  = 500
	BatchChunkLogs   = 200
	BatchChunkItunes = 500
	BatchModel       = "o4-mini"
	BatchMaxTokens   = 16000
	BatchTemperature = 0.1
)

// batchRequest represents a single line in OpenAI batch JSONL format.
type batchRequest struct {
	CustomID string         `json:"custom_id"`
	Method   string         `json:"method"`
	URL      string         `json:"url"`
	Body     map[string]any `json:"body"`
}

var categorySystemPrompts = map[string]string{
	"deduplication": `You are an audiobook library analyst. Analyze these audiobook records and identify: 1) DUPLICATE BOOKS (same audiobook, different records, not version-linked), 2) ORPHAN TRACKS (individual chapters imported as books), 3) MISSING MERGES (same audiobook in different formats needing version-linking). Output ONLY a JSON array of objects with: action (merge_versions|delete_orphan|fix_metadata), book_ids, primary_id (for merges), reason, fix (for metadata).`,

	"error_analysis": `You are a system diagnostics analyst. Analyze these log entries and operation records. Identify: 1) ERROR PATTERNS (recurring failures), 2) ROOT CAUSES (what's causing failures), 3) SUGGESTED FIXES. Output ONLY a JSON array of objects with: action (fix_metadata|delete_orphan), book_ids (if applicable), reason, fix.`,

	"metadata_quality": `You are an audiobook metadata quality analyst. Analyze these records and identify: 1) WRONG AUTHORS (narrator listed as author), 2) BAD TITLES (track names, garbled text), 3) MISSING SERIES (books that should be in a series), 4) WRONG SERIES ASSIGNMENTS. Output ONLY a JSON array of objects with: action (fix_metadata|reassign_series), book_ids, reason, fix.`,
}

// GetSystemPrompt returns the appropriate system prompt for the given category.
func GetSystemPrompt(category string) string {
	if prompt, ok := categorySystemPrompts[category]; ok {
		return prompt
	}
	// "general" combines all prompts
	return categorySystemPrompts["deduplication"] + "\n\n" +
		categorySystemPrompts["error_analysis"] + "\n\n" +
		categorySystemPrompts["metadata_quality"]
}

// BuildBatchJSONL constructs the OpenAI batch JSONL payload for diagnostics analysis.
// Data is chunked into manageable pieces per request line.
func BuildBatchJSONL(category, description string, books []SlimBook, itunesAlbums []ITunesAlbumSummary, logs, operationsData any) ([]byte, error) {
	systemPrompt := GetSystemPrompt(category)
	var buf bytes.Buffer
	chunkIdx := 0

	// Chunk books
	for start := 0; start < len(books); start += BatchChunkBooks {
		end := min(start+BatchChunkBooks, len(books))
		chunk := books[start:end]

		chunkJSON, err := json.Marshal(chunk)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal book chunk: %w", err)
		}

		userContent := fmt.Sprintf("Category: %s\nDescription: %s\n\nBooks (chunk %d, %d records):\n%s",
			category, description, chunkIdx+1, len(chunk), string(chunkJSON))

		if err := writeRequestLine(&buf, chunkIdx, systemPrompt, userContent); err != nil {
			return nil, err
		}
		chunkIdx++
	}

	// If no books were written, write at least one request with empty data
	if len(books) == 0 {
		userContent := fmt.Sprintf("Category: %s\nDescription: %s\n\nNo books in library.", category, description)
		if err := writeRequestLine(&buf, chunkIdx, systemPrompt, userContent); err != nil {
			return nil, err
		}
		chunkIdx++
	}

	// Chunk iTunes albums if present
	if len(itunesAlbums) > 0 {
		for start := 0; start < len(itunesAlbums); start += BatchChunkItunes {
			end := min(start+BatchChunkItunes, len(itunesAlbums))
			chunk := itunesAlbums[start:end]

			chunkJSON, err := json.Marshal(chunk)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal iTunes chunk: %w", err)
			}

			userContent := fmt.Sprintf("Category: %s\nDescription: %s\n\niTunes Albums (chunk, %d records):\n%s",
				category, description, len(chunk), string(chunkJSON))

			if err := writeRequestLine(&buf, chunkIdx, systemPrompt, userContent); err != nil {
				return nil, err
			}
			chunkIdx++
		}
	}

	// Chunk logs if present
	if logs != nil {
		logsJSON, err := json.Marshal(logs)
		if err == nil && len(logsJSON) > 2 { // not just "[]"
			// Parse as array and chunk
			var logEntries []any
			if json.Unmarshal(logsJSON, &logEntries) == nil && len(logEntries) > 0 {
				for start := 0; start < len(logEntries); start += BatchChunkLogs {
					end := min(start+BatchChunkLogs, len(logEntries))
					chunk := logEntries[start:end]

					chunkJSON, marshalErr := json.Marshal(chunk)
					if marshalErr != nil {
						continue
					}

					userContent := fmt.Sprintf("Category: %s\nDescription: %s\n\nLog entries (chunk, %d records):\n%s",
						category, description, len(chunk), string(chunkJSON))

					if err := writeRequestLine(&buf, chunkIdx, systemPrompt, userContent); err != nil {
						return nil, err
					}
					chunkIdx++
				}
			}
		}
	}

	return buf.Bytes(), nil
}

// writeRequestLine writes a single batch request line to the buffer.
func writeRequestLine(buf *bytes.Buffer, idx int, systemPrompt, userContent string) error {
	req := batchRequest{
		CustomID: fmt.Sprintf("chunk-%03d", idx),
		Method:   "POST",
		URL:      "/v1/chat/completions",
		Body: map[string]any{
			"model":       BatchModel,
			"max_tokens":  BatchMaxTokens,
			"temperature": BatchTemperature,
			"messages": []map[string]string{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": userContent},
			},
		},
	}

	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal batch request: %w", err)
	}
	buf.Write(line)
	buf.WriteByte('\n')
	return nil
}
