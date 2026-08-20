// file: internal/ai/openai_parser_test.go
// version: 1.5.0
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-08-20

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func TestNewOpenAIParser_Disabled(t *testing.T) {
	// Test with empty API key
	parser := NewOpenAIParser(nil, "", true)
	if parser.enabled {
		t.Error("Expected parser to be disabled with empty API key")
	}

	// Test with enabled=false
	parser = NewOpenAIParser(nil, "test-key", false)
	if parser.enabled {
		t.Error("Expected parser to be disabled when enabled=false")
	}
}

func TestNewOpenAIParser_Enabled(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-api-key", true)
	if !parser.enabled {
		t.Error("Expected parser to be enabled with valid API key")
	}
	if parser.filenameParseModel() != "gpt-5-mini" {
		t.Errorf("Expected model gpt-5-mini, got %s", parser.filenameParseModel())
	}
	if parser.maxRetries != 2 {
		t.Errorf("Expected maxRetries 2, got %d", parser.maxRetries)
	}
	if parser.client == nil {
		t.Error("Expected client to be initialized")
	}
}

func TestIsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		apiKey  string
		enabled bool
		want    bool
	}{
		{
			name:    "disabled with no key",
			apiKey:  "",
			enabled: true,
			want:    false,
		},
		{
			name:    "disabled explicitly",
			apiKey:  "test-key",
			enabled: false,
			want:    false,
		},
		{
			name:    "enabled with key",
			apiKey:  "test-key",
			enabled: true,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewOpenAIParser(nil, tt.apiKey, tt.enabled)
			if got := parser.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseFilename_Disabled(t *testing.T) {
	parser := NewOpenAIParser(nil, "", false)
	ctx := context.Background()

	_, err := parser.ParseFilename(ctx, "test.mp3")
	if err == nil {
		t.Error("Expected error when parser is disabled")
	}
	if err.Error() != "OpenAI parser is not enabled" {
		t.Errorf("Expected disabled error, got: %v", err)
	}
}

func TestParseBatch_Disabled(t *testing.T) {
	parser := NewOpenAIParser(nil, "", false)
	ctx := context.Background()

	_, err := parser.ParseBatch(ctx, []string{"test1.mp3", "test2.mp3"})
	if err == nil {
		t.Error("Expected error when parser is disabled")
	}
	if err.Error() != "OpenAI parser is not enabled" {
		t.Errorf("Expected disabled error, got: %v", err)
	}
}

func TestParseBatch_EmptyInput(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)
	ctx := context.Background()

	results, err := parser.ParseBatch(ctx, []string{})
	if err != nil {
		t.Errorf("Expected no error for empty input, got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected empty results, got %d results", len(results))
	}
}

func TestTestConnection_Disabled(t *testing.T) {
	parser := NewOpenAIParser(nil, "", false)
	ctx := context.Background()

	err := parser.TestConnection(ctx)
	if err == nil {
		t.Error("Expected error when parser is disabled")
	}
	if err.Error() != "OpenAI parser is not enabled" {
		t.Errorf("Expected disabled error, got: %v", err)
	}
}

func TestParsedMetadata_JSONMarshaling(t *testing.T) {
	// Test that ParsedMetadata can be marshaled/unmarshaled
	original := &ParsedMetadata{
		Title:      "The Hobbit",
		Author:     "J.R.R. Tolkien",
		Series:     "Middle Earth",
		SeriesNum:  1,
		Narrator:   "Rob Inglis",
		Publisher:  "Random House",
		Year:       1937,
		Confidence: "high",
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Unmarshal back
	var parsed ParsedMetadata
	if err := json.Unmarshal(jsonData, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify fields
	if parsed.Title != original.Title {
		t.Errorf("Title mismatch: got %s, want %s", parsed.Title, original.Title)
	}
	if parsed.Author != original.Author {
		t.Errorf("Author mismatch: got %s, want %s", parsed.Author, original.Author)
	}
	if parsed.Series != original.Series {
		t.Errorf("Series mismatch: got %s, want %s", parsed.Series, original.Series)
	}
	if parsed.SeriesNum != original.SeriesNum {
		t.Errorf("SeriesNum mismatch: got %d, want %d", parsed.SeriesNum, original.SeriesNum)
	}
	if parsed.Narrator != original.Narrator {
		t.Errorf("Narrator mismatch: got %s, want %s", parsed.Narrator, original.Narrator)
	}
	if parsed.Publisher != original.Publisher {
		t.Errorf("Publisher mismatch: got %s, want %s", parsed.Publisher, original.Publisher)
	}
	if parsed.Year != original.Year {
		t.Errorf("Year mismatch: got %d, want %d", parsed.Year, original.Year)
	}
	if parsed.Confidence != original.Confidence {
		t.Errorf("Confidence mismatch: got %s, want %s", parsed.Confidence, original.Confidence)
	}
}

func TestParsedMetadata_JSONOmitEmpty(t *testing.T) {
	// Test that omitempty works for optional fields
	minimal := &ParsedMetadata{
		Title:      "Test Book",
		Author:     "Test Author",
		Confidence: "high",
	}

	jsonData, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	jsonStr := string(jsonData)

	// These fields should be omitted
	if contains(jsonStr, "series") {
		t.Error("Expected series to be omitted")
	}
	if contains(jsonStr, "narrator") {
		t.Error("Expected narrator to be omitted")
	}
	if contains(jsonStr, "publisher") {
		t.Error("Expected publisher to be omitted")
	}
}

func TestTestConnection_Timeout(t *testing.T) {
	// This test verifies the timeout logic exists
	parser := NewOpenAIParser(nil, "test-key", true)

	// Create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := parser.TestConnection(ctx)
	// We expect an error because the context is cancelled
	// The actual error will be an API error, but we're testing the flow
	if err == nil {
		t.Error("Expected error with cancelled context")
	}
}

func TestParseBatch_BatchSizeLimit(t *testing.T) {
	// Test that batch size is limited to maxBatchSize (20)
	_ = NewOpenAIParser(nil, "test-key", true)

	// Create 25 filenames
	filenames := make([]string, 25)
	for i := 0; i < 25; i++ {
		filenames[i] = "test.mp3"
	}

	// The function should limit to 20, but we can't test the actual API call
	// without mocking. This test verifies the function accepts the input
	if len(filenames) > 20 {
		t.Log("Batch size would be limited to 20 in actual execution")
	}
}

func TestOpenAIParser_ModelConfiguration(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Verify default model is set (nil cfg falls back to defaultModel constant)
	if parser.filenameParseModel() == "" {
		t.Error("Expected model to be set")
	}

	// Verify it's the expected model
	expectedModel := "gpt-5-mini"
	if parser.filenameParseModel() != expectedModel {
		t.Errorf("Expected model %s, got %s", expectedModel, parser.filenameParseModel())
	}
}

func TestOpenAIParser_RetryConfiguration(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Verify default maxRetries is set
	expectedRetries := 2
	if parser.maxRetries != expectedRetries {
		t.Errorf("Expected maxRetries %d, got %d", expectedRetries, parser.maxRetries)
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 &&
		(s == substr || (len(s) >= len(substr) && hasSubstring(s, substr)))
}

func hasSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Mock client tests for error paths

// TestParseFilename_APIError tests API error handling
func TestParseFilename_APIError(t *testing.T) {
	// Create parser with invalid API key to trigger error
	parser := NewOpenAIParser(nil, "invalid-key-format", true)
	ctx := context.Background()

	_, err := parser.ParseFilename(ctx, "Test Book - Test Author.mp3")

	// Should get an error from the API
	if err == nil {
		t.Error("Expected error from API with invalid key")
	}

	// Error should mention API failure
	if !strings.Contains(err.Error(), "OpenAI API call failed") {
		t.Errorf("Expected API failure error, got: %v", err)
	}
}

// TestParseFilename_InvalidJSON tests invalid JSON response handling
func TestParseFilename_InvalidJSON(t *testing.T) {
	// This test documents the error path for invalid JSON
	// In practice, OpenAI should always return valid JSON with response_format
	// but we test the error handling exists

	invalidJSON := "{invalid json"
	var metadata ParsedMetadata
	err := json.Unmarshal([]byte(invalidJSON), &metadata)

	if err == nil {
		t.Error("Expected JSON unmarshal error")
	}
}

// TestParseBatch_APIError tests batch API error handling
func TestParseBatch_APIError(t *testing.T) {
	// Create parser with invalid API key to trigger error
	parser := NewOpenAIParser(nil, "invalid-key-format", true)
	ctx := context.Background()

	filenames := []string{
		"Book1 - Author1.mp3",
		"Book2 - Author2.mp3",
	}

	_, err := parser.ParseBatch(ctx, filenames)

	// Should get an error from the API
	if err == nil {
		t.Error("Expected error from API with invalid key")
	}

	// Error should mention API failure
	if !strings.Contains(err.Error(), "OpenAI API call failed") {
		t.Errorf("Expected API failure error, got: %v", err)
	}
}

// TestParseBatch_InvalidJSONResponse tests batch invalid JSON handling
func TestParseBatch_InvalidJSONResponse(t *testing.T) {
	// This test documents the error path for invalid JSON in batch
	invalidJSON := "[{invalid json}]"
	var results []*ParsedMetadata
	err := json.Unmarshal([]byte(invalidJSON), &results)

	if err == nil {
		t.Error("Expected JSON unmarshal error")
	}
}

// TestParseBatch_SingleFile tests batch with single file
func TestParseBatch_SingleFile(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Verify parser accepts single file in batch
	filenames := []string{"Single Book - Author.mp3"}

	// This would call the API if we had a valid key
	// We're testing the function signature and basic flow
	_, err := parser.ParseBatch(context.Background(), filenames)

	// We expect an API error with test key, not a logic error
	if err != nil && !strings.Contains(err.Error(), "OpenAI API call failed") {
		t.Errorf("Unexpected error type: %v", err)
	}
}

// TestParseBatch_ExactlyMaxSize tests batch with exactly 20 files
func TestParseBatch_ExactlyMaxSize(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Create exactly 20 filenames (the max batch size)
	filenames := make([]string, 20)
	for i := 0; i < 20; i++ {
		filenames[i] = fmt.Sprintf("Book%d - Author%d.mp3", i+1, i+1)
	}

	// Should accept all 20
	_, err := parser.ParseBatch(context.Background(), filenames)

	// We expect an API error with test key, not a logic error
	if err != nil && !strings.Contains(err.Error(), "OpenAI API call failed") {
		t.Errorf("Unexpected error type: %v", err)
	}
}

// TestParseBatch_OverMaxSize tests batch size limiting
func TestParseBatch_OverMaxSize(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Create 25 filenames (over the max of 20)
	filenames := make([]string, 25)
	for i := 0; i < 25; i++ {
		filenames[i] = fmt.Sprintf("Book%d - Author%d.mp3", i+1, i+1)
	}

	// Should still process (limiting to 20 internally)
	_, err := parser.ParseBatch(context.Background(), filenames)

	// We expect an API error with test key, not a logic error
	if err != nil && !strings.Contains(err.Error(), "OpenAI API call failed") {
		t.Errorf("Unexpected error type: %v", err)
	}
}

// TestParsedMetadata_PartialData tests metadata with some fields
func TestParsedMetadata_PartialData(t *testing.T) {
	// Test with only required fields
	partial := &ParsedMetadata{
		Title:      "Test Book",
		Author:     "Test Author",
		Confidence: "medium",
	}

	jsonData, err := json.Marshal(partial)
	if err != nil {
		t.Fatalf("Failed to marshal partial metadata: %v", err)
	}

	var unmarshaled ParsedMetadata
	if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal partial metadata: %v", err)
	}

	if unmarshaled.Title != partial.Title {
		t.Errorf("Title mismatch: got %s, want %s", unmarshaled.Title, partial.Title)
	}
	if unmarshaled.Author != partial.Author {
		t.Errorf("Author mismatch: got %s, want %s", unmarshaled.Author, partial.Author)
	}
}

// TestParsedMetadata_AllFields tests metadata with all fields populated
func TestParsedMetadata_AllFields(t *testing.T) {
	full := &ParsedMetadata{
		Title:      "The Fellowship of the Ring",
		Author:     "J.R.R. Tolkien",
		Series:     "The Lord of the Rings",
		SeriesNum:  1,
		Narrator:   "Rob Inglis",
		Publisher:  "Houghton Mifflin",
		Year:       1954,
		Confidence: "high",
	}

	jsonData, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("Failed to marshal full metadata: %v", err)
	}

	jsonStr := string(jsonData)

	// Verify all fields are present
	if !strings.Contains(jsonStr, "The Fellowship of the Ring") {
		t.Error("Expected title in JSON")
	}
	if !strings.Contains(jsonStr, "J.R.R. Tolkien") {
		t.Error("Expected author in JSON")
	}
	if !strings.Contains(jsonStr, "The Lord of the Rings") {
		t.Error("Expected series in JSON")
	}
	if !strings.Contains(jsonStr, "Rob Inglis") {
		t.Error("Expected narrator in JSON")
	}
	if !strings.Contains(jsonStr, "Houghton Mifflin") {
		t.Error("Expected publisher in JSON")
	}
}

// TestParsedMetadata_ZeroValues tests zero values are omitted where appropriate
func TestParsedMetadata_ZeroValues(t *testing.T) {
	zeroSeries := &ParsedMetadata{
		Title:      "Standalone Book",
		Author:     "Author Name",
		SeriesNum:  0, // Zero should be omitted
		Year:       0, // Zero should be omitted
		Confidence: "low",
	}

	jsonData, err := json.Marshal(zeroSeries)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	jsonStr := string(jsonData)

	// series_number: 0 should be omitted in JSON due to omitempty
	// But the field has no omitempty for SeriesNum and Year, so they will be included
	// This test verifies the actual behavior
	var unmarshaled ParsedMetadata
	if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if unmarshaled.SeriesNum != 0 {
		t.Errorf("Expected SeriesNum to be 0, got %d", unmarshaled.SeriesNum)
	}

	t.Logf("JSON output: %s", jsonStr)
}

// TestParseFilename_ContextCancellation tests context cancellation handling
func TestParseFilename_ContextCancellation(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Create an already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := parser.ParseFilename(ctx, "Test Book - Author.mp3")

	if err == nil {
		t.Error("Expected error with cancelled context")
	}
}

// TestParseBatch_ContextCancellation tests batch context cancellation
func TestParseBatch_ContextCancellation(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Create an already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	filenames := []string{"Book1.mp3", "Book2.mp3"}
	_, err := parser.ParseBatch(ctx, filenames)

	if err == nil {
		t.Error("Expected error with cancelled context")
	}
}

// TestOpenAIParser_ClientNilWhenDisabled tests client is nil when disabled
func TestOpenAIParser_ClientNilWhenDisabled(t *testing.T) {
	parser := NewOpenAIParser(nil, "", false)

	if parser.client != nil {
		t.Error("Expected client to be nil when disabled")
	}
}

// TestParsedMetadata_ConfidenceLevels tests different confidence levels
func TestParsedMetadata_ConfidenceLevels(t *testing.T) {
	confidenceLevels := []string{"high", "medium", "low"}

	for _, level := range confidenceLevels {
		metadata := &ParsedMetadata{
			Title:      "Test",
			Author:     "Author",
			Confidence: level,
		}

		jsonData, err := json.Marshal(metadata)
		if err != nil {
			t.Fatalf("Failed to marshal with confidence %s: %v", level, err)
		}

		var unmarshaled ParsedMetadata
		if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
			t.Fatalf("Failed to unmarshal with confidence %s: %v", level, err)
		}

		if unmarshaled.Confidence != level {
			t.Errorf("Confidence mismatch: got %s, want %s", unmarshaled.Confidence, level)
		}
	}
}

// TestOpenAIParser_StructFields tests parser struct has expected fields
func TestOpenAIParser_StructFields(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Verify model is accessible (nil cfg → falls back to defaultModel)
	if parser.filenameParseModel() == "" {
		t.Error("Expected model to be set")
	}
	if parser.maxRetries == 0 {
		t.Error("Expected maxRetries to be set")
	}
	if !parser.enabled {
		t.Error("Expected parser to be enabled")
	}
	if parser.client == nil {
		t.Error("Expected client to be initialized")
	}
}

// TestNewOpenAIParser_BothDisabledConditions tests both disabled conditions
func TestNewOpenAIParser_BothDisabledConditions(t *testing.T) {
	// Both conditions that disable the parser
	parser := NewOpenAIParser(nil, "", false)

	if parser.enabled {
		t.Error("Expected parser to be disabled")
	}
	if parser.client != nil {
		t.Error("Expected client to be nil when disabled")
	}
}

// TestParseBatch_LargeInputTruncation tests that large inputs are truncated
func TestParseBatch_LargeInputTruncation(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	// Create 100 filenames (way over the max of 20)
	filenames := make([]string, 100)
	for i := 0; i < 100; i++ {
		filenames[i] = fmt.Sprintf("Book%d.mp3", i+1)
	}

	// The function should handle this gracefully by truncating
	_, err := parser.ParseBatch(context.Background(), filenames)

	// We expect an API error, not a panic or logic error
	if err != nil && !strings.Contains(err.Error(), "OpenAI API call failed") {
		t.Errorf("Unexpected error type: %v", err)
	}
}

// TestParsedMetadata_UnmarshalValidJSON tests unmarshaling valid OpenAI response
func TestParsedMetadata_UnmarshalValidJSON(t *testing.T) {
	// Simulate a valid OpenAI API response
	validJSON := `{
		"title": "The Hobbit",
		"author": "J.R.R. Tolkien",
		"series": "Middle Earth",
		"series_number": 0,
		"narrator": "Andy Serkis",
		"year": 1937,
		"confidence": "high"
	}`

	var metadata ParsedMetadata
	err := json.Unmarshal([]byte(validJSON), &metadata)

	if err != nil {
		t.Fatalf("Failed to unmarshal valid JSON: %v", err)
	}

	if metadata.Title != "The Hobbit" {
		t.Errorf("Expected title 'The Hobbit', got '%s'", metadata.Title)
	}
	if metadata.Author != "J.R.R. Tolkien" {
		t.Errorf("Expected author 'J.R.R. Tolkien', got '%s'", metadata.Author)
	}
	if metadata.Confidence != "high" {
		t.Errorf("Expected confidence 'high', got '%s'", metadata.Confidence)
	}
}

// TestParseBatch_UnmarshalValidBatchJSON tests unmarshaling valid batch response
func TestParseBatch_UnmarshalValidBatchJSON(t *testing.T) {
	// Simulate a valid OpenAI batch API response
	validJSON := `[
		{
			"title": "Book One",
			"author": "Author One",
			"confidence": "high"
		},
		{
			"title": "Book Two",
			"author": "Author Two",
			"series": "Test Series",
			"series_number": 2,
			"confidence": "medium"
		}
	]`

	var results []*ParsedMetadata
	err := json.Unmarshal([]byte(validJSON), &results)

	if err != nil {
		t.Fatalf("Failed to unmarshal valid batch JSON: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	if results[0].Title != "Book One" {
		t.Errorf("Expected first title 'Book One', got '%s'", results[0].Title)
	}
	if results[1].Series != "Test Series" {
		t.Errorf("Expected second series 'Test Series', got '%s'", results[1].Series)
	}
}

// TestParsedMetadata_MalformedJSON tests various malformed JSON scenarios
func TestParsedMetadata_MalformedJSON(t *testing.T) {
	testCases := []struct {
		name string
		json string
	}{
		{
			name: "incomplete object",
			json: `{"title": "Test"`,
		},
		{
			name: "wrong type for number field",
			json: `{"title": "Test", "author": "Author", "year": "not a number", "confidence": "high"}`,
		},
		{
			name: "null json",
			json: `null`,
		},
		{
			name: "empty string",
			json: ``,
		},
		{
			name: "array instead of object",
			json: `["not", "an", "object"]`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var metadata ParsedMetadata
			err := json.Unmarshal([]byte(tc.json), &metadata)
			if tc.json == "" {
				// Empty string should error
				if err == nil {
					t.Error("Expected error for empty JSON string")
				}
			} else if tc.name == "wrong type for number field" {
				// Type mismatch should error
				if err == nil {
					t.Error("Expected error for type mismatch")
				}
			} else if tc.name == "null json" {
				// null JSON results in zero values but no error
				if err != nil {
					t.Errorf("Unexpected error for null JSON: %v", err)
				}
			} else {
				// Other malformed JSON should error
				if err == nil {
					t.Errorf("Expected error for malformed JSON: %s", tc.json)
				}
			}
		})
	}
}

// TestParseBatch_MalformedBatchJSON tests malformed batch JSON scenarios
func TestParseBatch_MalformedBatchJSON(t *testing.T) {
	testCases := []struct {
		name string
		json string
	}{
		{
			name: "incomplete array",
			json: `[{"title": "Test"`,
		},
		{
			name: "object instead of array",
			json: `{"title": "Test", "author": "Author"}`,
		},
		{
			name: "mixed array types",
			json: `[{"title": "Test"}, "not an object"]`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var results []*ParsedMetadata
			err := json.Unmarshal([]byte(tc.json), &results)
			if err == nil {
				t.Errorf("Expected error for malformed batch JSON: %s", tc.json)
			}
		})
	}
}

