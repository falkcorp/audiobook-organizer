// file: internal/ai/openai_parser_test.go
// version: 1.13.0
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-09-12

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// Over-ceiling input is an ERROR, not a silent truncation.
//
// This test replaced one that asserted `if len(filenames) > 20` on a slice it
// had just built 25 entries into, logged a sentence, and never called
// ParseBatch at all -- it passed whatever the function did, and would have
// passed with the function deleted. It was the only coverage of the cap.
//
// The behaviour it described was also genuinely dangerous once batch size
// became configurable on 2026-09-09: truncating to 20 and returning nil error
// means the scanner phase counts the batch as OK, so an operator setting a
// larger size would silently lose every filename past the 20th with a summary
// reporting success.
func TestParseBatch_OverCeilingIsAnErrorNotATruncation(t *testing.T) {
	parser := NewOpenAIParser(nil, "test-key", true)

	filenames := make([]string, config.AIParseBatchSizeCeiling+1)
	for i := range filenames {
		filenames[i] = "test.mp3"
	}

	results, err := parser.ParseBatch(context.Background(), filenames)

	if err == nil {
		t.Fatalf("ParseBatch(%d filenames) returned nil error: over-ceiling input must fail loudly, "+
			"because a truncation here is indistinguishable from success to every caller",
			len(filenames))
	}
	if results != nil {
		t.Errorf("results = %v, want nil alongside the error", results)
	}
	if !strings.Contains(err.Error(), "parse_batch_size") {
		t.Errorf("error %q does not name the config key an operator has to change; "+
			"an error that does not say what to do sends the reader into the source", err)
	}
}

