// file: cmd/dedup_bench_types.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8901-bcde-f23456789012

//go:build bench

package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// TestConfig describes a single test run configuration.
type TestConfig struct {
	Model         string  `json:"model"`
	PromptVariant string  `json:"prompt_variant"` // baseline, lookup, chain-of-thought
	Temperature   float64 `json:"temperature"`
	TopP          float64 `json:"top_p"`
}

// BenchRunResult captures the outcome of a single test run.
type BenchRunResult struct {
	Config           TestConfig     `json:"config"`
	Mode             string         `json:"mode"` // groups or full
	DurationMs       int64          `json:"duration_ms"`
	InputTokens      int64          `json:"input_tokens"`
	OutputTokens     int64          `json:"output_tokens"`
	CachedTokens     int64          `json:"cached_tokens"`
	TotalTokens      int64          `json:"total_tokens"`
	CostEstimate     float64        `json:"cost_estimate_usd"`
	NumSuggestions   int            `json:"num_suggestions"`
	ActionCounts     map[string]int `json:"action_counts"`
	ConfidenceCounts map[string]int `json:"confidence_counts"`
	Error            string         `json:"error,omitempty"`
	NumChunks        int            `json:"num_chunks,omitempty"`
}

// BenchSummary is the cross-run comparison.
type BenchSummary struct {
	Timestamp   string           `json:"timestamp"`
	AuthorCount int              `json:"author_count"`
	GroupCount  int              `json:"group_count"`
	Runs        []BenchRunResult `json:"runs"`
}

// AuthorData holds the frozen author data for all runs.
type AuthorData struct {
	Authors     []database.Author `json:"authors"`
	BookCounts  map[int]int       `json:"book_counts"`
	SampleBooks map[int][]string  `json:"sample_books"` // authorID -> up to 3 titles
}

// extractAuthorData loads all authors, book counts, and sample titles from the local DB.
func extractAuthorData(store database.AuthorReader) (*AuthorData, error) {
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("GetAllAuthors: %w", err)
	}

	bookCounts, err := store.GetAllAuthorBookCounts()
	if err != nil {
		return nil, fmt.Errorf("GetAllAuthorBookCounts: %w", err)
	}

	sampleBooks := make(map[int][]string, len(authors))
	for _, a := range authors {
		books, err := store.GetBooksByAuthorIDWithRoleCore(a.ID)
		if err != nil {
			continue
		}
		titles := make([]string, 0, 3)
		for i, b := range books {
			if i >= 3 {
				break
			}
			titles = append(titles, b.Title)
		}
		if len(titles) > 0 {
			sampleBooks[a.ID] = titles
		}
	}

	return &AuthorData{
		Authors:     authors,
		BookCounts:  bookCounts,
		SampleBooks: sampleBooks,
	}, nil
}

// buildGroupsInput converts heuristic groups to AI input format.
func buildGroupsInput(groups []server.AuthorDedupGroup, data *AuthorData) []ai.AuthorDedupInput {
	inputs := make([]ai.AuthorDedupInput, 0, len(groups))
	for i, g := range groups {
		variants := make([]string, 0, len(g.Variants))
		for _, v := range g.Variants {
			variants = append(variants, v.Name)
		}
		bc := data.BookCounts[g.Canonical.ID]
		samples := data.SampleBooks[g.Canonical.ID]
		inputs = append(inputs, ai.AuthorDedupInput{
			Index:         i,
			CanonicalName: g.Canonical.Name,
			VariantNames:  variants,
			BookCount:     bc,
			SampleTitles:  samples,
		})
	}
	return inputs
}

// buildFullInput converts all authors to AI discovery input format.
func buildFullInput(data *AuthorData) []ai.AuthorDiscoveryInput {
	inputs := make([]ai.AuthorDiscoveryInput, 0, len(data.Authors))
	for _, a := range data.Authors {
		inputs = append(inputs, ai.AuthorDiscoveryInput{
			ID:           a.ID,
			Name:         a.Name,
			BookCount:    data.BookCounts[a.ID],
			SampleTitles: data.SampleBooks[a.ID],
		})
	}
	return inputs
}

// buildTestConfigs creates the full matrix of test configurations.
func buildTestConfigs(models []string) []TestConfig {
	configs := []TestConfig{}

	// Dimension 1: All models with baseline prompt, temp=0
	for _, m := range models {
		configs = append(configs, TestConfig{
			Model:         m,
			PromptVariant: "baseline",
			Temperature:   0.0,
			TopP:          1.0,
		})
	}

	// Dimension 2: Prompt variations on gpt-5-mini and gpt-5
	for _, m := range []string{"gpt-5-mini", "gpt-5"} {
		for _, pv := range []string{"lookup", "chain-of-thought"} {
			configs = append(configs, TestConfig{
				Model:         m,
				PromptVariant: pv,
				Temperature:   0.0,
				TopP:          1.0,
			})
		}
	}

	// Dimension 3: Temperature variations on gpt-5-mini baseline
	for _, temp := range []float64{0.3, 0.7} {
		configs = append(configs, TestConfig{
			Model:         "gpt-5-mini",
			PromptVariant: "baseline",
			Temperature:   temp,
			TopP:          1.0,
		})
	}

	return configs
}

// writeJSON marshals v to a JSON file at path.
func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	return os.WriteFile(path, data, 0664)
}