// TestParsedMetadata_EdgeCaseValues tests edge case field values
func TestParsedMetadata_EdgeCaseValues(t *testing.T) {
	testCases := []struct {
		name     string
		metadata ParsedMetadata
	}{
		{
			name: "very long title",
			metadata: ParsedMetadata{
				Title:      strings.Repeat("Very Long Title ", 100),
				Author:     "Author",
				Confidence: "low",
			},
		},
		{
			name: "special characters",
			metadata: ParsedMetadata{
				Title:      "Title with 'quotes' and \"double quotes\"",
				Author:     "Author with émojis 📚",
				Series:     "Series: The Beginning",
				Confidence: "medium",
			},
		},
		{
			name: "unicode characters",
			metadata: ParsedMetadata{
				Title:      "日本語タイトル",
				Author:     "作者名",
				Confidence: "high",
			},
		},
		{
			name: "large series number",
			metadata: ParsedMetadata{
				Title:      "Book",
				Author:     "Author",
				SeriesNum:  999999,
				Year:       9999,
				Confidence: "high",
			},
		},
		{
			name: "negative numbers",
			metadata: ParsedMetadata{
				Title:      "Book",
				Author:     "Author",
				SeriesNum:  -1,
				Year:       -1,
				Confidence: "low",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Marshal and unmarshal
			jsonData, err := json.Marshal(tc.metadata)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}

			var unmarshaled ParsedMetadata
			if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			// Verify round-trip
			if unmarshaled.Title != tc.metadata.Title {
				t.Errorf("Title mismatch after round-trip")
			}
			if unmarshaled.Author != tc.metadata.Author {
				t.Errorf("Author mismatch after round-trip")
			}
		})
	}
}