// The ceiling admits its own boundary value: a parser configured exactly at the
// maximum must not be rejected. Guards an off-by-one in the comparison, which
// would make the documented maximum unusable.
func TestParseBatch_AtTheCeilingIsAccepted(t *testing.T) {
	parser := NewOpenAIParser(nil, "", false) // disabled: we want the cap check, not a network call

	filenames := make([]string, config.AIParseBatchSizeCeiling)
	for i := range filenames {
		filenames[i] = "test.mp3"
	}

	_, err := parser.ParseBatch(context.Background(), filenames)

	// A disabled parser fails with its own message; the point is that it is NOT
	// the ceiling error, i.e. exactly-at-the-ceiling got past the cap check.
	if err != nil && strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("exactly %d filenames was rejected by the ceiling check: the maximum must be inclusive (%v)",
			config.AIParseBatchSizeCeiling, err)
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
	for i := range 20 {
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
	for i := range 25 {
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
	for i := range 100 {
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

// Replies captured verbatim (whitespace compacted) from qwen2.5:7b-instruct via
// the OpenAI-compatible endpoint on 2026-09-12, using ParseBatch's own system
// prompt and JSON-object response format. The first is the reply that aborted
// library.ai-parse ops in production.
const (
	// "Disc 1".."Disc 8" (8 filenames), and separately "Season 2" (1 filename).
	observedEmptyResults = `{"results": []}`
	// "Book 10 - The Given Sacrifice", "The Tears of the Sun ... Part 01 of 63.mp3",
	// "Season 2", "Season 1": null placeholders for the two it could not parse.
	observedNullEntries = `{"results":[{"title":"The Given Sacrifice","author":"Book 10","series":null,"series_number":null,"narrator":null,"publisher":null,"year":null,"confidence":"medium"},{"title":"The Tears of the Sun A Novel of the Change","author":null,"series":"The Change","series_number":1,"narrator":null,"publisher":null,"year":null,"confidence":"high"},null,null]}`
	// "Disc 1", "Book 10 - The Given Sacrifice", "Disc 2": empty strings.
	observedEmptyStrings = `{"results":[{"title":"","author":"","series":"","series_number":null,"narrator":"","publisher":"","year":null,"confidence":"low"},{"title":"The Given Sacrifice","author":"","series":"","series_number":10,"narrator":"","publisher":"","year":null,"confidence":"medium"},{"title":"","author":"","series":"","series_number":null,"narrator":"","publisher":"","year":null,"confidence":"low"}]}`
)

// observedAllNullFields is the reply to the SAME 8 "Disc N" filenames that
// also produced observedEmptyResults: 8 entries, every field null.
var observedAllNullFields = `{"results":[` + strings.TrimSuffix(strings.Repeat(
	`{"title":null,"author":null,"series":null,"series_number":null,"narrator":null,"publisher":null,"year":null,"confidence":"low"},`, 8), ",") + `]}`

func TestParseBatchMetadataFromJSON(t *testing.T) {
	testCases := []struct {
		name     string
		json     string
		expected int
		wantErr  string // substring; empty means no error expected
		check    func(t *testing.T, results []*ParsedMetadata)
	}{
		{
			name: "valid batch with multiple items",
			json: `[
				{"title": "Book One", "author": "Author One", "confidence": "high"},
				{"title": "Book Two", "author": "Author Two", "series": "Test Series", "series_number": 2, "confidence": "medium"},
				{"title": "Book Three", "author": "Author Three", "confidence": "low"}
			]`,
			expected: 3,
			check: func(t *testing.T, r []*ParsedMetadata) {
				if r[0].Title != "Book One" || r[1].SeriesNum != 2 || r[2].Title != "Book Three" {
					t.Errorf("wrong results: %+v %+v %+v", r[0], r[1], r[2])
				}
			},
		},
		{
			name:     "valid batch with single item",
			json:     `[{"title": "Single Book", "author": "Single Author", "confidence": "high"}]`,
			expected: 1,
			check: func(t *testing.T, r []*ParsedMetadata) {
				if r[0].Title != "Single Book" {
					t.Errorf("title = %q", r[0].Title)
				}
			},
		},
		{
			name:     "empty bare array is zero results, padded to the batch size",
			json:     `[]`,
			expected: 2,
			check:    wantAllNil,
		},
		{
			// THE production failure: 3 of 14 batches of op
			// 01M2BNZAJDZ5F5HM2TS6XG1S8D, and the op aborted.
			name:     "observed: empty results for 8 disc folders",
			json:     observedEmptyResults,
			expected: 8,
			check:    wantAllNil,
		},
		{
			name:     "observed: empty results for a single filename",
			json:     observedEmptyResults,
			expected: 1,
			check:    wantAllNil,
		},
		{
			name:     "observed: null placeholders keep positions",
			json:     observedNullEntries,
			expected: 4,
			check: func(t *testing.T, r []*ParsedMetadata) {
				if r[0] == nil || r[0].Title != "The Given Sacrifice" {
					t.Errorf("r[0] = %+v", r[0])
				}
				if r[1] == nil || r[1].Series != "The Change" || r[1].SeriesNum != 1 || r[1].Author != "" {
					t.Errorf("r[1] = %+v", r[1])
				}
				if r[2] != nil || r[3] != nil {
					t.Errorf("r[2], r[3] = %+v, %+v; want nil", r[2], r[3])
				}
			},
		},
		{
			name:     "observed: all-null fields for the same 8 disc folders",
			json:     observedAllNullFields,
			expected: 8,
			check: func(t *testing.T, r []*ParsedMetadata) {
				for i, m := range r {
					if m == nil || m.Title != "" || m.Author != "" || m.Confidence != "low" {
						t.Errorf("r[%d] = %+v", i, m)
					}
				}
			},
		},
		{
			name:     "observed: empty-string fields",
			json:     observedEmptyStrings,
			expected: 3,
			check: func(t *testing.T, r []*ParsedMetadata) {
				if r[1].Title != "The Given Sacrifice" || r[1].SeriesNum != 10 || r[0].Title != "" {
					t.Errorf("wrong results: %+v %+v", r[0], r[1])
				}
			},
		},
		{
			name:     "unobserved: array wrapped under a key other than results",
			json:     `{"books": [{"title": "A"}, {"title": "B"}]}`,
			expected: 2,
			check: func(t *testing.T, r []*ParsedMetadata) {
				if r[0].Title != "A" || r[1].Title != "B" {
					t.Errorf("wrong results: %+v %+v", r[0], r[1])
				}
			},
		},
		{
			name:     "unobserved: bare object for a one-filename batch",
			json:     `{"title": "Test", "author": "Author"}`,
			expected: 1,
			check: func(t *testing.T, r []*ParsedMetadata) {
				if r[0].Title != "Test" || r[0].Author != "Author" {
					t.Errorf("r[0] = %+v", r[0])
				}
			},
		},
		{
			name:     "bare object for a multi-filename batch is an error",
			json:     `{"title": "Test", "author": "Author"}`,
			expected: 2,
			wantErr:  `no "results" array`,
		},
		{
			// The misassignment hazard: which of the 3 did the model drop?
			name:     "short results list is an error, never a positional guess",
			json:     `{"results": [{"title": "A"}, {"title": "B"}]}`,
			expected: 3,
			wantErr:  "got 2 result(s) for 3 filename(s)",
		},
		{
			name:     "long results list is an error",
			json:     `[{"title": "A"}, {"title": "B"}]`,
			expected: 1,
			wantErr:  "got 2 result(s) for 1 filename(s)",
		},
		{
			name:     "results null is an error",
			json:     `{"results": null}`,
			expected: 2,
			wantErr:  `"results" is not a JSON array`,
		},
		{
			name:     "results as an object is an error",
			json:     `{"results": {"title": "A"}}`,
			expected: 1,
			wantErr:  `"results" is not a JSON array`,
		},
		{
			name:     "two unknown array keys is an error, not a guess",
			json:     `{"a": [{"title": "A"}], "b": [{"title": "B"}]}`,
			expected: 1,
			wantErr:  "no \"results\" array",
		},
		{
			name:     "invalid JSON",
			json:     `[{invalid}]`,
			expected: 1,
			wantErr:  "failed to parse OpenAI response",
		},
		{
			name:     "null JSON is an error",
			json:     `null`,
			expected: 1,
			wantErr:  "neither a JSON object nor a JSON array",
		},
		{
			name:     "empty reply is an error",
			json:     "  ",
			expected: 1,
			wantErr:  "empty response",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := parseBatchMetadataFromJSON(tc.json, tc.expected)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got results %v", tc.wantErr, results)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(results) != tc.expected {
				t.Fatalf("got %d results, want exactly %d (one per filename)", len(results), tc.expected)
			}
			if tc.check != nil {
				tc.check(t, results)
			}
		})
	}
}

func wantAllNil(t *testing.T, r []*ParsedMetadata) {
	t.Helper()
	for i, m := range r {
		if m != nil {
			t.Errorf("r[%d] = %+v, want nil (no result for that filename)", i, m)
		}
	}
}

// The error must carry the reply, truncated and log-safe, so the next failure
// is diagnosable from the operation record.
func TestParseBatchMetadataFromJSON_ErrorCarriesSanitizedExcerpt(t *testing.T) {
	// "results" is the only key, so this reaches the count check rather than
	// the sibling-key rejection: the count-mismatch error must carry the
	// excerpt too.
	reply := "{\"results\": [{\"title\": \"A\"},\n{\"title\": \"" + strings.Repeat("y", 1000) + "\"}]}"
	_, err := parseBatchMetadataFromJSON(reply, 3)
	if err == nil {
		t.Fatal("expected an error for 2 results against 3 filenames")
	}
	msg := err.Error()
	if !strings.Contains(msg, "got 2 result(s) for 3 filename(s)") {
		t.Errorf("error is not the count mismatch: %q", msg)
	}
	if strings.ContainsAny(msg, "\n\r") {
		t.Errorf("error contains a raw line break: %q", msg)
	}
	if !strings.Contains(msg, `{"results": [{"title": "A"},\n{"title"`) {
		t.Errorf("error does not carry the escaped reply: %q", msg)
	}
	if !strings.Contains(msg, fmt.Sprintf("(%d bytes total)", len(reply))) {
		t.Errorf("error does not say the excerpt was truncated: %q", msg)
	}
	if len(msg) > maxResponseExcerptBytes+300 {
		t.Errorf("error is %d bytes; the excerpt was not bounded", len(msg))
	}
}

func TestParseMetadataFromJSON_Shapes(t *testing.T) {
	testCases := []struct {
		name      string
		json      string
		wantErr   string
		wantTitle string
	}{
		{name: "metadata object", json: `{"title": "T", "author": "A"}`, wantTitle: "T"},
		{name: "empty object is an empty result", json: `{}`},
		{name: "results wrapper holding one object", json: `{"results": [{"title": "T"}]}`, wantTitle: "T"},
		{name: "results wrapper holding nothing is an empty result", json: `{"results": []}`},
		{name: "single-key wrapper holding an object", json: `{"book": {"title": "T"}}`, wantTitle: "T"},
		{name: "results wrapper holding two is an error", json: `{"results": [{"title": "A"}, {"title": "B"}]}`, wantErr: "holds 2 results"},
		{name: "results wrapper holding a non-object", json: `{"results": ["T"]}`, wantErr: `"results" holds an element that is not a JSON object`},
		{name: "unknown scalar key is an error, not an empty parse", json: `{"message": "boom"}`, wantErr: "not a metadata field"},
		{name: "error scalar key is an error report", json: `{"error": "boom"}`, wantErr: `object carries an "error" key`},
		{name: "several unknown keys is an error", json: `{"a": 1, "b": 2}`, wantErr: "none of them is a metadata field"},
		{name: "null is an error, not an empty parse", json: `null`, wantErr: "not a JSON object"},
		{name: "array is an error", json: `[{"title": "T"}]`, wantErr: "not a JSON object"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := parseMetadataFromJSON(tc.json)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), "response: ") {
					t.Errorf("error does not carry the reply excerpt: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m == nil || m.Title != tc.wantTitle {
				t.Fatalf("metadata = %+v, want title %q", m, tc.wantTitle)
			}
		})
	}
}

