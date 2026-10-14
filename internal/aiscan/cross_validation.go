// file: internal/aiscan/cross_validation.go
// version: 1.1.0
// guid: d0e6f2a4-7b8c-9d0e-1f2a-3b4c5d6e7f8a
// last-edited: 2026-10-13

package aiscan

import (
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// CrossValidate compares groups scan suggestions against full scan suggestions.
// Returns unified ScanResults with agreement classification.
//
// Logic:
//   - For each groups suggestion, try to match against full suggestions by overlapping author IDs.
//     Fallback: match by canonical name.
//   - Match found + same action + same canonical -> "agreed" (use higher confidence)
//   - Match found + same action + different canonical -> "agreed" (use groups' canonical, note diff)
//   - Match found + different actions -> "disagreed" (include both)
//   - No match -> "groups_only"
//   - Unmatched full suggestions -> "full_only"
func CrossValidate(scanID int, groupsSuggestions, fullSuggestions []database.ScanSuggestion) []database.ScanResult {
	var results []database.ScanResult
	fullMatched := make([]bool, len(fullSuggestions))

	for _, gs := range groupsSuggestions {
		matchIdx := findMatch(gs, fullSuggestions)

		if matchIdx >= 0 {
			fullMatched[matchIdx] = true
			fs := fullSuggestions[matchIdx]

			if gs.Action == fs.Action {
				// Same action -> agreed
				result := database.ScanResult{
					ScanID:    scanID,
					Agreement: "agreed",
					Suggestion: database.ScanSuggestion{
						Action:        gs.Action,
						CanonicalName: gs.CanonicalName, // prefer groups' canonical
						Reason:        gs.Reason + " | " + fs.Reason,
						Confidence:    higherConfidence(gs.Confidence, fs.Confidence),
						AuthorIDs:     mergeIDs(gs.AuthorIDs, fs.AuthorIDs),
						// The groups scan drives an agreed row, so its heuristic
						// canonical is the keep; the full scan's choice covers a
						// groups row that carried no ids.
						KeepID:     firstNonZero(gs.KeepID, fs.KeepID),
						GroupIndex: gs.GroupIndex,
						Roles:      coalesce(gs.Roles, fs.Roles),
						Source:     "cross_validate",
					},
				}
				results = append(results, result)
			} else {
				// Different actions -> disagreed, include both as separate results
				results = append(results, database.ScanResult{
					ScanID:    scanID,
					Agreement: "disagreed",
					Suggestion: database.ScanSuggestion{
						Action:        gs.Action,
						CanonicalName: gs.CanonicalName,
						Reason:        "Groups: " + gs.Reason,
						Confidence:    gs.Confidence,
						AuthorIDs:     gs.AuthorIDs,
						KeepID:        gs.KeepID,
						GroupIndex:    gs.GroupIndex,
						Roles:         gs.Roles,
						Source:        "groups_scan",
					},
				})
				results = append(results, database.ScanResult{
					ScanID:    scanID,
					Agreement: "disagreed",
					Suggestion: database.ScanSuggestion{
						Action:        fs.Action,
						CanonicalName: fs.CanonicalName,
						Reason:        "Full: " + fs.Reason,
						Confidence:    fs.Confidence,
						AuthorIDs:     fs.AuthorIDs,
						KeepID:        fs.KeepID,
						GroupIndex:    fs.GroupIndex,
						Roles:         fs.Roles,
						Source:        "full_scan",
					},
				})
			}
		} else {
			// No match in full -> groups_only
			gs.Source = "groups_scan"
			results = append(results, database.ScanResult{
				ScanID:     scanID,
				Agreement:  "groups_only",
				Suggestion: gs,
			})
		}
	}

	// Unmatched full suggestions -> full_only
	for i, fs := range fullSuggestions {
		if !fullMatched[i] {
			fs.Source = "full_scan"
			results = append(results, database.ScanResult{
				ScanID:     scanID,
				Agreement:  "full_only",
				Suggestion: fs,
			})
		}
	}

	return results
}

// findMatch finds the best matching full suggestion for a groups suggestion.
// Primary match: overlapping author IDs. Fallback: canonical name match.
// Returns index into fullSuggestions, or -1 if no match.
func findMatch(gs database.ScanSuggestion, fullSuggestions []database.ScanSuggestion) int {
	// First pass: match by overlapping author IDs
	gsIDs := idSet(gs.AuthorIDs)
	bestIdx := -1
	bestOverlap := 0

	for i, fs := range fullSuggestions {
		overlap := 0
		for _, id := range fs.AuthorIDs {
			if gsIDs[id] {
				overlap++
			}
		}
		if overlap > bestOverlap {
			bestOverlap = overlap
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		return bestIdx
	}

	// Fallback: match by canonical name
	for i, fs := range fullSuggestions {
		if gs.CanonicalName == fs.CanonicalName {
			return i
		}
	}

	return -1
}

// higherConfidence returns the higher of two confidence levels.
func higherConfidence(a, b string) string {
	rank := map[string]int{"low": 0, "medium": 1, "high": 2}
	if rank[a] >= rank[b] {
		return a
	}
	return b
}

// mergeIDs combines two ID slices, deduplicating.
func mergeIDs(a, b []int) []int {
	seen := idSet(a)
	merged := append([]int{}, a...)
	for _, id := range b {
		if !seen[id] {
			merged = append(merged, id)
			seen[id] = true
		}
	}
	return merged
}

// idSet creates a set from a slice of ints.
func idSet(ids []int) map[int]bool {
	s := make(map[int]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

// coalesce returns the first non-nil json.RawMessage.
func coalesce(a, b json.RawMessage) json.RawMessage {
	if len(a) > 0 {
		return a
	}
	return b
}

// firstNonZero returns a, or b when a is zero.
func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