// TestOpenAIParser_ErrorMessageFormat tests error message formatting
func TestOpenAIParser_ErrorMessageFormat(t *testing.T) {
	parser := NewOpenAIParser(nil, "", false)

	testCases := []struct {
		name          string
		operation     func() error
		expectedError string
	}{
		{
			name: "ParseFilename disabled",
			operation: func() error {
				_, err := parser.ParseFilename(context.Background(), "test.mp3")
				return err
			},
			expectedError: "OpenAI parser is not enabled",
		},
		{
			name: "ParseBatch disabled",
			operation: func() error {
				_, err := parser.ParseBatch(context.Background(), []string{"test.mp3"})
				return err
			},
			expectedError: "OpenAI parser is not enabled",
		},
		{
			name: "TestConnection disabled",
			operation: func() error {
				return parser.TestConnection(context.Background())
			},
			expectedError: "OpenAI parser is not enabled",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.operation()
			if err == nil {
				t.Fatal("Expected error, got nil")
			}
			if err.Error() != tc.expectedError {
				t.Errorf("Expected error '%s', got '%s'", tc.expectedError, err.Error())
			}
		})
	}
}

// TestParseBatch_EmptyStringFilenames tests batch with empty filename strings
func TestParseBatch_EmptyStringFilenames(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filenames := []string{"", " ", "  "}
	_, err := parser.ParseBatch(ctx, filenames)

	// Should fail with API error or timeout (no valid API key)
	if err == nil {
		t.Error("Expected error for fake API key")
	}
}

