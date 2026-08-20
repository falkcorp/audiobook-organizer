// file: cmd/dedup_bench_crossval.go
// version: 1.1.0
// guid: 3c4d5e6f-7a8b-9012-cdef-333333333333
// last-edited: 2026-08-20

//go:build bench

package cmd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/spf13/cobra"
)

var (
	crossvalResultsA  string
	crossvalModelA    string
	crossvalModeA     string
	crossvalInputData string
	crossvalModelB    string
	crossvalVariant   string
)

var dedupBenchCrossvalCmd = &cobra.Command{
	Use:   "crossval",
	Short: "Cross-validate one model's output with another model",
	Long: `Sends model A's dedup suggestions to model B for a second opinion.
Supports two variants: "no-data" (just suggestions) and "with-data" (suggestions + original input).`,
	RunE: runDedupBenchCrossval,
}

func init() {
	dedupBenchCrossvalCmd.Flags().StringVar(&crossvalResultsA, "results-a", "", "Path to model A's batch_output.jsonl or dir with chunks (required)")
	dedupBenchCrossvalCmd.Flags().StringVar(&crossvalModelA, "model-a", "", "Model A name for labeling (required)")
	dedupBenchCrossvalCmd.Flags().StringVar(&crossvalModeA, "mode-a", "groups", "Was model A's run groups or full?")
	dedupBenchCrossvalCmd.Flags().StringVar(&crossvalInputData, "input-data", "", "Path to original input (groups.json or full_input.json)")
	dedupBenchCrossvalCmd.Flags().StringVar(&crossvalModelB, "model-b", "", "Model B for review (required)")
	dedupBenchCrossvalCmd.Flags().StringVar(&crossvalVariant, "variant", "both", "no-data, with-data, or both")
	_ = dedupBenchCrossvalCmd.MarkFlagRequired("results-a")
	_ = dedupBenchCrossvalCmd.MarkFlagRequired("model-a")
	_ = dedupBenchCrossvalCmd.MarkFlagRequired("model-b")
}

const crossvalPromptNoData = `You are an expert audiobook metadata reviewer performing a CROSS-VALIDATION review.

Another AI model (%s) analyzed author data and produced deduplication suggestions. Your job is to review each suggestion and either AGREE, DISAGREE, or MODIFY it.

You will receive ONLY the suggestions (not the original data). Use your knowledge of authors, narrators, and publishers to evaluate each one.

For each suggestion, respond with:
- "agree": The suggestion is correct as-is
- "disagree": The suggestion is wrong; explain why and provide the correct action
- "modify": The suggestion is partially right but needs adjustment

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".

Return ONLY valid JSON: {"reviews": [{"group_index": N, "original_action": "...", "original_canonical": "...", "verdict": "agree|disagree|modify", "corrected_action": "merge|split|rename|skip|alias|reclassify", "corrected_canonical": "Correct Name", "confidence": "high|medium|low", "reason": "brief explanation"}]}

Only include entries where you disagree or want to modify. If you agree with everything, return {"reviews": []}.`

const crossvalPromptWithData = `You are an expert audiobook metadata reviewer performing a CROSS-VALIDATION review.

Another AI model (%s) analyzed author data and produced deduplication suggestions. Your job is to review each suggestion and either AGREE, DISAGREE, or MODIFY it.

You will receive:
1. The ORIGINAL input data that model A was given
2. Model A's suggestions

Use both the original data AND your knowledge to evaluate each suggestion. Look for:
- False merges (different people merged together)
- Missed duplicates (obvious matches that model A skipped or split)
- Wrong canonical names
- Misclassified roles (author called narrator, etc.)

INITIALS FORMATTING: Always use spaces after periods in initials: "C. B. Lee" not "C.B. Lee".

Return ONLY valid JSON: {"reviews": [{"group_index": N, "original_action": "...", "original_canonical": "...", "verdict": "agree|disagree|modify", "corrected_action": "merge|split|rename|skip|alias|reclassify", "corrected_canonical": "Correct Name", "confidence": "high|medium|low", "reason": "brief explanation"}], "missed": [{"description": "what was missed", "author_ids_or_names": [], "suggested_action": "...", "canonical_name": "...", "confidence": "high|medium|low"}]}

For "reviews": only include entries where you disagree or want to modify.
For "missed": include any obvious duplicates or issues that model A completely missed.`

