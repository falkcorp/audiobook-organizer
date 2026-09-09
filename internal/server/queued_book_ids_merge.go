// file: internal/server/queued_book_ids_merge.go
// version: 1.1.0
// guid: f48d0f0d-f4dd-44ed-b70c-f3c21c2ce63e
// last-edited: 2026-09-09

package server

import (
	"encoding/json"
	"fmt"
)

// mergeUniqueBookIDs preserves first-seen order while producing the union of
// two book selections. Queue mergers use it only after confirming that all
// behavior-affecting flags are identical.
func mergeUniqueBookIDs(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, id := range append(existing, incoming...) {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		merged = append(merged, id)
	}
	return merged
}

// formatQueuedBookSummary renders what an OperationDef.SummarizeQueued hook
// returns for the three ops that hold a growing list of book IDs while queued:
// metadata.batch-apply-cached, metadata.batch-save and library.bulk-write-back.
//
// Only the prose is shared. Each op decodes its OWN params struct and hands the
// two counts in, so the JSON field names stay checked against the real type
// rather than against a lookalike shape declared here — a shared decoder would
// silently return zero for any op whose params drifted.
//
// done is work finished by earlier attempts of the same run (nonzero only for a
// resumed op); remaining is what is still queued. total is their sum, which is
// the number the user actually asked for: "how many books is that apply for?"
func formatQueuedBookSummary(done, remaining int, verb string) (int, int, string) {
	total := done + remaining
	if total <= 0 {
		return 0, 0, ""
	}
	noun := "books"
	if total == 1 {
		noun = "book"
	}
	// No "queued" in the text. The Activity page prints this directly beneath
	// its own "Waiting to start…" line and the bell prints it under a Queued
	// section header, so restating the state here would read as stutter in both
	// places. The message carries the SIZE; the surrounding UI carries the state.
	msg := fmt.Sprintf("%d %s to %s", total, noun, verb)
	if done > 0 {
		msg = fmt.Sprintf("%s (%d already done)", msg, done)
	}
	return done, total, msg
}

// decodeQueuedParams decodes a queued row's params into T. A queued row's params
// are whatever the registry persisted, so a decode failure means the summary is
// unknown rather than zero: callers return no summary and leave the row's
// existing text alone instead of overwriting it with a confident "0 books".
func decodeQueuedParams[T any](params json.RawMessage) (T, bool) {
	var out T
	if err := json.Unmarshal(params, &out); err != nil {
		return out, false
	}
	return out, true
}