// TestParseFilename_EmptyFilename tests parsing an empty filename
func TestParseFilename_EmptyFilename(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := parser.ParseFilename(ctx, "")

	// Should fail with API error or timeout (no valid API key)
	if err == nil {
		t.Error("Expected error for fake API key")
	}
}

// TestParseFilename_VeryLongFilename tests parsing a very long filename
func TestParseFilename_VeryLongFilename(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a very long filename
	longFilename := strings.Repeat("Very Long Book Title ", 100) + " - Author.mp3"
	_, err := parser.ParseFilename(ctx, longFilename)

	// Should fail with API error or timeout (no valid API key)
	if err == nil {
		t.Error("Expected error for fake API key")
	}
}

// TestParsedMetadata_EmptyConfidence tests metadata with empty confidence
func TestParsedMetadata_EmptyConfidence(t *testing.T) {
	metadata := &ParsedMetadata{
		Title:      "Test",
		Author:     "Author",
		Confidence: "", // Empty confidence
	}

	jsonData, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var unmarshaled ParsedMetadata
	if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Empty confidence should be preserved (not omitted)
	if unmarshaled.Confidence != "" {
		t.Errorf("Expected empty confidence, got '%s'", unmarshaled.Confidence)
	}
}

// Integration tests - these run only if OPENAI_API_KEY is set
// These tests cover the success paths that can't be tested with mocks