// End to end through ParseBatch against a fake OpenAI-compatible server: the
// production reply must now come back as one nil per filename, not an error.
func TestParseBatch_ObservedEmptyResultsThroughFakeServer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "empty results", content: observedEmptyResults},
		{name: "short results", content: `{"results": [{"title": "A"}]}`, wantErr: "got 1 result(s) for 2 filename(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"id": "chatcmpl-test", "object": "chat.completion", "model": "test",
				"choices": []any{map[string]any{
					"index": 0, "finish_reason": "stop",
					"message": map[string]any{"role": "assistant", "content": tc.content},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			origBaseURL := config.AppConfig.OpenAIBaseURL
			t.Setenv("OPENAI_BASE_URL", srv.URL)
			config.InitConfig()
			t.Cleanup(func() {
				config.Mutate(func(c *config.Config) { c.OpenAIBaseURL = origBaseURL })
			})

			p := NewOpenAIParser(nil, "test-api-key", true)
			results, err := p.ParseBatch(context.Background(), []string{"Disc 1", "Disc 2"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBatch: %v", err)
			}
			if len(results) != 2 {
				t.Fatalf("got %d results, want 2", len(results))
			}
			wantAllNil(t, results)
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
	_, err := parseBatchMetadataFromJSON("[{bad json}]", 1)

	if err == nil {
		t.Fatal("Expected error")
	}

	errorMsg := err.Error()
	if !strings.Contains(errorMsg, "failed to parse OpenAI response") {
		t.Errorf("Expected error to mention 'failed to parse OpenAI response', got: %s", errorMsg)
	}
}

// Error payloads that the wrapper shapes accepted by PR #3328 let through as
// empty metadata with a nil error. Each must be an error: an empty parse is
// saved as the book's AI result and stamped in the scan cache, so the book is
// never sent to the model again.
func TestParseBatchMetadataFromJSON_ErrorPayloadsAreNotEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		json     string
		expected int
		wantErr  string
	}{
		{name: "results next to an error key", json: `{"results": [], "error": "context length exceeded"}`, expected: 3,
			wantErr: `"results" is present alongside 1 other key(s)`},
		{name: "results holding metadata next to an error key", json: `{"results": [{"title": "A"}], "error": "truncated"}`, expected: 1,
			wantErr: `"results" is present alongside 1 other key(s)`},
		{name: "error array under a single key", json: `{"error": [{"code":500,"message":"overloaded"}]}`, expected: 1,
			wantErr: `object carries an "error" key, so it is an error report`},
		{name: "unknown key holding an error-shaped array", json: `{"data": [{"code":500,"message":"overloaded"}]}`, expected: 1,
			wantErr: "result 0 has 2 key(s) and none of them is a metadata field"},
		{name: "results holding error objects", json: `{"results": [{"error":"x"},{"error":"y"}]}`, expected: 2,
			wantErr: "result 0 has 1 key(s) and none of them is a metadata field"},
		{name: "error object among real results", json: `{"results": [{"title":"A"},{"message":"x"}]}`, expected: 2,
			wantErr: "result 1 has 1 key(s) and none of them is a metadata field"},
		{name: "bare array of error objects", json: `[{"error":"x"}]`, expected: 1,
			wantErr: "result 0 has 1 key(s) and none of them is a metadata field"},
		{name: "empty error array as the only key", json: `{"error": []}`, expected: 2,
			wantErr: `object's only key "error" holds no metadata object, so it is not a results wrapper`},
		{name: "errors key holding only nulls", json: `{"errors": [null]}`, expected: 1,
			wantErr: `object carries an "errors" key, so it is an error report`},
		{name: "unknown key holding an empty array", json: `{"data": []}`, expected: 2,
			wantErr: "not a results wrapper"},
		{name: "unknown key holding only nulls", json: `{"data": [null]}`, expected: 1,
			wantErr: "not a results wrapper"},
		{name: "non-object element", json: `{"results": ["A"]}`, expected: 1,
			wantErr: "result 0 is not a JSON object"},
		// An error key next to metadata keys (PR #3330 delta review).
		{name: "error next to a null title", json: `{"results": [{"title": null, "error": "rate limited"}]}`, expected: 1,
			wantErr: `result 0 carries an "error" key`},
		{name: "error next to confidence only", json: `{"results": [{"confidence":"low","error":"overloaded"}]}`, expected: 1,
			wantErr: `result 0 carries an "error" key`},
		{name: "bare object: error next to a null title", json: `{"error": "x", "title": null}`, expected: 1,
			wantErr: `the reply object carries an "error" key`},
		{name: "bare object: error next to a real title", json: `{"title":"Solo","error":"x"}`, expected: 1,
			wantErr: `the reply object carries an "error" key`},
		{name: "errors key in any case", json: `{"results": [{"title": "A", "Errors": ["x"]}]}`, expected: 1,
			wantErr: `result 0 carries an "Errors" key`},
		// An error key as the reply's only key, wrapping metadata-shaped
		// objects (PR #3330 round-4 review). The first is the JSON:API /
		// RFC 7807 error body.
		{name: "JSON:API errors body", json: `{"errors": [{"status": "429", "title": "Too Many Requests"}]}`, expected: 1,
			wantErr: `object carries an "errors" key, so it is an error report`},
		{name: "Error key wrapping a metadata object", json: `{"Error": [{"title": "Solo"}]}`, expected: 1,
			wantErr: `object carries an "Error" key, so it is an error report`},
		// Only null, [] and {} count as an empty error value; anything else
		// is an error (owner decision, PR #3330 round 5).
		{name: "empty-string error next to a title", json: `{"results": [{"title":"Solo","error":""}]}`, expected: 1,
			wantErr: `result 0 carries an "error" key`},
		{name: "error object next to a title", json: `{"results": [{"title":"Solo","error":{"code":1}}]}`, expected: 1,
			wantErr: `result 0 carries an "error" key`},
		{name: "bool error next to a title", json: `{"results": [{"title":"Solo","error":false}]}`, expected: 1,
			wantErr: `result 0 carries an "error" key`},
		{name: "bare object: empty-string error", json: `{"title":"Solo","error":""}`, expected: 1,
			wantErr: `the reply object carries an "error" key`},
		{name: "bare object: error object", json: `{"title":"Solo","error":{"code":1}}`, expected: 1,
			wantErr: `the reply object carries an "error" key`},
		{name: "null error as the only key", json: `{"error": null}`, expected: 1,
			wantErr: `object's only key "error" is not a JSON array`},
		{name: "empty errors array as the only key", json: `{"errors": []}`, expected: 1,
			wantErr: `object's only key "errors" holds no metadata object, so it is not a results wrapper`},
		{name: "null error next to results", json: `{"results": [{"title":"Solo"}], "error": null}`, expected: 1,
			wantErr: `"results" is present alongside 1 other key(s)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, err := parseBatchMetadataFromJSON(tc.json, tc.expected)
			if err == nil {
				t.Fatalf("got %d result(s) and no error; an error payload became metadata: %+v", len(results), results)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// An error key whose value is null, [] or {} says there is no error, so the
// result is accepted (owner decision, PR #3330 round 5). Non-empty values are
// rejected; those cases are in the ErrorPayloadsAreNotEmptyResults tables.
func TestErrorKeyWithAnEmptyValueIsIgnored(t *testing.T) {
	for _, obj := range []string{
		`{"title":"Solo","error":null}`,
		`{"title":"Solo","errors":[]}`,
		`{"title":"Solo","error":{}}`,
		`{"title":"Solo","Errors":null}`,
	} {
		t.Run(obj, func(t *testing.T) {
			for shape, js := range map[string]string{
				"batch element":            `{"results": [` + obj + `]}`,
				"bare one-filename object": obj,
			} {
				r, err := parseBatchMetadataFromJSON(js, 1)
				if err != nil {
					t.Errorf("batch %s: unexpected error: %v", shape, err)
					continue
				}
				if len(r) != 1 || r[0] == nil || r[0].Title != "Solo" {
					t.Errorf("batch %s: results = %+v, want one result titled Solo", shape, r)
				}
			}
			for shape, js := range map[string]string{
				"top-level object":        obj,
				"results wrapper element": `{"results": [` + obj + `]}`,
			} {
				m, err := parseMetadataFromJSON(js)
				if err != nil {
					t.Errorf("single-book %s: unexpected error: %v", shape, err)
					continue
				}
				if m == nil || m.Title != "Solo" {
					t.Errorf("single-book %s: got %+v, want title Solo", shape, m)
				}
			}
		})
	}
}

// encoding/json keeps only the last copy of a repeated key, so
// {"title":"Solo","error":"x","error":null} would decode to an empty error and
// be accepted, while the same keys in the other order were rejected. A
// repeated "error"/"errors" key, in either order or in two case variants, is
// an error report whatever its values say (PR #3330 round 6).
func TestRepeatedErrorKeyIsAnErrorReport(t *testing.T) {
	rejected := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: accepted; a repeated error key must be an error report", what)
			return
		}
		if !strings.Contains(err.Error(), "more than once") {
			t.Errorf("%s: error %q is not the repeated-key message", what, err)
		}
	}
	for _, obj := range []string{
		`{"title":"Solo","error":"x","error":null}`,
		`{"title":"Solo","error":null,"error":"x"}`,
		`{"title":"Solo","error":null,"Error":"x"}`,
		`{"title":"Solo","error":null,"Error":null}`,
		`{"title":"Solo","errors":[],"ERRORS":[]}`,
	} {
		t.Run(obj, func(t *testing.T) {
			_, err := parseBatchMetadataFromJSON(`{"results": [`+obj+`]}`, 1)
			rejected(t, "batch element", err)
			_, err = parseBatchMetadataFromJSON(obj, 1)
			rejected(t, "bare one-filename object", err)
			_, err = parseMetadataFromJSON(obj)
			rejected(t, "single-book top level", err)
			_, err = parseMetadataFromJSON(`{"results": [` + obj + `]}`)
			rejected(t, "single-book results element", err)
		})
	}
	// A repeated wrapper key is caught the same way, before the wrapper
	// rules read whichever copy encoding/json kept.
	for _, js := range []string{
		`{"errors":[{"title":"Too Many Requests"}],"errors":[]}`,
		`{"error":{"title":"Solo"},"error":null}`,
	} {
		_, err := parseBatchMetadataFromJSON(js, 1)
		rejected(t, "batch "+js, err)
		_, err = parseMetadataFromJSON(js)
		rejected(t, "single-book "+js, err)
	}
	// Different error keys, each once and empty, are still ignored.
	m, err := parseMetadataFromJSON(`{"title":"Solo","error":null,"errors":[]}`)
	if err != nil || m == nil || m.Title != "Solo" {
		t.Errorf("error and errors, each once and empty: got %+v, %v; want title Solo", m, err)
	}
}

// Any repeated key the parser acts on would otherwise be decided by key
// order, because encoding/json keeps the last copy (PR #3330 round 7).
func TestRepeatedKeyIsAnError(t *testing.T) {
	rejected := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: accepted; a repeated key must fail the reply", what)
			return
		}
		if !strings.Contains(err.Error(), "more than once") {
			t.Errorf("%s: error %q is not the repeated-key message", what, err)
		}
	}
	// A repeated wrapper key, in both orders: never "nothing found".
	for _, js := range []string{
		`{"results": [{"title":"A"}], "results": []}`,
		`{"results": [], "results": [{"title":"A"}]}`,
		`{"results": [{"title":"A"}], "Results": []}`,
		`{"books": [{"title":"A"}], "books": []}`,
		`{"books": [], "books": [{"title":"A"}]}`,
	} {
		_, err := parseBatchMetadataFromJSON(js, 1)
		rejected(t, "batch "+js, err)
		_, err = parseMetadataFromJSON(js)
		rejected(t, "single-book "+js, err)
	}
	// A repeated metadata key, on each of the four paths.
	for _, obj := range []string{
		`{"title":"A","title":"B"}`,
		`{"title":"A","Title":"B"}`,
		`{"author":"X","title":"A","AUTHOR":"Y"}`,
	} {
		_, err := parseBatchMetadataFromJSON(`{"results": [`+obj+`]}`, 1)
		rejected(t, "batch element "+obj, err)
		_, err = parseBatchMetadataFromJSON(obj, 1)
		rejected(t, "bare one-filename object "+obj, err)
		_, err = parseMetadataFromJSON(obj)
		rejected(t, "single-book top level "+obj, err)
		_, err = parseMetadataFromJSON(`{"results": [` + obj + `]}`)
		rejected(t, "single-book results element "+obj, err)
	}

	// Controls: no repeated key is accepted as before, each element is
	// checked on its own, and an ignored key in two spellings is harmless.
	r, err := parseBatchMetadataFromJSON(`{"results": [{"title":"A","author":"X"},{"title":"B"}]}`, 2)
	if err != nil || len(r) != 2 || r[0] == nil || r[1] == nil || r[0].Title != "A" || r[1].Title != "B" {
		t.Errorf("batch results with no repeated key: got %+v, %v", r, err)
	}
	m, err := parseMetadataFromJSON(`{"results": [{"title":"A"}]}`)
	if err != nil || m == nil || m.Title != "A" {
		t.Errorf("single-book results with no repeated key: got %+v, %v", m, err)
	}
	m, err = parseMetadataFromJSON(`{"title":"A","filename":"x","Filename":"y"}`)
	if err != nil || m == nil || m.Title != "A" {
		t.Errorf("an ignored key in two spellings: got %+v, %v", m, err)
	}
}

// The controls for the test above: element validation must not reject what a
// real reply sends for "found nothing" (null, {}), and must not reject a
// metadata object that carries extra keys.
func TestParseBatchMetadataFromJSON_ResultElementsStillAccepted(t *testing.T) {
	r, err := parseBatchMetadataFromJSON(`{"results": [{}, null, {"title": "A", "filename": "x.mp3"}]}`, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r[0] == nil || *r[0] != (ParsedMetadata{}) {
		t.Errorf("r[0] = %+v, want an empty non-nil result for {}", r[0])
	}
	if r[1] != nil {
		t.Errorf("r[1] = %+v, want nil for null", r[1])
	}
	if r[2] == nil || r[2].Title != "A" {
		t.Errorf("r[2] = %+v, want title A", r[2])
	}

	r, err = parseBatchMetadataFromJSON(`{"books": [null, {"title": "B"}]}`, 2)
	if err != nil {
		t.Fatalf("unknown wrapper key holding metadata: unexpected error: %v", err)
	}
	if r[0] != nil || r[1] == nil || r[1].Title != "B" {
		t.Errorf("results = %+v, %+v", r[0], r[1])
	}
}

func TestParseMetadataFromJSON_ErrorPayloadsAreNotEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		name    string
		json    string
		wantErr string
	}{
		{name: "error object under a single key", json: `{"error": {"message": "x"}}`,
			wantErr: `object carries an "error" key, so it is an error report`},
		{name: "unknown key holding an error-shaped object", json: `{"data": {"message": "x"}}`,
			wantErr: `"data" holds an object that has 1 key(s) and none of them is a metadata field`},
		{name: "results holding one error object", json: `{"results": [{"error": "x"}]}`,
			wantErr: `"results" holds an element that has 1 key(s) and none of them is a metadata field`},
		{name: "error key holding an error array", json: `{"error": [{"code": 500, "message": "overloaded"}]}`,
			wantErr: `object carries an "error" key, so it is an error report`},
		{name: "unknown key holding an error-shaped array", json: `{"data": [{"code": 500, "message": "overloaded"}]}`,
			wantErr: `"data" holds an element that has 2 key(s) and none of them is a metadata field`},
		{name: "empty error object as the only key", json: `{"error": {}}`,
			wantErr: `object's only key "error" is not a metadata field and holds no metadata object, so it is not a results wrapper`},
		{name: "empty error array as the only key", json: `{"error": []}`,
			wantErr: `object's only key "error" is not a metadata field and holds no metadata object, so it is not a results wrapper`},
		{name: "null error as the only key", json: `{"error": null}`,
			wantErr: `"error" is not a metadata field and holds neither an object nor an array`},
		{name: "empty errors array as the only key (plural)", json: `{"errors": []}`,
			wantErr: `object's only key "errors" is not a metadata field and holds no metadata object`},
		{name: "empty-string error next to a title", json: `{"title":"Solo","error":""}`,
			wantErr: `the reply object carries an "error" key`},
		{name: "error object next to a title", json: `{"title":"Solo","error":{"code":1}}`,
			wantErr: `the reply object carries an "error" key`},
		{name: "number error inside results", json: `{"results": [{"title":"Solo","error":1}]}`,
			wantErr: `"results" holds an element that carries an "error" key`},
		{name: "unknown key holding an empty object", json: `{"data": {}}`,
			wantErr: "not a results wrapper"},
		{name: "unknown key holding an empty array", json: `{"data": []}`,
			wantErr: "not a results wrapper"},
		// An error key as the only key, wrapping metadata (PR #3330 round-4
		// review): the model's text must not become the title.
		{name: "error key wrapping a metadata object", json: `{"error": {"title": "Solo", "author": "X"}}`,
			wantErr: `object carries an "error" key, so it is an error report`},
		{name: "JSON:API errors body", json: `{"errors": [{"title": "Too Many Requests", "status": 429}]}`,
			wantErr: `object carries an "errors" key, so it is an error report`},
		{name: "results element with an error next to a null title", json: `{"results": [{"title": null, "error": "x"}]}`,
			wantErr: `"results" holds an element that carries an "error" key`},
		{name: "top-level error next to a null title", json: `{"error": "x", "title": null}`,
			wantErr: `the reply object carries an "error" key`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := parseMetadataFromJSON(tc.json)
			if err == nil {
				t.Fatalf("got %+v and no error; an error payload became metadata", m)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}

	// Controls: "found nothing" and a metadata object with extra keys.
	for _, ok := range []struct{ json, wantTitle string }{
		{json: `{"results": [null]}`},
		{json: `{"results": [{}]}`},
		{json: `{"book": {"title": "T", "filename": "x.mp3"}}`, wantTitle: "T"},
	} {
		m, err := parseMetadataFromJSON(ok.json)
		if err != nil || m == nil || m.Title != ok.wantTitle {
			t.Errorf("parseMetadataFromJSON(%s) = %+v, %v; want title %q", ok.json, m, err, ok.wantTitle)
		}
	}
}

// encoding/json matches field names case-insensitively, so the metadata-key
// check must too: #3328 accepted {"Title": ...} and element validation must
// not start rejecting it.
func TestMetadataKeysMatchCaseInsensitively(t *testing.T) {
	r, err := parseBatchMetadataFromJSON(`{"results": [{"Title": "Capital", "Author": "X"}]}`, 1)
	if err != nil {
		t.Fatalf("batch: unexpected error: %v", err)
	}
	if r[0] == nil || r[0].Title != "Capital" || r[0].Author != "X" {
		t.Errorf("batch: r[0] = %+v", r[0])
	}
	r, err = parseBatchMetadataFromJSON(`{"TITLE": "Capital"}`, 1)
	if err != nil || r[0] == nil || r[0].Title != "Capital" {
		t.Errorf("batch bare object: %+v, %v", r, err)
	}
	for _, js := range []string{`{"results": [{"Title": "Capital"}]}`, `{"TITLE": "Capital"}`, `{"book": {"Title": "Capital"}}`} {
		m, err := parseMetadataFromJSON(js)
		if err != nil || m == nil || m.Title != "Capital" {
			t.Errorf("single %s: %+v, %v", js, m, err)
		}
	}
}

// Every parse failure must be a *ReplyParseError, including the count
// mismatch, which is not a decode error: internal/scanner classifies by that
// type and must never string-match an error that quotes the model's reply.
func TestReplyParseError_EveryParseFailureIsTyped(t *testing.T) {
	check := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected an error", what)
		}
		rpe, ok := errors.AsType[*ReplyParseError](err)
		if !ok {
			t.Fatalf("%s: %T %q is not a *ReplyParseError", what, err, err)
		}
		if rpe.Excerpt == "" || !strings.Contains(err.Error(), rpe.Excerpt) {
			t.Errorf("%s: Error() %q does not carry the excerpt %q", what, err, rpe.Excerpt)
		}
	}
	for _, tc := range []struct {
		json     string
		expected int
	}{
		{`{bad`, 1},
		{`null`, 1},
		{`{"results": [{"title": "A"}]}`, 2}, // count mismatch
		{`{"results": [], "error": "x"}`, 1},
		{`{"error": [{"code": 500}]}`, 1},
	} {
		_, err := parseBatchMetadataFromJSON(tc.json, tc.expected)
		check(t, "batch "+tc.json, err)
	}
	for _, js := range []string{`{bad`, `null`, `{"a": 1, "b": 2}`, `{"error": {"message": "x"}}`} {
		_, err := parseMetadataFromJSON(js)
		check(t, "single "+js, err)
	}
}

// logger.SanitizeLogValue escapes C0 and DEL only. Model output also must not
// carry bidi overrides/isolates, zero-width format characters, or U+2028 and
// U+2029 into an operation log, where they reorder or split what a reader
// sees.
func TestResponseExcerpt_EscapesFormatAndLineSeparatorRunes(t *testing.T) {
	in := "A\u202aB\u202eC\u2028D\u2029E\u2066F\u2069G\u200bH\ufeffI\U000e0001J"
	got := responseExcerpt(in)
	want := `A\u202aB\u202eC\u2028D\u2029E\u2066F\u2069G\u200bH\ufeffI\U000e0001J`
	if got != want {
		t.Errorf("responseExcerpt = %q, want %q", got, want)
	}
	if printable := "Café – Naïve «x»"; responseExcerpt(printable) != printable {
		t.Errorf("visible non-ASCII was altered: %q", responseExcerpt(printable))
	}

	// Truncation still lands on a UTF-8 boundary: the 3-byte U+202E starts
	// one byte before the cut, so it is dropped whole, never split.
	long := strings.Repeat("a", maxResponseExcerptBytes-1) + "\u202e" + strings.Repeat("b", 50)
	got = responseExcerpt(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncated excerpt is not valid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, '\u202e') {
		t.Errorf("raw U+202E survived: %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxResponseExcerptBytes-1)+"...") {
		t.Errorf("truncation moved: %q", got)
	}

	// Through the real error path.
	_, err := parseBatchMetadataFromJSON("{\"results\": [{\"title\": \"x\u202ey\u2028z\"}]}", 2)
	if err == nil || strings.ContainsAny(err.Error(), "\u202e\u2028") {
		t.Errorf("error carries a raw format or separator rune: %q", err)
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
					Model    string           `json:"model"`
					Messages []map[string]any `json:"messages"`
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