func runDedupBenchCrossval(cmd *cobra.Command, args []string) error {
	config.InitConfig()
	apiKey := config.AppConfig.OpenAIAPIKey
	if apiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY env var required")
	}

	// Load model A's suggestions
	suggestionsA, err := loadAllSuggestions(crossvalResultsA)
	if err != nil {
		return fmt.Errorf("load results: %w", err)
	}
	slog.Info("Loaded suggestions", "count", len(suggestionsA), "model", crossvalModelA)

	suggestionsText, _ := json.Marshal(suggestionsA)

	// Load input data if provided
	var inputText string
	if crossvalInputData != "" {
		data, err := os.ReadFile(crossvalInputData)
		if err != nil {
			return fmt.Errorf("read input data: %w", err)
		}
		inputText = string(data)
		slog.Info("Loaded original input data", "bytes", len(data))
	}

	// Fetch book evidence for uncertain items
	var bookEvidenceText string
	if benchServerURL != "" {
		uncertain := filterUncertain(suggestionsA)
		if len(uncertain) > 0 && crossvalInputData != "" {
			var groups []json.RawMessage
			if crossvalModeA == "groups" {
				json.Unmarshal([]byte(inputText), &groups)
			}
			if len(groups) > 0 {
				authorIDs := collectAuthorIDsFromGroups(uncertain, groups)
				books, _ := fetchBooksForAuthorIDs(benchServerURL, authorIDs)
				if len(books) > 0 {
					// Build evidence map
					evidence := map[int][]string{}
					for id, bl := range books {
						var titles []string
						for _, b := range bl {
							t := b.Title
							if b.Series != "" {
								t += " (" + b.Series + ")"
							}
							titles = append(titles, t)
						}
						evidence[id] = titles
					}
					evJSON, _ := json.Marshal(evidence)
					bookEvidenceText = string(evJSON)
				}
			}
		}
	}

	ts := time.Now().Format("2006-01-02T15-04-05")
	runDir := filepath.Join(benchOutputDir, ts+"-crossval")
	if err := os.MkdirAll(filepath.Join(runDir, "runs"), 0775); err != nil {
		return err
	}

	_ = writeJSON(filepath.Join(runDir, "config.json"), map[string]interface{}{
		"model_a":     crossvalModelA,
		"mode_a":      crossvalModeA,
		"model_b":     crossvalModelB,
		"suggestions": len(suggestionsA),
		"variant":     crossvalVariant,
	})

	variants := resolveVariants(crossvalVariant)

	for _, variant := range variants {
		label := fmt.Sprintf("%s-to-%s_%s", crossvalModelA, crossvalModelB, variant)
		outDir := filepath.Join(runDir, "runs", label)

		var systemPrompt, userPrompt string

		if variant == "no-data" {
			systemPrompt = fmt.Sprintf(crossvalPromptNoData, crossvalModelA)
			parts := []string{
				fmt.Sprintf("## %s's suggestions (%s mode)\n\n%s", crossvalModelA, crossvalModeA, string(suggestionsText)),
			}
			if bookEvidenceText != "" {
				parts = append(parts, "\n\n## Book title evidence for uncertain suggestions\n\n"+bookEvidenceText)
			}
			userPrompt = strings.Join(parts, "")
		} else {
			systemPrompt = fmt.Sprintf(crossvalPromptWithData, crossvalModelA)
			inputData := inputText
			if inputData == "" {
				inputData = "(original data not available)"
			}
			parts := []string{
				fmt.Sprintf("## Original input data (%s mode)\n\n%s", crossvalModeA, inputData),
				fmt.Sprintf("\n\n## %s's suggestions\n\n%s", crossvalModelA, string(suggestionsText)),
			}
			if bookEvidenceText != "" {
				parts = append(parts, "\n\n## Book title evidence for uncertain suggestions\n\n"+bookEvidenceText)
			}
			userPrompt = strings.Join(parts, "")
		}

		slog.Info("Reviewing suggestions", "variant", variant, "model", crossvalModelB, "reviewed_model", crossvalModelA, "tokens", len(userPrompt)/4)

		customID := fmt.Sprintf("crossval_%s_%s", label, ts)
		if err := submitSingleBatch(cmd.Context(), apiKey, crossvalModelB, systemPrompt, userPrompt, customID, outDir); err != nil {
			slog.Error("batch submission failed", "error", err)
			continue
		}
	}

	slog.Info("Check results later", "command", "dedup-bench check " + runDir)
	return nil
}

func loadAllSuggestions(path string) ([]map[string]interface{}, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return loadSuggestionsFromJSONL(path)
	}

	// Load from directory (multiple chunk subdirs)
	var all []map[string]interface{}
	entries, _ := os.ReadDir(path)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		jsonlPath := filepath.Join(path, e.Name(), "batch_output.jsonl")
		if _, err := os.Stat(jsonlPath); err == nil {
			subs, _ := loadSuggestionsFromJSONL(jsonlPath)
			all = append(all, subs...)
		}
	}
	return all, nil
}

func filterUncertain(suggestions []map[string]interface{}) []map[string]interface{} {
	var uncertain []map[string]interface{}
	for _, s := range suggestions {
		conf, _ := s["confidence"].(string)
		if conf == "medium" || conf == "low" {
			uncertain = append(uncertain, s)
		}
	}
	return uncertain
}

func resolveVariants(v string) []string {
	switch v {
	case "no-data":
		return []string{"no-data"}
	case "with-data":
		return []string{"with-data"}
	default:
		return []string{"no-data", "with-data"}
	}
}