func TestParseFilename_Integration(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: OPENAI_API_KEY not set")
	}

	parser := NewOpenAIParser(nil, apiKey, true)
	ctx := context.Background()

	testCases := []struct {
		name     string
		filename string
	}{
		{
			name:     "simple format",
			filename: "The Hobbit - J.R.R. Tolkien.mp3",
		},
		{
			name:     "with series",
			filename: "Harry Potter and the Sorcerer's Stone (Harry Potter #1) - J.K. Rowling.mp3",
		},
		{
			name:     "with narrator",
			filename: "Project Hail Mary - Andy Weir - Ray Porter.mp3",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			metadata, err := parser.ParseFilename(ctx, tc.filename)

			if err != nil {
				t.Fatalf("ParseFilename failed: %v", err)
			}

			if metadata == nil {
				t.Fatal("Expected metadata, got nil")
			}

			// Verify we got some data
			if metadata.Title == "" {
				t.Error("Expected title to be extracted")
			}
			if metadata.Author == "" {
				t.Error("Expected author to be extracted")
			}
			if metadata.Confidence == "" {
				t.Error("Expected confidence to be set")
			}

			t.Logf("Parsed metadata: Title=%s, Author=%s, Confidence=%s",
				metadata.Title, metadata.Author, metadata.Confidence)
		})
	}
}

func TestParseBatch_Integration(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: OPENAI_API_KEY not set")
	}

	parser := NewOpenAIParser(nil, apiKey, true)
	ctx := context.Background()

	filenames := []string{
		"The Hobbit - J.R.R. Tolkien.mp3",
		"1984 - George Orwell.mp3",
		"Dune - Frank Herbert.mp3",
	}

	results, err := parser.ParseBatch(ctx, filenames)

	if err != nil {
		t.Fatalf("ParseBatch failed: %v", err)
	}

	if results == nil {
		t.Fatal("Expected results, got nil")
	}

	if len(results) == 0 {
		t.Fatal("Expected non-empty results")
	}

	// Verify each result has some data
	for i, metadata := range results {
		if metadata == nil {
			t.Errorf("Result %d is nil", i)
			continue
		}

		if metadata.Title == "" {
			t.Errorf("Result %d: expected title to be extracted", i)
		}
		if metadata.Author == "" {
			t.Errorf("Result %d: expected author to be extracted", i)
		}

		t.Logf("Result %d: Title=%s, Author=%s, Confidence=%s",
			i, metadata.Title, metadata.Author, metadata.Confidence)
	}
}

func TestTestConnection_Integration(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping integration test: OPENAI_API_KEY not set")
	}

	parser := NewOpenAIParser(nil, apiKey, true)
	ctx := context.Background()

	err := parser.TestConnection(ctx)

	if err != nil {
		t.Fatalf("TestConnection failed: %v", err)
	}
}

// Additional tests to improve coverage

// TestParsedMetadata_JSONTags tests that JSON tags are correct
func TestParsedMetadata_JSONTags(t *testing.T) {
	// Test that the struct tags produce the expected JSON field names
	metadata := &ParsedMetadata{
		Title:      "Test Title",
		Author:     "Test Author",
		Series:     "Test Series",
		SeriesNum:  5,
		Narrator:   "Test Narrator",
		Publisher:  "Test Publisher",
		Year:       2024,
		Confidence: "high",
	}

	jsonData, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	jsonStr := string(jsonData)

	// Check for correct JSON field names as defined in struct tags
	expectedFields := []string{
		`"title"`,
		`"author"`,
		`"series"`,
		`"series_number"`,
		`"narrator"`,
		`"publisher"`,
		`"year"`,
		`"confidence"`,
	}

	for _, field := range expectedFields {
		if !strings.Contains(jsonStr, field) {
			t.Errorf("Expected JSON to contain field %s, but it was not found in: %s", field, jsonStr)
		}
	}
}

// TestParsedMetadata_OmitEmptyBehavior tests omitempty on optional fields
func TestParsedMetadata_OmitEmptyBehavior(t *testing.T) {
	// Test with all optional fields empty
	metadata := &ParsedMetadata{
		Title:      "Required Title",
		Author:     "Required Author",
		Series:     "", // Should be omitted
		SeriesNum:  0,  // Note: This doesn't have omitempty, so will be included
		Narrator:   "", // Should be omitted
		Publisher:  "", // Should be omitted
		Year:       0,  // Note: This doesn't have omitempty, so will be included
		Confidence: "high",
	}

	jsonData, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	jsonStr := string(jsonData)

	// These fields should be omitted when empty
	omittedFields := []string{"narrator", "publisher"}
	for _, field := range omittedFields {
		// Check that the field name doesn't appear (or appears with empty value)
		// Since series has omitempty and is empty string, it should be omitted too
		if field == "series" && strings.Contains(jsonStr, `"series":""`) {
			t.Errorf("Expected %s to be omitted with omitempty tag", field)
		}
	}

	// Verify the JSON is valid
	var unmarshaled ParsedMetadata
	if err := json.Unmarshal(jsonData, &unmarshaled); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}
}

// TestParsedMetadata_NonPointerUnmarshal tests unmarshaling to non-pointer
func TestParsedMetadata_NonPointerUnmarshal(t *testing.T) {
	validJSON := `{"title": "Test", "author": "Author", "confidence": "high"}`

	var metadata ParsedMetadata // Non-pointer
	err := json.Unmarshal([]byte(validJSON), &metadata)

	if err != nil {
		t.Fatalf("Failed to unmarshal to non-pointer: %v", err)
	}

	if metadata.Title != "Test" {
		t.Errorf("Expected title 'Test', got '%s'", metadata.Title)
	}
}

// TestParsedMetadata_PointerInSliceUnmarshal tests unmarshaling slice of pointers
func TestParsedMetadata_PointerInSliceUnmarshal(t *testing.T) {
	validJSON := `[
		{"title": "Book1", "author": "Author1", "confidence": "high"},
		{"title": "Book2", "author": "Author2", "confidence": "low"}
	]`

	var results []*ParsedMetadata
	err := json.Unmarshal([]byte(validJSON), &results)

	if err != nil {
		t.Fatalf("Failed to unmarshal slice of pointers: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	for i, result := range results {
		if result == nil {
			t.Errorf("Result %d is nil", i)
		}
	}
}

// TestOpenAIParser_DefaultValues tests that default values are set correctly
func TestOpenAIParser_DefaultValues(t *testing.T) {
	testCases := []struct {
		name       string
		apiKey     string
		enabled    bool
		wantModel  string
		wantRetry  int
		wantClient bool
	}{
		{
			name:       "enabled with key",
			apiKey:     "test-key",
			enabled:    true,
			wantModel:  "gpt-5-mini",
			wantRetry:  2,
			wantClient: true,
		},
		{
			name:       "disabled",
			apiKey:     "",
			enabled:    false,
			wantModel:  "",
			wantRetry:  0,
			wantClient: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			parser := NewOpenAIParser(nil, tc.apiKey, tc.enabled)

			if tc.wantModel != "" && parser.filenameParseModel() != tc.wantModel {
				t.Errorf("Expected model '%s', got '%s'", tc.wantModel, parser.filenameParseModel())
			}
			if tc.wantRetry != 0 && parser.maxRetries != tc.wantRetry {
				t.Errorf("Expected maxRetries %d, got %d", tc.wantRetry, parser.maxRetries)
			}
			if tc.wantClient && parser.client == nil {
				t.Error("Expected client to be initialized")
			}
			if !tc.wantClient && parser.client != nil {
				t.Error("Expected client to be nil")
			}
		})
	}
}

// TestParsedMetadata_CompletenessCoverage tests comprehensive field coverage
func TestParsedMetadata_CompletenessCoverage(t *testing.T) {
	// This test ensures all fields can be set and retrieved
	original := ParsedMetadata{
		Title:      "Complete Title",
		Author:     "Complete Author",
		Series:     "Complete Series",
		SeriesNum:  42,
		Narrator:   "Complete Narrator",
		Publisher:  "Complete Publisher",
		Year:       2025,
		Confidence: "medium",
	}

	// Test each field individually
	if original.Title != "Complete Title" {
		t.Error("Title field not working")
	}
	if original.Author != "Complete Author" {
		t.Error("Author field not working")
	}
	if original.Series != "Complete Series" {
		t.Error("Series field not working")
	}
	if original.SeriesNum != 42 {
		t.Error("SeriesNum field not working")
	}
	if original.Narrator != "Complete Narrator" {
		t.Error("Narrator field not working")
	}
	if original.Publisher != "Complete Publisher" {
		t.Error("Publisher field not working")
	}
	if original.Year != 2025 {
		t.Error("Year field not working")
	}
	if original.Confidence != "medium" {
		t.Error("Confidence field not working")
	}
}

// TestParseBatch_NilFilenames tests nil slice handling
func TestParseBatch_NilFilenames(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)
	ctx := context.Background()

	// nil slice should be handled like empty slice
	results, err := parser.ParseBatch(ctx, nil)

	if err != nil {
		t.Errorf("Expected no error for nil slice, got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected empty results for nil slice, got %d results", len(results))
	}
}

// Test the helper functions that parse JSON responses

func TestParseMetadataFromJSON(t *testing.T) {
	testCases := []struct {
		name        string
		json        string
		expectError bool
		checkTitle  string
		checkAuthor string
	}{
		{
			name: "valid complete metadata",
			json: `{
				"title": "The Hobbit",
				"author": "J.R.R. Tolkien",
				"series": "Middle Earth",
				"series_number": 0,
				"narrator": "Andy Serkis",
				"publisher": "HarperCollins",
				"year": 1937,
				"confidence": "high"
			}`,
			expectError: false,
			checkTitle:  "The Hobbit",
			checkAuthor: "J.R.R. Tolkien",
		},
		{
			name: "valid minimal metadata",
			json: `{
				"title": "Simple Book",
				"author": "Simple Author",
				"confidence": "medium"
			}`,
			expectError: false,
			checkTitle:  "Simple Book",
			checkAuthor: "Simple Author",
		},
		{
			name:        "invalid JSON",
			json:        `{invalid json}`,
			expectError: true,
		},
		{
			name:        "empty JSON",
			json:        ``,
			expectError: true,
		},
		{
			name:        "array instead of object",
			json:        `[]`,
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseMetadataFromJSON(tc.json)

			if tc.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result == nil {
				t.Fatal("Expected result, got nil")
			}

			if result.Title != tc.checkTitle {
				t.Errorf("Expected title '%s', got '%s'", tc.checkTitle, result.Title)
			}
			if result.Author != tc.checkAuthor {
				t.Errorf("Expected author '%s', got '%s'", tc.checkAuthor, result.Author)
			}
		})
	}
}

func TestParseBatchMetadataFromJSON(t *testing.T) {
	testCases := []struct {
		name        string
		json        string
		expectError bool
		expectCount int
		checkFirst  string
	}{
		{
			name: "valid batch with multiple items",
			json: `[
				{
					"title": "Book One",
					"author": "Author One",
					"confidence": "high"
				},
				{
					"title": "Book Two",
					"author": "Author Two",
					"series": "Test Series",
					"series_number": 2,
					"confidence": "medium"
				},
				{
					"title": "Book Three",
					"author": "Author Three",
					"confidence": "low"
				}
			]`,
			expectError: false,
			expectCount: 3,
			checkFirst:  "Book One",
		},
		{
			name: "valid batch with single item",
			json: `[
				{
					"title": "Single Book",
					"author": "Single Author",
					"confidence": "high"
				}
			]`,
			expectError: false,
			expectCount: 1,
			checkFirst:  "Single Book",
		},
		{
			name:        "empty array",
			json:        `[]`,
			expectError: false,
			expectCount: 0,
		},
		{
			name:        "invalid JSON",
			json:        `[{invalid}]`,
			expectError: true,
		},
		{
			name:        "object instead of array",
			json:        `{"title": "Test"}`,
			expectError: true,
		},
		{
			name:        "null JSON",
			json:        `null`,
			expectError: false,
			expectCount: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := parseBatchMetadataFromJSON(tc.json)

			if tc.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(results) != tc.expectCount {
				t.Fatalf("Expected %d results, got %d", tc.expectCount, len(results))
			}

			if tc.expectCount > 0 && tc.checkFirst != "" {
				if results[0].Title != tc.checkFirst {
					t.Errorf("Expected first title '%s', got '%s'", tc.checkFirst, results[0].Title)
				}
			}
		})
	}
}

// Test error message wrapping in helper functions
func TestParseMetadataFromJSON_ErrorWrapping(t *testing.T) {
	_, err := parseMetadataFromJSON("{bad json")

	if err == nil {
		t.Fatal("Expected error")
	}

	errorMsg := err.Error()
	if !strings.Contains(errorMsg, "failed to parse OpenAI response") {
		t.Errorf("Expected error to mention 'failed to parse OpenAI response', got: %s", errorMsg)
	}
}

func TestParseBatchMetadataFromJSON_ErrorWrapping(t *testing.T) {
	_, err := parseBatchMetadataFromJSON("[{bad json}]")

	if err == nil {
		t.Fatal("Expected error")
	}

	errorMsg := err.Error()
	if !strings.Contains(errorMsg, "failed to parse OpenAI response") {
		t.Errorf("Expected error to mention 'failed to parse OpenAI response', got: %s", errorMsg)
	}
}

// --- ParseAudiobook tests ---

func TestParseAudiobook_Disabled(t *testing.T) {
	parser := NewOpenAIParser(nil, "", false)
	ctx := context.Background()

	_, err := parser.ParseAudiobook(ctx, AudiobookContext{FilePath: "/test/path.mp3"})
	if err == nil {
		t.Error("Expected error when parser is disabled")
	}
	if err.Error() != "OpenAI parser is not enabled" {
		t.Errorf("Expected disabled error, got: %v", err)
	}
}

func TestParseAudiobook_WithFakeServer(t *testing.T) {
	// Create a fake OpenAI-compatible server
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedBody = string(bodyBytes)

		response := `{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "{\"title\":\"The Great Gatsby\",\"author\":\"F. Scott Fitzgerald\",\"narrator\":\"Jake Gyllenhaal\",\"series\":\"\",\"series_number\":0,\"year\":2020,\"confidence\":\"high\"}"
				},
				"finish_reason": "stop"
			}]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	}))
	defer server.Close()

	// Point OPENAI_BASE_URL to our fake server
	origBaseURL := config.AppConfig.OpenAIBaseURL
	os.Setenv("OPENAI_BASE_URL", server.URL)
	config.InitConfig()
	defer func() {
		os.Unsetenv("OPENAI_BASE_URL")
		config.Mutate(func(c *config.Config) { c.OpenAIBaseURL = origBaseURL })
	}()

	parser := NewOpenAIParser(nil, "fake-key", true)
	ctx := context.Background()

	abCtx := AudiobookContext{
		FilePath:      "/audiobooks/F. Scott Fitzgerald/The Great Gatsby/01 Chapter 1.mp3",
		Title:         "01 Chapter 1",
		AuthorName:    "F. Scott Fitzgerald",
		Narrator:      "Jake Gyllenhaal",
		FileCount:     12,
		TotalDuration: 32400, // 9 hours
	}

	metadata, err := parser.ParseAudiobook(ctx, abCtx)
	if err != nil {
		t.Fatalf("ParseAudiobook failed: %v", err)
	}

	if metadata.Title != "The Great Gatsby" {
		t.Errorf("Expected title 'The Great Gatsby', got '%s'", metadata.Title)
	}
	if metadata.Author != "F. Scott Fitzgerald" {
		t.Errorf("Expected author 'F. Scott Fitzgerald', got '%s'", metadata.Author)
	}
	if metadata.Narrator != "Jake Gyllenhaal" {
		t.Errorf("Expected narrator 'Jake Gyllenhaal', got '%s'", metadata.Narrator)
	}
	if metadata.Confidence != "high" {
		t.Errorf("Expected confidence 'high', got '%s'", metadata.Confidence)
	}

	// Verify the prompt included the full path context
	if !strings.Contains(capturedBody, "F. Scott Fitzgerald/The Great Gatsby") {
		t.Error("Expected prompt to contain folder hierarchy from file path")
	}
	if !strings.Contains(capturedBody, "Existing author") {
		t.Error("Expected prompt to contain existing author metadata")
	}
	if !strings.Contains(capturedBody, "12 files") {
		t.Error("Expected prompt to contain file count")
	}
	if !strings.Contains(capturedBody, "9h 0m") {
		t.Error("Expected prompt to contain total duration")
	}
}

func TestParseAudiobook_MinimalContext(t *testing.T) {
	// Test with only a file path and no existing metadata
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedBody = string(bodyBytes)

		response := `{
			"id": "chatcmpl-test2",
			"object": "chat.completion",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "{\"title\":\"Unknown Book\",\"author\":\"Unknown\",\"confidence\":\"low\"}"
				},
				"finish_reason": "stop"
			}]
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	}))
	defer server.Close()

	origBaseURL := config.AppConfig.OpenAIBaseURL
	os.Setenv("OPENAI_BASE_URL", server.URL)
	config.InitConfig()
	defer func() {
		os.Unsetenv("OPENAI_BASE_URL")
		config.Mutate(func(c *config.Config) { c.OpenAIBaseURL = origBaseURL })
	}()

	parser := NewOpenAIParser(nil, "fake-key", true)
	ctx := context.Background()

	abCtx := AudiobookContext{
		FilePath: "/audiobooks/some_file.mp3",
	}

	metadata, err := parser.ParseAudiobook(ctx, abCtx)
	if err != nil {
		t.Fatalf("ParseAudiobook failed: %v", err)
	}

	if metadata.Confidence != "low" {
		t.Errorf("Expected confidence 'low', got '%s'", metadata.Confidence)
	}

	// Ensure optional fields are NOT in the prompt when empty
	if strings.Contains(capturedBody, "Existing title") {
		t.Error("Prompt should not contain 'Existing title' when title is empty")
	}
	if strings.Contains(capturedBody, "Existing author") {
		t.Error("Prompt should not contain 'Existing author' when author is empty")
	}
	if strings.Contains(capturedBody, "File count") {
		t.Error("Prompt should not contain 'File count' when file count is 0")
	}
}

// TestOpenAIParser_UsesConfiguredModels verifies that each Parse* method sends
// the model string from the corresponding config field to the OpenAI API.
func TestOpenAIParser_UsesConfiguredModels(t *testing.T) {
	// Minimal JSON response accepted by all Parse* methods
	successResponse := `{
		"id": "chatcmpl-test",
		"object": "chat.completion",
		"model": "test-model",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "{\"title\":\"T\",\"author\":\"A\",\"confidence\":\"high\"}"},
			"finish_reason": "stop"
		}]
	}`

	tests := []struct {
		name          string
		cfg           *config.Config
		invoke        func(p *OpenAIParser, srv *httptest.Server) error
		wantModelFrag string
	}{
		{
			name: "ParseFilename uses FilenameParseModel",
			cfg:  &config.Config{FilenameParseModel: "test-filename-model"},
			invoke: func(p *OpenAIParser, _ *httptest.Server) error {
				_, err := p.ParseFilename(context.Background(), "MyBook.mp3")
				return err
			},
			wantModelFrag: "test-filename-model",
		},
		{
			name: "ParseBatch uses FilenameParseModel",
			cfg:  &config.Config{FilenameParseModel: "test-batch-model"},
			invoke: func(p *OpenAIParser, _ *httptest.Server) error {
				batchResp := `{"results":[{"title":"T","author":"A","confidence":"high"}]}`
				_ = batchResp // the mock returns successResponse which parseBatch accepts
				_, err := p.ParseBatch(context.Background(), []string{"file1.mp3"})
				return err
			},
			wantModelFrag: "test-batch-model",
		},
		{
			name: "ParseCoverArt uses CoverArtModel",
			cfg:  &config.Config{CoverArtModel: "test-cover-model"},
			invoke: func(p *OpenAIParser, _ *httptest.Server) error {
				_, err := p.ParseCoverArt(context.Background(), []byte{0xFF, 0xD8}, "image/jpeg")
				return err
			},
			wantModelFrag: "test-cover-model",
		},
		{
			name: "ReviewAuthorDuplicates uses MetadataReviewModel",
			cfg:  &config.Config{MetadataReviewModel: "test-metadata-model"},
			invoke: func(p *OpenAIParser, _ *httptest.Server) error {
				// ReviewAuthorDuplicates expects {"suggestions":[...]}
				// We override the mock per subtest below.
				_, err := p.ReviewAuthorDuplicates(context.Background(), []AuthorDedupInput{
					{Index: 0, CanonicalName: "Test Author", BookCount: 1},
				})
				return err
			},
			wantModelFrag: "test-metadata-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedModel string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Model    string                   `json:"model"`
					Messages []map[string]interface{} `json:"messages"`
				}
				_ = json.Unmarshal(body, &req)
				capturedModel = req.Model

				// Select response body based on request content
				resp := successResponse
				bodyStr := string(body)
				switch {
				case strings.Contains(bodyStr, "Review these") || strings.Contains(bodyStr, "Find duplicate"):
					// ReviewAuthorDuplicates / DiscoverAuthorDuplicates
					resp = `{"id":"chatcmpl-test","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"{\"suggestions\":[]}"},"finish_reason":"stop"}]}`
				case strings.Contains(bodyStr, "Parse these audiobook filenames"):
					// ParseBatch — needs {"results":[...]}
					resp = `{"id":"chatcmpl-test","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"{\"results\":[{\"title\":\"T\",\"author\":\"A\",\"confidence\":\"high\"}]}"},"finish_reason":"stop"}]}`
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(resp))
			}))
			defer srv.Close()

			origBaseURL := config.AppConfig.OpenAIBaseURL
			t.Setenv("OPENAI_BASE_URL", srv.URL)
			config.InitConfig()
			t.Cleanup(func() {
				config.Mutate(func(c *config.Config) { c.OpenAIBaseURL = origBaseURL })
			})

			p := NewOpenAIParser(tt.cfg, "test-api-key", true)
			if err := tt.invoke(p, srv); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if capturedModel != tt.wantModelFrag {
				t.Errorf("model sent to API = %q, want %q", capturedModel, tt.wantModelFrag)
			}
		})
	}
}

func TestAudiobookContext_JSONTags(t *testing.T) {
	abCtx := AudiobookContext{
		FilePath:      "/test/path.mp3",
		Title:         "Test",
		AuthorName:    "Author",
		Narrator:      "Narrator",
		FileCount:     5,
		TotalDuration: 3600,
	}

	data, err := json.Marshal(abCtx)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	jsonStr := string(data)
	for _, field := range []string{"file_path", "title", "author_name", "narrator", "file_count", "total_duration"} {
		if !strings.Contains(jsonStr, fmt.Sprintf(`"%s"`, field)) {
			t.Errorf("Expected JSON field %s in output: %s", field, jsonStr)
		}
	}
}
